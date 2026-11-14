package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jsonOf(t *testing.T, p string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, p)), &m); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return m
}

func TestLayeredSettingsWithDrift(t *testing.T) {
	e := newEnv(t)
	e.writeConfig(fileBackendConfig)
	base := filepath.Join(e.home, ".claude", "settings.json")
	writeFile(t, base, `{"model":"opus","theme":"dark","hooks":{"Stop":[{"timeout":10}]},"env":{"A":"1"}}`+"\n")
	e.ok("profile", "add", "main", "--dir", "~/.claude", "--login")
	e.ok("profile", "add", "work", "--login")
	ws := filepath.Join(e.home, ".claude-work", "settings.json")
	// Generated: the base without the owned paths.
	m := jsonOf(t, ws)
	if _, ok := m["model"]; ok {
		t.Fatalf("owned model leaked from the base: %v", m)
	}
	if m["hooks"] == nil || m["env"] == nil {
		t.Fatalf("inherited paths missing: %v", m)
	}
	// /model in the profile: owned, written back silently.
	writeFile(t, ws, strings.Replace(readFile(t, ws), `"hooks"`, `"model": "sonnet",`+"\n"+`  "hooks"`, 1))
	r := e.ok("work")
	if strings.Contains(r.err, "local change") {
		t.Fatalf("owned change reported as drift: %s", r)
	}
	ov := jsonOf(t, filepath.Join(e.home, ".config", "claude-hub", "profiles", "work", "settings.overlay.json"))
	if ov["model"] != "sonnet" {
		t.Fatalf("overlay: %v", ov)
	}
	// A base change reaches the profile at the next sync; the owned value stays.
	writeFile(t, base, `{"model":"opus","theme":"dark","hooks":{"Stop":[{"timeout":130}]},"env":{"A":"1"},"new":true}`+"\n")
	e.ok("sync", "work")
	m = jsonOf(t, ws)
	if m["model"] != "sonnet" || m["new"] != true {
		t.Fatalf("after base change: %v", m)
	}
	// An inherited path changed in the profile: drift, kept and reported.
	m["env"] = map[string]any{"A": "2"}
	b, _ := json.MarshalIndent(m, "", "  ")
	writeFile(t, ws, string(b))
	r = e.ok("work")
	expectContains(t, r.err, "work has 1 local change to inherited settings; run \"claude-hub settings resolve work\"")
	if jsonOf(t, ws)["env"].(map[string]any)["A"] != "2" {
		t.Fatal("drift was lost")
	}
	// Another base change does not wipe the drift.
	writeFile(t, base, `{"model":"opus","theme":"light","hooks":{"Stop":[{"timeout":130}]},"env":{"A":"1"},"new":true}`+"\n")
	r = e.ok("work")
	expectContains(t, r.err, "1 local change")
	m = jsonOf(t, ws)
	if m["env"].(map[string]any)["A"] != "2" || m["theme"] != nil {
		t.Fatalf("after base change with drift: %v", m)
	}
	// diff masks env values.
	d := e.ok("settings", "diff", "work")
	expectContains(t, d.out, "drift    env.A")
	if strings.Contains(d.out, `"2"`) {
		t.Fatalf("env value shown: %s", d.out)
	}
	expectContains(t, d.out, "owned    model")
	// resolve without a terminal needs --all.
	expectCode(t, e.hub("settings", "resolve", "work"), 64)
	e.ok("settings", "resolve", "work", "--all", "promote")
	if jsonOf(t, base)["env"].(map[string]any)["A"] != "2" {
		t.Fatal("not promoted into the base")
	}
	r = e.ok("work")
	if strings.Contains(r.err, "local change") {
		t.Fatalf("drift remains: %s", r)
	}
	// resolve own: the path joins the owned list.
	m = jsonOf(t, ws)
	m["deniedMcpServers"] = []any{"x"}
	b, _ = json.MarshalIndent(m, "", "  ")
	writeFile(t, ws, string(b))
	e.ok("sync")
	e.ok("settings", "resolve", "work", "--all", "own")
	expectContains(t, readFile(t, e.configFile()), `"deniedMcpServers"`)
	// discard: the base value comes back, the profile's value goes to a backup.
	m = jsonOf(t, ws)
	m["new"] = false
	b, _ = json.MarshalIndent(m, "", "  ")
	writeFile(t, ws, string(b))
	e.ok("sync")
	e.ok("settings", "resolve", "work", "--all", "discard")
	if jsonOf(t, ws)["new"] != true {
		t.Fatal("discard did not restore the base value")
	}
	found := false
	for _, n := range backups(t, e) {
		if strings.Contains(n, "settings-discard") {
			found = true
		}
	}
	if !found {
		t.Fatal("no discard backup")
	}
}

func TestSettingsModeOwnToLayeredKeepsTheFile(t *testing.T) {
	e := adopted(t)
	ws := filepath.Join(e.home, ".claude-work", "settings.json")
	before := jsonOf(t, ws)
	r := e.ok("settings", "mode", "work", "layered")
	expectContains(t, r.out, "owned paths")
	after := jsonOf(t, ws)
	bb, _ := json.Marshal(before)
	ab, _ := json.Marshal(after)
	if string(bb) != string(ab) {
		t.Fatalf("behaviour changed:\n%s\n%s", bb, ab)
	}
	r = e.ok("work")
	if strings.Contains(r.err, "local change") {
		t.Fatalf("drift after mode change: %s", r)
	}
	// layered -> shared with an overlay: conflict unless keep-base.
	expectCode(t, e.hub("settings", "mode", "work", "shared"), 1)
	e.ok("settings", "mode", "work", "shared", "--on-conflict", "keep-base")
	if !isLink(ws) {
		t.Fatal("not linked")
	}
	// shared -> own: a copy of the base.
	e.ok("settings", "mode", "work", "own")
	if isLink(ws) || jsonOf(t, ws)["model"] != "opus" {
		t.Fatal("not a copy of the base")
	}
}

func TestSettingsOwnAddRemove(t *testing.T) {
	e := setupTwo(t)
	writeFile(t, filepath.Join(e.home, ".claude", "settings.json"), `{"model":"opus","tui":{"x":1}}`+"\n")
	e.ok("sync")
	ws := filepath.Join(e.home, ".claude-work", "settings.json")
	e.ok("settings", "own", "work", "add", "tui")
	if jsonOf(t, ws)["tui"] == nil {
		t.Fatal("owning a path changed the value")
	}
	expectCode(t, e.hub("settings", "own", "work", "remove", "nope"), 1)
	e.ok("settings", "own", "work", "remove", "tui")
	if jsonOf(t, ws)["tui"] == nil {
		t.Fatal("the inherited value should come back")
	}
}

func TestStatuslineInstall(t *testing.T) {
	e := setupTwo(t)
	base := filepath.Join(e.home, ".claude", "settings.json")
	writeFile(t, base, `{"statusLine":{"type":"command","command":"other"}}`+"\n")
	expectCode(t, e.hub("statusline", "install"), 1)
	e.ok("statusline", "install", "--force")
	sl := jsonOf(t, base)["statusLine"].(map[string]any)
	if !strings.HasSuffix(sl["command"].(string), "claude-hub statusline") || !filepath.IsAbs(strings.Fields(sl["command"].(string))[0]) {
		t.Fatalf("statusLine: %v", sl)
	}
	// It reaches the layered profile.
	if jsonOf(t, filepath.Join(e.home, ".claude-work", "settings.json"))["statusLine"] == nil {
		t.Fatal("not inherited")
	}
	e.ok("statusline", "uninstall")
	if jsonOf(t, base)["statusLine"] != nil {
		t.Fatal("not removed")
	}
}

func TestBaseSettingsLinkSurvivesWrites(t *testing.T) {
	e := setupTwo(t)
	dot := filepath.Join(e.home, "dotfiles", "settings.json")
	writeFile(t, dot, `{"model":"opus"}`+"\n")
	base := filepath.Join(e.home, ".claude", "settings.json")
	if err := os.Symlink(dot, base); err != nil {
		t.Fatal(err)
	}
	e.ok("statusline", "install")
	if !isLink(base) || jsonOf(t, dot)["statusLine"] == nil {
		t.Fatal("statusline install replaced the base link")
	}
	ws := filepath.Join(e.home, ".claude-work", "settings.json")
	m := jsonOf(t, ws)
	m["x"] = 1
	b, _ := json.MarshalIndent(m, "", "  ")
	writeFile(t, ws, string(b))
	e.ok("sync")
	e.ok("settings", "resolve", "work", "--all", "promote")
	if !isLink(base) || jsonOf(t, dot)["x"] == nil {
		t.Fatal("promote replaced the base link")
	}
}
