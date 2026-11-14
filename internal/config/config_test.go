package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wonsong82/claude-hub/internal/config"
	_ "github.com/wonsong82/claude-hub/internal/share" // wires the never-share list
	"github.com/wonsong82/claude-hub/internal/xdg"
)

const sample = `schema = 1

[hub]
default_profile = "personal"
base_dir        = "~/.claude"
claude_bin      = ""
announce        = "tty"

[secrets]
backend = "auto"

[keys.key-personal]
note = "personal account"
[keys.key-work]
note = "work account"

[profiles.personal]
dir  = "~/.claude"
key  = "key-personal"
icon = "🟩"

[profiles.work]
dir  = "~/.claude-work"
key  = "key-work"
icon = "#eab308"

[profiles.work.share]
sessions    = "shared"
projects    = "shared"
skills      = "shared"
agents      = "shared"
commands    = "shared"
hooks       = "shared"
plugins     = "shared"
keybindings = "shared"
memory      = "shared"
extra       = ["themes"]

[profiles.work.settings]
mode = "layered"
own  = ["model", "modelSettings", "effortLevel", "theme"]

[profiles.login-only]
dir  = "~/.claude-login-only"
icon = "🟦"

[profiles.work.env]
CLAUDE_CODE_USE_BEDROCK = "1"

[[directory]]
path    = "~/workspace/client"
profile = "work"

[statusline]
segments = ["profile", "cpu", "ram", "disks", "append"]
disks    = [{ label = "Storage", path = "/System/Volumes/Data" }]
append   = [{ command = "~/bin/status.sh", timeout_ms = 1000 }]

[tmp]
dir          = ""
max_age_days = 0
`

func TestRoundTrip(t *testing.T) {
	c, err := config.Parse([]byte(sample), "/x/config.toml", "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsBase("personal") || c.IsBase("work") {
		t.Fatal("base detection")
	}
	if c.ProfileDir("work") != "/home/u/.claude-work" {
		t.Fatal(c.ProfileDir("work"))
	}
	if c.ShareState("work", "themes") != config.Shared || c.ShareState("login-only", "skills") != config.Own {
		t.Fatal("share state")
	}
	text := c.Canonical()
	c2, err := config.Parse([]byte(text), "/x/config.toml", "/home/u")
	if err != nil {
		t.Fatalf("canonical does not parse: %v\n%s", err, text)
	}
	if c2.Canonical() != text {
		t.Fatal("canonical form is not stable")
	}
	for _, want := range []string{`icon = "🟩"`, `extra = ["themes"]`, `append = [{ command = "~/bin/status.sh", timeout_ms = 1000 }]`, `CLAUDE_CODE_USE_BEDROCK = "1"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"unknown key":      strings.Replace(sample, "announce        = \"tty\"", "anounce = \"tty\"", 1),
		"newer schema":     strings.Replace(sample, "schema = 1", "schema = 2", 1),
		"no schema":        strings.Replace(sample, "schema = 1", "", 1),
		"bad name":         strings.Replace(sample, "[profiles.login-only]", "[profiles.Login]", 1),
		"reserved name":    strings.Replace(sample, "[profiles.login-only]", "[profiles.mcp]", 1),
		"hub command name": strings.Replace(sample, "[profiles.login-only]", "[profiles.doctor]", 1),
		"same dir":         strings.Replace(sample, `dir  = "~/.claude-login-only"`, `dir  = "~/.claude-work"`, 1),
		"undefined key":    strings.Replace(sample, `key  = "key-work"`, `key  = "key-nope"`, 1),
		"bad share value":  strings.Replace(sample, `sessions    = "shared"`, `sessions    = "yes"`, 1),
		"never-share":      strings.Replace(sample, `extra       = ["themes"]`, `extra       = [".credentials.json"]`, 1),
		"left alone":       strings.Replace(sample, `extra       = ["themes"]`, `extra       = ["history.jsonl"]`, 1),
		"bad mode":         strings.Replace(sample, `mode = "layered"`, `mode = "merged"`, 1),
		"forbidden env":    strings.Replace(sample, `CLAUDE_CODE_USE_BEDROCK = "1"`, `CLAUDE_CODE_OAUTH_TOKEN = "x"`, 1),
		"bad icon":         strings.Replace(sample, `icon = "#eab308"`, `icon = "#eab30"`, 1),
		"base with share":  strings.Replace(sample, "[profiles.work.share]", "[profiles.personal.share]\nskills = \"shared\"\n[profiles.work.share]", 1),
		"bad announce":     strings.Replace(sample, `announce        = "tty"`, `announce = "loud"`, 1),
	}
	for name, text := range cases {
		if _, err := config.Parse([]byte(text), "c.toml", "/home/u"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSaveKeepsBackupAndValidates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLAUDE_HUB_CONFIG", filepath.Join(dir, "c", "config.toml"))
	p := xdg.Get()
	c, exists, err := config.Load(p)
	if err != nil || exists {
		t.Fatal(err, exists)
	}
	c.Profiles["a"] = &config.Profile{Dir: "~/.claude-a"}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(p.LockDir()); err != nil {
		t.Fatal(err)
	}
	c.Profiles["b"] = &config.Profile{Dir: "~/.claude-b"}
	if err := c.Save(p.LockDir()); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(c.Path + ".bak")
	if err != nil || strings.Contains(string(bak), "profiles.b") || !strings.Contains(string(bak), "profiles.a") {
		t.Fatalf("backup: %v %s", err, bak)
	}
	c.Profiles["Bad"] = &config.Profile{}
	if err := c.Save(p.LockDir()); err == nil {
		t.Fatal("saved an invalid config")
	}
}
