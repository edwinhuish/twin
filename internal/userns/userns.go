// Package userns answers one question about the host: can a bubblewrap
// sandbox started from an ordinary desktop launch create the *nested* user
// namespace an Electron/Chromium program needs — and when it cannot, which
// already-installed binary can lend it the AppArmor label that makes it
// possible?
//
// Two rules collide on Ubuntu 26.04:
//
//  1. /usr/lib/sysctl.d/10-apparmor.conf pins
//     kernel.apparmor_restrict_unprivileged_userns=1 (rewritten on every boot,
//     and by any package that touches /usr/lib/sysctl.d), so only a program
//     whose AppArmor profile grants `userns` may create an unprivileged user
//     namespace.
//  2. /etc/apparmor.d/bwrap-userns-restrict does grant that to /usr/bin/bwrap,
//     but adds `allow pix /** -> &bwrap//&unpriv_bwrap` and unpriv_bwrap
//     carries `audit deny capability`. Every descendant of that bubblewrap
//     therefore loses capabilities — measured 2026-10-12: a nested
//     unshare(CLONE_NEWUSER|CLONE_NEWNS) inside it fails with EPERM, so
//     Chromium's namespace sandbox falls back to the SUID sandbox and dies,
//     and minijail never starts.
//
// A private copy of bwrap escapes rule 2 (no profile attaches to that path)
// but then falls foul of rule 1. The way out is a *trampoline*: exec a binary
// that does have a path-attached `flags=(unconfined) { userns, }` profile. The
// kernel replaces the label at exec and every descendant inherits it, so a
// bubblewrap copy spawned from there can create the nested namespace after
// all. Ubuntu ships a long list of such profiles (browsers, Electron apps), so
// on a normal desktop a suitable binary is already installed.
package userns

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edwinhuish/twin/internal/sysx"
)

// DefaultDir is where AppArmor keeps its profiles.
const DefaultDir = "/etc/apparmor.d"

// SysctlPath is the switch that decides whether an unprivileged process may
// create a user namespace.
const SysctlPath = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// SysctlUnconfined is SysctlPath's sibling, restricting the same thing for
// unconfined tasks specifically. A desktop launch is unconfined, so this one
// bites even when the first reads 0 — and it is the switch that defeats the
// `aa-exec` trick, where the resulting label is `unconfined//&something`.
const SysctlUnconfined = "/proc/sys/kernel/apparmor_restrict_unprivileged_unconfined"

// SysctlSource is the file that pins these switches. It is named in
// diagnostics on purpose: the values come back after every boot and after any
// package that touches /usr/lib/sysctl.d, so "why did this regress?" has an
// answer that does not involve blaming the user.
const SysctlSource = "/usr/lib/sysctl.d/10-apparmor.conf"

// Restricted reports whether the kernel is refusing unprivileged user
// namespaces, with the evidence for the verdict. Either switch being on is
// enough to cost us the plain path.
func Restricted() (bool, string) {
	if ok, why := restrictedAt(SysctlPath); ok {
		return true, why
	}
	return restrictedAt(SysctlUnconfined)
}

func restrictedAt(path string) (bool, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, "" // no such knob: nothing is restricting us
	}
	v := strings.TrimSpace(string(b))
	if v != "1" {
		return false, path + "=" + v
	}
	return true, SysctlSource + " 把 " + path + " 设成 1"
}

// Trampoline is one binary whose path-attached AppArmor profile grants
// `userns`, making it usable as a label source for a bubblewrap copy.
type Trampoline struct {
	// Profile is the profile name, for diagnostics only. The mechanism keys
	// off the path, never the name — an app that renames its profile must not
	// break the wrap.
	Profile string
	// Path is the resolved attachment path, already checked to exist and be
	// executable.
	Path string
	// Electron means the binary is an Electron build, so it can be put into
	// pure Node mode with ELECTRON_RUN_AS_NODE=1 and used as a silent
	// spawner. Without it the binary is still a valid label source, but we
	// would have to find a flag that makes it run a command instead of
	// opening a window — true for very few programs, so Electron wins.
	Electron bool
}

// Describe renders the trampoline for a log line.
func (t Trampoline) Describe() string {
	if t.Electron {
		return t.Path + "（profile " + t.Profile + "，Electron）"
	}
	return t.Path + "（profile " + t.Profile + "）"
}

// Discover scans an AppArmor profile directory and returns every binary that
// can serve as a trampoline, best first.
//
// "Best" means Electron-capable, then a stable path order, so the same machine
// always picks the same one and a wrap is reproducible.
func Discover(dir string) []Trampoline {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Trampoline
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Only plain files: a directory here (abstractions/, tunables/, abi/)
		// holds includes, not profiles.
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, d := range parseProfiles(string(data)) {
			if !d.grantsUserns() {
				continue
			}
			for _, att := range ExpandBraces(d.attach) {
				if !sysx.IsExecutableFile(att) || isScript(att) {
					continue
				}
				out = append(out, Trampoline{
					Profile:  d.name,
					Path:     att,
					Electron: IsElectron(att),
				})
			}
		}
	}
	sortTrampolines(out)
	return out
}

// sortTrampolines orders candidates so that the same machine always picks the
// same one: Electron-capable first (only those can be driven silently), then
// distribution-managed paths ahead of anything under /opt, then plain path
// order. Stability matters because the chosen path is baked into a generated
// launcher and only re-decided on the next `twin reinstall`.
func sortTrampolines(list []Trampoline) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Electron != b.Electron {
			return a.Electron
		}
		if ua, ub := strings.HasPrefix(a.Path, "/usr/"), strings.HasPrefix(b.Path, "/usr/"); ua != ub {
			return ua
		}
		return a.Path < b.Path
	})
}

// isScript reports whether the file opens with a shebang.
//
// A wrapper script cannot serve as a trampoline even though its path carries
// the profile, because exec'ing it re-enters the real binary through the
// script's own logic — and that logic can refuse the mode we need. Measured
// 2026-10-12: /usr/share/code/bin/code is a sh script that answers
// "You are trying to start Visual Studio Code as a super user" instead of
// running the Node script it was handed. The real ELF beside it is fine.
func isScript(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [2]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return false
	}
	return b[0] == '#' && b[1] == '!'
}

// Pick returns the best trampoline, or false when there is none that can run a
// command silently. Electron-only is a deliberate restriction: a browser
// profile also grants userns, but nothing makes `/usr/bin/firefox` spawn a
// bubblewrap copy instead of opening a window.
func Pick(list []Trampoline) (Trampoline, bool) {
	for _, t := range list {
		if t.Electron {
			return t, true
		}
	}
	return Trampoline{}, false
}

// profileDecl is one parsed `profile ... { ... }` block.
type profileDecl struct {
	name   string
	attach string // "" when the profile has no path attachment
	flags  string // the raw content of flags=(...)
	body   string
}

// grantsUserns reports whether this profile is one we can borrow a label from:
// path-attached, unconfined, and explicitly granting userns.
func (d profileDecl) grantsUserns() bool {
	if d.attach == "" {
		return false
	}
	if !strings.Contains(d.flags, "unconfined") {
		return false
	}
	return hasUsernsRule(d.body)
}

// hasUsernsRule looks for a real `userns` rule, skipping comments — the bwrap
// profile's prose mentions "user namespace" repeatedly without granting it.
func hasUsernsRule(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ',' || r == '{' || r == '}' || r == '\n'
		}) {
			if tok == "userns" {
				return true
			}
		}
	}
	return false
}

// parseProfiles extracts every top-level profile declaration from one profile
// file. The parser is deliberately small: it only needs the attachment path,
// the flags and whether the body grants userns, never the rule semantics.
func parseProfiles(src string) []profileDecl {
	var out []profileDecl
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		decl, ok := parseProfileHead(lines[i])
		if !ok {
			continue
		}
		// Walk forward counting braces to find the end of this profile, so a
		// `userns` inside a *different* profile cannot be mistaken for this
		// one's (bwrap-userns-restrict declares two profiles, and the one that
		// grants userns is not the one whose children keep capabilities).
		var body strings.Builder
		depth := 0
		seen := false
		for j := i; j < len(lines); j++ {
			line := lines[j]
			// The head line is part of the body too: a profile may be written
			// on one line (`profile x /a flags=(unconfined) { userns, }`), and
			// skipping it would lose the very rule we came for.
			body.WriteString(line)
			body.WriteByte('\n')
			for _, r := range line {
				switch r {
				case '{':
					depth++
					seen = true
				case '}':
					depth--
				}
			}
			if seen && depth <= 0 {
				i = j
				break
			}
			if j == len(lines)-1 {
				i = j
			}
		}
		decl.body = body.String()
		out = append(out, decl)
	}
	return out
}

// parseProfileHead parses `profile <name> [<attach>] [flags=(...)] {`.
//
// The attachment is optional (`profile unpriv_bwrap flags=(...) {`), which is
// why the token after the name is only treated as a path when it is not the
// flags clause or the opening brace.
func parseProfileHead(line string) (profileDecl, bool) {
	// Strip a leading indent but not a comment: profile lines never carry a
	// trailing comment before the brace.
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "profile") {
		return profileDecl{}, false
	}
	rest := trimmed[len("profile"):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return profileDecl{}, false // e.g. "profilesomething"
	}
	toks := splitTokens(rest)
	if len(toks) == 0 {
		return profileDecl{}, false
	}
	d := profileDecl{name: toks[0]}
	for _, t := range toks[1:] {
		switch {
		case strings.HasPrefix(t, "flags="):
			d.flags = strings.TrimSuffix(strings.TrimPrefix(t, "flags="), "{")
		case t == "{":
			// end of the head
		case d.attach == "" && !strings.HasPrefix(t, "flags=") && !strings.HasPrefix(t, "("):
			d.attach = t
		}
	}
	return d, true
}

// splitTokens splits on whitespace, honouring double quotes. AppArmor allows a
// quoted name and a quoted attachment (`profile "workbuddy" "/opt/.../workbuddy"`).
func splitTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '"' {
			j := i + 1
			for j < len(s) && s[j] != '"' {
				j++
			}
			out = append(out, s[i+1:j])
			i = j + 1
			continue
		}
		j := i
		for j < len(s) && s[j] != ' ' && s[j] != '\t' {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

// ExpandBraces expands AppArmor's brace alternation in an attachment path, so
// `/usr/share/code{/bin,}/code` yields both real paths. Nesting is supported
// because real profiles use it:
// `/{usr/lib/firefox{,-esr},opt/firefox}/firefox`.
func ExpandBraces(p string) []string {
	i := strings.IndexByte(p, '{')
	if i < 0 {
		return []string{p}
	}
	depth, closeAt := 0, -1
	for k := i; k < len(p); k++ {
		switch p[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				closeAt = k
			}
		}
		if closeAt >= 0 {
			break
		}
	}
	if closeAt < 0 {
		return []string{p} // unbalanced: hand it back untouched
	}
	prefix, body, suffix := p[:i], p[i+1:closeAt], p[closeAt+1:]
	var out []string
	for _, alt := range splitTopLevel(body) {
		out = append(out, ExpandBraces(prefix+alt+suffix)...)
	}
	return out
}

// splitTopLevel splits on commas that are not inside a nested brace group.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// IsElectron decides whether a binary is an Electron build by looking at
// its installation directory rather than its contents.
//
// Reading 200 MB of chrome-sandboxed binary to grep for a string would make
// every `twin doctor` slow; the layout check is instant and precise enough,
// because Electron always ships an app bundle next to the executable while
// plain Chromium never does. The parent directory is checked too, so a wrapper
// in bin/ still resolves.
func IsElectron(bin string) bool {
	rel := []string{
		"resources/app.asar",
		"resources/app",
		"resources/electron.asar",
		"resources/default_app.asar",
		"libelectron.so",
	}
	dirs := []string{filepath.Dir(bin)}
	if up := filepath.Dir(dirs[0]); up != dirs[0] {
		dirs = append(dirs, up)
	}
	for _, d := range dirs {
		for _, r := range rel {
			if sysx.Exists(filepath.Join(d, filepath.FromSlash(r))) {
				return true
			}
		}
	}
	return false
}

// The policy itself, the profile file it belongs in and the commands that
// install it live in install.go — next to the read-back that decides whether
// the thing is actually loaded, because "printed" and "installed" are the two
// states this package has to keep apart.
