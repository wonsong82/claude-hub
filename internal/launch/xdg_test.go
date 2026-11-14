package launch

import "github.com/wonsong82/claude-hub/internal/xdg"

func xdgPaths() xdg.Paths { return xdg.Get() }
