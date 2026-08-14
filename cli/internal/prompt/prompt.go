// Package prompt provides small line-based interactive prompt helpers for
// commands that support running without arguments (e.g. `brain add`).
package prompt

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Reader prompts for input on a line-buffered source (normally os.Stdin).
type Reader struct {
	r *bufio.Reader
}

func New(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r)}
}

// String prompts for a line of text, returning def if the user enters
// nothing. Returns an error on EOF (e.g. stdin isn't an interactive
// terminal) rather than looping or silently applying every default, so a
// script that pipes brain add with no args fails clearly instead of
// hanging or doing something the caller didn't ask for.
func (p *Reader) String(label, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		if err == io.EOF {
			return "", fmt.Errorf("input ended unexpectedly — not running in a terminal? Use explicit arguments instead: brain add <name> <mcp-port> <artifacts-port> [flags]")
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// Int prompts for an integer, reprompting on non-numeric input.
func (p *Reader) Int(label string, def int) (int, error) {
	for {
		s, err := p.String(label, strconv.Itoa(def))
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			fmt.Println("  not a number, try again")
			continue
		}
		return n, nil
	}
}

// YesNo prompts for a yes/no answer, reprompting on anything else.
func (p *Reader) YesNo(label string, def bool) (bool, error) {
	defStr := "n"
	if def {
		defStr = "y"
	}
	for {
		s, err := p.String(label+" (y/n)", defStr)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Println("  please answer y or n")
	}
}
