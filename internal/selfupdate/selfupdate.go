// Package selfupdate replaces a curl-installed binary with a verified release (design
// section 13). It is the only code that touches the network.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wonsong82/claude-hub/internal/xdg"
)

// DefaultBase is the GitHub releases URL. CLAUDE_HUB_RELEASE_BASE overrides it (mirrors, tests).
const DefaultBase = "https://github.com/wonsong82/claude-hub/releases"

// Base returns the release base URL.
func Base() string {
	if v := os.Getenv("CLAUDE_HUB_RELEASE_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBase
}

var client = &http.Client{Timeout: 60 * time.Second}

// Latest returns the latest release tag (vX.Y.Z) from the /latest redirect.
func Latest(base string) (string, error) {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(base + "/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no latest release found (HTTP %d)", resp.StatusCode)
	}
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return "", fmt.Errorf("unexpected redirect %s", loc)
	}
	return loc[i+5:], nil
}

// ArchiveName is the release archive for this platform.
func ArchiveName(tag string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", xdg.Name, strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

func get(url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// Checksum finds name in a checksums.txt.
func Checksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not in checksums.txt", name)
}

// Download fetches and verifies the archive and returns the binary inside it.
func Download(base, tag string) ([]byte, error) {
	name := ArchiveName(tag)
	sums, err := get(fmt.Sprintf("%s/download/%s/checksums.txt", base, tag))
	if err != nil {
		return nil, err
	}
	want, err := Checksum(sums, name)
	if err != nil {
		return nil, err
	}
	arc, err := get(fmt.Sprintf("%s/download/%s/%s", base, tag, name))
	if err != nil {
		return nil, err
	}
	s := sha256.Sum256(arc)
	if hex.EncodeToString(s[:]) != want {
		return nil, errors.New("checksum mismatch: the download was not installed")
	}
	return Extract(arc)
}

// Extract returns the claude-hub file from a .tar.gz.
func Extract(arc []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(arc)))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == xdg.Name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
	return nil, fmt.Errorf("the archive has no %s", xdg.Name)
}

// Replace atomically replaces the file at path with data (mode 0755).
func Replace(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+xdg.Name+".new-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o755); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
