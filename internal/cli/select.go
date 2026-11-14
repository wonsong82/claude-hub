package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/launch"
	"github.com/wonsong82/claude-hub/internal/resolve"
	"github.com/wonsong82/claude-hub/internal/settings"
	"github.com/wonsong82/claude-hub/internal/share"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// InitProfile is the name init gives the existing ~/.claude. ("default" is a hub command, so
// it cannot be a profile name.)
const InitProfile = "main"

func (x *Ctx) cmdInit(args []string) error {
	o, err := parseArgs(args, []string{"yes"}, nil)
	if err != nil {
		return err
	}
	if len(o.pos) > 0 {
		return usage("usage: claude-hub init [--yes]")
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	if x.exists {
		x.IO.Printf("config already exists: %s\n", xdg.Abbrev(c.Path, x.P.Home))
		return nil
	}
	claudeDir := filepath.Join(x.P.Home, ".claude")
	register := false
	if fi, err := os.Stat(claudeDir); err == nil && fi.IsDir() {
		q := "Register ~/.claude as the base profile '" + InitProfile + "' (Claude's own login, nothing moved) and make it the default?"
		if o.bools["yes"] {
			register = true
		} else if x.IO.Interactive {
			register, _ = x.IO.Confirm(q, false)
		}
	}
	if register {
		c.Profiles[InitProfile] = &config.Profile{}
		c.Hub.DefaultProfile = InitProfile
	}
	if err := x.Save(c); err != nil {
		return err
	}
	x.IO.Printf("created %s\n", xdg.Abbrev(c.Path, x.P.Home))
	if register {
		x.IO.Printf("profile '%s' = ~/.claude with Claude's own login; it is the default\n", InitProfile)
	}
	x.IO.Printf("next: claude-hub profile add <name> --login   (or --new-key <key> to use a setup-token)\n")
	return nil
}

func (x *Ctx) cmdDefault(args []string) error {
	o, err := parseArgs(args, []string{"none"}, nil)
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	switch {
	case o.bools["none"]:
		c.Hub.DefaultProfile = ""
		return x.Save(c)
	case len(o.pos) == 1:
		if _, err := x.profileOf(c, o.pos[0]); err != nil {
			return err
		}
		c.Hub.DefaultProfile = o.pos[0]
		return x.Save(c)
	case len(o.pos) == 0:
		if c.Hub.DefaultProfile == "" {
			x.IO.Printf("(none: plain claude when nothing else selects a profile)\n")
		} else {
			x.IO.Printf("%s\n", c.Hub.DefaultProfile)
		}
		return nil
	}
	return usage("usage: claude-hub default [<name> | --none]")
}

func (x *Ctx) cmdUse(args []string) error {
	o, err := parseArgs(args, []string{"rule", "clear"}, []string{"dir"})
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	dir := o.strs["dir"]
	if dir == "" {
		dir, _ = os.Getwd()
	}
	dir = c.Expand(dir)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if o.bools["clear"] {
		if len(o.pos) > 0 {
			return usage("usage: claude-hub use --clear [--dir PATH] [--rule]")
		}
		if o.bools["rule"] {
			var out []config.DirRule
			found := false
			for _, r := range c.Directory {
				if filepath.Clean(c.Expand(r.Path)) == dir {
					found = true
					continue
				}
				out = append(out, r)
			}
			if !found {
				return fail("no directory rule for %s", xdg.Abbrev(dir, x.P.Home))
			}
			c.Directory = out
			return x.Save(c)
		}
		f := filepath.Join(dir, ".claude-profile")
		if err := os.Remove(f); err != nil {
			return fail("no %s", xdg.Abbrev(f, x.P.Home))
		}
		x.IO.Printf("removed %s\n", xdg.Abbrev(f, x.P.Home))
		return nil
	}
	if err := need(o, 1, "use <profile> [--dir PATH] [--rule]"); err != nil {
		return err
	}
	name := o.pos[0]
	if _, err := x.profileOf(c, name); err != nil {
		return err
	}
	if o.bools["rule"] {
		path := xdg.Abbrev(dir, x.P.Home)
		replaced := false
		for i := range c.Directory {
			if filepath.Clean(c.Expand(c.Directory[i].Path)) == dir {
				c.Directory[i].Profile = name
				replaced = true
			}
		}
		if !replaced {
			c.Directory = append(c.Directory, config.DirRule{Path: path, Profile: name})
		}
		if err := x.Save(c); err != nil {
			return err
		}
		x.IO.Printf("directory rule: %s -> %s\n", path, name)
		return nil
	}
	f := filepath.Join(dir, ".claude-profile")
	if err := os.WriteFile(f, []byte(name+"\n"), 0o644); err != nil {
		return fail("%v", err)
	}
	x.IO.Printf("wrote %s (%s)\n", xdg.Abbrev(f, x.P.Home), name)
	x.IO.Printf("hint: add .claude-profile to .gitignore, or use --rule to keep it out of the repo\n")
	return nil
}

func (x *Ctx) cmdWhich(args []string) error {
	o, err := parseArgs(args, []string{"json"}, nil)
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	type wj struct {
		Profile string `json:"profile"`
		Source  string `json:"source"`
		Dir     string `json:"config_dir"`
		Auth    string `json:"auth"`
		Nested  bool   `json:"nested,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	var w wj
	if cur := os.Getenv(launch.VarProfile); cur != "" {
		w = wj{Profile: cur, Source: "running session (CLAUDE_HUB_PROFILE)", Nested: true}
	} else {
		name, src, err := resolve.Select(c, cwd, x.P.Home, os.Getenv(launch.VarSelect))
		if err != nil {
			w.Error = err.Error()
			if o.bools["json"] {
				_ = x.printJSON(w)
				return &silent{code: ExitConfig}
			}
			return cfgErr("%v", err)
		}
		w = wj{Profile: name, Source: src.String()}
	}
	if pr, ok := c.Profiles[w.Profile]; ok {
		w.Dir = xdg.Abbrev(c.ProfileDir(w.Profile), x.P.Home)
		if pr.Dir == "" {
			w.Dir += " (CLAUDE_CONFIG_DIR unset)"
		}
		w.Auth = authOf(c, w.Profile)
	}
	if o.bools["json"] {
		return x.printJSON(w)
	}
	if w.Profile == "" {
		x.IO.Printf("plain claude (no profile selected)\n")
		return nil
	}
	x.IO.Printf("%s (from %s)\nconfig dir: %s\nauth: %s\n", w.Profile, w.Source, w.Dir, w.Auth)
	return nil
}

func (x *Ctx) cmdExec(args []string) error {
	o, err := parseArgs(args, nil, nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 1 || len(o.rest) == 0 {
		return usage("usage: claude-hub exec <profile> -- <command> [args...]")
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	name := o.pos[0]
	if _, err := x.profileOf(c, name); err != nil {
		return err
	}
	token, hasKey, tmp, err := x.prepare(c, name)
	if err != nil {
		return err
	}
	env := launch.NewEnv(os.Environ())
	launch.Apply(env, c, name, token, hasKey, tmp, false)
	env.Unset(launch.VarGuard)
	path, err := exec.LookPath(o.rest[0])
	if err != nil {
		return &ExitError{Code: ExitNoClaude, Msg: o.rest[0] + ": command not found"}
	}
	argv := append([]string{o.rest[0]}, o.rest[1:]...)
	err = syscall.Exec(path, argv, env.Pairs())
	return &ExitError{Code: ExitRecursion, Msg: "cannot run " + path + ": " + err.Error()}
}

func (x *Ctx) cmdSync(args []string) error {
	o, err := parseArgs(args, nil, nil)
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	names := o.pos
	if len(names) == 0 {
		names = c.ProfileNames()
	}
	failed := false
	for _, n := range names {
		if _, err := x.profileOf(c, n); err != nil {
			return err
		}
		if c.IsBase(n) {
			continue
		}
		made, pending := share.EnsureLinks(c, n)
		for _, m := range made {
			x.IO.Printf("%s: linked %s\n", n, m)
		}
		for _, p := range pending {
			x.IO.Printf("%s: %s pending (the base has none yet)\n", n, p)
		}
		for _, pb := range linkProblems(c, n) {
			x.IO.Printf("%s: %s is %s, not a link to the base; \"claude-hub doctor --fix\" merges it\n", n, xdg.Abbrev(pb, x.P.Home), "a real copy")
		}
		if c.SettingsMode(n) == "layered" {
			res, err := settings.Sync(x.P, c, n)
			if err != nil {
				x.IO.Errf("claude-hub: %s: settings: %v\n", n, err)
				failed = true
				continue
			}
			switch {
			case res.Unchanged:
			case res.Changed:
				x.IO.Printf("%s: settings.json regenerated\n", n)
			}
			for _, s := range res.OwnedSaved {
				x.IO.Printf("%s: kept own %s\n", n, s)
			}
			if res.Drift > 0 {
				x.IO.Errf("claude-hub: %s has %d local change%s to inherited settings; run \"claude-hub settings resolve %s\"\n", n, res.Drift, plural(res.Drift), n)
			}
		}
	}
	if failed {
		return &silent{code: ExitFail}
	}
	return nil
}

func linkProblems(c *config.Config, n string) []string {
	var out []string
	for _, pb := range share.CheckLinks(c, n) {
		if pb.State == share.Real {
			out = append(out, pb.Path)
		}
	}
	return out
}
