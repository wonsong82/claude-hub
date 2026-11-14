package share

import (
	"testing"

	"github.com/wonsong82/claude-hub/internal/config"
)

func TestNeverShareRefusedEverywhere(t *testing.T) {
	for _, n := range []string{".claude.json", ".credentials.json", "backups", "policy-limits.json", "policy-limits.json.signature.json",
		"remote-settings.json.synced-at", "daemon", "daemon.lock", "daemon.log", "daemon.status.json", "jobs", "state",
		"mcp-needs-auth-cache.json", "statsig", "telemetry"} {
		if !NeverShare(n) {
			t.Errorf("%s not on the never-share list", n)
		}
		if config.CheckExtraName(n) == nil {
			t.Errorf("%s accepted as an extra item", n)
		}
	}
	for _, n := range []string{"history.jsonl", "cache", "settings.json", "skills", "file-history", "CLAUDE.md", "a/b", ""} {
		if config.CheckExtraName(n) == nil {
			t.Errorf("%s accepted as an extra item", n)
		}
	}
	if config.CheckExtraName("themes") != nil {
		t.Error("themes refused")
	}
}

func TestItemTable(t *testing.T) {
	if ItemForPath("file-history") != "projects" || ItemForPath("rules") != "memory" || ItemForPath("keybindings.json") != "keybindings" {
		t.Fatal("paths")
	}
	if len(Items) != len(config.ShareItems) {
		t.Fatal("item lists disagree")
	}
	for i, it := range Items {
		if it.Name != config.ShareItems[i] {
			t.Fatal("order")
		}
	}
}
