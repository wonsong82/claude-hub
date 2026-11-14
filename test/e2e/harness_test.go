// Package e2e runs the claude-hub binary end to end against a stand-in `claude`, in a
// temporary HOME with temporary XDG dirs. No test uses a real token or the real keychain.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var hubBin string

// The fixture token. Every command's output is checked for it.
const fixtureToken = "test-token-0123456789abcdef"

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "claude-hub-e2e-bin-")
	if err != nil {
		panic(err)
	}
	hubBin = filepath.Join(dir, "claude-hub")
	cmd := exec.Command("go", "build", "-o", hubBin, "../../cmd/claude-hub")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// standIn prints its environment with the token reduced to its length.
const standIn = `#!/bin/sh
t="${CLAUDE_CODE_OAUTH_TOKEN-}"; if [ -n "${CLAUDE_CODE_OAUTH_TOKEN+x}" ]; then t="set(len ${#t})"; else t="unset"; fi
a="${ANTHROPIC_API_KEY+set}"; a="${a:-unset}"
b="${ANTHROPIC_AUTH_TOKEN+set}"; b="${b:-unset}"
printf 'dir=%s token=%s profile=%s api=%s auth=%s args=' "${CLAUDE_CONFIG_DIR-unset}" "$t" "${CLAUDE_HUB_PROFILE-unset}" "$a" "$b"
for x in "$@"; do printf '[%s]' "$x"; done
printf '\n'
`

type hubEnv struct {
	t     *testing.T
	root  string
	home  string
	bin   string // folder holding the stand-in claude
	cwd   string
	path  string
	extra map[string]string
	unset map[string]bool
	stdin string
}

func newEnv(t *testing.T) *hubEnv {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	e := &hubEnv{t: t, root: root, home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), extra: map[string]string{}, unset: map[string]bool{}}
	mustMkdir(t, e.home)
	mustMkdir(t, e.bin)
	writeExec(t, filepath.Join(e.bin, "claude"), standIn)
	e.cwd = e.home
	e.path = e.bin + ":/usr/bin:/bin"
	return e
}

func mustMkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeExec(t *testing.T, p, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *hubEnv) configFile() string {
	return filepath.Join(e.home, ".config", "claude-hub", "config.toml")
}

func (e *hubEnv) writeConfig(body string) { writeFile(e.t, e.configFile(), body) }

// fileBackendConfig is a minimal config that keeps tests off the real keychain.
const fileBackendConfig = `schema = 1

[hub]
default_profile = ""
base_dir = "~/.claude"

[secrets]
backend = "file"
`

func (e *hubEnv) environ() []string {
	env := []string{
		"HOME=" + e.home,
		"PATH=" + e.path,
		"TERM=dumb",
		"SHELL=/bin/sh",
		"USER=tester",
		"LANG=C",
		"CLAUDE_HUB_DOCTOR_NO_EXEC=1",
	}
	for k, v := range e.extra {
		env = append(env, k+"="+v)
	}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !e.unset[k] {
			out = append(out, kv)
		}
	}
	return out
}

type result struct {
	out, err string
	code     int
}

func (r result) String() string {
	return fmt.Sprintf("rc=%d stdout=%q stderr=%q", r.code, r.out, r.err)
}

// runPath runs the executable at path with argv[0] = name.
func (e *hubEnv) runPath(path, name string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Args[0] = name
	cmd.Env = e.environ()
	cmd.Dir = e.cwd
	cmd.Stdin = strings.NewReader(e.stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("run %s: %v", name, err)
	}
	r := result{so.String(), se.String(), code}
	if strings.Contains(r.out, fixtureToken) || strings.Contains(r.err, fixtureToken) {
		e.t.Fatalf("the fixture token leaked into the output of %v: %s", args, r)
	}
	e.stdin = ""
	return r
}

// hub runs claude-hub.
func (e *hubEnv) hub(args ...string) result {
	e.t.Helper()
	return e.runPath(hubBin, hubBin, args...)
}

// ok runs claude-hub and fails the test on a non-zero exit.
func (e *hubEnv) ok(args ...string) result {
	e.t.Helper()
	r := e.hub(args...)
	if r.code != 0 {
		e.t.Fatalf("claude-hub %v: %s", args, r)
	}
	return r
}

// withToken feeds the fixture token (or another) on stdin to the next command.
func (e *hubEnv) withToken(tok string) *hubEnv {
	e.stdin = tok + "\n"
	return e
}

func expectCode(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("want exit %d, got %s", code, r)
	}
}

func expectOut(t *testing.T, r result, want string) {
	t.Helper()
	if strings.TrimSpace(r.out) != want {
		t.Fatalf("stdout:\n got %q\nwant %q\n(%s)", strings.TrimSpace(r.out), want, r)
	}
}

func expectContains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%q does not contain %q", s, sub)
	}
}

func lenStr(s string) string { return fmt.Sprintf("set(len %d)", len(s)) }

func isDarwin() bool { return runtime.GOOS == "darwin" }
