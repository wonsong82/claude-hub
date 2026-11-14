package e2e

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// treeHash hashes names, link targets and contents under root, without following links.
func treeHash(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			tgt, _ := os.Readlink(p)
			lines = append(lines, rel+" -> "+tgt)
		case d.Type().IsRegular():
			b, _ := os.ReadFile(p)
			lines = append(lines, fmt.Sprintf("%s %x", rel, sha256.Sum256(b)))
		default:
			lines = append(lines, rel+"/")
		}
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func findShell(name string) string {
	for _, p := range []string{"/bin/" + name, "/usr/bin/" + name, "/usr/local/bin/" + name, "/opt/homebrew/bin/" + name} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// shimEnv installs the hub at a stable path and a fake Claude install: ~/.local/bin/claude
// -> ~/.local/share/claude/versions/1.0.
func shimEnv(t *testing.T) (*hubEnv, string) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	stable := filepath.Join(e.home, "hubinstall", "claude-hub")
	b, err := os.ReadFile(hubBin)
	if err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Dir(stable))
	if err := os.WriteFile(stable, b, 0o755); err != nil {
		t.Fatal(err)
	}
	vers := filepath.Join(e.home, ".local", "share", "claude", "versions")
	writeExec(t, filepath.Join(vers, "1.0"), "#!/bin/sh\necho \"real=1.0 profile=${CLAUDE_HUB_PROFILE-unset} args=$*\"\n")
	mustMkdir(t, filepath.Join(e.home, ".local", "bin"))
	if err := os.Symlink(filepath.Join(vers, "1.0"), filepath.Join(e.home, ".local", "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	e.path = "/usr/bin:/bin"
	return e, stable
}

func TestShimInstallRepairUninstall(t *testing.T) {
	zsh, bash := findShell("zsh"), findShell("bash")
	if zsh == "" && bash == "" {
		t.Skip("no zsh or bash")
	}
	e, stable := shimEnv(t)
	installBefore := treeHash(t, filepath.Join(e.home, ".local", "share", "claude"))
	linkBefore, _ := os.Readlink(filepath.Join(e.home, ".local", "bin", "claude"))

	original := "# user settings\nexport FOO=1\n"
	var shells []string
	var files []string
	if zsh != "" {
		shells = append(shells, "zsh")
		files = append(files, filepath.Join(e.home, ".zprofile"), filepath.Join(e.home, ".zshrc"))
	}
	if bash != "" {
		shells = append(shells, "bash")
		files = append(files, filepath.Join(e.home, ".bash_profile"), filepath.Join(e.home, ".bashrc"))
	}
	for _, f := range files {
		writeFile(t, f, original)
	}
	r := e.runPath(stable, "claude-hub", "install-shim", "--shell", strings.Join(shells, ","))
	if r.code != 0 {
		t.Fatalf("install-shim: %s", r)
	}
	shimLink := filepath.Join(e.home, ".local", "share", "claude-hub", "shim", "claude")
	if tgt, _ := os.Readlink(shimLink); tgt != stable {
		t.Fatalf("shim target %q", tgt)
	}
	ents, _ := os.ReadDir(filepath.Dir(shimLink))
	if len(ents) != 1 {
		t.Fatal("the shim folder holds more than claude")
	}
	for _, s := range shells {
		expectContains(t, r.out, s+": claude -> ~/.local/share/claude-hub/shim/claude; claude --version: real=1.0")
	}
	for _, f := range files {
		body := readFile(t, f)
		if !strings.HasPrefix(body, original) || !strings.HasSuffix(body, "# <<< claude-hub shim <<<\n") {
			t.Fatalf("%s:\n%s", f, body)
		}
		if _, err := os.Stat(f + ".claude-hub.bak"); err != nil {
			t.Fatal("no backup of", f)
		}
	}
	// A third-party installer appends after the block; doctor sees it, install-shim repairs it.
	for _, f := range files {
		writeFile(t, f, readFile(t, f)+"export PATH=\"$HOME/.local/bin:$PATH\"\n")
	}
	e.extra["SHELL"] = map[bool]string{true: zsh, false: bash}[zsh != ""]
	delete(e.extra, "CLAUDE_HUB_DOCTOR_NO_EXEC")
	d := e.runPath(stable, "claude-hub", "doctor")
	expectContains(t, d.out, "is not last")
	r = e.runPath(stable, "claude-hub", "install-shim", "--shell", strings.Join(shells, ","))
	if r.code != 0 {
		t.Fatalf("repair: %s", r)
	}
	for _, f := range files {
		body := readFile(t, f)
		if !strings.HasSuffix(body, "# <<< claude-hub shim <<<\n") || strings.Count(body, "# >>> claude-hub shim >>>") != 1 {
			t.Fatalf("not repaired: %s", body)
		}
	}
	// Under the name claude, every subcommand reaches the real one.
	for _, args := range [][]string{{"update"}, {"doctor"}, {"install"}} {
		rr := e.runPath(shimLink, "claude", args...)
		expectContains(t, rr.out, "real=1.0 profile=unset args="+args[0])
	}
	// Uninstall restores the files apart from the removed block.
	r = e.runPath(stable, "claude-hub", "uninstall-shim")
	if r.code != 0 {
		t.Fatalf("uninstall-shim: %s", r)
	}
	for _, f := range files {
		want := original + "export PATH=\"$HOME/.local/bin:$PATH\"\n"
		if got := readFile(t, f); got != want {
			t.Fatalf("%s not restored:\n%q\nwant\n%q", f, got, want)
		}
	}
	if _, err := os.Stat(filepath.Dir(shimLink)); err == nil {
		t.Fatal("the shim folder is still there")
	}
	// Auto-update invariants: Claude's install is untouched.
	if treeHash(t, filepath.Join(e.home, ".local", "share", "claude")) != installBefore {
		t.Fatal("Claude's versions folder changed")
	}
	if l, _ := os.Readlink(filepath.Join(e.home, ".local", "bin", "claude")); l != linkBefore {
		t.Fatal("~/.local/bin/claude changed")
	}
}

func TestShimFish(t *testing.T) {
	fish := findShell("fish")
	if fish == "" {
		t.Skip("fish not installed")
	}
	e, stable := shimEnv(t)
	r := e.runPath(stable, "claude-hub", "install-shim", "--shell", "fish")
	if r.code != 0 {
		t.Fatalf("install-shim fish: %s", r)
	}
	f := filepath.Join(e.home, ".config", "fish", "conf.d", "claude-hub.fish")
	expectContains(t, readFile(t, f), "fish_add_path --move --prepend")
	e.runPath(stable, "claude-hub", "uninstall-shim")
	if _, err := os.Stat(f); err == nil {
		t.Fatal("fish file left behind")
	}
}

func TestShimDryRun(t *testing.T) {
	e, stable := shimEnv(t)
	writeFile(t, filepath.Join(e.home, ".zshrc"), "x\n")
	r := e.runPath(stable, "claude-hub", "install-shim", "--shell", "zsh", "--dry-run")
	expectContains(t, r.out, "would add the block to ~/.zshrc")
	if readFile(t, filepath.Join(e.home, ".zshrc")) != "x\n" {
		t.Fatal("dry run changed a file")
	}
}

func TestAutoUpdateInvariantsDuringLaunch(t *testing.T) {
	e, stable := shimEnv(t)
	before := treeHash(t, filepath.Join(e.home, ".local"))
	mustMkdir(t, filepath.Join(e.home, ".claude"))
	e.ok("profile", "add", "main", "--dir", "~/.claude", "--login")
	r := e.runPath(stable, "claude-hub", "main", "update")
	expectContains(t, r.out, "real=1.0 profile=main args=update")
	e.runPath(stable, "claude-hub", "doctor")
	after := treeHash(t, filepath.Join(e.home, ".local"))
	// The hub writes only its own share/claude-hub and state/claude-hub folders.
	var filtered []string
	for _, l := range strings.Split(after, "\n") {
		if !strings.HasPrefix(l, "share/claude-hub") && !strings.HasPrefix(l, "state") {
			filtered = append(filtered, l)
		}
	}
	if strings.Join(filtered, "\n") != before {
		t.Fatalf("Claude's install changed:\n%s\n---\n%s", before, strings.Join(filtered, "\n"))
	}
	env := strings.Join(e.environ(), " ")
	if strings.Contains(env, "DISABLE_AUTOUPDATER") {
		t.Fatal("test env sets DISABLE_AUTOUPDATER")
	}
	r = e.runPath(stable, "claude-hub", "exec", "main", "--", "sh", "-c", "echo ${DISABLE_AUTOUPDATER-unset}")
	expectOut(t, r, "unset")
}
