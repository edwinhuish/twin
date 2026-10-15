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
// Three places print this sentence — the report from `twin install --ext`,
// verify and doctor — and they must not drift into three different
// instructions for one action.
func EnableHint() string {
	return "注销重登一次（shell 只在启动时扫扩展目录），再跑 gnome-extensions enable " + ExtUUID
}

// The two lists that together say whether the shell will actually load an
// extension. Same read-only trick as the tray table: dconf's mmap path answers
// without a session bus, so "is it enabled?" is answerable from an installer
// or a script.
//
// enabledKeys alone would be one fact short. When the shell refuses an
// extension — it threw on enable, it is incompatible, the user turned it off
// from a session that then crashed — the uuid ends up in *both* lists, and a
// uuid in the disable list is not running whatever the enable list says. That
// difference decides whether twin routes the tray override through the helper
// or falls back to the host table, so it has to be read, not assumed.
const (
	shellSchema = "org.gnome.shell"
	enabledKey  = "enabled-extensions"
	blockedKey  = "disabled-extensions"
)

// ExtOutcome says what a call actually did. Same contract as Outcome: the
// caller prints a fact, not an assumption, so a second identical install can
// say "nothing to do" instead of claiming a change every time.
type ExtOutcome struct {
	// Installed is true when files were written or rewritten.
	Installed bool
	// Enabled is true when the shell's enable list now carries the uuid *and*
	// its disable list does not: a uuid in both is not waiting for anything,
	// it is refused, and calling that "enabled" would send the reader off to
	// log out and back in for nothing.
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
	// Blocked is true when the shell's own disable list carries the uuid: the
	// files are in place and the enable list may well name it, but the shell
	// will not load it.
	Blocked bool
	// Why is filled in when the lists could not be read at all (no
	// gsettings). An unreadable list is not "disabled": saying so would send
	// the user to fix the wrong thing.
	Why string
}

// Usable reports whether the shell will draw tray icons through the helper.
//
// This is the switch the tray override hangs on, so it is deliberately
// conservative: files in place, uuid in the enable list, uuid not in the
// disable list, and both lists readable. Anything else — including "cannot
// tell" — means twin writes the host's table instead, which is the path that
// works on every machine. Getting this wrong in the other direction would
// leave an instance with no tray icon at all, and nothing on screen to say
// why.
func (s ExtState) Usable() bool {
	return s.Installed && s.Enabled && !s.Blocked && s.Why == ""
}

// ShortWhy is the one clause that says why the helper is not usable, in the
// words four call sites print (the install block, the rehearsal, `verify` and
// `doctor`). It is one function for the same reason EnableHint is: four copies
// of one sentence drift into four different explanations of one state.
//
// It carries no command — the command needs room to explain itself, and it
// belongs to the site that has that room.
func (s ExtState) ShortWhy() string {
	switch {
	case !s.Installed && s.Why != "":
		return "助手扩展未装入，扩展列表也读不到"
	case !s.Installed:
		return "助手扩展未装入"
	case s.Why != "":
		return "读不到 shell 的扩展列表"
	case s.Blocked:
		return "助手扩展在 shell 的禁用列表里"
	case !s.Enabled:
		return "助手扩展已装入但未启用"
	default:
		return "助手扩展不可用"
	}
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

	enabled, blocked, err := shellLists()
	if err != nil {
		out.NeedsRelogin = true
		out.Detail = firstLine(err.Error())
		return out, nil
	}
	// A uuid in both lists is *not* enabled, whatever it looks like: the shell
	// will not load it. Asking the shell again is the one thing that can clear
	// the disable list, so that case goes to enableNow instead of returning a
	// cheerful "已启用" for an extension that is not running.
	if contains(enabled, ExtUUID) && !contains(blocked, ExtUUID) {
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
	back, blocked, err := shellLists()
	if err != nil {
		return out, fmt.Errorf("enable 之后读不回扩展列表：%w", err)
	}
	if contains(blocked, ExtUUID) {
		// The shell only clears the disable list for a uuid it knows about
		// (`_extensions.has(uuid)` in enableExtension): still being in there
		// after an `enable` that exited 0 means it has not scanned this uuid
		// yet, so the answer is the same login, not a retry.
		out.NeedsRelogin = true
		out.Detail = "enable 退 0，但它还在 shell 的禁用列表里：shell 还没扫到这个 uuid"
		return out, nil
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
// `doctor` both need exactly this, and so does the install path — it is the
// switch between the helper's own table and the host's.
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

	// Both lists or neither, for the same reason Usable() is conservative: a
	// uuid in the enable list with the disable list unreadable is "cannot
	// tell", and that has to reach the caller as such.
	enabled, blocked, err := shellLists()
	if err != nil {
		st.Why = firstLine(err.Error())
		return st
	}
	st.Enabled = contains(enabled, ExtUUID)
	st.Blocked = contains(blocked, ExtUUID)
	return st
}

// shellLists reads both extension lists. They are one fact in two halves: the
// enable list says what should load, the disable list says what the shell
// refused, and a uuid can be in both.
func shellLists() (enabled, blocked []string, err error) {
	if enabled, err = shellList(enabledKey); err != nil {
		return nil, nil, err
	}
	if blocked, err = shellList(blockedKey); err != nil {
		return nil, nil, err
	}
	return enabled, blocked, nil
}

// shellList reads one of the shell's extension lists.
func shellList(key string) ([]string, error) {
	gs := sysx.Which("gsettings")
	if gs == "" {
		return nil, fmt.Errorf("gsettings 不在 PATH")
	}
	res, err := sysx.Run(gs, "get", shellSchema, key)
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
