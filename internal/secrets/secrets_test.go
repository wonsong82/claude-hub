package secrets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const tok = "test-token-abcdefghijklmnop"

func TestFingerprint(t *testing.T) {
	fp := Fingerprint(tok)
	if !strings.HasPrefix(fp, "sha256:") || !strings.HasSuffix(fp, " len=27") || len(fp) != len("sha256:12345678 len=27") {
		t.Fatal(fp)
	}
	if strings.Contains(fp, "test") || Fingerprint(tok) != fp {
		t.Fatal("unstable or leaking")
	}
}

func TestCheckToken(t *testing.T) {
	for _, bad := range []string{"", "a b", `a"b`, "a\\b", "a'b", "a\nb", "é"} {
		if CheckToken(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if CheckToken(tok) != nil {
		t.Fatal("rejected a token")
	}
}

func TestFileBackend(t *testing.T) {
	d := filepath.Join(t.TempDir(), "secrets")
	f := &File{Dir: d}
	if _, err := f.Get("k"); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := f.Set("k", tok); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(d, "k"))
	di, _ := os.Stat(d)
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatal(fi.Mode(), di.Mode())
	}
	if got, err := f.Get("k"); err != nil || got != tok {
		t.Fatal(got, err)
	}
	names, ok, _ := f.List()
	if !ok || len(names) != 1 || names[0] != "k" {
		t.Fatal(names)
	}
	os.Chmod(filepath.Join(d, "k"), 0o640)
	if _, err := f.Get("k"); err == nil || strings.Contains(err.Error(), tok) {
		t.Fatal("wide mode accepted", err)
	}
	os.Chmod(filepath.Join(d, "k"), 0o600)
	os.Chmod(d, 0o755)
	if _, err := f.Get("k"); err == nil {
		t.Fatal("wide folder accepted")
	}
	os.Chmod(d, 0o700)
	if err := f.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if err := f.Delete("k"); err != ErrNotFound {
		t.Fatal(err)
	}
}

// standIn writes a fake tool that records its argv and stdin, and keeps a one-entry store.
func standIn(t *testing.T, name string) (path, logf string) {
	dir := t.TempDir()
	logf = filepath.Join(dir, "log")
	store := filepath.Join(dir, "store")
	path = filepath.Join(dir, name)
	script := `#!/bin/sh
printf 'ARGS:%s\n' "$*" >> "` + logf + `"
case "$1" in
  -i) cat >> "` + logf + `"; tr -d '\n' < /dev/null; awk '{for(i=1;i<=NF;i++) if($i=="-w"){v=$(i+1); gsub(/"/,"",v); print v}}' "` + logf + `" | tail -1 > "` + store + `" ;;
  store) cat > "` + store + `"; printf 'STDIN-USED\n' >> "` + logf + `" ;;
  find-generic-password|lookup) [ -s "` + store + `" ] || exit $([ "$1" = lookup ] && echo 1 || echo 44); cat "` + store + `"; echo ;;
  delete-generic-password|clear) [ -s "` + store + `" ] || exit $([ "$1" = clear ] && echo 1 || echo 44); : > "` + store + `" ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, logf
}

func argLines(t *testing.T, logf string) []string {
	b, _ := os.ReadFile(logf)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "ARGS:") {
			out = append(out, l)
		}
	}
	return out
}

func TestKeychainNeverPutsTheTokenInArgv(t *testing.T) {
	bin, logf := standIn(t, "security")
	old := SecurityBin
	SecurityBin = bin
	defer func() { SecurityBin = old }()
	k := &Keychain{}
	if _, err := k.Get("key-a"); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := k.Set("key-a", tok); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get("key-a"); err != nil || got != tok {
		t.Fatal(got, err)
	}
	if err := k.Delete("key-a"); err != nil {
		t.Fatal(err)
	}
	for _, l := range argLines(t, logf) {
		if strings.Contains(l, tok) {
			t.Fatalf("token in argv: %s", l)
		}
	}
	b, _ := os.ReadFile(logf)
	if !strings.Contains(string(b), "add-generic-password -U -s claude-hub -a key-a") {
		t.Fatalf("stdin command not sent:\n%s", b)
	}
}

func TestSecretToolNeverPutsTheTokenInArgv(t *testing.T) {
	bin, logf := standIn(t, "secret-tool")
	old := SecretToolBin
	SecretToolBin = bin
	defer func() { SecretToolBin = old }()
	s := &SecretTool{}
	if _, err := s.Get("key-a"); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := s.Set("key-a", tok); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("key-a"); err != nil || got != tok {
		t.Fatal(got, err)
	}
	for _, l := range argLines(t, logf) {
		if strings.Contains(l, tok) {
			t.Fatalf("token in argv: %s", l)
		}
	}
	if err := s.Delete("key-a"); err != nil {
		t.Fatal(err)
	}
}

func TestBackendErrorsDoNotEchoToolOutput(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "security")
	os.WriteFile(bin, []byte("#!/bin/sh\necho "+tok+"; echo "+tok+" >&2; exit 51\n"), 0o755)
	old := SecurityBin
	SecurityBin = bin
	defer func() { SecurityBin = old }()
	_, err := (&Keychain{}).Get("k")
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatal(err)
	}
	err = (&Keychain{}).Set("k", tok)
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatal(err)
	}
}

// TestRealKeychain runs against a throwaway keychain file. It is opt-in (CI's macOS job sets
// CLAUDE_HUB_TEST_KEYCHAIN=1) and never touches the user's keychains.
func TestRealKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("CLAUDE_HUB_TEST_KEYCHAIN") != "1" {
		t.Skip("set CLAUDE_HUB_TEST_KEYCHAIN=1 on macOS")
	}
	kc := filepath.Join(t.TempDir(), "hub-test.keychain-db")
	if out, err := exec.Command("/usr/bin/security", "create-keychain", "-p", "test", kc).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	defer exec.Command("/usr/bin/security", "delete-keychain", kc).Run()
	exec.Command("/usr/bin/security", "unlock-keychain", "-p", "test", kc).Run()
	k := &Keychain{Path: kc}
	if err := k.Set("key-a", tok); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get("key-a"); err != nil || got != tok {
		t.Fatal(got, err)
	}
	if err := k.Delete("key-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("key-a"); err != ErrNotFound {
		t.Fatal(err)
	}
}
