package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adopted creates a base and an existing secondary folder with own content, registered with
// --adopt (everything own).
func adopted(t *testing.T) *hubEnv {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	base := filepath.Join(e.home, ".claude")
	work := filepath.Join(e.home, ".claude-work")
	writeFile(t, filepath.Join(base, "skills", "common", "SKILL.md"), "same")
	writeFile(t, filepath.Join(base, "skills", "clash", "SKILL.md"), "base version")
	writeFile(t, filepath.Join(work, "skills", "common", "SKILL.md"), "same")
	writeFile(t, filepath.Join(work, "skills", "clash", "SKILL.md"), "work version")
	writeFile(t, filepath.Join(work, "skills", "mine", "SKILL.md"), "only in work")
	writeFile(t, filepath.Join(base, "projects", "-p1", "a.jsonl"), "base a")
	writeFile(t, filepath.Join(work, "projects", "-p1", "a.jsonl"), "work a")
	writeFile(t, filepath.Join(work, "projects", "-p2", "b.jsonl"), "work b")
	writeFile(t, filepath.Join(work, "file-history", "s1", "f"), "fh")
	writeFile(t, filepath.Join(work, ".claude.json"), "{}")
	writeFile(t, filepath.Join(base, "settings.json"), `{"model":"opus","theme":"dark"}`+"\n")
	writeFile(t, filepath.Join(work, "settings.json"), `{"model":"sonnet","theme":"dark"}`+"\n")
	if err := os.Symlink(filepath.Join(base, "hooks"), filepath.Join(work, "hooks")); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(base, "hooks"))
	e.ok("profile", "add", "main", "--dir", "~/.claude", "--login")
	r := e.ok("profile", "add", "work", "--adopt", "--login")
	expectContains(t, r.out, "shares with the base: hooks")
	return e
}

func backups(t *testing.T, e *hubEnv) []string {
	ents, _ := os.ReadDir(filepath.Join(e.home, ".local", "share", "claude-hub", "backups"))
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

func TestShareAbortsOnConflictAndChangesNothing(t *testing.T) {
	e := adopted(t)
	r := e.hub("profile", "share", "work", "skills")
	expectCode(t, r, 1)
	expectContains(t, r.err, "skills/clash")
	if readFile(t, filepath.Join(e.home, ".claude-work", "skills", "clash", "SKILL.md")) != "work version" {
		t.Fatal("changed")
	}
	if len(backups(t, e)) != 0 {
		t.Fatal("a backup was made")
	}
	// --dry-run prints the plan and changes nothing.
	r = e.ok("profile", "share", "work", "skills", "--dry-run")
	expectContains(t, r.out, "1 to add")
	expectContains(t, r.out, "1 duplicate")
	expectContains(t, r.out, "1 conflict")
}

func TestShareKeepBase(t *testing.T) {
	e := adopted(t)
	e.ok("profile", "share", "work", "skills", "--on-conflict", "keep-base")
	work := filepath.Join(e.home, ".claude-work")
	base := filepath.Join(e.home, ".claude")
	if !isLink(filepath.Join(work, "skills")) {
		t.Fatal("not linked")
	}
	if readFile(t, filepath.Join(base, "skills", "clash", "SKILL.md")) != "base version" {
		t.Fatal("base lost its version")
	}
	if readFile(t, filepath.Join(base, "skills", "mine", "SKILL.md")) != "only in work" {
		t.Fatal("the add was not copied")
	}
	bk := backups(t, e)
	if len(bk) != 1 || !strings.Contains(bk[0], "-work-skills") {
		t.Fatalf("backups: %v", bk)
	}
	bdir := filepath.Join(e.home, ".local", "share", "claude-hub", "backups", bk[0])
	if readFile(t, filepath.Join(bdir, "profile", "skills", "clash", "SKILL.md")) != "work version" {
		t.Fatal("the profile's version is not in the backup")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(bdir, "MANIFEST.json"))), &m); err != nil || m["operation"] != "share" {
		t.Fatalf("manifest: %v %v", m, err)
	}
	expectContains(t, readFile(t, e.configFile()), `skills = "shared"`)
}

func TestShareKeepProfileNeedsYes(t *testing.T) {
	e := adopted(t)
	r := e.hub("profile", "share", "work", "skills", "--on-conflict", "keep-profile")
	expectCode(t, r, 64)
	e.ok("profile", "share", "work", "skills", "--on-conflict", "keep-profile", "--yes")
	base := filepath.Join(e.home, ".claude")
	if readFile(t, filepath.Join(base, "skills", "clash", "SKILL.md")) != "work version" {
		t.Fatal("the profile's version did not win")
	}
	bk := backups(t, e)
	bdir := filepath.Join(e.home, ".local", "share", "claude-hub", "backups", bk[0])
	if readFile(t, filepath.Join(bdir, "base", "skills", "clash", "SKILL.md")) != "base version" {
		t.Fatal("the base's version is not in the backup")
	}
}

func TestShareProjectsKeepBothAndPartialItem(t *testing.T) {
	e := adopted(t)
	expectCode(t, e.hub("profile", "share", "work", "skills", "--on-conflict", "keep-both"), 64)
	e.ok("profile", "share", "work", "projects", "--on-conflict", "keep-both")
	base := filepath.Join(e.home, ".claude")
	work := filepath.Join(e.home, ".claude-work")
	if readFile(t, filepath.Join(base, "projects", "-p1", "a.jsonl")) != "base a" ||
		readFile(t, filepath.Join(base, "projects", "-p1", "a.work.jsonl")) != "work a" ||
		readFile(t, filepath.Join(base, "projects", "-p2", "b.jsonl")) != "work b" ||
		readFile(t, filepath.Join(base, "file-history", "s1", "f")) != "fh" {
		t.Fatal("merge result wrong")
	}
	if !isLink(filepath.Join(work, "projects")) || !isLink(filepath.Join(work, "file-history")) {
		t.Fatal("not linked")
	}
}

func TestUnshare(t *testing.T) {
	e := adopted(t)
	base := filepath.Join(e.home, ".claude")
	work := filepath.Join(e.home, ".claude-work")
	writeFile(t, filepath.Join(base, "hooks", "h.sh"), "hook")
	e.ok("profile", "unshare", "work", "hooks")
	if isLink(filepath.Join(work, "hooks")) || readFile(t, filepath.Join(work, "hooks", "h.sh")) != "hook" {
		t.Fatal("--copy is the default")
	}
	// sessions are always empty; projects needs a choice.
	e.ok("profile", "share", "work", "sessions")
	writeFile(t, filepath.Join(base, "sessions", "1.json"), "{}")
	e.ok("profile", "unshare", "work", "sessions", "--copy")
	if ents, _ := os.ReadDir(filepath.Join(work, "sessions")); len(ents) != 0 || isLink(filepath.Join(work, "sessions")) {
		t.Fatal("sessions must start empty")
	}
	e.ok("profile", "share", "work", "projects", "--on-conflict", "keep-base")
	expectCode(t, e.hub("profile", "unshare", "work", "projects"), 64)
	e.ok("profile", "unshare", "work", "projects", "--empty")
	if isLink(filepath.Join(work, "projects")) {
		t.Fatal("still linked")
	}
	if readFile(t, filepath.Join(base, "projects", "-p2", "b.jsonl")) != "work b" {
		t.Fatal("the base must keep everything")
	}
	expectContains(t, readFile(t, e.configFile()), `projects = "own"`)
}

func TestNeverShare(t *testing.T) {
	e := adopted(t)
	for _, item := range []string{".claude.json", ".credentials.json", "backups", "daemon", "policy-limits.json.x", "statsig", "telemetry", "history.jsonl"} {
		r := e.hub("profile", "share", "work", item)
		expectCode(t, r, 64)
	}
	cfg := readFile(t, e.configFile())
	e.writeConfig(strings.Replace(cfg, "extra = []", `extra = [".credentials.json"]`, 1))
	expectCode(t, e.hub("work"), 78)
	e.writeConfig(cfg)
	// --adopt refuses a folder whose never-share entry is a link.
	odd := filepath.Join(e.home, ".claude-odd")
	mustMkdir(t, odd)
	if err := os.Symlink(filepath.Join(e.home, ".claude", ".claude.json"), filepath.Join(odd, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	expectCode(t, e.hub("profile", "add", "odd", "--adopt", "--login"), 1)
	// doctor fails on it.
	cfg = readFile(t, e.configFile())
	e.writeConfig(cfg + "\n[profiles.odd]\ndir = \"~/.claude-odd\"\n")
	r := e.hub("doctor")
	expectCode(t, r, 1)
	expectContains(t, r.out, "must never be shared")
}

func TestExtraItem(t *testing.T) {
	e := adopted(t)
	writeFile(t, filepath.Join(e.home, ".claude-work", "themes", "t.json"), "{}")
	e.ok("profile", "share", "work", "themes")
	expectContains(t, readFile(t, e.configFile()), `extra = ["themes"]`)
	if readFile(t, filepath.Join(e.home, ".claude", "themes", "t.json")) != "{}" {
		t.Fatal("not merged")
	}
	e.ok("profile", "unshare", "work", "themes")
	expectContains(t, readFile(t, e.configFile()), `extra = []`)
}

func TestBrokenLinkWarnsAndDoctorFixMerges(t *testing.T) {
	e := setupTwo(t)
	work := filepath.Join(e.home, ".claude-work")
	// A tool replaced the shared skills link with a real folder.
	os.Remove(filepath.Join(work, "skills"))
	writeFile(t, filepath.Join(work, "skills", "new", "SKILL.md"), "new")
	r := e.ok("work")
	expectContains(t, r.err, "should link to the base")
	expectContains(t, r.out, "profile=work")
	// Without a terminal, moving data needs --yes.
	expectContains(t, e.hub("doctor", "--fix").err, "needs a terminal or --yes")
	if isLink(filepath.Join(work, "skills")) {
		t.Fatal("merged without confirmation")
	}
	e.ok("doctor", "--fix", "--yes")
	if !isLink(filepath.Join(work, "skills")) || readFile(t, filepath.Join(e.home, ".claude", "skills", "new", "SKILL.md")) != "new" {
		t.Fatal("doctor --fix did not merge it back")
	}
	// A missing link is re-created.
	os.Remove(filepath.Join(work, "agents"))
	e.ok("doctor", "--fix")
	if !isLink(filepath.Join(work, "agents")) {
		t.Fatal("not re-created")
	}
}

func TestPendingSharedFileIsLinkedBySync(t *testing.T) {
	e := setupTwo(t)
	work := filepath.Join(e.home, ".claude-work")
	if _, err := os.Lstat(filepath.Join(work, "keybindings.json")); err == nil {
		t.Fatal("a dangling link was made")
	}
	writeFile(t, filepath.Join(e.home, ".claude", "keybindings.json"), "[]")
	expectContains(t, e.ok("sync").out, "work: linked keybindings.json")
	if !isLink(filepath.Join(work, "keybindings.json")) {
		t.Fatal("not linked")
	}
}

func isLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}
