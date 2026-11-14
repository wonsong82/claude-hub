// Package jsonx is a small JSON value model that keeps object key order, so that files the
// hub rewrites (settings.json) keep the layout Claude Code and the user gave them. It also
// provides the dot-path operations the layered settings use (design section 5).
//
// Values are: *Object, []any, string, json.Number, bool and nil.
package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Object is a JSON object with ordered keys.
type Object struct {
	keys []string
	m    map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{m: map[string]any{}} }

// Keys returns the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Len returns the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// Get returns the value under k.
func (o *Object) Get(k string) (any, bool) {
	v, ok := o.m[k]
	return v, ok
}

// Set sets k, appending it when new.
func (o *Object) Set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

// Delete removes k.
func (o *Object) Delete(k string) {
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Parse decodes JSON text. Empty or whitespace-only input yields an empty object.
func Parse(data []byte) (any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return NewObject(), nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return v, nil
}

// ParseObject decodes JSON text that must be an object.
func ParseObject(data []byte) (*Object, error) {
	v, err := Parse(data)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, fmt.Errorf("not a JSON object")
	}
	return o, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// FromPlain converts values produced by encoding/json (map[string]any etc.) into jsonx
// values; maps get sorted keys.
func FromPlain(v any) any {
	switch t := v.(type) {
	case map[string]any:
		o := NewObject()
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			o.Set(k, FromPlain(t[k]))
		}
		return o
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = FromPlain(x)
		}
		return out
	case float64:
		return json.Number(fmt.Sprint(t))
	case int:
		return json.Number(fmt.Sprint(t))
	default:
		return v
	}
}

// Marshal encodes v with two-space indentation and a trailing newline.
func Marshal(v any) []byte {
	var b bytes.Buffer
	writeValue(&b, v, 0)
	b.WriteByte('\n')
	return b.Bytes()
}

// MarshalCompact encodes v on one line.
func MarshalCompact(v any) []byte {
	var b bytes.Buffer
	writeValue(&b, v, -1)
	return b.Bytes()
}

func writeString(b *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
}

func indent(b *bytes.Buffer, n int) {
	if n < 0 {
		return
	}
	b.WriteByte('\n')
	for i := 0; i < n; i++ {
		b.WriteString("  ")
	}
}

func writeValue(b *bytes.Buffer, v any, lvl int) {
	next := lvl + 1
	if lvl < 0 {
		next = -1
	}
	switch t := v.(type) {
	case *Object:
		if t == nil || len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
				if lvl < 0 {
					b.WriteByte(' ')
				}
			}
			indent(b, next)
			writeString(b, k)
			b.WriteString(": ")
			writeValue(b, t.m[k], next)
		}
		indent(b, lvl)
		b.WriteByte('}')
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
				if lvl < 0 {
					b.WriteByte(' ')
				}
			}
			indent(b, next)
			writeValue(b, x, next)
		}
		indent(b, lvl)
		b.WriteByte(']')
	case string:
		writeString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(raw)
	}
}

// Equal compares two values. Object key order does not matter; array order does.
func Equal(a, b any) bool {
	switch x := a.(type) {
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.keys {
			yv, ok := y.m[k]
			if !ok || !Equal(x.m[k], yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		if x == y {
			return true
		}
		fx, e1 := x.Float64()
		fy, e2 := y.Float64()
		return e1 == nil && e2 == nil && fx == fy
	default:
		return a == b
	}
}

// Clone deep-copies v.
func Clone(v any) any {
	switch t := v.(type) {
	case *Object:
		o := NewObject()
		for _, k := range t.keys {
			o.Set(k, Clone(t.m[k]))
		}
		return o
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = Clone(x)
		}
		return out
	default:
		return v
	}
}

// Path is a parsed dot path.
type Path []string

// ParsePath splits a dot path. A backslash escapes a dot or a backslash.
func ParsePath(s string) (Path, error) {
	if s == "" {
		return nil, fmt.Errorf("empty path")
	}
	var out Path
	var cur strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == '.':
			if cur.Len() == 0 {
				return nil, fmt.Errorf("path %q has an empty segment", s)
			}
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if esc || cur.Len() == 0 {
		return nil, fmt.Errorf("path %q is malformed", s)
	}
	return append(out, cur.String()), nil
}

// MustPath parses a path known to be valid.
func MustPath(s string) Path {
	p, err := ParsePath(s)
	if err != nil {
		panic(err)
	}
	return p
}

// String encodes the path, escaping dots and backslashes in segments.
func (p Path) String() string {
	parts := make([]string, len(p))
	for i, s := range p {
		s = strings.ReplaceAll(s, `\`, `\\`)
		parts[i] = strings.ReplaceAll(s, ".", `\.`)
	}
	return strings.Join(parts, ".")
}

// HasPrefix reports whether q is p or lies under p.
func (p Path) HasPrefix(q Path) bool {
	if len(q) > len(p) {
		return false
	}
	for i := range q {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// Get returns the value at path.
func Get(root any, p Path) (any, bool) {
	cur := root
	for _, seg := range p {
		o, ok := cur.(*Object)
		if !ok {
			return nil, false
		}
		cur, ok = o.Get(seg)
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// SetPath sets the value at path, creating (or replacing non-object) intermediate values.
func SetPath(root *Object, p Path, v any) {
	cur := root
	for i, seg := range p {
		if i == len(p)-1 {
			cur.Set(seg, v)
			return
		}
		nx, ok := cur.Get(seg)
		o, isObj := nx.(*Object)
		if !ok || !isObj {
			o = NewObject()
			cur.Set(seg, o)
		}
		cur = o
	}
}

// DeletePath removes the value at path. Parent objects that become empty are kept.
func DeletePath(root *Object, p Path) {
	cur := root
	for i, seg := range p {
		if i == len(p)-1 {
			cur.Delete(seg)
			return
		}
		nx, ok := cur.Get(seg)
		o, isObj := nx.(*Object)
		if !ok || !isObj {
			return
		}
		cur = o
	}
}

// Change is one leaf difference between two values.
type Change struct {
	Path Path
	A    any
	AHas bool
	B    any
	BHas bool
}

// Diff returns the minimal paths where a and b differ. Objects are compared per key;
// arrays and scalars are atomic.
func Diff(a, b any) []Change {
	var out []Change
	diffInto(&out, nil, a, true, b, true)
	return out
}

func diffInto(out *[]Change, prefix Path, a any, aHas bool, b any, bHas bool) {
	ao, aObj := a.(*Object)
	bo, bObj := b.(*Object)
	if aHas && bHas && aObj && bObj {
		seen := map[string]bool{}
		for _, k := range ao.keys {
			seen[k] = true
			bv, ok := bo.m[k]
			diffInto(out, append(append(Path(nil), prefix...), k), ao.m[k], true, bv, ok)
		}
		for _, k := range bo.keys {
			if !seen[k] {
				diffInto(out, append(append(Path(nil), prefix...), k), nil, false, bo.m[k], true)
			}
		}
		return
	}
	if aHas == bHas && (!aHas || Equal(a, b)) {
		return
	}
	*out = append(*out, Change{Path: prefix, A: a, AHas: aHas, B: b, BHas: bHas})
}
