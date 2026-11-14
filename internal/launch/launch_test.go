package launch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wonsong82/claude-hub/internal/config"
)

func TestApply(t *testing.T) {
	c := config.New("c", "/home/u")
	c.Profiles["work"] = &config.Profile{Dir: "~/.claude-work", Env: map[string]string{"X": "1"}}
	c.Profiles["main"] = &config.Profile{}
	e := NewEnv([]string{"CLAUDE_CONFIG_DIR=/old", "CLAUDE_CODE_OAUTH_TOKEN=old", "ANTHROPIC_API_KEY=k", "ANTHROPIC_AUTH_TOKEN=a", "PATH=/bin"})
	Apply(e, c, "work", "", false, "", true)
	want := map[string]string{"CLAUDE_CONFIG_DIR": "/home/u/.claude-work", "CLAUDE_HUB_PROFILE": "work", "X": "1", "PATH": "/bin"}
	for k, v := range want {
		if got, _ := e.Get(k); got != v {
			t.Errorf("%s=%q", k, got)
		}
	}
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if _, ok := e.Get(k); ok {
			t.Errorf("%s still set", k)
		}
	}
	if _, ok := e.Get(VarGuard); !ok {
		t.Error("no guard")
	}
	e = NewEnv([]string{"CLAUDE_CONFIG_DIR=/old"})
	Apply(e, c, "main", "tok", true, "/t", false)
	if _, ok := e.Get("CLAUDE_CONFIG_DIR"); ok {
		t.Error("a profile without dir must unset CLAUDE_CONFIG_DIR")
	}
	if v, _ := e.Get("CLAUDE_CODE_OAUTH_TOKEN"); v != "tok" {
		t.Error("token")
	}
	if v, _ := e.Get(VarTmp); v != "/t" {
		t.Error("tmp")
	}
	if _, ok := e.Get(VarGuard); ok {
		t.Error("guard set for exec")
	}
}

func TestCleanTmp(t *testing.T) {
	home := t.TempDir()
	for _, bad := range []string{"/", home, "/tmp", "/var/tmp"} {
		if SafeTmpDir(bad, home) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	d := filepath.Join(home, "ctmp")
	os.MkdirAll(filepath.Join(d, "sub"), 0o700)
	old, fresh := filepath.Join(d, "sub", "old"), filepath.Join(d, "fresh")
	os.WriteFile(old, []byte("x"), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	past := time.Now().Add(-10 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(filepath.Join(d, "sub"), past, past)
	if err := CleanTmp(d, 7, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("old file kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh file deleted")
	}
}

func TestRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	p := xdgPaths()
	os.MkdirAll(p.RunDir(), 0o700)
	os.WriteFile(filepath.Join(p.RunDir(), "999999"), []byte("gone\n"), 0o600)
	os.WriteFile(filepath.Join(p.RunDir(), "1"), []byte("init\n"), 0o600)
	if Stale(p) != 1 {
		t.Fatal("stale count")
	}
	live := Live(p)
	if len(live) != 1 || live[0].Profile != "init" {
		t.Fatal(live)
	}
	if _, err := os.Stat(filepath.Join(p.RunDir(), "999999")); err == nil {
		t.Fatal("not pruned")
	}
}
