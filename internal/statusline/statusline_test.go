package statusline

import (
	"strings"
	"testing"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

func TestIcon(t *testing.T) {
	if Icon("#eab308") != "\033[38;2;234;179;8m██\033[0m" {
		t.Fatal(Icon("#eab308"))
	}
	if Icon("") != "⬜" || Icon("🟩") != "🟩" {
		t.Fatal("emoji")
	}
}

func TestRenderNeverFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := xdg.Get()
	c := config.New("c", p.Home)
	c.Profiles["work"] = &config.Profile{Icon: "🟦"}
	c.Statusline = &config.Statusline{Segments: []string{"profile", "cpu", "ram", "disks", "append"},
		Disks:  []config.Disk{{Label: "Root", Path: "/"}, {Label: "Gone", Path: "/no/such"}},
		Append: []config.AppendCmd{{Command: "exit 3"}, {Command: "head -c 3", TimeoutMS: 500}}}
	line := Render(c, p, "work", []byte("abcdef"))
	if !strings.HasPrefix(line, "🟦 work") || !strings.Contains(line, "Root ") || strings.Contains(line, "Gone") || !strings.HasSuffix(line, "abc") {
		t.Fatal(line)
	}
	if got := Render(nil, p, "", nil); !strings.HasPrefix(got, "⬜ default") {
		t.Fatal(got)
	}
}
