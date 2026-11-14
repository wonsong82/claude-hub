package jsonx

import (
	"strings"
	"testing"
)

func TestOrderKeptAndRoundTrip(t *testing.T) {
	in := `{"z":1,"a":{"y":[1,2,{"q":null}],"b":"<&>"},"m":true}`
	v, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(MarshalCompact(v)); got != `{"z": 1, "a": {"y": [1, 2, {"q": null}], "b": "<&>"}, "m": true}` {
		t.Fatal(got)
	}
	out := Marshal(v)
	v2, err := Parse(out)
	if err != nil || !Equal(v, v2) {
		t.Fatalf("%v %s", err, out)
	}
	if !strings.HasPrefix(string(out), "{\n  \"z\": 1,\n  \"a\": {") {
		t.Fatalf("indent: %s", out)
	}
}

func TestPaths(t *testing.T) {
	p, err := ParsePath(`enabledPlugins.foo@v1\.2`)
	if err != nil || len(p) != 2 || p[1] != "foo@v1.2" || p.String() != `enabledPlugins.foo@v1\.2` {
		t.Fatal(p, err)
	}
	for _, bad := range []string{"", ".a", "a.", "a..b", `a\`} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	o := NewObject()
	SetPath(o, MustPath("env.FOO"), "1")
	SetPath(o, MustPath("env.BAR"), "2")
	DeletePath(o, MustPath("env.FOO"))
	if string(MarshalCompact(o)) != `{"env": {"BAR": "2"}}` {
		t.Fatal(string(MarshalCompact(o)))
	}
	if v, ok := Get(o, MustPath("env.BAR")); !ok || v != "2" {
		t.Fatal(v)
	}
}

func TestDiff(t *testing.T) {
	a, _ := Parse([]byte(`{"a":1,"b":{"c":[1,2],"d":"x"},"e":{}}`))
	b, _ := Parse([]byte(`{"a":1,"b":{"c":[2,1],"f":2},"g":3}`))
	var got []string
	for _, ch := range Diff(a, b) {
		got = append(got, ch.Path.String())
	}
	want := "b.c b.d b.f e g"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v want %s", got, want)
	}
	n1, _ := Parse([]byte(`{"x":1.0}`))
	n2, _ := Parse([]byte(`{"x":1}`))
	if len(Diff(n1, n2)) != 0 {
		t.Fatal("1.0 and 1 differ")
	}
}
