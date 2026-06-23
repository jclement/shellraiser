// Package ui provides shellraiser's colourful structured logging — built so that
// `docker compose logs -f` is a pleasure to watch.
package ui

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// ANSI palette (256-colour). Disabled when NO_COLOR / SHELLRAISER_NO_COLOR is set.
const (
	reset  = "\x1b[0m"
	dim    = "\x1b[2m"
	bold   = "\x1b[1m"
	purple = "\x1b[38;5;141m"
	green  = "\x1b[38;5;79m"
	red    = "\x1b[38;5;203m"
	yellow = "\x1b[38;5;221m"
	blue   = "\x1b[38;5;75m"
	cyan   = "\x1b[38;5;80m"
	gray   = "\x1b[38;5;245m"
)

var colorEnabled = os.Getenv("NO_COLOR") == "" && os.Getenv("SHELLRAISER_NO_COLOR") == ""

func c(code, s string) string {
	if !colorEnabled {
		return s
	}
	return code + s + reset
}

func clock() string { return time.Now().Format("15:04:05") }

// line prints: "<dim ts> <coloured symbol> <coloured comp> <msg>".
func line(symbol, color, comp, msg string) {
	fmt.Fprintf(os.Stdout, "%s %s %s %s\n",
		c(dim, clock()),
		c(color, symbol),
		c(bold+color, pad(comp, 8)),
		msg,
	)
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// Banner prints the startup wordmark.
func Banner() {
	if colorEnabled {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "  "+purple+bold+"▟█▙ shellraiser"+reset+"  "+dim+"sandboxed vibe coding"+reset)
		fmt.Fprintln(os.Stdout, "  "+purple+"▜█▛"+reset+" "+dim+"────────────────────────"+reset)
		fmt.Fprintln(os.Stdout)
		return
	}
	fmt.Fprintln(os.Stdout, "shellraiser — sandboxed vibe coding")
}

// Boot prints a config summary line: "comp  k=v  k=v".
func Boot(comp string, kv ...string) {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, c(gray, kv[i]+"=")+kv[i+1])
	}
	line("▸", purple, comp, strings.Join(parts, "  "))
}

// Ready highlights the magic link.
func Ready(url string) { line("●", green, "ready", c(bold+cyan, url)) }

// Info, Warn, Error are general-purpose level helpers.
func Info(comp, format string, a ...any)  { line("·", blue, comp, fmt.Sprintf(format, a...)) }
func Warn(comp, format string, a ...any)  { line("⚠", yellow, comp, fmt.Sprintf(format, a...)) }
func Error(comp, format string, a ...any) { line("✗", red, comp, fmt.Sprintf(format, a...)) }

// Session logs a session lifecycle event in a worktree.
func Session(action, kind, title, worktree string) {
	msg := fmt.Sprintf("%s %s %s", c(cyan, kind), title, c(dim, "· "+worktree))
	line("◆", purple, action, msg)
}

// Done logs an agent finishing a unit of work (the ding moment).
func Done(kind, title, worktree string) {
	line("✓", green, "done", fmt.Sprintf("%s %s %s", c(cyan, kind), title, c(dim, "· "+worktree)))
}

// Colour primitives for ad-hoc views (e.g. the `sr status` dashboard). They
// honour NO_COLOR like everything else in this package.
func Dim(s string) string    { return c(dim, s) }
func Bold(s string) string   { return c(bold, s) }
func Green(s string) string  { return c(green, s) }
func Red(s string) string    { return c(red, s) }
func Yellow(s string) string { return c(yellow, s) }
func Gray(s string) string   { return c(gray, s) }
func Cyan(s string) string   { return c(cyan, s) }
func Accent(s string) string { return c(purple, s) }

// Print writes a raw line (used by multi-line views).
func Print(s string) { fmt.Fprintln(os.Stdout, s) }

// Exit logs a session exiting with a code.
func Exit(kind, title string, code int) {
	color := green
	if code != 0 {
		color = red
	}
	line("⨯", color, "exit", fmt.Sprintf("%s %s %s", c(cyan, kind), title, c(color, fmt.Sprintf("(%d)", code))))
}
