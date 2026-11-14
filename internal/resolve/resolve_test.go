package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wonsong82/claude-hub/internal/config"
)

func mk(t *testing.T, p, body string, mode os.FileMode) {
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFindClaude(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	self := filepath.Join(root, "hub", "claude-hub")
	mk(t, self, "hub", 0o755)
	shimDir := filepath.Join(root, "shim")
	os.MkdirAll(shimDir, 0o755)
	os.Symlink(self, filepath.Join(shimDir, "claude"))
	other := filepath.Join(root, "other")
	os.MkdirAll(other, 0o755)
	os.Symlink(self, filepath.Join(other, "claude")) // another link to the hub itself
	noexec := filepath.Join(root, "noexec", "claude")
	mk(t, noexec, "x", 0o644)
	real := filepath.Join(root, "real", "claude")
	versions := filepath.Join(root, "versions", "1.0")
	mk(t, versions, "real", 0o755)
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.Symlink(versions, real)
	home := filepath.Join(root, "home")
	path := shimDir + ":" + other + ":" + filepath.Dir(noexec) + ":" + filepath.Dir(real)
	if got := FindClaude(nil, self, shimDir, path, home); got != real {
		t.Fatalf("got %q, want the unresolved %q", got, real)
	}
	// Only the hub: nothing found, unless ~/.local/bin/claude exists.
	if got := FindClaude(nil, self, shimDir, shimDir+":"+other, home); got != "" {
		t.Fatal(got)
	}
	lb := filepath.Join(home, ".local", "bin", "claude")
	mk(t, lb, "x", 0o755)
	if got := FindClaude(nil, self, shimDir, shimDir, home); got != lb {
		t.Fatal(got)
	}
	// hub.claude_bin wins, and must be executable.
	c := config.New("c", home)
	c.Hub.ClaudeBin = versions
	if got := FindClaude(c, self, shimDir, path, home); got != versions {
		t.Fatal(got)
	}
	c.Hub.ClaudeBin = noexec
	if got := FindClaude(c, self, shimDir, path, home); got != "" {
		t.Fatal(got)
	}
}

func TestSelectPrecedence(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	home := filepath.Join(root, "home")
	deep := filepath.Join(home, "ws", "a", "b")
	os.MkdirAll(deep, 0o755)
	c := config.New("c", home)
	for _, n := range []string{"d", "rule", "rule2", "file", "env"} {
		c.Profiles[n] = &config.Profile{Dir: "~/.claude-" + n}
	}
	type step struct {
		setup    func()
		env      string
		want     string
		wantKind string
	}
	steps := []step{
		{func() {}, "", "", "none"},
		{func() { c.Hub.DefaultProfile = "d" }, "", "d", "default"},
		{func() { c.Directory = append(c.Directory, config.DirRule{Path: "~/ws", Profile: "rule"}) }, "", "rule", "rule"},
		{func() { c.Directory = append(c.Directory, config.DirRule{Path: "~/ws/a", Profile: "rule2"}) }, "", "rule2", "rule"},
		{func() { mk(t, filepath.Join(home, "ws", ".claude-profile"), "\n file \n", 0o644) }, "", "file", "file"},
		{func() {}, "env", "env", "env"},
	}
	for i, s := range steps {
		s.setup()
		got, src, err := Select(c, deep, home, s.env)
		if err != nil || got != s.want || src.Kind != s.wantKind {
			t.Fatalf("step %d: got %q %v %v", i, got, src, err)
		}
	}
	// A rule path that is a prefix string but not a parent folder does not match.
	c2 := config.New("c", home)
	c2.Profiles["x"] = &config.Profile{}
	c2.Directory = []config.DirRule{{Path: "~/w", Profile: "x"}}
	if _, ok := MatchRule(c2, filepath.Join(home, "ws")); ok {
		t.Fatal("~/w matched ~/ws")
	}
	// Unknown names are errors naming the source.
	_, _, err := Select(c, deep, home, "ghost")
	if ue, ok := err.(*UnknownError); !ok || ue.Source.Kind != "env" {
		t.Fatal(err)
	}
	mk(t, filepath.Join(deep, ".claude-profile"), "ghost", 0o644)
	_, _, err = Select(c, deep, home, "")
	if ue, ok := err.(*UnknownError); !ok || ue.Source.Path != filepath.Join(deep, ".claude-profile") {
		t.Fatal(err)
	}
}
