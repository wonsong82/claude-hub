package shim

import (
	"strings"
	"testing"
)

func TestBlocks(t *testing.T) {
	b := Block("zsh", "/h/.local/share/claude-hub/shim", "/h")
	if !strings.Contains(b, `"$HOME/.local/share/claude-hub/shim":*) ;; *) export PATH="$HOME/.local/share/claude-hub/shim:$PATH"`) {
		t.Fatal(b)
	}
	orig := "a\nb\n"
	one := AppendBlock(orig, b)
	if p, last := BlockIsLast(one); !p || !last {
		t.Fatal("not last")
	}
	// A third party appends; the repair moves the block last, once.
	two := AppendBlock(one+"export PATH=x\n", b)
	if strings.Count(two, BeginMarker) != 1 || !strings.HasSuffix(two, EndMarker+"\n") {
		t.Fatal(two)
	}
	back, found := RemoveBlock(two)
	if !found || back != orig+"export PATH=x\n" {
		t.Fatalf("%q", back)
	}
	if p, _ := BlockIsLast(one + "x\n"); !p {
		t.Fatal("present")
	}
	if _, last := BlockIsLast(one + "x\n"); last {
		t.Fatal("reported last")
	}
	// No trailing newline: a newline is added before the block.
	if got := AppendBlock("a", b); !strings.HasPrefix(got, "a\n"+BeginMarker) {
		t.Fatal(got)
	}
	f := Block("fish", "/opt/shim", "/h")
	if !strings.Contains(f, `fish_add_path --move --prepend --path "/opt/shim"`) {
		t.Fatal(f)
	}
}

func TestStablePath(t *testing.T) {
	if StablePath("/no/such/claude-hub") != "/no/such/claude-hub" {
		t.Fatal("curl install path changed")
	}
}
