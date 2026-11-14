package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// setupTwo makes a base profile "personal" (key-personal, dir ~/.claude) and a secondary
// "work" (Claude's login), default personal.
func setupTwo(t *testing.T) *hubEnv {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	mustMkdir(t, filepath.Join(e.home, ".claude"))
	e.withToken(fixtureToken).ok("profile", "add", "personal", "--dir", "~/.claude", "--new-key", "key-personal", "--note", "personal account", "--icon", "🟩")
	e.ok("profile", "add", "work", "--login", "--icon", "#eab308")
	e.ok("default", "personal")
	return e
}

func TestProfilesSetDirTokenAndName(t *testing.T) {
	e := setupTwo(t)
	r := e.ok("personal", "--x")
	expectOut(t, r, "dir="+filepath.Join(e.home, ".claude")+" token="+lenStr(fixtureToken)+" profile=personal api=unset auth=unset args=[--x]")
	r = e.ok("work", "--x")
	expectOut(t, r, "dir="+filepath.Join(e.home, ".claude-work")+" token=unset profile=work api=unset auth=unset args=[--x]")
}

func TestProfileWithoutDirUnsetsConfigDir(t *testing.T) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig + "\n[profiles.main]\n")
	e.extra["CLAUDE_CONFIG_DIR"] = "/inherited"
	r := e.ok("main", "-p", "hi")
	expectOut(t, r, "dir=unset token=unset profile=main api=unset auth=unset args=[-p][hi]")
}

func TestArgumentsPassThrough(t *testing.T) {
	e := setupTwo(t)
	expectOut(t, e.ok("mcp", "list"), "dir="+filepath.Join(e.home, ".claude")+" token="+lenStr(fixtureToken)+" profile=personal api=unset auth=unset args=[mcp][list]")
	expectContains(t, e.ok("fix the bug").out, "args=[fix the bug]")
	expectContains(t, e.ok("-p", "say \"hi\"  twice").out, "args=[-p][say \"hi\"  twice]")
	expectContains(t, e.ok("work", "mcp", "list").out, "profile=work api=unset auth=unset args=[mcp][list]")
	// "--" passes a reserved word to claude.
	expectContains(t, e.ok("--", "doctor").out, "profile=personal api=unset auth=unset args=[doctor]")
	expectContains(t, e.ok("work", "--", "--help").out, "profile=work api=unset auth=unset args=[--help]")
}

func TestHubCommandsAreNotRecognisedAsClaude(t *testing.T) {
	e := setupTwo(t)
	shimDir := filepath.Join(e.root, "shimbin")
	mustMkdir(t, shimDir)
	link := filepath.Join(shimDir, "claude")
	if err := os.Symlink(hubBin, link); err != nil {
		t.Fatal(err)
	}
	e.path = shimDir + ":" + e.path
	for _, args := range [][]string{{"doctor"}, {"update"}, {"install"}, {"version"}, {"mcp", "list"}, {"--", "x"}} {
		r := e.runPath(link, "claude", args...)
		expectCode(t, r, 0)
		want := "args="
		for _, a := range args {
			want += "[" + a + "]"
		}
		expectContains(t, r.out, "profile=personal")
		if !strings.HasSuffix(strings.TrimSpace(r.out), want) {
			t.Fatalf("claude %v: %s", args, r)
		}
	}
	// A profile name still selects under the name claude.
	expectContains(t, e.runPath(link, "claude", "work", "-p", "x").out, "profile=work api=unset auth=unset args=[-p][x]")
}

func TestUnknownProfileIsAnErrorAndClaudeNeverStarts(t *testing.T) {
	e := setupTwo(t)
	writeFile(t, filepath.Join(e.home, "proj", ".claude-profile"), "\n  nosuch  \n")
	e.cwd = filepath.Join(e.home, "proj")
	r := e.hub("--x")
	expectCode(t, r, 78)
	if r.out != "" {
		t.Fatalf("claude ran: %s", r)
	}
	expectContains(t, r.err, "nosuch")
	expectContains(t, r.err, filepath.Join(e.home, "proj", ".claude-profile"))

	e.extra["CLAUDE_HUB_SELECT"] = "ghost"
	r = e.hub("--x")
	expectCode(t, r, 78)
	expectContains(t, r.err, "CLAUDE_HUB_SELECT")
}

func TestSelectionOrder(t *testing.T) {
	e := setupTwo(t)
	e.ok("profile", "add", "lab", "--login")
	proj := filepath.Join(e.home, "ws", "lg", "deep", "er")
	mustMkdir(t, proj)
	e.cwd = proj
	// default
	expectContains(t, e.ok("-x").out, "profile=personal")
	// directory rule, longest prefix wins
	e.ok("use", "work", "--rule", "--dir", filepath.Join(e.home, "ws"))
	expectContains(t, e.ok("-x").out, "profile=work")
	e.ok("use", "lab", "--rule", "--dir", filepath.Join(e.home, "ws", "lg"))
	expectContains(t, e.ok("-x").out, "profile=lab")
	// .claude-profile in an ancestor beats rules
	writeFile(t, filepath.Join(e.home, "ws", "lg", "deep", ".claude-profile"), "work\n")
	expectContains(t, e.ok("-x").out, "profile=work")
	// CLAUDE_HUB_SELECT beats the file
	e.extra["CLAUDE_HUB_SELECT"] = "personal"
	expectContains(t, e.ok("-x").out, "profile=personal")
	// the argument beats everything
	expectContains(t, e.ok("lab", "-x").out, "profile=lab")
	delete(e.extra, "CLAUDE_HUB_SELECT")
	// which explains it
	r := e.ok("which")
	expectContains(t, r.out, "work (from "+filepath.Join(e.home, "ws", "lg", "deep", ".claude-profile"))
	expectContains(t, r.out, "auth: login")
	// use --clear removes the file, the rule takes over again
	e.ok("use", "--clear", "--dir", filepath.Join(e.home, "ws", "lg", "deep"))
	expectContains(t, e.ok("-x").out, "profile=lab")
}

func TestProfileFileSearchStopsAtHome(t *testing.T) {
	e := setupTwo(t)
	// A file above HOME is never read.
	writeFile(t, filepath.Join(e.root, ".claude-profile"), "work\n")
	e.cwd = e.home
	expectContains(t, e.ok("-x").out, "profile=personal")
}

func TestNoProfileSelectedRunsPlainClaude(t *testing.T) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig + "\n[profiles.work]\ndir = \"~/.claude-work\"\n")
	e.extra["CLAUDE_CONFIG_DIR"] = "/kept"
	e.extra["ANTHROPIC_API_KEY"] = "test-api"
	e.extra["CLAUDE_CODE_OAUTH_TOKEN"] = "abc"
	r := e.ok("-x")
	expectOut(t, r, "dir=/kept token=set(len 3) profile=unset api=set auth=unset args=[-x]")
}

func TestNestedCalls(t *testing.T) {
	e := setupTwo(t)
	// Inside a work session: keep its dir and token exactly.
	e.extra["CLAUDE_HUB_PROFILE"] = "work"
	e.extra["CLAUDE_CONFIG_DIR"] = "/nested/dir"
	e.extra["CLAUDE_CODE_OAUTH_TOKEN"] = "NESTED"
	expectOut(t, e.ok("-p", "hi"), "dir=/nested/dir token=set(len 6) profile=work api=unset auth=unset args=[-p][hi]")
	// An explicit name switches, and a login profile clears the inherited token.
	e.extra["CLAUDE_HUB_PROFILE"] = "personal"
	expectOut(t, e.ok("work", "-x"), "dir="+filepath.Join(e.home, ".claude-work")+" token=unset profile=work api=unset auth=unset args=[-x]")
	// A nested call without a token (hooks) gets its own profile's key back.
	delete(e.extra, "CLAUDE_CODE_OAUTH_TOKEN")
	e.extra["CLAUDE_CONFIG_DIR"] = filepath.Join(e.home, ".claude")
	expectOut(t, e.ok("-p", "hi"), "dir="+filepath.Join(e.home, ".claude")+" token="+lenStr(fixtureToken)+" profile=personal api=unset auth=unset args=[-p][hi]")
	// ...but a nested login profile gets no token.
	e.extra["CLAUDE_HUB_PROFILE"] = "work"
	expectContains(t, e.ok("-p", "hi").out, "token=unset profile=work")
	// The .claude-profile of the cwd is ignored inside a session.
	writeFile(t, filepath.Join(e.home, ".claude-profile"), "personal\n")
	expectContains(t, e.ok("-p", "hi").out, "profile=work")
}

func TestNestedKeyUnreadableFailsClosed(t *testing.T) {
	e := setupTwo(t)
	os.Remove(filepath.Join(e.home, ".local", "share", "claude-hub", "secrets", "key-personal"))
	e.extra["CLAUDE_HUB_PROFILE"] = "personal"
	r := e.hub("-p", "hi")
	expectCode(t, r, 78)
	if r.out != "" {
		t.Fatalf("claude ran: %s", r)
	}
}

func TestLoginProfileClearsInheritedToken(t *testing.T) {
	e := setupTwo(t)
	e.extra["CLAUDE_CODE_OAUTH_TOKEN"] = "from-shell"
	expectContains(t, e.ok("work").out, "token=unset profile=work")
}

func TestAPIKeyVariablesAreUnsetForSelectedProfiles(t *testing.T) {
	e := setupTwo(t)
	e.extra["ANTHROPIC_API_KEY"] = "test-api"
	e.extra["ANTHROPIC_AUTH_TOKEN"] = "test-auth"
	expectContains(t, e.ok("work").out, "api=unset auth=unset")
	expectContains(t, e.ok("personal").out, "api=unset auth=unset")
	expectContains(t, e.ok().out, "profile=personal api=unset auth=unset")
	// doctor warns.
	r := e.ok("doctor")
	expectContains(t, r.out, "ANTHROPIC_API_KEY is set")
	expectContains(t, r.out, "ANTHROPIC_AUTH_TOKEN is set")
}

func TestMissingKeyFailsClosed(t *testing.T) {
	e := setupTwo(t)
	os.Remove(filepath.Join(e.home, ".local", "share", "claude-hub", "secrets", "key-personal"))
	r := e.hub("personal", "-x")
	expectCode(t, r, 78)
	if r.out != "" {
		t.Fatalf("the stand-in claude was started: %s", r)
	}
	expectContains(t, r.err, "profile 'personal' uses key 'key-personal'")
	expectContains(t, r.err, "profile set-key personal")
	expectContains(t, r.err, "profile unset-key personal")
}

func TestKeyFileWithWideModeFailsClosed(t *testing.T) {
	e := setupTwo(t)
	if err := os.Chmod(filepath.Join(e.home, ".local", "share", "claude-hub", "secrets", "key-personal"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.hub("personal")
	expectCode(t, r, 78)
	expectContains(t, r.err, "0600")
}

func TestRecursionGuard(t *testing.T) {
	e := setupTwo(t)
	// hub.claude_bin points at another copy of the hub: a direct hub-to-hub exec.
	cp := filepath.Join(e.root, "copy", "claude-hub-copy")
	b, err := os.ReadFile(hubBin)
	if err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Dir(cp))
	if err := os.WriteFile(cp, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := readFile(t, e.configFile())
	e.writeConfig(strings.Replace(cfg, `claude_bin = ""`, `claude_bin = "`+cp+`"`, 1))
	r := e.hub("personal")
	expectCode(t, r, 126)
	expectContains(t, r.err, "recursion")
	// A legitimate nested call (a new process) passes: the guard value is another PID.
	e.writeConfig(cfg)
	e.extra["CLAUDE_HUB_GUARD"] = "1"
	expectCode(t, e.hub("personal"), 0)
}

func TestShimAheadOfRealClaudeFollowsUpdates(t *testing.T) {
	e := setupTwo(t)
	hubDir := filepath.Join(e.root, "hubbin")
	mustMkdir(t, hubDir)
	if err := os.Symlink(hubBin, filepath.Join(hubDir, "claude")); err != nil {
		t.Fatal(err)
	}
	versions := filepath.Join(e.root, "real", "versions")
	for _, v := range []string{"1.0", "2.0"} {
		writeExec(t, filepath.Join(versions, v), "#!/bin/sh\necho \"real="+v+" args=$*\"\n")
	}
	realBin := filepath.Join(e.root, "realbin")
	mustMkdir(t, realBin)
	if err := os.Symlink(filepath.Join(versions, "1.0"), filepath.Join(realBin, "claude")); err != nil {
		t.Fatal(err)
	}
	e.path = hubDir + ":" + realBin + ":/usr/bin:/bin"
	r := e.runPath(filepath.Join(hubDir, "claude"), "claude", "mcp", "list")
	expectOut(t, r, "real=1.0 args=mcp list")
	os.Remove(filepath.Join(realBin, "claude"))
	if err := os.Symlink(filepath.Join(versions, "2.0"), filepath.Join(realBin, "claude")); err != nil {
		t.Fatal(err)
	}
	expectOut(t, e.runPath(filepath.Join(hubDir, "claude"), "claude", "--version"), "real=2.0 args=--version")
}

func TestNoRealClaude(t *testing.T) {
	e := setupTwo(t)
	hubDir := filepath.Join(e.root, "hubbin")
	mustMkdir(t, hubDir)
	if err := os.Symlink(hubBin, filepath.Join(hubDir, "claude")); err != nil {
		t.Fatal(err)
	}
	e.path = hubDir + ":/usr/bin:/bin"
	r := e.runPath(filepath.Join(hubDir, "claude"), "claude", "--x")
	expectCode(t, r, 127)
	expectCode(t, e.hub("--x"), 127)
}

func TestFallbackToLocalBin(t *testing.T) {
	e := setupTwo(t)
	writeExec(t, filepath.Join(e.home, ".local", "bin", "claude"), "#!/bin/sh\necho local-bin\n")
	e.path = "/usr/bin:/bin"
	expectOut(t, e.ok("--x"), "local-bin")
}

func TestExec(t *testing.T) {
	e := setupTwo(t)
	e.extra["ANTHROPIC_API_KEY"] = "x"
	r := e.ok("exec", "personal", "--", "sh", "-c", `echo "$CLAUDE_HUB_PROFILE $CLAUDE_CONFIG_DIR ${#CLAUDE_CODE_OAUTH_TOKEN} ${ANTHROPIC_API_KEY-unset} ${CLAUDE_HUB_GUARD-noguard}"`)
	expectOut(t, r, "personal "+filepath.Join(e.home, ".claude")+" "+strconv.Itoa(len(fixtureToken))+" unset noguard")
	expectCode(t, e.hub("exec", "personal"), 64)
	expectCode(t, e.hub("exec", "nosuch", "--", "true"), 78)
}

func TestProfileEnvAndTmp(t *testing.T) {
	e := setupTwo(t)
	e.ok("profile", "set", "work", "env", "CLAUDE_CODE_USE_BEDROCK=1")
	expectCode(t, e.hub("profile", "set", "work", "env", "MY_TOKEN=x"), 64)
	expectCode(t, e.hub("profile", "set", "work", "env", "CLAUDE_CONFIG_DIR=x"), 64)
	e.ok("profile", "set", "work", "env", "MY_TOKEN_NAME=x", "--not-a-secret")
	tmp := filepath.Join(e.root, "ctmp")
	cfg := readFile(t, e.configFile())
	e.writeConfig(cfg + "\n[tmp]\ndir = \"" + tmp + "\"\nmax_age_days = 0\n")
	r := e.ok("exec", "work", "--", "sh", "-c", `echo "$CLAUDE_CODE_USE_BEDROCK $MY_TOKEN_NAME $CLAUDE_CODE_TMPDIR"`)
	expectOut(t, r, "1 x "+tmp)
	if fi, err := os.Stat(tmp); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("tmp dir: %v %v", fi, err)
	}
}

func TestAnnounce(t *testing.T) {
	e := setupTwo(t)
	// stderr is not a terminal: no announcement by default.
	r := e.ok("work")
	if r.err != "" {
		t.Fatalf("announced on a non-terminal: %q", r.err)
	}
	cfg := readFile(t, e.configFile())
	e.writeConfig(strings.Replace(cfg, `announce = "tty"`, `announce = "always"`, 1))
	expectContains(t, e.ok("work").err, "claude-hub: profile 'work' (from argument, login)")
	expectContains(t, e.ok().err, "claude-hub: profile 'personal' (from default_profile, key key-personal)")
}

func TestStatusline(t *testing.T) {
	e := setupTwo(t)
	e.stdin = `{"model":{"display_name":"x"}}`
	e.extra["CLAUDE_HUB_PROFILE"] = "work"
	r := e.ok("statusline")
	expectContains(t, r.out, "██\x1b[0m work")
	delete(e.extra, "CLAUDE_HUB_PROFILE")
	e.stdin = "{}"
	expectContains(t, e.ok("statusline").out, "⬜ default")
	// append commands get the payload; a slow one is cut off.
	cfg := readFile(t, e.configFile())
	e.writeConfig(cfg + "\n[statusline]\nsegments = [\"profile\", \"append\"]\ndisks = []\nappend = [{ command = \"cat\", timeout_ms = 1000 }, { command = \"sleep 5; echo late\", timeout_ms = 100 }]\n")
	e.stdin = "PAYLOAD"
	expectOut(t, e.ok("statusline"), "⬜ default · PAYLOAD")
}

func TestWhichJSONAndVersion(t *testing.T) {
	e := setupTwo(t)
	r := e.ok("which", "--json")
	expectContains(t, r.out, `"profile": "personal"`)
	expectContains(t, r.out, `"auth": "key key-personal"`)
	expectContains(t, e.ok("version").out, "real claude: "+filepath.Join(e.bin, "claude"))
}
