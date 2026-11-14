// Package shim installs the opt-in `claude` shim: one symlink in the hub's shim folder and
// a marked PATH block at the end of the shell rc files (design section 10).
package shim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Markers of the managed block.
const (
	BeginMarker = "# >>> claude-hub shim >>>"
	EndMarker   = "# <<< claude-hub shim <<<"
	beginLine   = BeginMarker + ` managed by "claude-hub install-shim"; keep this block last`
)

// StablePath returns the hub path a shim should point to: for a Homebrew install the
// prefix's bin link (never a Cellar path), else the executable as installed.
func StablePath(exe string) string {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		real = exe
	}
	if i := strings.Index(real, "/Cellar/"+xdg.Name+"/"); i >= 0 {
		cand := filepath.Join(real[:i], "bin", xdg.Name)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if strings.Contains(exe, "/Cellar/") {
		return real
	}
	return exe
}

// IsHomebrew reports whether exe lives in a Homebrew Cellar.
func IsHomebrew(exe string) bool {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		real = exe
	}
	return strings.Contains(real, "/Cellar/")
}

// pathExpr writes dir with $HOME substituted, as the rc block uses it.
func pathExpr(dir, home string) string {
	if home != "" && strings.HasPrefix(dir, home+"/") {
		return "$HOME/" + dir[len(home)+1:]
	}
	return dir
}

// Block returns the rc block for a shell.
func Block(shell, shimDir, home string) string {
	d := pathExpr(shimDir, home)
	if shell == "fish" {
		return fmt.Sprintf("%s\nfish_add_path --move --prepend --path \"%s\"\n%s\n", beginLine, d, EndMarker)
	}
	return fmt.Sprintf("%s\ncase \"$PATH\" in \"%s\":*) ;; *) export PATH=\"%s:$PATH\" ;; esac\n%s\n", beginLine, d, d, EndMarker)
}

// RCFiles returns the rc files for a shell.
func RCFiles(shell, home string) []string {
	switch shell {
	case "zsh":
		zd := os.Getenv("ZDOTDIR")
		if zd == "" {
			zd = home
		}
		return []string{filepath.Join(zd, ".zprofile"), filepath.Join(zd, ".zshrc")}
	case "bash":
		login := filepath.Join(home, ".bash_profile")
		if !fsutil.Exists(login) && fsutil.Exists(filepath.Join(home, ".profile")) {
			login = filepath.Join(home, ".profile")
		}
		return []string{login, filepath.Join(home, ".bashrc")}
	case "fish":
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		return []string{filepath.Join(cfg, "fish", "conf.d", xdg.Name+".fish")}
	}
	return nil
}

// AllRCFiles returns every file any shell may hold a block in.
func AllRCFiles(home string) []string {
	var out []string
	seen := map[string]bool{}
	for _, sh := range []string{"zsh", "bash", "fish"} {
		for _, f := range RCFiles(sh, home) {
			if !seen[f] {
				out = append(out, f)
				seen[f] = true
			}
		}
	}
	if p := filepath.Join(home, ".profile"); !seen[p] {
		out = append(out, p)
	}
	if p := filepath.Join(home, ".bash_profile"); !seen[p] {
		out = append(out, p)
	}
	return out
}

// RemoveBlock removes every managed block from text.
func RemoveBlock(text string) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	var out strings.Builder
	in, found := false, false
	for _, l := range lines {
		t := strings.TrimRight(l, "\r\n")
		if !in && strings.HasPrefix(t, BeginMarker) {
			in, found = true, true
			continue
		}
		if in {
			if strings.HasPrefix(t, EndMarker) {
				in = false
			}
			continue
		}
		out.WriteString(l)
	}
	return out.String(), found
}

// BlockIsLast reports whether the file has the block and nothing but blank lines after it.
func BlockIsLast(text string) (present, last bool) {
	i := strings.LastIndex(text, EndMarker)
	if i < 0 {
		return strings.Contains(text, BeginMarker), false
	}
	rest := text[i+len(EndMarker):]
	return true, strings.TrimSpace(rest) == ""
}

// AppendBlock removes any block and appends a fresh one at the end.
func AppendBlock(text, block string) string {
	t, _ := RemoveBlock(text)
	if t != "" && !strings.HasSuffix(t, "\n") {
		t += "\n"
	}
	return t + block
}

// Change is one file the shim install or uninstall touches.
type Change struct {
	File   string
	Action string // add, repair, remove, create
}

// Install creates the shim link and writes the rc blocks. With dryRun nothing changes.
func Install(p xdg.Paths, target string, shells []string, dryRun bool) ([]Change, error) {
	var changes []Change
	sd := p.ShimDir()
	link := filepath.Join(sd, "claude")
	if !(fsutil.IsSymlink(link) && fsutil.LinkTarget(link) == target) {
		changes = append(changes, Change{File: link, Action: "create"})
	}
	for _, sh := range shells {
		block := Block(sh, sd, p.Home)
		for _, f := range RCFiles(sh, p.Home) {
			old, err := os.ReadFile(f)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			newText := AppendBlock(string(old), block)
			if sh == "fish" {
				newText = block
			}
			if string(old) == newText {
				continue
			}
			act := "add"
			if strings.Contains(string(old), BeginMarker) {
				act = "repair"
			}
			changes = append(changes, Change{File: f, Action: act})
			if dryRun {
				continue
			}
			if err == nil && !fsutil.Exists(f+".claude-hub.bak") && sh != "fish" {
				if err := os.WriteFile(f+".claude-hub.bak", old, 0o600); err != nil {
					return nil, err
				}
			}
			perm := os.FileMode(0o644)
			if fi, err := os.Stat(f); err == nil {
				perm = fi.Mode().Perm()
			}
			if err := fsutil.WriteFileAtomic(f, []byte(newText), perm); err != nil {
				return nil, err
			}
		}
	}
	if dryRun {
		return changes, nil
	}
	if err := os.MkdirAll(sd, 0o755); err != nil {
		return nil, err
	}
	// The folder holds exactly one entry.
	ents, _ := os.ReadDir(sd)
	for _, e := range ents {
		if e.Name() != "claude" {
			return nil, fmt.Errorf("%s holds %s; it must hold only the claude link", sd, e.Name())
		}
	}
	if fsutil.Exists(link) && !(fsutil.IsSymlink(link) && fsutil.LinkTarget(link) == target) {
		if !fsutil.IsSymlink(link) {
			return nil, fmt.Errorf("%s is not a link; remove it by hand", link)
		}
		if err := os.Remove(link); err != nil {
			return nil, err
		}
	}
	if !fsutil.Exists(link) {
		if err := os.Symlink(target, link); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// Uninstall removes the blocks from every rc file and the shim folder.
func Uninstall(p xdg.Paths, dryRun bool) ([]Change, error) {
	var changes []Change
	for _, f := range AllRCFiles(p.Home) {
		old, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		newText, found := RemoveBlock(string(old))
		if !found {
			continue
		}
		changes = append(changes, Change{File: f, Action: "remove"})
		if dryRun {
			continue
		}
		if strings.HasSuffix(f, ".fish") && strings.TrimSpace(newText) == "" {
			if err := os.Remove(f); err != nil {
				return nil, err
			}
			continue
		}
		fi, _ := os.Stat(f)
		if err := fsutil.WriteFileAtomic(f, []byte(newText), fi.Mode().Perm()); err != nil {
			return nil, err
		}
	}
	sd := p.ShimDir()
	if fsutil.Exists(sd) {
		changes = append(changes, Change{File: sd, Action: "remove"})
		if !dryRun {
			ents, _ := os.ReadDir(sd)
			for _, e := range ents {
				if e.Name() != "claude" {
					return nil, fmt.Errorf("%s holds %s besides the shim; not removing it", sd, e.Name())
				}
			}
			link := filepath.Join(sd, "claude")
			if fsutil.Exists(link) && !fsutil.IsSymlink(link) {
				return nil, fmt.Errorf("%s is not a link; not removing it", link)
			}
			_ = os.Remove(link)
			if err := os.Remove(sd); err != nil {
				return nil, err
			}
		}
	}
	return changes, nil
}

// Installed reports whether the shim link exists.
func Installed(p xdg.Paths) bool { return fsutil.IsSymlink(filepath.Join(p.ShimDir(), "claude")) }

// CleanShell runs a command in a clean login-and-interactive shell and returns its last
// output line.
func CleanShell(shellPath, home, command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	env := []string{"HOME=" + home, "TERM=" + orDefault(os.Getenv("TERM"), "dumb"), "USER=" + os.Getenv("USER"), "LOGNAME=" + os.Getenv("LOGNAME"),
		"SHELL=" + shellPath}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "ZDOTDIR", "CLAUDE_HUB_CONFIG"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	var args []string
	if filepath.Base(shellPath) == "fish" {
		args = []string{"-l", "-i", "-c", command}
	} else {
		args = []string{"-lic", command}
	}
	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.Env = env
	cmd.Dir = home
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	cmd.Stdin = nil
	err := cmd.Run()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if err != nil && last == "" {
		return "", err
	}
	return last, nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// Shells returns every supported shell present on this machine (zsh, bash, fish), so the
// shim is on PATH whichever of them a terminal, an app or a tool starts.
func Shells() []string {
	var out []string
	for _, s := range []string{"zsh", "bash", "fish"} {
		if ShellPath(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// ShellPath finds a shell binary by name.
func ShellPath(name string) string {
	if login := os.Getenv("SHELL"); filepath.Base(login) == name {
		return login
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// Verify checks in a clean shell that `claude` resolves to the shim and that
// `claude --version` runs.
func Verify(p xdg.Paths, shellPath string) (resolved, versionLine string, ok bool) {
	link := filepath.Join(p.ShimDir(), "claude")
	resolved, err := CleanShell(shellPath, p.Home, "command -v claude")
	if err != nil {
		return "", "", false
	}
	if filepath.Clean(resolved) != link {
		return resolved, "", false
	}
	v, err := CleanShell(shellPath, p.Home, "claude --version")
	if err != nil || v == "" {
		return resolved, v, false
	}
	return resolved, v, true
}
