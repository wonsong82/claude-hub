package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/launch"
	"github.com/wonsong82/claude-hub/internal/migrate"
	"github.com/wonsong82/claude-hub/internal/secrets"
	"github.com/wonsong82/claude-hub/internal/settings"
	"github.com/wonsong82/claude-hub/internal/share"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

func (x *Ctx) cmdProfile(args []string) error {
	if len(args) == 0 {
		return usage("usage: claude-hub profile add|remove|list|show|rename|set-key|unset-key|set|unset|share|unshare ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return x.profileAdd(rest)
	case "remove":
		return x.profileRemove(rest)
	case "list":
		return x.profileList(rest)
	case "show":
		return x.profileShow(rest)
	case "rename":
		return x.profileRename(rest)
	case "set-key":
		return x.profileSetKey(rest)
	case "unset-key":
		return x.profileUnsetKey(rest)
	case "set":
		return x.profileSet(rest)
	case "unset":
		return x.profileUnset(rest)
	case "share":
		return x.profileShare(rest)
	case "unshare":
		return x.profileUnshare(rest)
	}
	return usage("unknown profile command %q", sub)
}

func (x *Ctx) profileOf(c *config.Config, name string) (*config.Profile, error) {
	pr, ok := c.Profiles[name]
	if !ok {
		return nil, cfgErr("profile '%s' is not defined", name)
	}
	return pr, nil
}

// backend opens the secret backend; on the first key it records the resolved backend.
func (x *Ctx) backend(c *config.Config, record bool) (secrets.Backend, error) {
	be, err := secrets.Open(c.Secrets.Backend, x.P)
	if err != nil {
		return nil, err
	}
	if record && c.Secrets.Backend == "auto" {
		c.Secrets.Backend = be.Name()
	}
	return be, nil
}

// storeNewKey reads a token and stores it under name. The config is not saved here.
func (x *Ctx) storeNewKey(c *config.Config, name, note string) (string, error) {
	if err := config.CheckKeyName(name); err != nil {
		return "", usage("%v", err)
	}
	if _, ok := c.Keys[name]; ok {
		return "", fail("key '%s' already exists", name)
	}
	tok, err := x.IO.ReadToken(fmt.Sprintf("Token for key '%s' (from `claude setup-token`, not echoed): ", name))
	if err != nil {
		return "", fail("%v", err)
	}
	if err := secrets.CheckToken(tok); err != nil {
		return "", fail("%v", err)
	}
	be, err := x.backend(c, true)
	if err != nil {
		return "", err
	}
	if err := be.Set(name, tok); err != nil {
		return "", fail("storing key '%s' in the %s backend failed: %v", name, be.Name(), err)
	}
	c.Keys[name] = &config.Key{Note: note}
	return secrets.Fingerprint(tok), nil
}

func (x *Ctx) checkKeyReadable(c *config.Config, key string) error {
	if _, ok := c.Keys[key]; !ok {
		return fail("key '%s' does not exist; see \"claude-hub key list\"", key)
	}
	be, err := x.backend(c, false)
	if err != nil {
		return err
	}
	if _, err := be.Get(key); err != nil {
		return fail("key '%s' is not readable from the %s backend: %v", key, be.Name(), err)
	}
	return nil
}

// expandItems turns a comma list into item names; "all" and "none" expand.
func expandItems(list string, withSettings bool) ([]string, error) {
	var out []string
	for _, it := range splitList(list) {
		switch it {
		case "all":
			out = append(out, config.ShareItems...)
			if withSettings {
				out = append(out, "settings")
			}
		case "none":
		default:
			if it == "settings" && withSettings {
				out = append(out, it)
				continue
			}
			if _, ok := share.Lookup(it); !ok {
				if err := config.CheckExtraName(it); err != nil {
					return nil, usage("%v", err)
				}
			}
			out = append(out, it)
		}
	}
	return out, nil
}

func (x *Ctx) profileAdd(args []string) error {
	o, err := parseArgs(args, []string{"login", "adopt"}, []string{"dir", "key", "new-key", "note", "icon", "share", "own", "settings"})
	if err != nil {
		return err
	}
	if err := need(o, 1, "profile add <name> [--dir PATH] [--key KEY | --new-key KEY | --login] ..."); err != nil {
		return err
	}
	name := o.pos[0]
	if err := config.CheckProfileName(name); err != nil {
		return usage("%v", err)
	}
	n := 0
	for _, f := range []string{"key", "new-key", "login"} {
		if o.has(f) {
			n++
		}
	}
	if n > 1 {
		return usage("--key, --new-key and --login exclude each other")
	}
	if n == 0 && !x.IO.Interactive {
		return usage("profile add needs a terminal or one of --key <key>, --new-key <key> or --login")
	}
	if o.has("note") && !o.has("new-key") {
		return usage("--note goes with --new-key")
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	if _, ok := c.Profiles[name]; ok {
		return fail("profile '%s' already exists", name)
	}
	icon := o.strs["icon"]
	if !config.ValidIcon(icon) {
		return usage("--icon: use an emoji or #rrggbb")
	}
	dirSpec := o.strs["dir"]
	if dirSpec == "" {
		dirSpec = "~/.claude-" + name
	} else if !strings.HasPrefix(dirSpec, "~") && !strings.HasPrefix(dirSpec, "$HOME") && !filepath.IsAbs(dirSpec) {
		abs, _ := filepath.Abs(dirSpec)
		dirSpec = abs
	}
	pr := &config.Profile{Dir: dirSpec, Icon: icon}
	c.Profiles[name] = pr
	dir := c.ProfileDir(name)
	for _, other := range c.ProfileNames() {
		if other != name && c.ProfileDir(other) == dir {
			delete(c.Profiles, name)
			return fail("profile '%s' already uses %s", other, xdg.Abbrev(dir, x.P.Home))
		}
	}
	isBase := c.IsBase(name)
	if isBase && (o.has("share") || o.has("own") || o.has("settings")) {
		delete(c.Profiles, name)
		return usage("%s is the base dir: the base profile owns everything and takes no --share, --own or --settings", xdg.Abbrev(dir, x.P.Home))
	}
	existing := false
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		existing = true
	}
	if existing && !o.bools["adopt"] && !isBase {
		delete(c.Profiles, name)
		return fail("%s already holds files; add --adopt to register it as it is (nothing is moved)", xdg.Abbrev(dir, x.P.Home))
	}
	var partial []string
	if !isBase {
		pr.Share = &config.Share{Extra: []string{}}
		if existing {
			// --adopt: links to the base are shared, the rest own; settings own.
			for _, ent := range share.NeverShareList() {
				pat := strings.TrimSuffix(ent, "*")
				matches, _ := filepath.Glob(filepath.Join(dir, pat+"*"))
				for _, m := range matches {
					if share.NeverShare(filepath.Base(m)) && fsutil.IsSymlink(m) {
						delete(c.Profiles, name)
						return fail("%s is a link, but %s must never be shared; replace it with the profile's own copy first", m, filepath.Base(m))
					}
				}
			}
			for _, it := range share.Items {
				linked, total := 0, 0
				for _, ps := range it.Paths {
					st := share.State(dir, c.BaseDir(), ps.Name)
					if st == share.Linked {
						linked++
					}
					total++
				}
				if linked > 0 {
					pr.Share.Set(it.Name, config.Shared)
					if linked < total {
						partial = append(partial, it.Name)
					}
				} else {
					pr.Share.Set(it.Name, config.Own)
				}
			}
			mode := config.ModeOwn
			if fsutil.IsSymlink(filepath.Join(dir, settings.FileName)) && share.State(dir, c.BaseDir(), settings.FileName) == share.Linked {
				mode = config.ModeShared
			}
			pr.Settings = &config.SettingsCfg{Mode: mode, Own: []string{}}
		} else {
			for _, it := range config.ShareItems {
				pr.Share.Set(it, config.Shared)
			}
			mode := config.ModeLayered
			if o.has("settings") {
				mode = o.strs["settings"]
				if mode != config.ModeShared && mode != config.ModeOwn && mode != config.ModeLayered {
					delete(c.Profiles, name)
					return usage("--settings must be shared, own or layered")
				}
			}
			pr.Settings = &config.SettingsCfg{Mode: mode, Own: append([]string(nil), config.DefaultOwn...)}
			shareList, err := expandItems(o.strs["share"], true)
			if err != nil {
				delete(c.Profiles, name)
				return err
			}
			ownList, err := expandItems(o.strs["own"], true)
			if err != nil {
				delete(c.Profiles, name)
				return err
			}
			if strings.Contains(o.strs["share"], "none") {
				for _, it := range config.ShareItems {
					pr.Share.Set(it, config.Own)
				}
			}
			for _, it := range shareList {
				if it == "settings" {
					if !o.has("settings") {
						pr.Settings.Mode = config.ModeShared
					}
					continue
				}
				migrate.SetShare(pr, it, config.Shared)
			}
			for _, it := range ownList {
				if it == "settings" {
					if !o.has("settings") {
						pr.Settings.Mode = config.ModeOwn
					}
					continue
				}
				migrate.SetShare(pr, it, config.Own)
			}
		}
	} else if o.bools["adopt"] && !existing {
		// nothing to adopt; fine
	}

	// Authentication.
	choice := ""
	switch {
	case o.has("key"):
		choice = "key"
	case o.has("new-key"):
		choice = "new-key"
	case o.bools["login"]:
		choice = "login"
	default:
		opts := []string{}
		kinds := []string{}
		if len(c.Keys) > 0 {
			opts = append(opts, "an existing key")
			kinds = append(kinds, "key")
		}
		opts = append(opts, "a new key (paste a `claude setup-token` token)", "Claude's own login (/login in this profile)")
		kinds = append(kinds, "new-key", "login")
		i, err := x.IO.Choose(fmt.Sprintf("How should profile '%s' authenticate?", name), opts)
		if err != nil {
			delete(c.Profiles, name)
			return err
		}
		choice = kinds[i]
		switch choice {
		case "key":
			names := c.KeyNames()
			labels := make([]string, len(names))
			be, _ := x.backend(c, false)
			for i, k := range names {
				fp := "unreadable"
				if be != nil {
					if t, err := be.Get(k); err == nil {
						fp = secrets.Fingerprint(t)
					}
				}
				labels[i] = fmt.Sprintf("%s  %s  %s", k, c.Keys[k].Note, fp)
			}
			j, err := x.IO.Choose("Key:", labels)
			if err != nil {
				delete(c.Profiles, name)
				return err
			}
			o.strs["key"] = names[j]
		case "new-key":
			kn, err := x.IO.Ask("Key name: ")
			if err != nil {
				delete(c.Profiles, name)
				return err
			}
			o.strs["new-key"] = kn
		}
	}
	fp := ""
	switch choice {
	case "key":
		if err := x.checkKeyReadable(c, o.strs["key"]); err != nil {
			delete(c.Profiles, name)
			return err
		}
		pr.Key = o.strs["key"]
	case "new-key":
		f, err := x.storeNewKey(c, o.strs["new-key"], o.strs["note"])
		if err != nil {
			delete(c.Profiles, name)
			return err
		}
		pr.Key, fp = o.strs["new-key"], f
	}

	if !existing && !isBase {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			delete(c.Profiles, name)
			return err
		}
		_ = os.Chmod(dir, 0o700)
	}
	if err := x.Save(c); err != nil {
		return err
	}
	if fp != "" {
		x.IO.Printf("stored key '%s' (%s)\n", pr.Key, fp)
	}
	auth := "Claude's own login: start it and run /login once"
	if pr.Key != "" {
		auth = "key " + pr.Key
	}
	x.IO.Printf("created profile '%s' at %s (%s)\n", name, xdg.Abbrev(dir, x.P.Home), auth)
	if isBase {
		x.IO.Printf("it is the base profile: the folder other profiles share from\n")
		return nil
	}
	if !existing {
		made, pending := share.EnsureLinks(c, name)
		_ = made
		if len(pending) > 0 {
			x.IO.Printf("pending (linked once the base has them): %s\n", strings.Join(pending, ", "))
		}
		if c.SettingsMode(name) == config.ModeLayered {
			if _, err := settings.Sync(x.P, c, name); err != nil {
				x.IO.Errf("claude-hub: settings sync: %v\n", err)
			}
		} else if c.SettingsMode(name) == config.ModeShared {
			b := filepath.Join(c.BaseDir(), settings.FileName)
			if fsutil.Exists(b) {
				_ = os.Symlink(b, filepath.Join(dir, settings.FileName))
			}
		}
	}
	var sh, own []string
	for _, it := range share.ItemsFor(c, name) {
		if c.ShareState(name, it.Name) == config.Shared {
			sh = append(sh, it.Name)
		} else {
			own = append(own, it.Name)
		}
	}
	x.IO.Printf("shares with the base: %s; settings %s; own: %s\n", joinOr(sh, "nothing"), c.SettingsMode(name), joinOr(own, "nothing"))
	if c.ShareState(name, "sessions") == config.Shared || c.ShareState(name, "projects") == config.Shared {
		x.IO.Printf("note: shared sessions and projects make every sharing profile one trust domain (\"claude-hub profile unshare %s sessions projects --empty\" to keep it apart)\n", name)
	}
	for _, it := range partial {
		x.IO.Printf("%s is only partly linked; \"claude-hub profile share %s %s\" completes it\n", it, name, it)
	}
	if pr.Key == "" {
		x.IO.Printf("next: claude-hub %s, then /login\n", name)
	}
	return nil
}

func (x *Ctx) liveRefuse(name string, force bool) error {
	if force {
		return nil
	}
	if runs := launch.LiveFor(x.P, name); len(runs) > 0 {
		var pids []string
		for _, r := range runs {
			pids = append(pids, fmt.Sprint(r.PID))
		}
		return fail("profile '%s' has running sessions (pid %s); stop them, or use --force", name, strings.Join(pids, ", "))
	}
	return nil
}

func (x *Ctx) profileRemove(args []string) error {
	o, err := parseArgs(args, []string{"archive", "purge", "yes"}, nil)
	if err != nil {
		return err
	}
	if err := need(o, 1, "profile remove <name> [--archive | --purge] [--yes]"); err != nil {
		return err
	}
	if o.bools["archive"] && o.bools["purge"] {
		return usage("--archive and --purge exclude each other")
	}
	name := o.pos[0]
	c, err := x.Config()
	if err != nil {
		return err
	}
	pr, err := x.profileOf(c, name)
	if err != nil {
		return err
	}
	if c.Hub.DefaultProfile == name {
		return fail("'%s' is the default profile; change the default first (\"claude-hub default <other>\" or \"--none\")", name)
	}
	dir := c.ProfileDir(name)
	data := o.bools["archive"] || o.bools["purge"]
	if data {
		if c.IsBase(name) {
			return fail("'%s' is the base profile; its folder is never archived or purged", name)
		}
		if err := x.liveRefuse(name, false); err != nil {
			return err
		}
		if err := safeToRemove(dir, c.BaseDir(), x.P.Home); err != nil {
			return fail("%v", err)
		}
	}
	if o.bools["purge"] {
		x.IO.Printf("this destroys %s:\n", xdg.Abbrev(dir, x.P.Home))
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if fsutil.IsSymlink(p) {
				continue
			}
			x.IO.Printf("  %s (%s)\n", e.Name(), migrate.Human(fsutil.TreeSize(p)))
		}
		if c.ShareState(name, "projects") != config.Shared {
			n := 0
			_ = filepath.Walk(filepath.Join(dir, "projects"), func(p string, fi os.FileInfo, err error) error {
				if err == nil && strings.HasSuffix(p, ".jsonl") {
					n++
				}
				return nil
			})
			x.IO.Printf("  %d transcript(s) in projects\n", n)
		}
		a, err := x.IO.Ask(fmt.Sprintf("type the profile name (%s) to confirm: ", name))
		if err != nil {
			return err
		}
		if a != name {
			return fail("not confirmed")
		}
	}
	// Remove from the config, with its directory rules.
	delete(c.Profiles, name)
	var rules []config.DirRule
	for _, r := range c.Directory {
		if r.Profile != name {
			rules = append(rules, r)
		}
	}
	c.Directory = rules
	if err := x.Save(c); err != nil {
		return err
	}
	// Overlay and state go to a backup rather than being destroyed.
	bk := migrate.NewBackupDir(x.P, name, "remove")
	for _, d := range []struct{ src, rel string }{{x.P.ProfileConfigDir(name), "config"}, {x.P.ProfileStateDir(name), "state"}} {
		if fsutil.Exists(d.src) {
			_ = fsutil.MoveTree(d.src, filepath.Join(bk, d.rel))
		}
	}
	switch {
	case o.bools["archive"]:
		dst := filepath.Join(migrate.NewBackupDir(x.P, name, "archive"), filepath.Base(dir))
		if err := fsutil.MoveTree(dir, dst); err != nil {
			return fail("archiving %s failed: %v (the profile is already removed from the config)", dir, err)
		}
		x.IO.Printf("removed profile '%s'; its folder is archived at %s\n", name, xdg.Abbrev(dst, x.P.Home))
	case o.bools["purge"]:
		if err := fsutil.RemoveTree(dir); err != nil {
			return fail("purging %s failed: %v", dir, err)
		}
		x.IO.Printf("removed profile '%s' and deleted %s\n", name, xdg.Abbrev(dir, x.P.Home))
	default:
		x.IO.Printf("removed profile '%s'; its folder is left at %s\n", name, xdg.Abbrev(dir, x.P.Home))
	}
	if pr.Key != "" && len(c.ProfilesUsingKey(pr.Key)) == 0 {
		x.IO.Printf("key '%s' is now unused (it stays in the store; \"claude-hub key remove %s\" deletes it)\n", pr.Key, pr.Key)
	}
	return nil
}

// safeToRemove applies the removal safety rules (design section 7).
func safeToRemove(dir, base, home string) error {
	d := filepath.Clean(dir)
	for _, bad := range []string{"/", filepath.Clean(home), filepath.Clean(base)} {
		if d == bad || strings.HasPrefix(bad+"/", d+"/") {
			return fmt.Errorf("refusing: %s is /, $HOME, the base dir or an ancestor of one", d)
		}
	}
	looks := false
	for _, n := range []string{".claude.json", "settings.json", "projects"} {
		if fsutil.Exists(filepath.Join(d, n)) {
			looks = true
		}
	}
	if !looks {
		return fmt.Errorf("refusing: %s does not look like a Claude config dir (no .claude.json, settings.json or projects)", d)
	}
	return nil
}

type profileJSON struct {
	Name     string            `json:"name"`
	Icon     string            `json:"icon,omitempty"`
	Dir      string            `json:"dir"`
	Auth     string            `json:"auth"`
	Key      string            `json:"key,omitempty"`
	Default  bool              `json:"default"`
	Base     bool              `json:"base"`
	Shared   []string          `json:"shared"`
	Settings string            `json:"settings,omitempty"`
	Own      []string          `json:"own,omitempty"`
	Items    map[string]string `json:"items,omitempty"`
	Drift    int               `json:"drift,omitempty"`
	Env      []string          `json:"env,omitempty"`
	Live     []int             `json:"live_sessions,omitempty"`
	KeyFP    string            `json:"key_fingerprint,omitempty"`
}

func (x *Ctx) describe(c *config.Config, name string, full bool) profileJSON {
	pr := c.Profiles[name]
	j := profileJSON{Name: name, Icon: pr.Icon, Dir: xdg.Abbrev(c.ProfileDir(name), x.P.Home), Auth: authOf(c, name),
		Key: pr.Key, Default: c.Hub.DefaultProfile == name, Base: c.IsBase(name), Shared: []string{}}
	if pr.Dir == "" {
		j.Dir += " (Claude's default; CLAUDE_CONFIG_DIR unset)"
	}
	for _, it := range share.ItemsFor(c, name) {
		if c.ShareState(name, it.Name) == config.Shared {
			j.Shared = append(j.Shared, it.Name)
		}
	}
	j.Settings = c.SettingsMode(name)
	if !full {
		return j
	}
	j.Own = c.OwnPaths(name)
	if !j.Base {
		j.Items = map[string]string{}
		for _, it := range share.ItemsFor(c, name) {
			var states []string
			for _, ps := range it.Paths {
				states = append(states, ps.Name+": "+share.State(c.ProfileDir(name), c.BaseDir(), ps.Name).String())
			}
			j.Items[it.Name] = c.ShareState(name, it.Name) + " (" + strings.Join(states, ", ") + ")"
		}
		j.Items["settings"] = j.Settings + " (" + settings.FileName + ": " + share.State(c.ProfileDir(name), c.BaseDir(), settings.FileName).String() + ")"
	}
	if d, err := settings.LoadDrift(settings.FilesFor(x.P, c, name).Drift); err == nil {
		j.Drift = len(d)
	}
	for k := range pr.Env {
		j.Env = append(j.Env, k)
	}
	sort.Strings(j.Env)
	for _, r := range launch.LiveFor(x.P, name) {
		j.Live = append(j.Live, r.PID)
	}
	if pr.Key != "" {
		if be, err := x.backend(c, false); err == nil {
			if t, err := be.Get(pr.Key); err == nil {
				j.KeyFP = secrets.Fingerprint(t)
			} else {
				j.KeyFP = "unreadable"
			}
		}
	}
	return j
}

func (x *Ctx) profileList(args []string) error {
	o, err := parseArgs(args, []string{"json", "names"}, nil)
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	if o.bools["names"] {
		for _, n := range c.ProfileNames() {
			x.IO.Printf("%s\n", n)
		}
		return nil
	}
	var list []profileJSON
	for _, n := range c.ProfileNames() {
		list = append(list, x.describe(c, n, false))
	}
	if o.bools["json"] {
		if list == nil {
			list = []profileJSON{}
		}
		return x.printJSON(list)
	}
	if len(list) == 0 {
		x.IO.Printf("no profiles; add one with \"claude-hub profile add <name> --login\"\n")
		return nil
	}
	for _, j := range list {
		mark := " "
		if j.Default {
			mark = "*"
		}
		icon := j.Icon
		if icon == "" {
			icon = "  "
		}
		shared := fmt.Sprintf("%d shared", len(j.Shared))
		if j.Base {
			shared = "base"
		}
		x.IO.Printf("%s %s %-16s %-14s %-28s %s\n", mark, icon, j.Name, j.Auth, j.Dir, shared)
	}
	return nil
}

func (x *Ctx) profileShow(args []string) error {
	o, err := parseArgs(args, []string{"json"}, nil)
	if err != nil {
		return err
	}
	if err := need(o, 1, "profile show <name> [--json]"); err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	if _, err := x.profileOf(c, o.pos[0]); err != nil {
		return err
	}
	j := x.describe(c, o.pos[0], true)
	if o.bools["json"] {
		return x.printJSON(j)
	}
	x.IO.Printf("profile   %s %s%s\n", j.Name, j.Icon, map[bool]string{true: "  (default)", false: ""}[j.Default])
	x.IO.Printf("dir       %s\n", j.Dir)
	if j.Key != "" {
		x.IO.Printf("auth      key %s (%s)\n", j.Key, j.KeyFP)
	} else {
		x.IO.Printf("auth      login (Claude's own /login in this folder)\n")
	}
	if j.Base {
		x.IO.Printf("base      yes: this folder is what other profiles share\n")
	} else {
		keys := make([]string, 0, len(j.Items))
		for k := range j.Items {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			x.IO.Printf("  %-12s %s\n", k, j.Items[k])
		}
		x.IO.Printf("settings  %s, owned paths: %s, drift: %d\n", j.Settings, joinOr(j.Own, "none"), j.Drift)
	}
	x.IO.Printf("env       %s\n", joinOr(j.Env, "none"))
	if len(j.Live) > 0 {
		x.IO.Printf("running   pid %v\n", j.Live)
	}
	return nil
}

func (x *Ctx) profileRename(args []string) error {
	o, err := parseArgs(args, []string{"move-dir"}, nil)
	if err != nil {
		return err
	}
	if err := need(o, 2, "profile rename <old> <new> [--move-dir]"); err != nil {
		return err
	}
	oldN, newN := o.pos[0], o.pos[1]
	if err := config.CheckProfileName(newN); err != nil {
		return usage("%v", err)
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	pr, err := x.profileOf(c, oldN)
	if err != nil {
		return err
	}
	if _, ok := c.Profiles[newN]; ok {
		return fail("profile '%s' already exists", newN)
	}
	if o.bools["move-dir"] {
		if pr.Dir != "~/.claude-"+oldN && c.ProfileDir(oldN) != filepath.Join(x.P.Home, ".claude-"+oldN) {
			return fail("--move-dir works only for the default folder ~/.claude-%s", oldN)
		}
		if err := x.liveRefuse(oldN, false); err != nil {
			return err
		}
		dst := filepath.Join(x.P.Home, ".claude-"+newN)
		if fsutil.Exists(dst) {
			return fail("%s already exists", dst)
		}
		if err := os.Rename(c.ProfileDir(oldN), dst); err != nil {
			return fail("moving the folder failed: %v", err)
		}
		pr.Dir = "~/.claude-" + newN
		if pr.Key == "" {
			x.IO.Errf("claude-hub: '%s' uses Claude's own login, which is stored per folder path; it may have to /login again\n", newN)
		}
	}
	delete(c.Profiles, oldN)
	c.Profiles[newN] = pr
	if c.Hub.DefaultProfile == oldN {
		c.Hub.DefaultProfile = newN
	}
	for i := range c.Directory {
		if c.Directory[i].Profile == oldN {
			c.Directory[i].Profile = newN
		}
	}
	if err := x.Save(c); err != nil {
		return err
	}
	for _, pair := range [][2]string{{x.P.ProfileConfigDir(oldN), x.P.ProfileConfigDir(newN)}, {x.P.ProfileStateDir(oldN), x.P.ProfileStateDir(newN)}} {
		if fsutil.Exists(pair[0]) {
			_ = os.MkdirAll(filepath.Dir(pair[1]), 0o700)
			_ = os.Rename(pair[0], pair[1])
		}
	}
	x.IO.Printf("renamed '%s' to '%s'\n", oldN, newN)
	x.IO.Printf("note: .claude-profile files elsewhere that name '%s' still hold the old name\n", oldN)
	return nil
}

func (x *Ctx) profileSetKey(args []string) error {
	o, err := parseArgs(args, nil, []string{"new", "note"})
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	if o.has("new") {
		if err := need(o, 1, "profile set-key <name> --new <key> [--note TEXT]"); err != nil {
			return err
		}
		pr, err := x.profileOf(c, o.pos[0])
		if err != nil {
			return err
		}
		fp, err := x.storeNewKey(c, o.strs["new"], o.strs["note"])
		if err != nil {
			return err
		}
		pr.Key = o.strs["new"]
		if err := x.Save(c); err != nil {
			return err
		}
		x.IO.Printf("stored key '%s' (%s); profile '%s' now uses it\n", pr.Key, fp, o.pos[0])
		return nil
	}
	if err := need(o, 2, "profile set-key <name> <key>"); err != nil {
		return err
	}
	pr, err := x.profileOf(c, o.pos[0])
	if err != nil {
		return err
	}
	if err := x.checkKeyReadable(c, o.pos[1]); err != nil {
		return err
	}
	pr.Key = o.pos[1]
	if err := x.Save(c); err != nil {
		return err
	}
	x.IO.Printf("profile '%s' now uses key '%s'\n", o.pos[0], o.pos[1])
	return nil
}

func (x *Ctx) profileUnsetKey(args []string) error {
	o, err := parseArgs(args, nil, nil)
	if err != nil {
		return err
	}
	if err := need(o, 1, "profile unset-key <name>"); err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	pr, err := x.profileOf(c, o.pos[0])
	if err != nil {
		return err
	}
	old := pr.Key
	pr.Key = ""
	if err := x.Save(c); err != nil {
		return err
	}
	if old != "" {
		x.IO.Printf("profile '%s' now uses Claude's own login (key '%s' stays in the store)\n", o.pos[0], old)
	}
	x.IO.Printf("next: claude-hub %s, then /login (unless this folder is already logged in)\n", o.pos[0])
	return nil
}

func (x *Ctx) profileSet(args []string) error {
	o, err := parseArgs(args, []string{"not-a-secret"}, nil)
	if err != nil {
		return err
	}
	if err := need(o, 3, "profile set <name> icon <value> | profile set <name> env NAME=value"); err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	pr, err := x.profileOf(c, o.pos[0])
	if err != nil {
		return err
	}
	switch o.pos[1] {
	case "icon":
		if !config.ValidIcon(o.pos[2]) {
			return usage("icon: use an emoji or #rrggbb")
		}
		pr.Icon = o.pos[2]
	case "env":
		k, v, ok := strings.Cut(o.pos[2], "=")
		if !ok || !config.ValidEnvName(k) {
			return usage("env: use NAME=value")
		}
		if config.ForbiddenEnv(k) {
			return usage("%s is set by claude-hub and cannot be a profile variable", k)
		}
		if config.SecretLikeEnv(k) && !o.bools["not-a-secret"] {
			return usage("%s looks like a secret, and the config holds no secrets; add --not-a-secret if it is not one", k)
		}
		if pr.Env == nil {
			pr.Env = map[string]string{}
		}
		pr.Env[k] = v
	default:
		return usage("profile set <name> icon|env ...")
	}
	return x.Save(c)
}

func (x *Ctx) profileUnset(args []string) error {
	o, err := parseArgs(args, nil, nil)
	if err != nil {
		return err
	}
	if err := need(o, 3, "profile unset <name> env NAME"); err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	pr, err := x.profileOf(c, o.pos[0])
	if err != nil {
		return err
	}
	if o.pos[1] != "env" {
		return usage("profile unset <name> env NAME")
	}
	if _, ok := pr.Env[o.pos[2]]; !ok {
		return fail("profile '%s' has no env %s", o.pos[0], o.pos[2])
	}
	delete(pr.Env, o.pos[2])
	return x.Save(c)
}

// itemFor resolves an item name for share/unshare.
func itemFor(c *config.Config, profile, name string) (share.Item, error) {
	if it, ok := share.Lookup(name); ok {
		return it, nil
	}
	if err := config.CheckExtraName(name); err != nil {
		return share.Item{}, usage("%v", err)
	}
	for _, it := range share.ItemsFor(c, profile) {
		if it.Name == name {
			return it, nil
		}
	}
	isDir := true
	if fi, err := os.Lstat(filepath.Join(c.ProfileDir(profile), name)); err == nil {
		isDir = fi.IsDir()
	} else if fi, err := os.Stat(filepath.Join(c.BaseDir(), name)); err == nil {
		isDir = fi.IsDir()
	}
	return share.ExtraItem(name, isDir), nil
}

func (x *Ctx) profileShare(args []string) error {
	o, err := parseArgs(args, []string{"dry-run", "yes", "force"}, []string{"on-conflict"})
	if err != nil {
		return err
	}
	if err := needAtLeast(o, 2, "profile share <name> <item>... [--on-conflict abort|keep-base|keep-profile|keep-both]"); err != nil {
		return err
	}
	policy := o.strs["on-conflict"]
	switch policy {
	case "", migrate.Abort, migrate.KeepBase, migrate.KeepProfile, migrate.KeepBoth:
	default:
		return usage("--on-conflict must be abort, keep-base, keep-profile or keep-both")
	}
	name := o.pos[0]
	c, err := x.Config()
	if err != nil {
		return err
	}
	if _, err := x.profileOf(c, name); err != nil {
		return err
	}
	items, err := expandItems(strings.Join(o.pos[1:], ","), true)
	if err != nil {
		return err
	}
	for _, item := range items {
		c, err = x.Reload()
		if err != nil {
			return err
		}
		if item == "settings" {
			if err := x.settingsMode(c, name, config.ModeShared, policy, o); err != nil {
				return err
			}
			continue
		}
		it, err := itemFor(c, name, item)
		if err != nil {
			return err
		}
		if policy == migrate.KeepBoth && !it.KeepBoth {
			return usage("--on-conflict keep-both is offered for projects and extra items only; a renamed %s entry would be a duplicate definition", item)
		}
		if c.ShareState(name, item) == config.Shared && !hasProblem(share.CheckLinks(c, name), item) {
			x.IO.Printf("%s is already shared\n", item)
			continue
		}
		pl, err := migrate.PlanShare(x.P, c, name, it, migrate.Options{OnConflict: policy})
		if err != nil {
			return fail("%v", err)
		}
		if len(pl.Conflicts) > 0 && policy == "" && x.IO.Interactive && !o.bools["dry-run"] {
			x.IO.Printf("%s", pl.Describe(x.P.Home))
			choices := []string{migrate.KeepBase, migrate.KeepProfile}
			if it.KeepBoth {
				choices = append(choices, migrate.KeepBoth)
			}
			choices = append(choices, migrate.Abort)
			i, err := x.IO.Choose(fmt.Sprintf("%d conflict(s): which version wins?", len(pl.Conflicts)), choices)
			if err != nil {
				return err
			}
			if choices[i] != migrate.Abort {
				pl, err = migrate.PlanShare(x.P, c, name, it, migrate.Options{OnConflict: choices[i]})
				if err != nil {
					return fail("%v", err)
				}
			}
		}
		x.IO.Printf("%s", pl.Describe(x.P.Home))
		if o.bools["dry-run"] {
			continue
		}
		if len(pl.Conflicts) > 0 {
			return fail("%d conflict(s) in %s: %s; nothing changed. Choose --on-conflict keep-base, keep-profile%s", len(pl.Conflicts), item, strings.Join(pl.Conflicts, ", "), map[bool]string{true: " or keep-both", false: ""}[it.KeepBoth])
		}
		if err := x.liveRefuse(name, o.bools["force"]); err != nil {
			return err
		}
		if pl.WritesBase {
			var others []string
			for _, r := range launch.Live(x.P) {
				if r.Profile != name && (c.IsBase(r.Profile) || c.ShareState(r.Profile, item) == config.Shared) {
					others = append(others, fmt.Sprintf("%s (pid %d)", r.Profile, r.PID))
				}
			}
			if len(others) > 0 && !o.bools["force"] {
				pl.Confirm = append(pl.Confirm, "running sessions see the base change: "+strings.Join(others, ", "))
			}
		}
		for _, why := range pl.Confirm {
			x.IO.Printf("  needs confirmation: %s\n", why)
		}
		if len(pl.Confirm) > 0 || x.IO.Interactive {
			ok, err := x.IO.Confirm("Proceed?", o.bools["yes"])
			if err != nil {
				return usage("%s needs confirmation: run it in a terminal or add --yes", item)
			}
			if !ok {
				return fail("aborted; nothing changed")
			}
		}
		if err := migrate.Run(x.P, pl.Journal); err != nil {
			return fail("%v", err)
		}
		for _, pth := range pl.Paths {
			for _, m := range pth.Missing {
				x.IO.Printf("reinstall in the base if wanted: %s\n", m)
			}
		}
		x.IO.Printf("%s is now shared\n", item)
	}
	return nil
}

func (x *Ctx) profileUnshare(args []string) error {
	o, err := parseArgs(args, []string{"dry-run", "yes", "force", "copy", "empty"}, nil)
	if err != nil {
		return err
	}
	if err := needAtLeast(o, 2, "profile unshare <name> <item>... [--copy | --empty]"); err != nil {
		return err
	}
	if o.bools["copy"] && o.bools["empty"] {
		return usage("--copy and --empty exclude each other")
	}
	name := o.pos[0]
	c, err := x.Config()
	if err != nil {
		return err
	}
	if _, err := x.profileOf(c, name); err != nil {
		return err
	}
	items, err := expandItems(strings.Join(o.pos[1:], ","), true)
	if err != nil {
		return err
	}
	mode := ""
	if o.bools["copy"] {
		mode = "copy"
	} else if o.bools["empty"] {
		mode = "empty"
	}
	for _, item := range items {
		c, err = x.Reload()
		if err != nil {
			return err
		}
		if item == "settings" {
			if err := x.settingsMode(c, name, config.ModeOwn, "", o); err != nil {
				return err
			}
			continue
		}
		it, err := itemFor(c, name, item)
		if err != nil {
			return err
		}
		if c.ShareState(name, item) != config.Shared {
			x.IO.Printf("%s is already own\n", item)
			continue
		}
		m := mode
		if item == "projects" && m == "" {
			sz := fsutil.TreeSize(filepath.Join(c.BaseDir(), "projects"))
			if !x.IO.Interactive {
				return usage("unsharing projects needs --copy (%s) or --empty", migrate.Human(sz))
			}
			i, err := x.IO.Choose(fmt.Sprintf("projects: copy the base's %s into the profile, or start empty?", migrate.Human(sz)), []string{"copy", "empty"})
			if err != nil {
				return err
			}
			m = []string{"copy", "empty"}[i]
		}
		pl, err := migrate.PlanUnshare(c, name, it, migrate.Options{Unshare: m})
		if err != nil {
			return fail("%v", err)
		}
		x.IO.Printf("%s", pl.Describe(x.P.Home))
		if o.bools["dry-run"] {
			continue
		}
		if err := x.liveRefuse(name, o.bools["force"]); err != nil {
			return err
		}
		if x.IO.Interactive {
			ok, err := x.IO.Confirm("Proceed?", o.bools["yes"])
			if err != nil || !ok {
				return fail("aborted; nothing changed")
			}
		}
		if err := migrate.Run(x.P, pl.Journal); err != nil {
			return fail("%v", err)
		}
		x.IO.Printf("%s is now own\n", item)
	}
	return nil
}

var errAborted = errors.New("aborted")

func hasProblem(ps []share.Problem, item string) bool {
	for _, p := range ps {
		if p.Item == item {
			return true
		}
	}
	return false
}
