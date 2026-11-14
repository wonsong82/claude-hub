// Package settings implements settings.json modes (design section 5): generate, sync with
// write-back of owned paths, drift on inherited paths, resolve, and mode changes.
package settings

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/jsonx"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// FileName is Claude Code's settings file.
const FileName = "settings.json"

// Generate computes generate(B, O, own): B, with every owned path replaced by O's value
// (or removed when O has none). Arrays are atomic.
func Generate(b, o *jsonx.Object, own []string) *jsonx.Object {
	r := jsonx.Clone(b).(*jsonx.Object)
	for _, s := range own {
		p, err := jsonx.ParsePath(s)
		if err != nil {
			continue
		}
		jsonx.DeletePath(r, p)
		if v, ok := jsonx.Get(o, p); ok {
			jsonx.SetPath(r, p, jsonx.Clone(v))
		}
	}
	return r
}

// Drift is a set of local changes to inherited paths.
type Drift map[string]DriftValue

// DriftValue is a drifted value, or its absence.
type DriftValue struct {
	Value  any
	Absent bool
}

// Apply writes the drift on top of r.
func (d Drift) Apply(r *jsonx.Object) {
	for _, k := range d.Keys() {
		p, err := jsonx.ParsePath(k)
		if err != nil {
			continue
		}
		if d[k].Absent {
			jsonx.DeletePath(r, p)
		} else {
			jsonx.SetPath(r, p, jsonx.Clone(d[k].Value))
		}
	}
}

// Keys returns the drifted paths, sorted.
func (d Drift) Keys() []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Files are the paths involved for one profile.
type Files struct {
	Base, Profile, Overlay, Last, Drift, Hashes string
}

// FilesFor returns the file paths for a profile.
func FilesFor(p xdg.Paths, c *config.Config, name string) Files {
	sd := filepath.Join(p.ProfileStateDir(name), "settings")
	return Files{
		Base:    filepath.Join(c.BaseDir(), FileName),
		Profile: filepath.Join(c.ProfileDir(name), FileName),
		Overlay: p.OverlayFile(name),
		Last:    filepath.Join(sd, "last.json"),
		Drift:   filepath.Join(sd, "settings.drift.json"),
		Hashes:  filepath.Join(sd, "hashes.json"),
	}
}

func readObj(path string) (*jsonx.Object, []byte, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return jsonx.NewObject(), nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	o, err := jsonx.ParseObject(b)
	if err != nil {
		return nil, nil, true, fmt.Errorf("%s: %v", path, err)
	}
	return o, b, true, nil
}

// ReadObject reads a JSON object file; a missing file is an empty object.
func ReadObject(path string) (*jsonx.Object, bool, error) {
	o, _, ok, err := readObj(path)
	return o, ok, err
}

// LoadDrift reads the stored drift.
func LoadDrift(path string) (Drift, error) {
	o, _, _, err := readObj(path)
	if err != nil {
		return nil, err
	}
	d := Drift{}
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		e, ok := v.(*jsonx.Object)
		if !ok {
			continue
		}
		if a, _ := e.Get("absent"); a == true {
			d[k] = DriftValue{Absent: true}
		} else if val, ok := e.Get("value"); ok {
			d[k] = DriftValue{Value: val}
		}
	}
	return d, nil
}

func saveDrift(path string, d Drift) error {
	if len(d) == 0 {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	o := jsonx.NewObject()
	for _, k := range d.Keys() {
		e := jsonx.NewObject()
		if d[k].Absent {
			e.Set("absent", true)
		} else {
			e.Set("value", d[k].Value)
		}
		o.Set(k, e)
	}
	return fsutil.WriteFileAtomic(path, jsonx.Marshal(o), 0o600)
}

func hashOwn(own []string) string { return fsutil.HashBytes([]byte(strings.Join(own, "\n"))) }

// Result of a sync.
type Result struct {
	Changed     bool     // the profile's file was rewritten
	OwnedSaved  []string // owned paths copied into the overlay
	Drift       int      // drifted paths now stored
	NewDrift    []string // paths that drifted in this sync
	Unchanged   bool     // the four-hash fast path
	ProfileFile string
}

// Sync runs the layered sync for one profile (design section 5). The caller checks that
// the profile is layered.
func Sync(p xdg.Paths, c *config.Config, name string) (*Result, error) {
	f := FilesFor(p, c, name)
	l, err := fsutil.LockFile(filepath.Join(p.LockDir(), "settings-"+name+".lock"))
	if err != nil {
		return nil, err
	}
	defer l.Unlock()
	own := c.OwnPaths(name)
	res := &Result{ProfileFile: f.Profile}

	B, bb, _, err := readObj(f.Base)
	if err != nil {
		return nil, err
	}
	O, ob, _, err := readObj(f.Overlay)
	if err != nil {
		return nil, err
	}
	cb, cerr := os.ReadFile(f.Profile)
	cExists := cerr == nil
	if cerr != nil && !errors.Is(cerr, fs.ErrNotExist) {
		return nil, cerr
	}
	hashes, _, _, _ := readObj(f.Hashes)
	want := map[string]string{"c": fsutil.HashBytes(cb), "b": fsutil.HashBytes(bb), "o": fsutil.HashBytes(ob), "own": hashOwn(own)}
	same := cExists
	for k, v := range want {
		if h, _ := hashes.Get(k); h != v {
			same = false
		}
	}
	drift, err := LoadDrift(f.Drift)
	if err != nil {
		return nil, err
	}
	if same {
		res.Unchanged = true
		res.Drift = len(drift)
		return res, nil
	}

	L, _, lExists, err := readObj(f.Last)
	if err != nil {
		return nil, err
	}
	if !lExists {
		L = Generate(B, O, own)
	}
	if cExists {
		C, err := jsonx.ParseObject(cb)
		if err != nil {
			return nil, fmt.Errorf("%s is not valid JSON (%v); fix it, then run \"claude-hub sync\"", f.Profile, err)
		}
		// Owned paths: copy C's value (or its absence) into O.
		oChanged := false
		for _, s := range own {
			pth, err := jsonx.ParsePath(s)
			if err != nil {
				continue
			}
			lv, lok := jsonx.Get(L, pth)
			cv, cok := jsonx.Get(C, pth)
			if lok == cok && (!lok || jsonx.Equal(lv, cv)) {
				continue
			}
			if cok {
				jsonx.SetPath(O, pth, jsonx.Clone(cv))
			} else {
				jsonx.DeletePath(O, pth)
			}
			oChanged = true
			res.OwnedSaved = append(res.OwnedSaved, s)
		}
		_ = oChanged
		// Inherited paths: every difference between C and L is drift. C carries the drift
		// re-applied last time, so drift is recomputed in full.
		Li, Ci := stripOwned(L, own), stripOwned(C, own)
		newDrift := Drift{}
		for _, ch := range jsonx.Diff(Li, Ci) {
			k := ch.Path.String()
			if ch.BHas {
				newDrift[k] = DriftValue{Value: jsonx.Clone(ch.B)}
			} else {
				newDrift[k] = DriftValue{Absent: true}
			}
			if _, had := drift[k]; !had {
				res.NewDrift = append(res.NewDrift, k)
			}
		}
		drift = newDrift
	}
	if err := write(f, B, O, own, drift, res, cb, cExists); err != nil {
		return nil, err
	}
	res.Drift = len(drift)
	return res, nil
}

func stripOwned(o *jsonx.Object, own []string) *jsonx.Object {
	r := jsonx.Clone(o).(*jsonx.Object)
	for _, s := range own {
		if p, err := jsonx.ParsePath(s); err == nil {
			jsonx.DeletePath(r, p)
		}
	}
	return r
}

// write generates the file, re-applies drift, and stores overlay, snapshot, drift and hashes.
func write(f Files, B, O *jsonx.Object, own []string, drift Drift, res *Result, cb []byte, cExists bool) error {
	G := Generate(B, O, own)
	final := jsonx.Clone(G).(*jsonx.Object)
	drift.Apply(final)
	out := jsonx.Marshal(final)
	ob := jsonx.Marshal(O)
	if O.Len() == 0 {
		if _, err := os.Stat(f.Overlay); errors.Is(err, fs.ErrNotExist) {
			ob = nil
		}
	}
	if ob != nil {
		old, _ := os.ReadFile(f.Overlay)
		if string(old) != string(ob) {
			if err := fsutil.WriteFileAtomic(f.Overlay, ob, 0o644); err != nil {
				return err
			}
		}
	}
	if !cExists || string(cb) != string(out) || fsutil.IsSymlink(f.Profile) {
		perm := os.FileMode(0o644)
		if fi, err := os.Stat(f.Profile); err == nil && !fsutil.IsSymlink(f.Profile) {
			perm = fi.Mode().Perm()
		}
		if err := fsutil.WriteFileAtomic(f.Profile, out, perm); err != nil {
			return err
		}
		res.Changed = true
	}
	if err := fsutil.WriteFileAtomic(f.Last, jsonx.Marshal(G), 0o600); err != nil {
		return err
	}
	if err := saveDrift(f.Drift, drift); err != nil {
		return err
	}
	bb, _ := os.ReadFile(f.Base)
	h := jsonx.NewObject()
	h.Set("c", fsutil.HashBytes(out))
	h.Set("b", fsutil.HashBytes(bb))
	h.Set("o", fsutil.HashBytes(ob))
	h.Set("own", hashOwn(own))
	return fsutil.WriteFileAtomic(f.Hashes, jsonx.Marshal(h), 0o600)
}

// Regenerate rewrites the profile's file from B, O and the given drift (used after resolve).
func Regenerate(p xdg.Paths, c *config.Config, name string, drift Drift) error {
	f := FilesFor(p, c, name)
	l, err := fsutil.LockFile(filepath.Join(p.LockDir(), "settings-"+name+".lock"))
	if err != nil {
		return err
	}
	defer l.Unlock()
	B, _, _, err := readObj(f.Base)
	if err != nil {
		return err
	}
	O, _, _, err := readObj(f.Overlay)
	if err != nil {
		return err
	}
	cb, cerr := os.ReadFile(f.Profile)
	return write(f, B, O, c.OwnPaths(name), drift, &Result{}, cb, cerr == nil)
}

// IsOwned reports whether path lies under an owned path.
func IsOwned(path string, own []string) bool {
	p, err := jsonx.ParsePath(path)
	if err != nil {
		return false
	}
	for _, s := range own {
		if q, err := jsonx.ParsePath(s); err == nil && p.HasPrefix(q) {
			return true
		}
	}
	return false
}

// DiffEntry is one line of `settings diff`.
type DiffEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // owned, drift, differs
	Profile any    `json:"profile,omitempty"`
	PHas    bool   `json:"profile_set"`
	Base    any    `json:"base,omitempty"`
	BHas    bool   `json:"base_set"`
}

// DiffWithBase lists where the profile's file differs from the base.
func DiffWithBase(p xdg.Paths, c *config.Config, name string) ([]DiffEntry, error) {
	f := FilesFor(p, c, name)
	B, _, _, err := readObj(f.Base)
	if err != nil {
		return nil, err
	}
	C, _, _, err := readObj(f.Profile)
	if err != nil {
		return nil, err
	}
	drift, _ := LoadDrift(f.Drift)
	own := c.OwnPaths(name)
	var out []DiffEntry
	for _, ch := range jsonx.Diff(B, C) {
		k := ch.Path.String()
		kind := "differs"
		if IsOwned(k, own) {
			kind = "owned"
		} else if _, ok := drift[k]; ok {
			kind = "drift"
		}
		out = append(out, DiffEntry{Path: k, Kind: kind, Profile: ch.B, PHas: ch.BHas, Base: ch.A, BHas: ch.AHas})
	}
	return out, nil
}

// Mask hides env values in displayed settings values.
func Mask(path string, v any) any {
	p, err := jsonx.ParsePath(path)
	if err != nil {
		return v
	}
	if len(p) >= 1 && p[0] == "env" {
		if len(p) >= 2 {
			return "***"
		}
		if o, ok := v.(*jsonx.Object); ok {
			m := jsonx.NewObject()
			for _, k := range o.Keys() {
				m.Set(k, "***")
			}
			return m
		}
	}
	return v
}
