package migrate

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/share"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

type fx struct {
	p          xdg.Paths
	c          *config.Config
	base, work string
}

func setup(t *testing.T) *fx {
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "CLAUDE_HUB_CONFIG"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", home)
	p := xdg.Get()
	c := config.New(p.ConfigFile, home)
	c.Profiles["base"] = &config.Profile{Dir: "~/.claude"}
	sh := &config.Share{Extra: []string{}}
	for _, it := range config.ShareItems {
		sh.Set(it, config.Own)
	}
	c.Profiles["work"] = &config.Profile{Dir: "~/.claude-work", Share: sh, Settings: &config.SettingsCfg{Mode: config.ModeOwn}}
	os.MkdirAll(filepath.Dir(c.Path), 0o755)
	if err := c.Save(p.LockDir()); err != nil {
		t.Fatal(err)
	}
	f := &fx{p: p, c: c, base: filepath.Join(home, ".claude"), work: filepath.Join(home, ".claude-work")}
	os.MkdirAll(f.base, 0o700)
	os.MkdirAll(f.work, 0o700)
	return f
}

func put(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// contents is the multiset of regular-file contents under roots (links not followed).
func contents(roots ...string) map[string]int {
	m := map[string]int{}
	for _, r := range roots {
		_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && filepath.Base(p) != "MANIFEST.json" {
				b, _ := os.ReadFile(p)
				m[fmt.Sprintf("%x", sha256.Sum256(b))]++
			}
			return nil
		})
	}
	return m
}

func contained(before, after map[string]int) bool {
	for k, n := range before {
		if after[k] < n {
			return false
		}
	}
	return true
}

// state describes base and profile trees with link targets relative to HOME, so that two
// fixtures can be compared.
func state(f *fx) string {
	home := filepath.Dir(f.base)
	var lines []string
	for _, root := range []string{f.base, f.work} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(home, p)
			switch {
			case d.Type()&os.ModeSymlink != 0:
				tgt, _ := os.Readlink(p)
				r2, _ := filepath.Rel(home, tgt)
				lines = append(lines, rel+" -> "+r2)
			case d.Type().IsRegular():
				b, _ := os.ReadFile(p)
				lines = append(lines, fmt.Sprintf("%s %x", rel, sha256.Sum256(b)))
			default:
				lines = append(lines, rel+"/")
			}
			return nil
		})
	}
	sort.Strings(lines)
	return fmt.Sprint(lines)
}

func TestPlanAddDuplicateConflict(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.base, "skills", "same", "S.md"), "1")
	put(t, filepath.Join(f.work, "skills", "same", "S.md"), "1")
	put(t, filepath.Join(f.base, "skills", "diff", "S.md"), "base")
	put(t, filepath.Join(f.work, "skills", "diff", "S.md"), "work")
	put(t, filepath.Join(f.work, "skills", "new", "S.md"), "new")
	it, _ := share.Lookup("skills")
	pl, err := PlanShare(f.p, f.c, "work", it, Options{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, u := range pl.Paths[0].Units {
		kinds[u.Path] = u.Kind
	}
	if kinds["same"] != Duplicate || kinds["diff"] != Conflict || kinds["new"] != Add || len(pl.Conflicts) != 1 {
		t.Fatalf("%v %v", kinds, pl.Conflicts)
	}
	// keep-both is refused for skills.
	if _, err := PlanShare(f.p, f.c, "work", it, Options{OnConflict: KeepBoth}); err == nil {
		t.Fatal("keep-both accepted for skills")
	}
}

// randomTree fills dir with a random tree drawing contents from a small pool so that
// duplicates and conflicts happen.
func randomTree(t *testing.T, r *rand.Rand, dir string, depth int) {
	n := r.Intn(4)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("e%d", r.Intn(4))
		p := filepath.Join(dir, name)
		if fsutil.Exists(p) {
			continue
		}
		switch k := r.Intn(5); {
		case k == 0 && depth < 2:
			os.MkdirAll(p, 0o700)
			randomTree(t, r, p, depth+1)
		case k == 1:
			os.MkdirAll(dir, 0o700)
			os.Symlink(fmt.Sprintf("target-%d", r.Intn(3)), p)
		default:
			put(t, p, fmt.Sprintf("content-%d", r.Intn(5)))
		}
	}
}

func TestMigrationInvariantProperty(t *testing.T) {
	items := []string{"skills", "projects", "keybindings", "memory", "plugins", "sessions", "themes"}
	policies := []string{Abort, KeepBase, KeepProfile, KeepBoth}
	for seed := int64(1); seed <= 30; seed++ {
		for _, item := range items {
			for _, pol := range policies {
				t.Run(fmt.Sprintf("%d-%s-%s", seed, item, pol), func(t *testing.T) {
					f := setup(t)
					r := rand.New(rand.NewSource(seed))
					var it share.Item
					if item == "themes" {
						it = share.ExtraItem("themes", true)
					} else {
						it, _ = share.Lookup(item)
					}
					for _, ps := range it.Paths {
						for _, root := range []string{f.base, f.work} {
							if r.Intn(4) == 0 {
								continue
							}
							if ps.Dir {
								os.MkdirAll(filepath.Join(root, ps.Name), 0o700)
								randomTree(t, r, filepath.Join(root, ps.Name), 0)
							} else {
								put(t, filepath.Join(root, ps.Name), fmt.Sprintf("content-%d", r.Intn(3)))
							}
						}
					}
					before := contents(f.base, f.work)
					st := state(f)
					pl, err := PlanShare(f.p, f.c, "work", it, Options{OnConflict: pol})
					if pol == KeepBoth && !it.KeepBoth {
						if err == nil {
							t.Fatal("keep-both accepted")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(pl.Conflicts) > 0 {
						if state(f) != st {
							t.Fatal("planning changed something")
						}
						return
					}
					if err := Run(f.p, pl.Journal); err != nil {
						t.Fatal(err)
					}
					after := contents(f.base, f.work, f.p.BackupsDir())
					if !contained(before, after) {
						t.Fatal("content lost")
					}
					for _, ps := range it.Paths {
						s := share.State(f.work, f.base, ps.Name)
						if s != share.Linked && !(s == share.Missing && !ps.Dir) {
							t.Fatalf("%s is %s", ps.Name, s)
						}
					}
					// And back: unshare with copy keeps everything too.
					c, _, _ := config.Load(f.p)
					if c.ShareState("work", it.Name) != config.Shared {
						t.Fatal("config not updated")
					}
					mode := "copy"
					if seed%2 == 0 {
						mode = "empty"
					}
					up, err := PlanUnshare(c, "work", it, Options{Unshare: mode})
					if err != nil {
						t.Fatal(err)
					}
					if err := Run(f.p, up.Journal); err != nil {
						t.Fatal(err)
					}
					if !contained(before, contents(f.base, f.work, f.p.BackupsDir())) {
						t.Fatal("content lost on unshare")
					}
				})
			}
		}
	}
}

func TestFaultInjectionResumeAndRollback(t *testing.T) {
	build := func(t *testing.T) (*fx, *Plan) {
		f := setup(t)
		put(t, filepath.Join(f.base, "projects", "-p", "a.jsonl"), "base a")
		put(t, filepath.Join(f.work, "projects", "-p", "a.jsonl"), "work a")
		put(t, filepath.Join(f.work, "projects", "-q", "b.jsonl"), "work b")
		put(t, filepath.Join(f.work, "file-history", "s", "x"), "fh")
		it, _ := share.Lookup("projects")
		pl, err := PlanShare(f.p, f.c, "work", it, Options{OnConflict: KeepProfile})
		if err != nil {
			t.Fatal(err)
		}
		return f, pl
	}
	// The uninterrupted result.
	f0, pl0 := build(t)
	initial := state(f0)
	if err := Run(f0.p, pl0.Journal); err != nil {
		t.Fatal(err)
	}
	final := state(f0)
	n := len(pl0.Journal.Steps)
	if n < 5 {
		t.Fatalf("only %d steps", n)
	}
	defer func() { AfterStep = nil }()
	for k := 0; k < n-1; k++ {
		for _, mode := range []string{"resume", "rollback"} {
			t.Run(fmt.Sprintf("after-step-%d-%s", k+1, mode), func(t *testing.T) {
				f, pl := build(t)
				if state(f) != initial {
					t.Fatal("fixture differs")
				}
				before := contents(f.base, f.work)
				AfterStep = func(i int) error {
					if i == k {
						return fmt.Errorf("killed")
					}
					return nil
				}
				if err := Run(f.p, pl.Journal); err != ErrInterrupted {
					t.Fatalf("want interruption, got %v", err)
				}
				AfterStep = nil
				// Every byte is in its place or in the backup.
				if !contained(before, contents(f.base, f.work, f.p.BackupsDir())) {
					t.Fatal("content missing after the interruption")
				}
				j, err := Pending(f.p)
				if err != nil || j == nil {
					t.Fatal("no journal", err)
				}
				// A new migration is refused while one is pending.
				if err := Run(f.p, &Journal{Operation: "x"}); err == nil {
					t.Fatal("ran a second migration")
				}
				if mode == "resume" {
					if err := Resume(f.p, j); err != nil {
						t.Fatal(err)
					}
					if state(f) != final {
						t.Fatal("resume did not reach the final state")
					}
				} else {
					if err := Rollback(f.p, j); err != nil {
						t.Fatal(err)
					}
					if state(f) != initial {
						t.Fatal("rollback did not restore the initial state")
					}
					c, _, _ := config.Load(f.p)
					if c.ShareState("work", "projects") != config.Own {
						t.Fatal("config not rolled back")
					}
				}
				if j, _ := Pending(f.p); j != nil {
					t.Fatal("journal left behind")
				}
			})
		}
	}
}

func TestPluginsReportsMissing(t *testing.T) {
	f := setup(t)
	put(t, filepath.Join(f.work, "plugins", "installed_plugins.json"), `{"version":2,"plugins":{"a@m":[],"b@m":[]}}`)
	put(t, filepath.Join(f.work, "plugins", "known_marketplaces.json"), `{"m":{},"n":{}}`)
	put(t, filepath.Join(f.base, "plugins", "installed_plugins.json"), `{"version":2,"plugins":{"a@m":[]}}`)
	put(t, filepath.Join(f.base, "plugins", "known_marketplaces.json"), `{"m":{}}`)
	it, _ := share.Lookup("plugins")
	pl, err := PlanShare(f.p, f.c, "work", it, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := pl.Paths[0].Missing
	sort.Strings(m)
	if fmt.Sprint(m) != "[marketplace n plugin b@m]" || len(pl.Confirm) == 0 {
		t.Fatalf("%v %v", m, pl.Confirm)
	}
}
