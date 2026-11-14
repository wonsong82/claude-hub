package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/jsonx"
	"github.com/wonsong82/claude-hub/internal/migrate"
	"github.com/wonsong82/claude-hub/internal/settings"
)

func (x *Ctx) cmdSettings(args []string) error {
	if len(args) == 0 {
		return usage("usage: claude-hub settings mode|own|diff|resolve ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "mode":
		o, err := parseArgs(rest, []string{"yes", "force", "dry-run"}, []string{"on-conflict"})
		if err != nil {
			return err
		}
		if err := need(o, 2, "settings mode <profile> shared|own|layered"); err != nil {
			return err
		}
		c, err := x.Config()
		if err != nil {
			return err
		}
		if _, err := x.profileOf(c, o.pos[0]); err != nil {
			return err
		}
		return x.settingsMode(c, o.pos[0], o.pos[1], o.strs["on-conflict"], o)
	case "own":
		return x.settingsOwn(rest)
	case "diff":
		return x.settingsDiff(rest)
	case "resolve":
		return x.settingsResolve(rest)
	}
	return usage("unknown settings command %q", sub)
}

func (x *Ctx) settingsMode(c *config.Config, name, mode, policy string, o *opts) error {
	switch policy {
	case "", migrate.Abort, migrate.KeepBase, migrate.KeepProfile:
	default:
		return usage("--on-conflict for settings must be abort, keep-base or keep-profile")
	}
	mp, err := settings.PlanMode(x.P, c, name, mode, policy)
	if err != nil {
		return fail("%v", err)
	}
	if mp.Journal == nil {
		x.IO.Printf("settings of '%s' are already %s\n", name, mode)
		return nil
	}
	x.IO.Printf("settings of '%s': %s -> %s\n", name, mp.From, mp.To)
	for _, s := range mp.Journal.Steps {
		switch s.Kind {
		case migrate.KMove:
			x.IO.Printf("  move %s to the backup\n", s.Src)
		case migrate.KLink:
			x.IO.Printf("  link %s -> %s\n", s.Dst, s.Src)
		}
	}
	for _, n := range mp.Notes {
		x.IO.Printf("  %s\n", n)
	}
	if mp.Conflict != "" {
		return fail("%s; nothing changed", mp.Conflict)
	}
	if o != nil && o.bools["dry-run"] {
		return nil
	}
	if err := x.liveRefuse(name, o != nil && o.bools["force"]); err != nil {
		return err
	}
	if len(mp.Confirm) > 0 {
		for _, why := range mp.Confirm {
			x.IO.Printf("  needs confirmation: %s\n", why)
		}
		ok, err := x.IO.Confirm("Proceed?", o != nil && o.bools["yes"])
		if err != nil {
			return usage("this needs confirmation: run it in a terminal or add --yes")
		}
		if !ok {
			return fail("aborted; nothing changed")
		}
	}
	if err := mp.Run(x.P, name); err != nil {
		return fail("%v", err)
	}
	x.IO.Printf("settings of '%s' are now %s\n", name, mode)
	return nil
}

func (x *Ctx) settingsOwn(args []string) error {
	o, err := parseArgs(args, []string{"discard", "promote"}, nil)
	if err != nil {
		return err
	}
	if err := needAtLeast(o, 3, "settings own <profile> add|remove <path>..."); err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	name, op, paths := o.pos[0], o.pos[1], o.pos[2:]
	pr, err := x.profileOf(c, name)
	if err != nil {
		return err
	}
	if c.IsBase(name) {
		return fail("'%s' is the base profile", name)
	}
	for _, p := range paths {
		if _, err := jsonx.ParsePath(p); err != nil {
			return usage("%v", err)
		}
	}
	if pr.Settings == nil {
		pr.Settings = &config.SettingsCfg{Mode: config.ModeOwn}
	}
	f := settings.FilesFor(x.P, c, name)
	O, _, err := settings.ReadObject(f.Overlay)
	if err != nil {
		return err
	}
	switch op {
	case "add":
		C, _, err := settings.ReadObject(f.Profile)
		if err != nil {
			return err
		}
		for _, p := range paths {
			if contains(pr.Settings.Own, p) {
				continue
			}
			pr.Settings.Own = append(pr.Settings.Own, p)
			// Keep the current effective value, so owning a path changes nothing.
			pth := jsonx.MustPath(p)
			if v, ok := jsonx.Get(C, pth); ok {
				jsonx.SetPath(O, pth, jsonx.Clone(v))
			}
		}
	case "remove":
		if o.bools["discard"] && o.bools["promote"] {
			return usage("--discard and --promote exclude each other")
		}
		B, _, err := settings.ReadObject(f.Base)
		if err != nil {
			return err
		}
		var discarded []string
		for _, p := range paths {
			if !contains(pr.Settings.Own, p) {
				return fail("%s is not owned by '%s'", p, name)
			}
			pth := jsonx.MustPath(p)
			ov, ook := jsonx.Get(O, pth)
			bv, bok := jsonx.Get(B, pth)
			if ook && (!bok || !jsonx.Equal(ov, bv)) {
				choice := ""
				switch {
				case o.bools["discard"]:
					choice = "discard"
				case o.bools["promote"]:
					choice = "promote"
				case x.IO.Interactive:
					i, err := x.IO.Choose(fmt.Sprintf("%s: the profile's value differs from the base", p), []string{"discard (saved under backups/)", "promote (write it into the base)"})
					if err != nil {
						return err
					}
					choice = []string{"discard", "promote"}[i]
				default:
					return usage("%s has its own value; add --discard or --promote", p)
				}
				if choice == "promote" {
					if err := x.promote(c, name, map[string]settings.DriftValue{p: {Value: ov}}); err != nil {
						return err
					}
				} else {
					discarded = append(discarded, p)
				}
			}
		}
		if len(discarded) > 0 {
			d := settings.Drift{}
			for _, p := range discarded {
				v, _ := jsonx.Get(O, jsonx.MustPath(p))
				d[p] = settings.DriftValue{Value: v}
			}
			if err := x.saveDiscarded(name, d); err != nil {
				return err
			}
		}
		var keep []string
		for _, s := range pr.Settings.Own {
			if !contains(paths, s) {
				keep = append(keep, s)
			}
		}
		pr.Settings.Own = keep
		for _, p := range paths {
			jsonx.DeletePath(O, jsonx.MustPath(p))
		}
	default:
		return usage("settings own <profile> add|remove <path>...")
	}
	if pr.Settings.Own == nil {
		pr.Settings.Own = []string{}
	}
	if err := fsutil.WriteFileAtomic(f.Overlay, jsonx.Marshal(O), 0o644); err != nil {
		return err
	}
	if err := x.Save(c); err != nil {
		return err
	}
	if c.SettingsMode(name) == config.ModeLayered {
		if _, err := settings.Sync(x.P, c, name); err != nil {
			return fail("sync: %v", err)
		}
	}
	x.IO.Printf("owned paths of '%s': %s\n", name, joinOr(pr.Settings.Own, "none"))
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// promote writes values into the base settings.json, keeping the old base in a backup.
func (x *Ctx) promote(c *config.Config, profile string, vals map[string]settings.DriftValue) error {
	f := settings.FilesFor(x.P, c, profile)
	B, exists, err := settings.ReadObject(f.Base)
	if err != nil {
		return err
	}
	if exists {
		bk := migrate.NewBackupDir(x.P, profile, "settings-promote")
		old, _ := os.ReadFile(f.Base)
		if err := fsutil.WriteFileAtomic(filepath.Join(bk, "base-settings.json"), old, 0o600); err != nil {
			return err
		}
	}
	for p, v := range vals {
		pth := jsonx.MustPath(p)
		if v.Absent {
			jsonx.DeletePath(B, pth)
		} else {
			jsonx.SetPath(B, pth, jsonx.Clone(v.Value))
		}
	}
	return fsutil.WriteThrough(f.Base, jsonx.Marshal(B), 0o644)
}

func (x *Ctx) saveDiscarded(profile string, d settings.Drift) error {
	bk := migrate.NewBackupDir(x.P, profile, "settings-discard")
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
	return fsutil.WriteFileAtomic(filepath.Join(bk, "discarded.json"), jsonx.Marshal(o), 0o600)
}

func (x *Ctx) settingsDiff(args []string) error {
	o, err := parseArgs(args, []string{"json"}, nil)
	if err != nil {
		return err
	}
	if err := need(o, 1, "settings diff <profile> [--json]"); err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	if _, err := x.profileOf(c, o.pos[0]); err != nil {
		return err
	}
	entries, err := settings.DiffWithBase(x.P, c, o.pos[0])
	if err != nil {
		return fail("%v", err)
	}
	for i := range entries {
		entries[i].Profile = settings.Mask(entries[i].Path, entries[i].Profile)
		entries[i].Base = settings.Mask(entries[i].Path, entries[i].Base)
	}
	if o.bools["json"] {
		out := []map[string]any{}
		for _, e := range entries {
			m := map[string]any{"path": e.Path, "kind": e.Kind}
			if e.PHas {
				m["profile"] = jsonRaw(e.Profile)
			}
			if e.BHas {
				m["base"] = jsonRaw(e.Base)
			}
			out = append(out, m)
		}
		return x.printJSON(out)
	}
	if len(entries) == 0 {
		x.IO.Printf("no differences from the base\n")
	}
	for _, e := range entries {
		x.IO.Printf("%-8s %s\n  profile: %s\n  base:    %s\n", e.Kind, e.Path, show(e.Profile, e.PHas), show(e.Base, e.BHas))
	}
	return nil
}

type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return r, nil }

func jsonRaw(v any) rawJSON { return rawJSON(jsonx.MarshalCompact(v)) }

func show(v any, has bool) string {
	if !has {
		return "(absent)"
	}
	return string(jsonx.MarshalCompact(v))
}

func (x *Ctx) settingsResolve(args []string) error {
	o, err := parseArgs(args, nil, []string{"all"})
	if err != nil {
		return err
	}
	if err := need(o, 1, "settings resolve <profile> [--all promote|own|discard]"); err != nil {
		return err
	}
	all := o.strs["all"]
	if all != "" && all != "promote" && all != "own" && all != "discard" {
		return usage("--all must be promote, own or discard")
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	name := o.pos[0]
	pr, err := x.profileOf(c, name)
	if err != nil {
		return err
	}
	if c.SettingsMode(name) != config.ModeLayered {
		return fail("'%s' is not layered; drift exists only for layered settings", name)
	}
	if _, err := settings.Sync(x.P, c, name); err != nil {
		return fail("%v", err)
	}
	f := settings.FilesFor(x.P, c, name)
	drift, err := settings.LoadDrift(f.Drift)
	if err != nil {
		return err
	}
	if len(drift) == 0 {
		x.IO.Printf("'%s' has no drift\n", name)
		return nil
	}
	if all == "" && !x.IO.Interactive {
		return usage("settings resolve needs a terminal or --all promote|own|discard")
	}
	B, _, _ := settings.ReadObject(f.Base)
	O, _, err := settings.ReadObject(f.Overlay)
	if err != nil {
		return err
	}
	promote := map[string]settings.DriftValue{}
	discard := settings.Drift{}
	remaining := settings.Drift{}
	for _, k := range drift.Keys() {
		bv, bok := jsonx.Get(B, jsonx.MustPath(k))
		choice := all
		if choice == "" {
			x.IO.Printf("%s\n  profile: %s\n  base:    %s\n", k, show(maskV(k, drift[k]), !drift[k].Absent), show(settings.Mask(k, bv), bok))
			i, err := x.IO.Choose("settle it:", []string{"promote (into the base)", "own (keep it for this profile)", "discard (take the base's value)", "skip"})
			if err != nil {
				return err
			}
			choice = []string{"promote", "own", "discard", "skip"}[i]
		}
		switch choice {
		case "promote":
			promote[k] = drift[k]
		case "own":
			if pr.Settings == nil {
				pr.Settings = &config.SettingsCfg{Mode: config.ModeLayered}
			}
			if !contains(pr.Settings.Own, k) {
				pr.Settings.Own = append(pr.Settings.Own, k)
			}
			if !drift[k].Absent {
				jsonx.SetPath(O, jsonx.MustPath(k), jsonx.Clone(drift[k].Value))
			} else {
				jsonx.DeletePath(O, jsonx.MustPath(k))
			}
		case "discard":
			discard[k] = drift[k]
		default:
			remaining[k] = drift[k]
		}
	}
	if len(promote) > 0 {
		if err := x.promote(c, name, promote); err != nil {
			return err
		}
	}
	if len(discard) > 0 {
		if err := x.saveDiscarded(name, discard); err != nil {
			return err
		}
	}
	if err := fsutil.WriteFileAtomic(f.Overlay, jsonx.Marshal(O), 0o644); err != nil {
		return err
	}
	if err := x.Save(c); err != nil {
		return err
	}
	if err := settings.Regenerate(x.P, c, name, remaining); err != nil {
		return err
	}
	x.IO.Printf("resolved %d path(s): %d promoted, %d owned, %d discarded; %d left\n", len(drift)-len(remaining), len(promote), len(drift)-len(remaining)-len(promote)-len(discard), len(discard), len(remaining))
	return nil
}

func maskV(k string, d settings.DriftValue) any { return settings.Mask(k, d.Value) }
