// Package launch builds a profile's environment, keeps the run registry, and execs the
// real claude (design section 9, steps 7 to 11).
package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Env is an environment as an ordered set.
type Env struct {
	keys []string
	m    map[string]string
}

// NewEnv parses KEY=VALUE pairs.
func NewEnv(pairs []string) *Env {
	e := &Env{m: map[string]string{}}
	for _, kv := range pairs {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		e.Set(kv[:i], kv[i+1:])
	}
	return e
}

// Get returns a value.
func (e *Env) Get(k string) (string, bool) {
	v, ok := e.m[k]
	return v, ok
}

// Set sets a value.
func (e *Env) Set(k, v string) {
	if _, ok := e.m[k]; !ok {
		e.keys = append(e.keys, k)
	}
	e.m[k] = v
}

// Unset removes a value.
func (e *Env) Unset(k string) {
	if _, ok := e.m[k]; !ok {
		return
	}
	delete(e.m, k)
	for i, x := range e.keys {
		if x == k {
			e.keys = append(e.keys[:i], e.keys[i+1:]...)
			return
		}
	}
}

// Pairs returns KEY=VALUE pairs in insertion order.
func (e *Env) Pairs() []string {
	out := make([]string, 0, len(e.keys))
	for _, k := range e.keys {
		out = append(out, k+"="+e.m[k])
	}
	return out
}

// Vars set or cleared by the hub.
const (
	VarConfigDir = "CLAUDE_CONFIG_DIR"
	VarToken     = "CLAUDE_CODE_OAUTH_TOKEN"
	VarProfile   = "CLAUDE_HUB_PROFILE"
	VarGuard     = "CLAUDE_HUB_GUARD"
	VarSelect    = "CLAUDE_HUB_SELECT"
	VarTmp       = "CLAUDE_CODE_TMPDIR"
)

// APIKeyVars are unset for every selected profile (decision 16).
var APIKeyVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

// Apply sets the environment fully determined by the profile (design section 9, step 7).
// token is used when hasKey is true. tmpDir is the usable [tmp] dir or "". guard sets
// CLAUDE_HUB_GUARD to this PID (only when the hub execs claude itself).
func Apply(e *Env, c *config.Config, name, token string, hasKey bool, tmpDir string, guard bool) {
	pr := c.Profiles[name]
	keys := make([]string, 0, len(pr.Env))
	for k := range pr.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e.Set(k, pr.Env[k])
	}
	if pr.Dir == "" {
		e.Unset(VarConfigDir)
	} else {
		e.Set(VarConfigDir, c.ProfileDir(name))
	}
	if hasKey {
		e.Set(VarToken, token)
	} else {
		e.Unset(VarToken)
	}
	for _, k := range APIKeyVars {
		e.Unset(k)
	}
	e.Set(VarProfile, name)
	if tmpDir != "" {
		e.Set(VarTmp, tmpDir)
	}
	if guard {
		e.Set(VarGuard, strconv.Itoa(os.Getpid()))
	}
}

// PrepareTmp creates the [tmp] dir (mode 0700) and returns it when usable.
func PrepareTmp(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	return dir, nil
}

// Exec replaces the process with path. argv[0] is the path as found.
func Exec(path string, args []string, env []string) error {
	argv := append([]string{path}, args...)
	return syscall.Exec(path, argv, env)
}

// Register writes the run registry entry for this PID (which becomes Claude's).
func Register(p xdg.Paths, profile string) {
	d := p.RunDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(d, strconv.Itoa(os.Getpid())), []byte(profile+"\n"), 0o600)
}

// Alive reports whether a PID exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Run is a live registry entry.
type Run struct {
	PID     int
	Profile string
}

// Live returns live runs and prunes entries whose PID is gone.
func Live(p xdg.Paths) []Run {
	ents, err := os.ReadDir(p.RunDir())
	if err != nil {
		return nil
	}
	var out []Run
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		path := filepath.Join(p.RunDir(), e.Name())
		if err != nil {
			continue
		}
		if !Alive(pid) || pid == os.Getpid() {
			if pid != os.Getpid() {
				_ = os.Remove(path)
			}
			continue
		}
		b, _ := os.ReadFile(path)
		out = append(out, Run{PID: pid, Profile: strings.TrimSpace(string(b))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// LiveFor returns live runs of one profile.
func LiveFor(p xdg.Paths, profile string) []Run {
	var out []Run
	for _, r := range Live(p) {
		if r.Profile == profile {
			out = append(out, r)
		}
	}
	return out
}

// Stale counts registry entries whose PID is gone, without pruning.
func Stale(p xdg.Paths) int {
	ents, err := os.ReadDir(p.RunDir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if pid, err := strconv.Atoi(e.Name()); err == nil && !Alive(pid) {
			n++
		}
	}
	return n
}

// SafeTmpDir refuses dangerous cleanup targets.
func SafeTmpDir(dir, home string) error {
	d := filepath.Clean(dir)
	for _, bad := range []string{"/", filepath.Clean(home), "/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp"} {
		if d == bad {
			return fmt.Errorf("refusing to clean %s", d)
		}
	}
	fi, err := os.Lstat(d)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", d)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by you", d)
	}
	return nil
}

// StartTmpClean starts a detached child that deletes old files under dir.
func StartTmpClean(self, dir string, days int, home string) {
	if days <= 0 || SafeTmpDir(dir, home) != nil {
		return
	}
	cmd := exec.Command(self, "__clean-tmp", dir, strconv.Itoa(days))
	cmd.Args[0] = xdg.Name
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if devnull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// CleanTmp deletes files under dir not modified for days, then empty folders as old.
func CleanTmp(dir string, days int, home string) error {
	if err := SafeTmpDir(dir, home); err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var dirs []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		if fi, err := os.Lstat(dirs[i]); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(dirs[i]) // only when empty
		}
	}
	return nil
}
