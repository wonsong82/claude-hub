package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const fakeBinary = "#!/bin/sh\necho fake-claude-hub 1.2.3\n"

func tarGz(t *testing.T, name, body string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// releaseServer serves a fake GitHub release v1.2.3 for this platform.
func releaseServer(t *testing.T, corrupt bool) (*httptest.Server, *int32) {
	archive := fmt.Sprintf("claude-hub_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	data := tarGz(t, "claude-hub", fakeBinary)
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	if corrupt {
		sum = strings.Repeat("0", 64)
	}
	sums := sum + "  " + archive + "\n" + strings.Repeat("1", 64) + "  other.tar.gz\n"
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "http://"+r.Host+"/releases/tag/v1.2.3", http.StatusFound)
		case "/releases/download/v1.2.3/checksums.txt":
			w.Write([]byte(sums))
		case "/releases/download/v1.2.3/" + archive:
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func runInstaller(t *testing.T, script, base, dir string) (string, error) {
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = []string{"HOME=" + filepath.Dir(dir), "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "CLAUDE_HUB_RELEASE_BASE=" + base + "/releases", "CLAUDE_HUB_INSTALL_DIR=" + dir}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallerVerifiesAndInstalls(t *testing.T) {
	srv, _ := releaseServer(t, false)
	dir := filepath.Join(t.TempDir(), "bin")
	out, err := runInstaller(t, "../../install.sh", srv.URL, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "claude-hub"))
	if err != nil || string(b) != fakeBinary {
		t.Fatalf("installed: %q %v", b, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "claude-hub"))
	if fi.Mode().Perm() != 0o755 {
		t.Fatal(fi.Mode())
	}
	expectContains(t, out, "is not on PATH")
	expectContains(t, out, "claude-hub init")
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatal("the installer wrote more than claude-hub")
	}
}

func TestInstallerChecksumMismatchAborts(t *testing.T) {
	srv, _ := releaseServer(t, true)
	dir := filepath.Join(t.TempDir(), "bin")
	out, err := runInstaller(t, "../../install.sh", srv.URL, dir)
	if err == nil {
		t.Fatal("succeeded")
	}
	expectContains(t, out, "checksum mismatch")
	if _, err := os.Stat(filepath.Join(dir, "claude-hub")); err == nil {
		t.Fatal("installed a bad download")
	}
}

func TestTruncatedInstallerRunsNothing(t *testing.T) {
	srv, hits := releaseServer(t, false)
	full, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{len(full) / 4, len(full) / 2, len(full) - len("main \"$@\"\n")} {
		p := filepath.Join(t.TempDir(), "install.sh")
		os.WriteFile(p, full[:cut], 0o644)
		dir := filepath.Join(t.TempDir(), "bin")
		runInstaller(t, p, srv.URL, dir)
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("truncated at %d: created %s", cut, dir)
		}
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("a truncated installer reached the network")
	}
}

func TestSelfUpdate(t *testing.T) {
	srv, _ := releaseServer(t, false)
	e := newEnv(t)
	copyBin := filepath.Join(e.root, "inst", "claude-hub")
	b, _ := os.ReadFile(hubBin)
	mustMkdir(t, filepath.Dir(copyBin))
	os.WriteFile(copyBin, b, 0o755)
	e.extra["CLAUDE_HUB_RELEASE_BASE"] = srv.URL + "/releases"
	r := e.runPath(copyBin, "claude-hub", "self-update")
	if r.code != 0 {
		t.Fatalf("%s", r)
	}
	got, _ := os.ReadFile(copyBin)
	if string(got) != fakeBinary {
		t.Fatal("not replaced")
	}
	// A bad checksum leaves the binary alone.
	bad, _ := releaseServer(t, true)
	os.WriteFile(copyBin, b, 0o755)
	e.extra["CLAUDE_HUB_RELEASE_BASE"] = bad.URL + "/releases"
	r = e.runPath(copyBin, "claude-hub", "self-update", "--version", "v1.2.3")
	expectCode(t, r, 1)
	expectContains(t, r.err, "checksum mismatch")
	if got, _ := os.ReadFile(copyBin); !bytes.Equal(got, b) {
		t.Fatal("replaced after a mismatch")
	}
}
