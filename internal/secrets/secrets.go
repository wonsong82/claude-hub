// Package secrets stores tokens in the OS secret store (design section 8). Tokens are never
// passed on a command line, logged, or included in error messages; the output of the
// backend tools is never echoed, only their exit status.
package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Service is the secret-store service name.
const Service = xdg.Name

// ErrNotFound means the store has no entry under that name.
var ErrNotFound = errors.New("not in the store")

// Backend is a secret store.
type Backend interface {
	Name() string
	// Get returns the token. Errors never contain the token.
	Get(name string) (string, error)
	Set(name, token string) error
	Delete(name string) error
	// List returns names in the store, or ok=false when the backend cannot list.
	List() (names []string, ok bool, err error)
}

// Fingerprint is sha256:<first 8 hex> len=<n>.
func Fingerprint(token string) string {
	s := sha256.Sum256([]byte(token))
	return fmt.Sprintf("sha256:%s len=%d", hex.EncodeToString(s[:])[:8], len(token))
}

// CheckToken rejects input that cannot be a setup-token: empty, or containing whitespace,
// quotes, backslashes or control characters. The message never includes the token.
func CheckToken(t string) error {
	if t == "" {
		return errors.New("empty token")
	}
	for _, r := range t {
		if r <= 0x20 || r == 0x7f || r == '"' || r == '\'' || r == '\\' || r > 0x7e {
			return errors.New("the token contains a character a setup-token never has (space, quote, backslash or non-ASCII)")
		}
	}
	return nil
}

// Tools are the executables backends run; tests replace them with stand-ins.
var (
	SecurityBin   = "/usr/bin/security"
	SecretToolBin = "secret-tool"
)

// Open returns the backend for a configured name ("auto" resolved).
func Open(name string, p xdg.Paths) (Backend, error) {
	if name == "" || name == "auto" {
		name = Auto()
	}
	switch name {
	case "keychain":
		return &Keychain{}, nil // the default (login) keychain
	case "secret-tool":
		return &SecretTool{}, nil
	case "file":
		return &File{Dir: p.SecretsDir()}, nil
	}
	return nil, fmt.Errorf("unknown secrets backend %q", name)
}

// Auto picks the backend for this machine: keychain on macOS; on Linux secret-tool when it
// exists and reaches a Secret Service, else file.
func Auto() string {
	if runtime.GOOS == "darwin" {
		return "keychain"
	}
	if path, err := exec.LookPath(SecretToolBin); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "lookup", "service", Service, "account", "__claude-hub-probe__")
		cmd.Stdout, cmd.Stderr = nil, nil
		err := cmd.Run()
		// Exit 1 with no output = reachable, no such item. A missing Secret Service fails
		// differently (non-1 status or a timeout).
		var ee *exec.ExitError
		if err == nil || (errors.As(err, &ee) && ee.ExitCode() == 1 && ctx.Err() == nil) {
			return "secret-tool"
		}
	}
	return "file"
}

func run(stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	err := cmd.Run()
	return out.Bytes(), err
}

func status(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return "tool not found"
	}
	return "could not run"
}

// Keychain is the macOS login keychain backend. Path selects another keychain file (tests
// use a throwaway one); empty means the user's default keychain.
type Keychain struct{ Path string }

func (k *Keychain) args(a ...string) []string {
	if k.Path != "" {
		a = append(a, k.Path)
	}
	return a
}

// Name implements Backend.
func (*Keychain) Name() string { return "keychain" }

// Get reads with find-generic-password -w from a pipe.
func (k *Keychain) Get(name string) (string, error) {
	out, err := run(nil, SecurityBin, k.args("find-generic-password", "-s", Service, "-a", name, "-w")...)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("the keychain did not return it (locked?): %s", status(err))
	}
	t := strings.TrimRight(string(out), "\r\n")
	if t == "" {
		return "", ErrNotFound
	}
	return t, nil
}

// Set sends add-generic-password -U on stdin to `security -i`, so the token never appears in
// a process argument list. The write is verified by reading it back.
func (k *Keychain) Set(name, token string) error {
	if err := CheckToken(token); err != nil {
		return err
	}
	cmd := fmt.Sprintf("add-generic-password -U -s %s -a %s -l %s -w \"%s\"", Service, name, Service+":"+name, token)
	if k.Path != "" {
		cmd += " \"" + k.Path + "\""
	}
	cmd += "\n"
	if _, err := run([]byte(cmd), SecurityBin, "-i"); err != nil {
		return fmt.Errorf("the keychain refused the write: %s", status(err))
	}
	got, err := k.Get(name)
	if err != nil || got != token {
		return errors.New("the keychain did not store the token (locked?)")
	}
	return nil
}

// Delete removes the item.
func (k *Keychain) Delete(name string) error {
	_, err := run(nil, SecurityBin, k.args("delete-generic-password", "-s", Service, "-a", name)...)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return ErrNotFound
		}
		return fmt.Errorf("the keychain did not delete it: %s", status(err))
	}
	return nil
}

// List is not supported: listing the keychain would need dump-keychain.
func (*Keychain) List() ([]string, bool, error) { return nil, false, nil }

// SecretTool is the Linux Secret Service backend.
type SecretTool struct{}

// Name implements Backend.
func (*SecretTool) Name() string { return "secret-tool" }

// Get runs secret-tool lookup.
func (*SecretTool) Get(name string) (string, error) {
	out, err := run(nil, SecretToolBin, "lookup", "service", Service, "account", name)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && len(out) == 0 {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("secret-tool did not return it: %s", status(err))
	}
	t := strings.TrimRight(string(out), "\r\n")
	if t == "" {
		return "", ErrNotFound
	}
	return t, nil
}

// Set runs secret-tool store with the token on stdin.
func (s *SecretTool) Set(name, token string) error {
	if err := CheckToken(token); err != nil {
		return err
	}
	if _, err := run([]byte(token), SecretToolBin, "store", "--label", Service+":"+name, "service", Service, "account", name); err != nil {
		return fmt.Errorf("secret-tool refused the write: %s", status(err))
	}
	got, err := s.Get(name)
	if err != nil || got != token {
		return errors.New("secret-tool did not store the token")
	}
	return nil
}

// Delete runs secret-tool clear.
func (s *SecretTool) Delete(name string) error {
	if _, err := s.Get(name); errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if _, err := run(nil, SecretToolBin, "clear", "service", Service, "account", name); err != nil {
		return fmt.Errorf("secret-tool did not delete it: %s", status(err))
	}
	return nil
}

// List is not supported: `secret-tool search` would print the secrets.
func (*SecretTool) List() ([]string, bool, error) { return nil, false, nil }

// File stores each token in Dir/<name>, mode 0600, in a 0700 folder.
type File struct{ Dir string }

// Name implements Backend.
func (*File) Name() string { return "file" }

func (f *File) path(name string) string { return filepath.Join(f.Dir, name) }

// CheckModes verifies the folder and file permissions.
func (f *File) CheckModes(name string) error {
	if fi, err := os.Stat(f.Dir); err == nil {
		if fi.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s has mode %o; it must be 0700", f.Dir, fi.Mode().Perm())
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			return fmt.Errorf("%s is owned by someone else", f.Dir)
		}
	}
	if name == "" {
		return nil
	}
	fi, err := os.Lstat(f.path(name))
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", f.path(name))
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has mode %o; it must be 0600", f.path(name), fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by someone else", f.path(name))
	}
	return nil
}

// Get reads the file, refusing wide modes or another owner.
func (f *File) Get(name string) (string, error) {
	if _, err := os.Lstat(f.path(name)); errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err := f.CheckModes(name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(f.path(name))
	if err != nil {
		return "", fmt.Errorf("cannot read %s", f.path(name))
	}
	t := strings.TrimRight(string(b), "\r\n")
	if t == "" {
		return "", ErrNotFound
	}
	return t, nil
}

// Set writes by temp file plus rename.
func (f *File) Set(name, token string) error {
	if err := CheckToken(token); err != nil {
		return err
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(f.Dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(f.path(name), []byte(token+"\n"), 0o600)
}

// Delete removes the file.
func (f *File) Delete(name string) error {
	err := os.Remove(f.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// List returns the stored names.
func (f *File) List() ([]string, bool, error) {
	ents, err := os.ReadDir(f.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, true, nil
}
