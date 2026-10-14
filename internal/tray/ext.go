// This file is the second half of the tray subsystem: the piece that runs
// inside the shell.
//
// twin can point the host's override table at any icon it likes, but the host
// draws those entries through a different path than the icon the program sends
// itself — and that path takes its size from the icon actor's *box* instead of
// from the panel icon size. So a twin icon comes out at the theme's 16px while
// the same program's own icon follows the icon-size setting. Nothing outside
// the shell can fix that: the box is set from JS inside the host extension.
//
// Hence a helper extension, shipped inside this binary and written to the
// user's extension directory. It is deliberately one small concern (see
// ext/extension.js) and deliberately self-erasing: if a future shell renames
// what it patches, the extension degrades to doing nothing and the override
// keeps working — it does not fork or replace the host.
//
// Installing it is a file write; *enabling* it is a session action, and the
// first one cannot succeed at all: the shell scans extension directories once,
// at startup, so a uuid it has never seen cannot be enabled by any command
// until the next login. That is a fact about the shell, not a limitation of
// this code, and it is reported as such instead of being retried in a loop.

package tray

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
)

// ExtUUID is the helper's extension id, and therefore also the name of its
// directory: the shell keys everything —— the enable list, the per-extension
// settings, the error messages —— off this one string, so the two cannot be
// allowed to drift. It follows the same "<name>@<domain>" shape as every other
// extension on this machine, which is what keeps it from colliding with one.
const ExtUUID = "twin-tray@edwinhuish.github.io"

//go:embed ext/metadata.json ext/extension.js
var extFiles embed.FS

// extNames is what gets written, in the order the messages name them. The
// embed above pins the set at compile time: an extension that loses a file
// silently stops working, so a missing file has to be a build failure rather
// than a runtime surprise.
var extNames = []string{"metadata.json", "extension.js"}

// ExtFiles names the files the helper is made of. The preview in
// `twin install --ext --dry-run` prints them, and a second hand-written
// list there would drift the moment the extension gains a file.
func ExtFiles() []string {
	return append([]string(nil), extNames...)
}

// EnableHint spells the step that turns the helper on once its files are in
// place, in the words of the only reason it is needed: the shell scans the
// extension directories once, at startup, so a uuid it has never seen cannot be
// enabled by any command until the next login.
//
// Four places print this sentence — the install-time check, the report from
// `twin install --ext`, verify and doctor — and they must not drift into
// four different instructions for one action.
func EnableHint() string {
	return "注销重登一次（shell 只在启动时扫扩展目录），再跑 gnome-extensions enable " + ExtUUID
}

// The list the shell loads extensions from. Same read-only trick as the tray
// table: dconf's mmap path answers without a session bus, so "is it enabled?"
// is answerable from an installer or a script.
const (
	shellSchema = "org.gnome.shell"
	enabledKey  = "enabled-extensions"
)

// ExtOutcome says what a call actually did. Same contract as Outcome: the
// caller prints a fact, not an assumption, so a second identical install can
// say "nothing to do" instead of claiming a change every time.
type ExtOutcome struct {
	// Installed is true when files were written or rewritten.
	Installed bool
	// Enabled is true when the shell's enable list now carries the uuid.
	Enabled bool
	// NeedsRelogin is true when the files are in place but the running shell
	// cannot load them yet. It is not an error and not retryable: only the next
	// login rescans the extension directories.
	NeedsRelogin bool
	// Detail is the shell's own words when enabling failed, empty otherwise.
	Detail string
}

// ExtState is what CheckExt can tell without touching anything.
type ExtState struct {
	Installed bool
	Enabled   bool
	// Why is filled in when the enable list could not be read at all (no
	// gsettings). An unreadable list is not "disabled": saying so would send
	// the user to fix the wrong thing.
	Why string
}

// ExtDir is the helper's own directory.
func ExtDir(p inst.Paths) string {
	return filepath.Join(p.ExtensionsDir(), ExtUUID)
}

// EnsureExt writes the helper and asks the shell to enable it. Idempotent, and
// it never touches the host extension: the helper runs *alongside* it.
func EnsureExt(p inst.Paths) (ExtOutcome, error) {
	var out ExtOutcome
	dir := ExtDir(p)

	// Compare before writing. The shell loads these files at startup and the
	// mtime is the only thing that says "there is something new here", so an
	// unchanged install must not touch them.
	var pending [][2]string
	for _, name := range extNames {
		want, err := extFiles.ReadFile("ext/" + name)
		if err != nil {
			return out, fmt.Errorf("内嵌的托盘扩展缺了 %s：%w", name, err)
		}
		dest := filepath.Join(dir, name)
		if have, err := sysx.ReadFile(dest); err == nil && have == string(want) {
			continue
		}
		pending = append(pending, [2]string{dest, string(want)})
	}
	if len(pending) > 0 {
		if err := sysx.EnsureDir(dir); err != nil {
			return out, err
		}
		for _, w := range pending {
			if err := sysx.WriteFileAtomic(w[0], []byte(w[1]), 0o644); err != nil {
				return out, err
			}
		}
		out.Installed = true
	}

	enabled, err := shellEnabled()
	if err != nil {
		out.NeedsRelogin = true
		out.Detail = firstLine(err.Error())
		return out, nil
	}
	if contains(enabled, ExtUUID) {
		out.Enabled = true
		return out, nil
	}
	return enableNow(out)
}

// enableNow asks the running shell to turn the helper on.
//
// It is allowed to fail, and on a first install it always does: the shell has
// never scanned this uuid, and `enable` on an unknown extension is an error by
// the shell's own rules, not a bug here. The caller turns that into "log out
// once" rather than a FAIL.
//
// It returns the outcome rather than filling one in: an ExtOutcome is three
// booleans that have to travel back to the caller together, and a value
// parameter would silently drop all three.
func enableNow(out ExtOutcome) (ExtOutcome, error) {
	ge := sysx.Which("gnome-extensions")
	if ge == "" {
		out.NeedsRelogin = true
		out.Detail = "本机没有 gnome-extensions 命令"
		return out, nil
	}
	res, err := sysx.Run(ge, "enable", ExtUUID)
	if err != nil {
		return out, err
	}
	if res.Code != 0 {
		out.NeedsRelogin = true
		out.Detail = firstLine(res.Stderr)
		return out, nil
	}
	// Read back rather than trust the exit code: the whole reason this project
	// has a "verify" verb is that a shell command reporting success is not
	// evidence that anything changed.
	back, err := shellEnabled()
	if err != nil {
		return out, fmt.Errorf("enable 之后读不回启用列表：%w", err)
	}
	if !contains(back, ExtUUID) {
		out.NeedsRelogin = true
		out.Detail = "gnome-extensions enable 退 0，但启用列表里没有它"
		return out, nil
	}
	out.Enabled = true
	return out, nil
}

// CheckExt reports the helper's state without changing anything. `verify` and
// `doctor` both need exactly this.
func CheckExt(p inst.Paths) ExtState {
	dir := ExtDir(p)
	st := ExtState{}
	missing := ""
	for _, name := range extNames {
		if !sysx.Exists(filepath.Join(dir, name)) {
			missing = name
			break
		}
	}
	st.Installed = missing == ""

	list, err := shellEnabled()
	if err != nil {
		st.Why = firstLine(err.Error())
		return st
	}
	st.Enabled = contains(list, ExtUUID)
	return st
}

// shellEnabled reads the list of extensions the shell loads.
func shellEnabled() ([]string, error) {
	gs := sysx.Which("gsettings")
	if gs == "" {
		return nil, fmt.Errorf("gsettings 不在 PATH")
	}
	res, err := sysx.Run(gs, "get", shellSchema, enabledKey)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s", firstLine(res.Stderr))
	}
	return parseStringList(res.Stdout)
}

// parseStringList reads the `as` form `gsettings get` prints: `['a', 'b']`.
//
// It reuses the quoting rules of the a(sss) reader below instead of splitting
// on commas: an entry that survives mangled would read as "not enabled", and
// the fix for that is a command that does nothing.
func parseStringList(lit string) ([]string, error) {
	s := strings.TrimSpace(lit)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "@") {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			return nil, fmt.Errorf("只有类型标注没有值：%q", s)
		}
		s = strings.TrimSpace(s[i+1:])
	}
	if s == "[]" {
		return nil, nil
	}
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("不是数组：%q", s)
	}
	var out []string
	for _, item := range splitTop(s[1:len(s)-1], ',') {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		v, err := unquote(item)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
