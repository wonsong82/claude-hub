package cli

import (
	"errors"
	"strings"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/secrets"
)

func (x *Ctx) cmdKey(args []string) error {
	if len(args) == 0 {
		return usage("usage: claude-hub key add|set|list|rename|remove ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		o, err := parseArgs(rest, nil, []string{"note"})
		if err != nil {
			return err
		}
		if err := need(o, 1, "key add <name> [--note TEXT]"); err != nil {
			return err
		}
		c, err := x.Config()
		if err != nil {
			return err
		}
		fp, err := x.storeNewKey(c, o.pos[0], o.strs["note"])
		if err != nil {
			return err
		}
		if err := x.Save(c); err != nil {
			return err
		}
		x.IO.Printf("stored key '%s' in the %s backend (%s)\n", o.pos[0], c.Secrets.Backend, fp)
		return nil
	case "set":
		o, err := parseArgs(rest, nil, nil)
		if err != nil {
			return err
		}
		if err := need(o, 1, "key set <name>"); err != nil {
			return err
		}
		c, err := x.Config()
		if err != nil {
			return err
		}
		name := o.pos[0]
		if _, ok := c.Keys[name]; !ok {
			return fail("key '%s' does not exist; use \"claude-hub key add %s\"", name, name)
		}
		tok, err := x.IO.ReadToken("New token for key '" + name + "' (not echoed): ")
		if err != nil {
			return fail("%v", err)
		}
		if err := secrets.CheckToken(tok); err != nil {
			return fail("%v", err)
		}
		be, err := x.backend(c, false)
		if err != nil {
			return err
		}
		if err := be.Set(name, tok); err != nil {
			return fail("storing key '%s' failed: %v", name, err)
		}
		x.IO.Printf("replaced key '%s' (%s)\n", name, secrets.Fingerprint(tok))
		return nil
	case "list":
		o, err := parseArgs(rest, []string{"json"}, nil)
		if err != nil {
			return err
		}
		c, err := x.Config()
		if err != nil {
			return err
		}
		type kj struct {
			Name        string   `json:"name"`
			Note        string   `json:"note"`
			Backend     string   `json:"backend"`
			Fingerprint string   `json:"fingerprint"`
			Profiles    []string `json:"profiles"`
		}
		var list []kj
		be, beErr := x.backend(c, false)
		for _, n := range c.KeyNames() {
			fp := "unreadable"
			if beErr == nil {
				if t, err := be.Get(n); err == nil {
					fp = secrets.Fingerprint(t)
				} else if errors.Is(err, secrets.ErrNotFound) {
					fp = "missing from the store"
				}
			}
			bn := c.Secrets.Backend
			if beErr == nil {
				bn = be.Name()
			}
			ps := c.ProfilesUsingKey(n)
			if ps == nil {
				ps = []string{}
			}
			list = append(list, kj{n, c.Keys[n].Note, bn, fp, ps})
		}
		if o.bools["json"] {
			if list == nil {
				list = []kj{}
			}
			return x.printJSON(list)
		}
		if len(list) == 0 {
			x.IO.Printf("no keys (profiles without a key use Claude's own login)\n")
		}
		for _, k := range list {
			x.IO.Printf("%-16s %-22s %-12s %s  used by: %s\n", k.Name, k.Fingerprint, k.Backend, k.Note, joinOr(k.Profiles, "none"))
		}
		return nil
	case "rename":
		o, err := parseArgs(rest, nil, nil)
		if err != nil {
			return err
		}
		if err := need(o, 2, "key rename <old> <new>"); err != nil {
			return err
		}
		c, err := x.Config()
		if err != nil {
			return err
		}
		oldN, newN := o.pos[0], o.pos[1]
		if _, ok := c.Keys[oldN]; !ok {
			return fail("key '%s' does not exist", oldN)
		}
		if _, ok := c.Keys[newN]; ok {
			return fail("key '%s' already exists", newN)
		}
		if err := checkKeyName(newN); err != nil {
			return err
		}
		be, err := x.backend(c, false)
		if err != nil {
			return err
		}
		tok, err := be.Get(oldN)
		if err != nil {
			return fail("cannot read key '%s': %v", oldN, err)
		}
		if err := be.Set(newN, tok); err != nil {
			return fail("storing key '%s' failed: %v", newN, err)
		}
		c.Keys[newN] = c.Keys[oldN]
		delete(c.Keys, oldN)
		for _, p := range c.ProfilesUsingKey(oldN) {
			c.Profiles[p].Key = newN
		}
		if err := x.Save(c); err != nil {
			_ = be.Delete(newN)
			return err
		}
		if err := be.Delete(oldN); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			x.IO.Errf("claude-hub: the old entry '%s' could not be deleted from the store: %v\n", oldN, err)
		}
		x.IO.Printf("renamed key '%s' to '%s'\n", oldN, newN)
		return nil
	case "remove":
		o, err := parseArgs(rest, []string{"yes"}, nil)
		if err != nil {
			return err
		}
		if err := need(o, 1, "key remove <name> [--yes]"); err != nil {
			return err
		}
		c, err := x.Config()
		if err != nil {
			return err
		}
		name := o.pos[0]
		if _, ok := c.Keys[name]; !ok {
			return fail("key '%s' does not exist", name)
		}
		users := c.ProfilesUsingKey(name)
		if len(users) > 0 && !o.bools["yes"] {
			return fail("key '%s' is used by %s; add --yes to switch those profiles to Claude's own login", name, strings.Join(users, ", "))
		}
		be, err := x.backend(c, false)
		if err != nil {
			return err
		}
		if err := be.Delete(name); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return fail("deleting key '%s' failed: %v", name, err)
		}
		for _, p := range users {
			c.Profiles[p].Key = ""
		}
		delete(c.Keys, name)
		if err := x.Save(c); err != nil {
			return err
		}
		x.IO.Printf("removed key '%s'\n", name)
		for _, p := range users {
			x.IO.Printf("profile '%s' now uses Claude's own login: start it and run /login\n", p)
		}
		return nil
	}
	return usage("unknown key command %q", sub)
}

func checkKeyName(n string) error {
	if err := config.CheckKeyName(n); err != nil {
		return usage("%v", err)
	}
	return nil
}
