package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/jsonx"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

func obj(t *testing.T, s string) *jsonx.Object {
	t.Helper()
	o, err := jsonx.ParseObject([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestGenerate(t *testing.T) {
	cases := []struct {
		b, o string
		own  []string
		want string
	}{
		{`{"model":"opus","theme":"dark","hooks":{"a":1}}`, `{}`, config.DefaultOwn, `{"hooks":{"a":1}}`},
		{`{"model":"opus","hooks":{"a":1}}`, `{"model":"sonnet"}`, config.DefaultOwn, `{"hooks":{"a":1},"model":"sonnet"}`},
		{`{"env":{"A":"1","B":"2"}}`, `{"env":{"A":"9"}}`, []string{"env.A"}, `{"env":{"A":"9","B":"2"}}`},
		{`{"env":{"A":"1"}}`, `{}`, []string{"env.A"}, `{"env":{}}`},
		{`{"arr":[1,2,3]}`, `{"arr":[9]}`, []string{"arr"}, `{"arr":[9]}`},
		{`{"modelSettings":{"x":{"effortLevel":"high"}}}`, `{"modelSettings":{"y":{}}}`, []string{"modelSettings"}, `{"modelSettings":{"y":{}}}`},
		{`{"x":"str"}`, `{"x":{"y":1}}`, []string{"x.y"}, `{"x":{"y":1}}`},
	}
	for i, c := range cases {
		got := Generate(obj(t, c.b), obj(t, c.o), c.own)
		if !jsonx.Equal(got, obj(t, c.want)) {
			t.Errorf("case %d: got %s want %s", i, jsonx.MarshalCompact(got), c.want)
		}
	}
}

// fixture builds a config with a base and a layered profile "work" in a temp home.
func fixture(t *testing.T) (xdg.Paths, *config.Config, Files) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CLAUDE_HUB_CONFIG", "")
	p := xdg.Get()
	c := config.New(p.ConfigFile, home)
	c.Profiles["base"] = &config.Profile{Dir: "~/.claude"}
	c.Profiles["work"] = &config.Profile{Dir: "~/.claude-work", Settings: &config.SettingsCfg{Mode: config.ModeLayered, Own: append([]string(nil), config.DefaultOwn...)}}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.MkdirAll(filepath.Join(home, ".claude-work"), 0o700)
	os.MkdirAll(filepath.Dir(c.Path), 0o755)
	if err := c.Save(p.LockDir()); err != nil {
		t.Fatal(err)
	}
	return p, c, FilesFor(p, c, "work")
}

func put(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) *jsonx.Object {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return obj(t, string(b))
}

func TestSyncOwnedDriftAndFastPath(t *testing.T) {
	p, c, f := fixture(t)
	put(t, f.Base, `{"model":"opus","hooks":{"t":10},"env":{"A":"1"}}`)
	res, err := Sync(p, c, "work")
	if err != nil || !res.Changed {
		t.Fatal(res, err)
	}
	if !jsonx.Equal(read(t, f.Profile), obj(t, `{"hooks":{"t":10},"env":{"A":"1"}}`)) {
		t.Fatal(string(jsonx.Marshal(read(t, f.Profile))))
	}
	// Nothing changed: the four-hash fast path.
	res, _ = Sync(p, c, "work")
	if !res.Unchanged {
		t.Fatal("no fast path")
	}
	// Owned change -> overlay; inherited change -> drift.
	put(t, f.Profile, `{"model":"sonnet","hooks":{"t":130},"env":{"A":"1"}}`)
	res, err = Sync(p, c, "work")
	if err != nil || res.Drift != 1 || len(res.OwnedSaved) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if v, _ := jsonx.Get(read(t, f.Overlay), jsonx.MustPath("model")); v != "sonnet" {
		t.Fatal("overlay")
	}
	d, _ := LoadDrift(f.Drift)
	if _, ok := d["hooks.t"]; !ok {
		t.Fatalf("drift %v", d)
	}
	// Base changes: drift and owned value survive, the rest follows the base.
	put(t, f.Base, `{"model":"opus","hooks":{"t":10},"env":{"A":"2"},"n":1}`)
	res, _ = Sync(p, c, "work")
	got := read(t, f.Profile)
	if !jsonx.Equal(got, obj(t, `{"hooks":{"t":130},"env":{"A":"2"},"n":1,"model":"sonnet"}`)) || res.Drift != 1 {
		t.Fatalf("got %s", jsonx.MarshalCompact(got))
	}
	// The snapshot holds no drift.
	if !jsonx.Equal(read(t, f.Last), obj(t, `{"hooks":{"t":10},"env":{"A":"2"},"n":1,"model":"sonnet"}`)) {
		t.Fatal("snapshot")
	}
	// Removing an owned value (absence) is copied too.
	put(t, f.Profile, `{"hooks":{"t":130},"env":{"A":"2"},"n":1}`)
	Sync(p, c, "work")
	if _, ok := jsonx.Get(read(t, f.Overlay), jsonx.MustPath("model")); ok {
		t.Fatal("absence not copied")
	}
	// Deleting an inherited path is drift too, and stays deleted.
	put(t, f.Profile, `{"hooks":{"t":130},"env":{"A":"2"}}`)
	res, _ = Sync(p, c, "work")
	if res.Drift != 2 {
		t.Fatalf("drift %d", res.Drift)
	}
	if _, ok := jsonx.Get(read(t, f.Profile), jsonx.MustPath("n")); ok {
		t.Fatal("a deleted path came back")
	}
	// Regenerate with no drift settles it.
	if err := Regenerate(p, c, "work", Drift{}); err != nil {
		t.Fatal(err)
	}
	if !jsonx.Equal(read(t, f.Profile), obj(t, `{"hooks":{"t":10},"env":{"A":"2"},"n":1}`)) {
		t.Fatal(string(jsonx.Marshal(read(t, f.Profile))))
	}
}

func TestSyncNoSnapshotTreatsDifferencesAsDrift(t *testing.T) {
	p, c, f := fixture(t)
	put(t, f.Base, `{"a":1}`)
	put(t, f.Profile, `{"a":2,"model":"x"}`)
	res, err := Sync(p, c, "work")
	if err != nil || res.Drift != 1 {
		t.Fatal(res, err)
	}
	if !jsonx.Equal(read(t, f.Profile), obj(t, `{"a":2,"model":"x"}`)) {
		t.Fatal("first sync lost data")
	}
}

func TestSyncRefusesBrokenJSON(t *testing.T) {
	p, c, f := fixture(t)
	put(t, f.Base, `{"a":1}`)
	Sync(p, c, "work")
	put(t, f.Profile, `{"a":`)
	if _, err := Sync(p, c, "work"); err == nil {
		t.Fatal("accepted broken JSON")
	}
	b, _ := os.ReadFile(f.Profile)
	if string(b) != `{"a":` {
		t.Fatal("overwrote the broken file")
	}
}

func TestModeOwnToLayeredIsLossless(t *testing.T) {
	p, c, f := fixture(t)
	c.Profiles["work"].Settings = &config.SettingsCfg{Mode: config.ModeOwn}
	c.Save(p.LockDir())
	put(t, f.Base, `{"model":"opus","hooks":{"PermissionRequest":[{"timeout":10}]},"theme":"dark"}`)
	put(t, f.Profile, `{"model":"sonnet","hooks":{"PermissionRequest":[{"timeout":130}]},"theme":"dark","x":1}`)
	before := read(t, f.Profile)
	mp, err := PlanMode(p, c, "work", config.ModeLayered, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := mp.Run(p, "work"); err != nil {
		t.Fatal(err)
	}
	if !jsonx.Equal(read(t, f.Profile), before) {
		t.Fatal("behaviour changed")
	}
	c2, _, _ := config.Load(p)
	own := c2.OwnPaths("work")
	want := map[string]bool{"hooks.PermissionRequest": true, "x": true, "model": true}
	for _, o := range own {
		delete(want, o)
	}
	if len(want) != 0 || c2.SettingsMode("work") != config.ModeLayered {
		t.Fatalf("own %v", own)
	}
	res, _ := Sync(p, c2, "work")
	if res.Drift != 0 {
		t.Fatal("drift after the change")
	}
}
