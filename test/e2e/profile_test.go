package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileAddAuthentication(t *testing.T) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	// --new-key with the token on stdin.
	r := e.withToken(fixtureToken).ok("profile", "add", "alpha", "--new-key", "key-a", "--note", "first")
	expectContains(t, r.out, "stored key 'key-a' (sha256:")
	expectContains(t, r.out, "len=27")
	// --key with an existing key.
	e.ok("profile", "add", "beta", "--key", "key-a")
	// --login.
	e.ok("profile", "add", "gamma", "--login")
	cfg := readFile(t, e.configFile())
	for _, want := range []string{"[profiles.alpha]", "key = \"key-a\"", "[profiles.gamma]", "[keys.key-a]", "note = \"first\""} {
		expectContains(t, cfg, want)
	}
	if strings.Contains(cfg, fixtureToken) {
		t.Fatal("token in the config")
	}
	if strings.Contains(strings.SplitN(cfg, "[profiles.gamma]", 2)[1], "key =") {
		t.Fatal("gamma has a key")
	}
	expectContains(t, e.ok("beta").out, "token="+lenStr(fixtureToken)+" profile=beta")
	expectContains(t, e.ok("gamma").out, "token=unset profile=gamma")
	// No terminal and no flag: usage error naming the flags.
	r = e.hub("profile", "add", "delta")
	expectCode(t, r, 64)
	for _, f := range []string{"--key", "--new-key", "--login"} {
		expectContains(t, r.err, f)
	}
	// The flags exclude each other.
	expectCode(t, e.hub("profile", "add", "delta", "--login", "--key", "key-a"), 64)
	// An unknown key is refused.
	expectCode(t, e.hub("profile", "add", "delta", "--key", "nope"), 1)
	// Reserved names.
	expectCode(t, e.hub("profile", "add", "mcp", "--login"), 64)
	expectCode(t, e.hub("profile", "add", "doctor", "--login"), 64)
	expectCode(t, e.hub("profile", "add", "Bad_Name", "--login"), 64)
	if _, err := os.Stat(filepath.Join(e.home, ".claude-delta")); err == nil {
		t.Fatal("a refused profile left a folder")
	}
}

func TestFailedStoreLeavesNoProfile(t *testing.T) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	// Make the file backend unusable: a file where its folder should be.
	writeFile(t, filepath.Join(e.home, ".local", "share", "claude-hub", "secrets"), "not a folder")
	r := e.withToken(fixtureToken).hub("profile", "add", "alpha", "--new-key", "key-a")
	expectCode(t, r, 1)
	cfg := readFile(t, e.configFile())
	if strings.Contains(cfg, "alpha") || strings.Contains(cfg, "key-a") {
		t.Fatalf("half-made profile in the config:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".claude-alpha")); err == nil {
		t.Fatal("a folder was created")
	}
	// A bad token is refused too.
	os.Remove(filepath.Join(e.home, ".local", "share", "claude-hub", "secrets"))
	expectCode(t, e.withToken("has space").hub("profile", "add", "alpha", "--new-key", "key-a"), 1)
	expectCode(t, e.hub("profile", "add", "alpha", "--new-key", "key-a"), 1) // empty stdin
}

func TestSetKeyUnsetKeyAndKeyRemove(t *testing.T) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	e.ok("profile", "add", "work", "--login")
	e.withToken(fixtureToken).ok("key", "add", "key-a", "--note", "a")
	e.ok("profile", "set-key", "work", "key-a")
	expectContains(t, e.ok("work").out, "token="+lenStr(fixtureToken))
	e.withToken(fixtureToken+"XY").ok("profile", "set-key", "work", "--new", "key-b")
	expectContains(t, e.ok("work").out, "token="+lenStr(fixtureToken+"XY"))
	r := e.ok("profile", "unset-key", "work")
	expectContains(t, r.out, "/login")
	expectContains(t, e.ok("work").out, "token=unset")
	// key remove: refused while used, --yes switches the profiles to login.
	e.ok("profile", "set-key", "work", "key-a")
	e.ok("profile", "add", "lab", "--key", "key-a")
	expectCode(t, e.hub("key", "remove", "key-a"), 1)
	r = e.ok("key", "remove", "key-a", "--yes")
	expectContains(t, r.out, "profile 'lab' now uses Claude's own login")
	expectContains(t, r.out, "profile 'work' now uses Claude's own login")
	expectContains(t, e.ok("work").out, "token=unset")
	if _, err := os.Stat(filepath.Join(e.home, ".local", "share", "claude-hub", "secrets", "key-a")); err == nil {
		t.Fatal("the key is still in the store")
	}
	// key rename moves the token and the profile references.
	e.ok("profile", "set-key", "lab", "key-b")
	e.ok("key", "rename", "key-b", "key-c")
	expectContains(t, e.ok("lab").out, "token="+lenStr(fixtureToken+"XY"))
	r = e.ok("key", "list")
	expectContains(t, r.out, "key-c")
	expectContains(t, r.out, "used by: lab")
	// key set rotates.
	e.withToken(fixtureToken).ok("key", "set", "key-c")
	expectContains(t, e.ok("lab").out, "token="+lenStr(fixtureToken))
	// no command takes a token as an argument
	expectCode(t, e.hub("key", "add", "key-d", fixtureToken), 64)
}

func TestDoctorCleanWithZeroKeys(t *testing.T) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	mustMkdir(t, filepath.Join(e.home, ".claude"))
	e.ok("profile", "add", "main", "--dir", "~/.claude", "--login")
	e.ok("profile", "add", "work", "--login")
	e.ok("default", "main")
	r := e.ok("doctor")
	expectContains(t, r.out, "ok: no keys, all profiles use Claude's login"[4:])
	if strings.Contains(r.out, "fail ") {
		t.Fatalf("doctor failed: %s", r)
	}
	expectContains(t, r.out, "work: login")
	r = e.ok("doctor", "--json")
	expectContains(t, r.out, `"status": "ok"`)
}

func TestInit(t *testing.T) {
	e := newEnv(t)
	mustMkdir(t, filepath.Join(e.home, ".claude"))
	r := e.ok("init", "--yes")
	expectContains(t, r.out, "profile 'main'")
	expectContains(t, e.ok("-x").out, "dir=unset token=unset profile=main")
	expectContains(t, e.ok("init").out, "already exists")
}

func TestProfileListShowRenameRemove(t *testing.T) {
	e := setupTwo(t)
	writeFile(t, filepath.Join(e.home, ".claude-work", "settings.json"), "{}\n")
	r := e.ok("profile", "list")
	expectContains(t, r.out, "* 🟩 personal")
	expectContains(t, r.out, "key key-personal")
	expectContains(t, r.out, "work")
	r = e.ok("profile", "show", "work", "--json")
	expectContains(t, r.out, `"auth": "login"`)
	expectContains(t, r.out, `"settings": "layered"`)
	// rename keeps rules and default.
	e.ok("use", "work", "--rule", "--dir", filepath.Join(e.home, "w"))
	e.ok("profile", "rename", "work", "job")
	cfg := readFile(t, e.configFile())
	expectContains(t, cfg, "[profiles.job]")
	expectContains(t, cfg, `profile = "job"`)
	// the default profile cannot be removed.
	expectCode(t, e.hub("profile", "remove", "personal"), 1)
	// remove leaves the folder.
	r = e.ok("profile", "remove", "job")
	expectContains(t, r.out, "left at ~/.claude-work")
	if _, err := os.Stat(filepath.Join(e.home, ".claude-work")); err != nil {
		t.Fatal("the folder was removed")
	}
	if strings.Contains(readFile(t, e.configFile()), "job") {
		t.Fatal("the rule or profile is still in the config")
	}
}

func TestRemoveArchiveKeepsSharedDataInBase(t *testing.T) {
	e := setupTwo(t)
	writeFile(t, filepath.Join(e.home, ".claude", "skills", "s1", "SKILL.md"), "base skill")
	writeFile(t, filepath.Join(e.home, ".claude-work", ".claude.json"), "{}")
	// The base and the default are refused.
	expectCode(t, e.hub("profile", "remove", "personal", "--archive"), 1)
	r := e.ok("profile", "remove", "work", "--archive")
	expectContains(t, r.out, "archived")
	if readFile(t, filepath.Join(e.home, ".claude", "skills", "s1", "SKILL.md")) != "base skill" {
		t.Fatal("shared data was touched")
	}
	if _, err := os.Stat(filepath.Join(e.home, ".claude-work")); err == nil {
		t.Fatal("the folder is still there")
	}
	// --purge needs a terminal.
	e.ok("profile", "add", "lab", "--login")
	writeFile(t, filepath.Join(e.home, ".claude-lab", ".claude.json"), "{}")
	expectCode(t, e.hub("profile", "remove", "lab", "--purge"), 64)
}
