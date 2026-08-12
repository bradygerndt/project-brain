// Package ui provides colored terminal output helpers, a straight port
// of the ANSI helpers bin/brain.js used (ok/err/info/dim/bold).
package ui

import "fmt"

const (
	reset   = "\x1b[0m"
	bold_   = "\x1b[1m"
	dim_    = "\x1b[2m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	red     = "\x1b[31m"
	cyan    = "\x1b[36m"
	magenta = "\x1b[35m"
)

func Ok(format string, a ...any) {
	fmt.Printf(green+"✓"+reset+" "+format+"\n", a...)
}

func Err(format string, a ...any) {
	fmt.Printf(red+"✗"+reset+" "+format+"\n", a...)
}

func Info(format string, a ...any) {
	fmt.Printf(cyan+"→"+reset+" "+format+"\n", a...)
}

func Dim(format string, a ...any) {
	fmt.Printf(dim_+format+reset+"\n", a...)
}

func Bold(format string, a ...any) {
	fmt.Printf(bold_+format+reset+"\n", a...)
}

func Magenta(s string) string {
	return magenta + s + reset
}

func DimStr(s string) string {
	return dim_ + s + reset
}

func GreenStr(s string) string {
	return green + s + reset
}
