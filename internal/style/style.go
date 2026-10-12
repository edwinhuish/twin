// Package style decides whether this process may put ANSI escape codes on a
// stream, and supplies the glyphs the CLI prefixes its lines with.
//
// The decision is made per stream, not per program: stdout and stderr are
// redirected independently, and `twin new wechat > log` has to leave the log
// readable while the warnings still land in colour on the terminal. It is also
// made by the caller at output time rather than in an init, so anything that
// captures twin's output in a test or a script gets plain text for free.
package style

import (
	"os"
	"syscall"
	"unsafe"
)

// Style is a colour decision, already resolved for one stream.
type Style struct{ on bool }

// For resolves the decision for one stream.
func For(f *os.File) Style { return Style{on: allowed(isTerminal(f), os.Getenv)} }

// Enabled reports whether escape codes will actually be emitted. Callers that
// lay out columns can ask before padding.
func (s Style) Enabled() bool { return s.on }

// Level is the shared verdict vocabulary: it exists so the diagnostic packages
// and the CLI can agree on a glyph without either importing the other's enum.
type Level int

const (
	// LevelOK is a check that passed.
	LevelOK Level = iota
	// LevelWarn is something degraded but not broken.
	LevelWarn
	// LevelFail is something broken.
	LevelFail
	// LevelSkip is a check that could not be run here — a fact, not a failure.
	LevelSkip
)

// Glyph is the emoji for a level, without colour. The glyph carries the verdict
// on its own, so a line still reads correctly after `| grep`, in a terminal with
// no colour, or in a bug report where the escapes were stripped.
func Glyph(l Level) string {
	switch l {
	case LevelOK:
		return "✅"
	case LevelWarn:
		return "⚠️"
	case LevelFail:
		return "❌"
	default:
		return "➖"
	}
}

// Mark is the level's glyph, coloured.
func (s Style) Mark(l Level) string {
	switch l {
	case LevelOK:
		return s.Green(Glyph(l))
	case LevelWarn:
		return s.Yellow(Glyph(l))
	case LevelFail:
		return s.Red(Glyph(l))
	default:
		return s.Dim(Glyph(l))
	}
}

// Bold / Dim / Green / Yellow / Red / Cyan are the whole palette. The CLI is
// not a syntax highlighter: colour is spent on verdicts and section titles, and
// nothing else, so that a line that is coloured is a line that matters.
func (s Style) Bold(t string) string   { return s.paint("1", t) }
func (s Style) Dim(t string) string    { return s.paint("2", t) }
func (s Style) Green(t string) string  { return s.paint("32", t) }
func (s Style) Yellow(t string) string { return s.paint("33", t) }
func (s Style) Red(t string) string    { return s.paint("31", t) }
func (s Style) Cyan(t string) string   { return s.paint("36", t) }

func (s Style) paint(code, t string) string {
	if !s.on || t == "" {
		return t
	}
	return "\x1b[" + code + "m" + t + "\x1b[0m"
}

// allowed is the whole policy, in the order that keeps each rule explicable.
// An explicit NO_COLOR wins over everything, because that is the convention it
// was defined for; CLICOLOR_FORCE then overrides the "not a terminal" guess for
// people who pipe into a pager that does render escapes; only then does the
// terminal get a vote.
//
// getenv is a parameter so the policy can be tested without touching the
// process environment.
func allowed(tty bool, getenv func(string) string) bool {
	if getenv("NO_COLOR") != "" {
		return false
	}
	if v := getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if !tty {
		return false
	}
	return getenv("TERM") != "dumb"
}

// isTerminal asks the kernel with TCGETS instead of inspecting the file mode:
// /dev/null is a character device too, so `twin new wechat > /dev/null` would
// be handed escape codes under the cheaper test.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS,
		uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return errno == 0
}
