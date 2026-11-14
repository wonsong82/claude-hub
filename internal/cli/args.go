package cli

import (
	"strings"
)

// opts is a parsed command line: flags and positionals.
type opts struct {
	pos   []string
	bools map[string]bool
	strs  map[string]string
	rest  []string // after "--"
}

// parseArgs parses flags anywhere among positionals. boolFlags and strFlags are names
// without the dashes. "--" ends parsing; what follows goes to rest.
func parseArgs(args []string, boolFlags, strFlags []string) (*opts, error) {
	o := &opts{bools: map[string]bool{}, strs: map[string]string{}}
	isBool := map[string]bool{}
	isStr := map[string]bool{}
	for _, b := range boolFlags {
		isBool[b] = true
	}
	for _, s := range strFlags {
		isStr[s] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.rest = args[i+1:]
			break
		}
		if !strings.HasPrefix(a, "--") || a == "-" {
			if strings.HasPrefix(a, "-") && len(a) > 1 && a != "-" {
				return nil, usage("unknown flag %s", a)
			}
			o.pos = append(o.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		switch {
		case isBool[name]:
			if hasVal {
				return nil, usage("--%s takes no value", name)
			}
			o.bools[name] = true
		case isStr[name]:
			if !hasVal {
				if i+1 >= len(args) {
					return nil, usage("--%s needs a value", name)
				}
				i++
				val = args[i]
			}
			o.strs[name] = val
		default:
			return nil, usage("unknown flag --%s", name)
		}
	}
	return o, nil
}

func (o *opts) has(s string) bool { _, ok := o.strs[s]; return ok || o.bools[s] }

func orNone(s string) string {
	if s == "" {
		return "not found"
	}
	return s
}

// splitList splits a comma list.
func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
