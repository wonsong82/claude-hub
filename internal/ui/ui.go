// Package ui handles terminal input: questions, confirmations and no-echo token entry.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// IO bundles the streams a command uses.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// Interactive is true when stdin and stderr are terminals.
	Interactive bool
	inFD        int
	reader      *bufio.Reader
}

// Std returns IO on the process streams.
func Std() *IO {
	fd := int(os.Stdin.Fd())
	return &IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, inFD: fd,
		Interactive: term.IsTerminal(fd) && term.IsTerminal(int(os.Stderr.Fd()))}
}

func (u *IO) r() *bufio.Reader {
	if u.reader == nil {
		u.reader = bufio.NewReader(u.In)
	}
	return u.reader
}

// Printf writes to stdout.
func (u *IO) Printf(format string, a ...any) { fmt.Fprintf(u.Out, format, a...) }

// Errf writes to stderr.
func (u *IO) Errf(format string, a ...any) { fmt.Fprintf(u.Err, format, a...) }

// ErrNotInteractive means a question was needed without a terminal.
var ErrNotInteractive = errors.New("needs a terminal or --yes")

// Ask prints a question on stderr and reads one line.
func (u *IO) Ask(q string) (string, error) {
	if !u.Interactive {
		return "", ErrNotInteractive
	}
	fmt.Fprint(u.Err, q)
	line, err := u.r().ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Confirm asks a yes/no question; yes skips it. Without a terminal it is an error.
func (u *IO) Confirm(q string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	a, err := u.Ask(q + " [y/N] ")
	if err != nil {
		return false, err
	}
	a = strings.ToLower(a)
	return a == "y" || a == "yes", nil
}

// ReadToken reads a token: from a no-echo prompt when stdin is a terminal, else the first
// line of stdin. The token is never echoed.
func (u *IO) ReadToken(prompt string) (string, error) {
	if f, ok := u.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(u.Err, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(u.Err)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, err := u.r().ReadString('\n')
	if err != nil && line == "" {
		if err == io.EOF {
			return "", errors.New("no token on stdin")
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Choose asks for one of n numbered options (1-based); returns the index.
func (u *IO) Choose(title string, options []string) (int, error) {
	if !u.Interactive {
		return 0, ErrNotInteractive
	}
	fmt.Fprintln(u.Err, title)
	for i, o := range options {
		fmt.Fprintf(u.Err, "  %d) %s\n", i+1, o)
	}
	for {
		a, err := u.Ask("> ")
		if err != nil {
			return 0, err
		}
		var n int
		if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		fmt.Fprintf(u.Err, "enter a number from 1 to %d\n", len(options))
	}
}

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool { return term.IsTerminal(fd) }
