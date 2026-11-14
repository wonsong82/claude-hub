// Command claude-hub is an unofficial profile launcher for Claude Code.
package main

import (
	"os"

	"github.com/wonsong82/claude-hub/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
