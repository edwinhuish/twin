// Package inst holds the twin instance schema — the single source of truth
// that `profile.yml` reads and writes.
package inst

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/edwinhuish/twin/internal/yamlz"
)

// Level is the rung of the identity ladder an instance relies on.
//
// The ladder was trimmed to what we could actually verify on this machine:
// `argv0` (Qt ignores it) and `shim` (LD_PRELOAD) were dropped rather than
// shipped as dead code — see 应用包裹生成器_twin设计.md §2.
type Level string

const (
	// LevelNone is T1: desktop entry + icon only, exec the program untouched.
	// Honest only when the program derives its own name from something we can
	// set (args/env) or does not collide at all.
	LevelNone Level = "none"
	// LevelAlias renames the *real path* via a bubblewrap overlay, which is what
	// the window app_id is derived from, and lets argv[0] default to the same
	// name so the tray SNI Id matches. This is the verified workhorse.
	LevelAlias Level = "alias"
	// LevelArgument / LevelEnv let the program rename itself via its own flags
	// or environment. Detected but not yet implemented.
	LevelArgument Level = "argument"
	LevelEnv      Level = "env"
	// LevelRedirect is L5: the program hard-codes its app_id, so the real path
	// rename does not change what the window reports. It still runs through the
	// same bubblewrap rename — that is what leaves processes traceable evidence
	// (the alias path in mountinfo and in argv[0]) — and additionally gets a
	// rule for a shell extension that rewrites the window→app mapping.
	LevelRedirect Level = "redirect"
)

// Levels lists every level in ladder order.
var Levels = []Level{LevelNone, LevelAlias, LevelArgument, LevelEnv, LevelRedirect}

// Implemented reports whether the generator can actually produce this level.
// Anything false must be refused loudly rather than approximated silently.
func (l Level) Implemented() bool {
	return l == LevelNone || l == LevelAlias || l == LevelRedirect
}

// Renames reports whether the level runs the program through a bubblewrap
// overlay under a new file name. Alias and redirect both do, and both need the
// same three artefacts arranged in the same order; they differ only in whether
// a redirection rule is also written.
func (l Level) Renames() bool { return l == LevelAlias || l == LevelRedirect }

// ParseLevel validates a user-supplied level string.
func ParseLevel(s string) (Level, error) {
	for _, l := range Levels {
		if string(l) == s {
			return l, nil
		}
	}
	return "", fmt.Errorf("unknown --level %q (want one of: %s)", s, levelNames())
}

func levelNames() string {
	out := make([]string, len(Levels))
	for i, l := range Levels {
		out[i] = string(l)
	}
	return strings.Join(out, " | ")
}

// Bind is one directory mounted into the sandbox.
//
// It is written the way a Docker Compose volume is — "source:target[:ro]" —
// because that is the shape everyone already reads: the left side is where the
// data really is, the right side is where the program will look for it. That is
// also bubblewrap's own `--bind SRC DEST` order, so profile.yml, rules.d and the
// rendered launcher all run the same direction and none of them needs a mental
// flip.
type Bind struct {
	Source string // where the data lives (may start with ~)
	Target string // the path the program sees (may start with ~)
	// ReadOnly renders `--ro-bind` instead of `--bind`. It is the only reason
	// this is not just a pair of strings: writable is what the private-copy
	// isolation is for, while read-only is for handing a program somebody
	// else's files without letting it touch them.
	ReadOnly bool
	// Dev renders `--dev-bind`, which bubblewrap describes as "bind mount the
	// host path SRC on DEST, allowing device access". It is here because the
	// root a program runs under stopped being twin's constant: the root itself
	// is `--dev-bind / /` (written as `bwrap.root: dev`), and a definition that
	// tightens it to `--ro-bind / /` needs a way to say which bindings are meant
	// to hand over devices — a distinction only the mapping's own line can
	// carry.
	//
	// It is a way to say it, not a workaround for something `--bind` cannot
	// do: measured on bubblewrap 0.11, both spellings come out of `ls -l` as a
	// character-device mount, so nothing here depends on `:dev` being the only
	// one that works. What cannot be expressed at all is `:ro` together with
	// `:dev` — bubblewrap has no read-only device bind — so Check refuses that
	// pair rather than silently dropping one of the two.
	Dev bool
	// Try renders the `-try` spelling of whichever option the mode picked
	// (`--bind-try` / `--ro-bind-try` / `--dev-bind-try`), which bubblewrap
	// documents as "equal to --bind but ignores non-existent SRC".
	//
	// It exists because a mapping whose source may legitimately not be there yet
	// — a rules.d entry pointing at a directory the program creates on first
	// run — has no other honest spelling: plain `--bind` turns "not there yet"
	// into "the program does not start at all", and manufacturing an empty
	// source for it would be twin answering a question about somebody else's
	// data. It only ever *skips* the mount; where the source is there, the two
	// spellings are the same mount.
	Try bool
}

// IsRootMount reports whether this entry is the base's root mount: the whole
// host root bound onto itself — `/:/:dev`, the way every definition written
// before `bwrap.root` carries it (that *is* `--dev-bind / /`).
//
// It is recognised by shape rather than by a flag of its own, and three places
// depend on that: Build emits it *before* every other mount (it is the surface
// the rest is laid on — bound after the alias overlay it puts the host's
// directory back over the copy the program is supposed to run from), the
// reports that read `bind` as "the program's data directories" leave it out
// (the root is not one of them), and the launcher's mount points skip it (both
// of its ends are `/`, which is already there).
//
// New definitions do not reach here: the root is `bwrap.root`, and only the
// old spelling keeps it in this list — see Instance.RootBind, which is what
// tells the two sources apart.
func (b Bind) IsRootMount() bool { return b.Source == "/" && b.Target == "/" }

// RootBind is `bwrap.root` as the mapping it stands for, and whether that key
// said anything at all.
//
// It renders as a Bind because that is what it is — the four modes a mapping
// can have, applied to a path bound onto itself — and going through the same
// value keeps the two spellings from drifting: `root: dev` and `/:/:dev` reach
// bwrap by the same code (sandbox.bindGroup). The three accepted words are
// deliberately the bind modes verbatim (`isBindMode`), so there is no fourth
// list of words to keep in step.
func (i *Instance) RootBind() (Bind, bool) {
	switch i.Root {
	case "":
		return Bind{}, false
	case "ro":
		return Bind{Source: "/", Target: "/", ReadOnly: true}, true
	case "rw":
		return Bind{Source: "/", Target: "/"}, true
	default: // "dev", the only one Check lets through besides the two above
		return Bind{Source: "/", Target: "/", Dev: true}, true
	}
}

// String renders the one-line form the config files carry.
//
// `:try` is written *after* the mode, and the mode is spelled out even when it
// is the default one: the file has exactly one way to say "optional mapping"
// (`目标:源:rw:try`, three colons), and a bare `源:目标:try` would be a second
// spelling of the same thing — read back as a target named `try` by every
// reader that does not know the suffix. Round-tripping is the rule here because
// `twin reinstall` re-renders from the parsed value: a spelling that drifts shows up as
// a diff nobody made.
func (b Bind) String() string {
	s := b.Source + ":" + b.Target
	switch {
	case b.ReadOnly:
		s += ":ro"
	case b.Dev:
		s += ":dev"
	case b.Try:
		s += ":rw" // spelled only to make room for the :try after it
	}
	if b.Try {
		s += ":try"
	}
	return s
}

// ParseBind reads the one-line form: "source:target", "source:target:ro",
// "source:target:rw", "source:target:dev", "source:target:rw:try", or a bare
// "path" meaning the same directory both ways.
//
// The bare form is not an optimisation. A directory bound onto itself is what
// upgrades it to writable in place under `--ro-bind / /`, which is the one
// mapping WeChat's received-files directory has always needed.
//
// The split is Docker's: a trailing ":ro"/":rw"/":dev" is the mode, then the
// first colon separates the two sides. A path that itself contains a colon is
// therefore ambiguous, and — as in Docker — the ambiguity is resolved the
// documented way rather than guessed at.
//
// `:try` comes after the mode and *requires* it: `源:目标:try` is refused with
// the spelling to write instead. Reading it as a mode would be inventing a
// fourth one, and reading `源:try` as "the optional mapping" would silently
// take away a target path somebody wrote — a mapping is the one thing in a
// wrap that is silently wrong when it is wrong.
func ParseBind(spec string) (Bind, error) {
	parts := strings.Split(strings.TrimSpace(spec), ":")
	b := Bind{}
	// Only a *trailing* :try is the suffix, and only when a mode is already
	// there: `x:y:try` (three parts) is the miswrite this refuses. `x:try`
	// (two parts) stays what it always was — a target path named `try`.
	if len(parts) >= 3 && parts[len(parts)-1] == "try" {
		if len(parts) < 4 {
			return Bind{}, fmt.Errorf("路径映射 %q 的 :try 要跟在写全的访问模式后面：写成 "+
				"`源:目标:ro:try`（可写的写 :rw:try，带设备访问的写 :dev:try）", spec)
		}
		b.Try = true
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 2 && isBindMode(parts[1]) {
		// `x:ro` has no target, only what looks like a mode. Reading it as
		// "target named ro" would build a relative mount point out of a typo,
		// so it is refused instead. `:dev` lands here too — and the advice below
		// covers it, since `x:x:dev` is exactly how a device path bound onto
		// itself is written.
		return Bind{}, fmt.Errorf("路径映射 %q 的右侧是访问模式而不是目标路径："+
			"两侧写全（%s:%s:%s），或去掉模式（同一路径默认可写）", spec, parts[0], parts[0], parts[1])
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			b.ReadOnly = true
		case "dev":
			b.Dev = true
		case "rw":
		default:
			return Bind{}, fmt.Errorf("路径映射 %q 的访问模式 %q 不认识（只有 ro、rw 和 dev）", spec, parts[2])
		}
		parts = parts[:2]
	}
	if len(parts) > 3 {
		return Bind{}, fmt.Errorf("路径映射 %q 的冒号太多（形如 \"源:目标[:ro|:rw|:dev][:try]\"）；"+
			"ro 与 dev 也不能同时用：bwrap 没有只读的设备映射", spec)
	}
	b.Source = parts[0]
	b.Target = b.Source
	if len(parts) == 2 {
		b.Target = parts[1]
	}
	if b.Source == "" || b.Target == "" {
		return Bind{}, fmt.Errorf("路径映射 %q 少了一侧路径", spec)
	}
	return b, nil
}

// isBindMode reports whether a token is an access mode rather than a path. It
// is the list ParseBind accepts, kept in one place so that "one path plus a
// mode" — which has no target and would otherwise be read as a path named
// after the mode — is caught for every mode there is.
func isBindMode(s string) bool {
	switch s {
	case "ro", "rw", "dev":
		return true
	}
	return false
}

// EnvVar is one `--setenv` pair, written on one line as "键=值".
//
// It is a pair rather than a YAML mapping because the file's job is to say what
// bwrap will be called with, and `--setenv KEY VALUE` is two arguments: writing
// `键: 值` would put a second, differently-shaped spelling of the same thing in
// the same file. The separator is `=`, the spelling the reader already types
// everywhere (`docker -e`, systemd `Environment=`), and the split is at the
// *first* one: the values twin carries are full of colons (`PATH=/a:/b`, a URL),
// and an `=` showing up inside a value still survives — only the first separates.
type EnvVar struct {
	Key   string
	Value string
}

// String renders the one-line form the definition carries.
func (e EnvVar) String() string { return e.Key + "=" + e.Value }

// ParseEnvVar reads the one-line form: "键=值". The value may be empty (a
// program that treats "set but empty" differently from "unset" needs that),
// which is why the `=` is not optional.
func ParseEnvVar(spec string) (EnvVar, error) {
	key, value, ok := strings.Cut(strings.TrimSpace(spec), "=")
	if !ok {
		return EnvVar{}, fmt.Errorf("环境变量 %q 少了等号：写成 `键=值`（值可以是空的，键不行）", spec)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return EnvVar{}, fmt.Errorf("环境变量 %q 没有键名：写成 `键=值`", spec)
	}
	// A key with a space in it would reach bwrap as one argument that no
	// program can look up. `=` cannot get in here — it is the separator, and
	// everything from the first one on went to the value.
	if strings.ContainsAny(key, " \t") {
		return EnvVar{}, fmt.Errorf("环境变量 %q 的键名里有空格：键名得是一个词，写法是 `键=值`", spec)
	}
	return EnvVar{Key: key, Value: value}, nil
}

// Instance is one wrapped program.
type Instance struct {
	ID string

	// Where the program is, and which entry describes it — `source.exec` and
	// `source.desktop`. Both are paths the way profile.yml writes paths:
	// absolute, `~`-rooted, or relative to the file itself (see Load).
	Target string
	Source string

	// Args is `source.args`: the program's own arguments, appended to it in the
	// order written.
	//
	// A list of lines, one line per argument group, split on whitespace exactly
	// the way bwrap.extras is — the same shape for the same reason: a line is
	// what a reader can compare against the program's own `--help`, and twin
	// does not parse arguments. What a terminal would make of that line is
	// what the program gets, because the launcher adds no quotes of its own
	// (see sandbox.progArgs): `~` and `$HOME` are expanded by the shell that
	// starts the program, and a value with a space is written the way it would
	// be typed — `--opt="a b"` — with the quotes riding inside the token.
	//
	// They sit after the program path and before the `"$@"` the desktop
	// environment adds from Exec's field codes, because the program's own
	// switches belong in front of the file/URL operands GLib hands it. Both
	// shapes of the launcher carry them — level none execs the program
	// directly, an alias-level one execs the alias — so they mean the same
	// thing on both sides of the ladder; that is why they are part of the
	// instance rather than of the bwrap invocation.
	Args []string

	// DesktopOverride is the file holding the *text* of the desktop entry twin
	// installs: `override.desktop`. It is a file rather than six keys because
	// that text is the one part of a wrap a user rewrites instead of reads —
	// a display name, keywords, a menu category — and a key per line meant
	// editing the configuration to change what ends up in a .desktop anyway.
	// `install` generates it from the source entry when it is not there yet, and
	// copies it to Paths.Desktop(id) verbatim every time after that, so it is
	// also the answer to "what exactly is being installed?".
	DesktopOverride string

	// WMClass is the StartupWMClass written into the generated entry. Empty
	// means "the id", which is the useful default for a renamed program and is
	// why `gen` leaves it empty. Like every other field of the entry's text it
	// is only consulted when that text is *generated*: once the file exists,
	// editing this key changes nothing until the file is regenerated.
	WMClass string

	// TrayOverride says whether this instance asks twin to point the host's
	// tray override table at its own icon. It is true by default and is only
	// interesting when false: the table is global, so a program whose tray id
	// twin cannot predict (level=none) or a machine whose tray host is
	// something else leaves it alone.
	TrayOverride bool

	IconSource string
	Badge      string
	BadgeStyle string

	// Level, Unshare and NestedNS are this instance's isolation.
	Level   Level
	Unshare []string
	// NestedNS says the program creates a user namespace of its own while it
	// runs — the Chromium/Electron class, which probes for one to build its
	// sandbox and dies without it. This is application knowledge, not machine
	// state, so it belongs in the snapshot and travels with it; the
	// machine-specific half (which trampoline, where the bubblewrap copy
	// lives) stays in config.toml, where the next machine can decide its own.
	//
	// When it is set, the launcher routes through a private bubblewrap copy,
	// because the system one cannot grant what the program needs (see
	// internal/userns).
	NestedNS bool

	// Root, Binds, Env and Extras are the bubblewrap invocation, and they are
	// all of it: the base is not in Go anywhere (see sandbox.BaseFor), so what
	// the program gets is readable in this file.
	//
	// Root is `bwrap.root`: which root the program runs under, as one of the
	// bind modes (`ro` → `--ro-bind / /`, `rw` → `--bind / /`, `dev` →
	// `--dev-bind / /`, what `gen` writes). It is a key of its own rather than
	// an entry in Binds because the root is the one mount whose *position* is
	// not its own: it goes down before everything, wherever the file lists it
	// (see Bind.IsRootMount) — so as a mapping it was a line that taught the
	// reader a wrong lesson about the file's order, spelled `/:/:dev`.
	//
	// Empty means "this key says nothing about the root", which is what every
	// definition written before it means — those carry the mount in Binds (or
	// `--dev-bind / /` in Extras) and are still read. Either way the root can
	// only be said once: Check refuses a definition that says it twice.
	Root string
	// Binds is `--bind` / `--ro-bind` / `--dev-bind` / `--bind-try` and so on,
	// in the order written — the program's data, not the surface it runs on.
	// Older definitions keep the root mount in here (`/:/:dev`); Build still
	// emits that one before every other mount, and the reports that read this
	// list as "the program's data directories" leave it out — see
	// Bind.IsRootMount.
	Binds []Bind
	// Env is `--setenv` pairs, in the order written (later pairs win, as bwrap
	// does it). The instance's own TWIN_INSTANCE is one of them: `twin gen`
	// writes it here, because it is a variable twin sets and has no business
	// being invisible in the definition.
	Env []EnvVar
	// Extras is everything else bwrap is called with — one line per argument
	// group, in command order: `--proc /proc`, `--tmpfs /dev/shm`,
	// `--die-with-parent`, and whatever config.toml, rules.d and `gen`'s `--`
	// region append. It is a *merged* list, not "a few more options": Build
	// splits each line into tokens and hands them over, and twin adds nothing
	// of its own anywhere.
	//
	// One line per group rather than one token per element: a line is what a
	// reader can compare against `bwrap --help`, and the shape is the same one
	// config.toml and rules.d use. The price is exact and documented — a value
	// containing a space cannot be written here at all (a path with a space
	// belongs in Binds, which is parsed and `~`-expanded; nothing in Extras is,
	// because twin does not know any bwrap option's arity — 设计 §2.1).
	Extras []string
}

// ProfileName is the file name the working copy and the instance archive both
// carry. One schema, one word for it: the header line inside the file is what
// tells the two apart, not the name — they are the same document at two moments.
const ProfileName = "profile.yml"

// Paths computes every artefact location from an explicit home directory.
type Paths struct{ Home string }

func (p Paths) Desktop(id string) string {
	return filepath.Join(p.Home, ".local/share/applications", id+".desktop")
}
func (p Paths) Icon(id string) string {
	return filepath.Join(p.Home, ".local/share/appicons", id+".png")
}
func (p Paths) InstanceDir(id string) string {
	return filepath.Join(p.Home, ".local/share/twin", id)
}
func (p Paths) Launch(id string) string { return filepath.Join(p.InstanceDir(id), "launch") }

// Profile is the instance snapshot: the file a human is meant to read and edit.
// YAML because the schema is nested (source / override / isolation / bwrap) and
// a nested file reads as a hierarchy instead of as a wall of headers.
//
// There is deliberately no Paths method for the desktop entry's own text (the
// file `override.desktop` points at): its place is "beside this definition",
// which is a fact about the file being read, not about $HOME — `gen -o` puts
// both of them wherever the user asked.
func (p Paths) Profile(id string) string {
	return filepath.Join(p.InstanceDir(id), ProfileName)
}

// LegacyProfile is the pre-YAML snapshot name. Nothing reads it any more, but
// finding one has to be reported rather than ignored: an instance whose file
// silently stops being an instance is exactly the kind of failure twin exists
// to prevent.
func (p Paths) LegacyProfile(id string) string {
	return filepath.Join(p.InstanceDir(id), "instance.toml")
}

func (p Paths) StateDir(id string) string {
	return filepath.Join(p.Home, ".local/state/twin", id)
}

// RelStateDir is the state directory written with a leading ~ instead of an
// absolute home. profile.yml is meant to be human-edited and replayed on
// another machine (设计 §5.1), so it must not bake in today's home; Expand
// turns this back into a real path at use time.
func (p Paths) RelStateDir(id string) string {
	return "~/.local/state/twin/" + id
}
func (p Paths) RulesDir() string {
	return filepath.Join(p.Home, ".config/twin/rules.d")
}

// ExtensionsDir is where the shell looks for *user* extensions. twin's tray
// helper goes there and nowhere else: the system directory would need root, and
// the point of the helper is that twin can install it without any. It is a
// shared directory — every other user extension lives in it too, which is why
// nothing but the helper's own subdirectory is ever written.
func (p Paths) ExtensionsDir() string {
	return filepath.Join(p.Home, ".local/share/gnome-shell/extensions")
}

// GlobalConfig is the environment layer: machine-scoped defaults. It is not the
// same file as Profile(id), which is one instance's snapshot.
func (p Paths) GlobalConfig() string {
	return filepath.Join(p.Home, ".config/twin/config.toml")
}
func (p Paths) RuleFile(id string) string {
	return filepath.Join(p.RulesDir(), id+".json")
}

// TrayTableDir holds the neutral tray table: one small JSON file per instance,
// read by the shell-side helper (see internal/tray/table.go).
//
// It lives with the other per-instance artefacts and not in ~/.config because
// it is generated, not configured: `twin remove` deletes the row with the
// instance, and nothing hand-edits it.
func (p Paths) TrayTableDir() string {
	return filepath.Join(p.Home, ".local/share/twin/tray.d")
}

// TrayTable is the file the helper reads this instance's override from. The
// name is the instance id, which — for an instance that renamed the program —
// is also the StatusNotifierItem id it matches on.
func (p Paths) TrayTable(id string) string {
	return filepath.Join(p.TrayTableDir(), id+".json")
}

// BinDir holds the shared, machine-wide helpers twin needs at run time.
//
// They live outside any instance directory on purpose: neither carries
// per-instance identity, and the bubblewrap copy in particular must be at one
// stable path for the trampoline's AppArmor label to reach it. One copy serves
// every wrapped program.
func (p Paths) BinDir() string {
	return filepath.Join(p.Home, ".local/share/twin/bin")
}

// BwrapCopy is the private bubblewrap binary.
//
// The whole point of the copy is that no AppArmor profile attaches to this
// path. /usr/bin/bwrap has one, and it stacks unpriv_bwrap — with its
// `audit deny capability` — onto every descendant, which is what stops a
// Chromium sandbox from creating the nested user namespace it probes for.
func (p Paths) BwrapCopy() string {
	return filepath.Join(p.BinDir(), "bwrap")
}

// Bootstrap is the small Node script that sits between the trampoline and the
// bubblewrap copy. It exists because the trampoline is an Electron binary
// driven in ELECTRON_RUN_AS_NODE mode, so it can only run a script — and that
// script has three jobs: drop the mode variable before it leaks into the
// sandbox, spawn bubblewrap, and pass the exit status through.
func (p Paths) Bootstrap() string {
	return filepath.Join(p.BinDir(), "bootstrap.js")
}

// SystemDesktopDirs are the places a source .desktop file may live.
func (p Paths) SystemDesktopDirs() []string {
	return []string{
		filepath.Join(p.Home, ".local/share/applications"),
		"/usr/local/share/applications",
		"/usr/share/applications",
	}
}

// resolvePath is the other half of Expand, and the base is a different one on
// purpose: Expand answers "which home is this ~ about" and runs at use time on
// this machine, while resolvePath answers "which directory is this relative path
// about" and runs once, when the definition is read — the directory the
// definition file itself lives in.
//
// Absolute paths are already an answer and `~` is deliberately left spelled that
// way: it is turned into a home by Expand, which is what keeps one definition
// replayable on another machine. Everything else is relative, including the
// empty string, and only the non-empty ones are joined.
func resolvePath(base, s string) string {
	if s == "" || IsResolvablePath(s) {
		return s
	}
	return filepath.Join(base, s)
}

// IsResolvablePath reports whether a path carries its own meaning — absolute, or
// rooted at `~`. Anything else is relative, and a relative path needs a base:
// profile.yml has one (the file's own directory, see Load), a rules.d file does
// not, which is why config refuses them there rather than picking a base.
func IsResolvablePath(s string) bool {
	return filepath.IsAbs(s) || s == "~" || strings.HasPrefix(s, "~/")
}

// There is no exported resolvePath any more. Its one outside caller was `gen`,
// which used to write the desktop entry's file next to the definition it was
// writing — the base was the definition's own directory, and `gen` and Load had
// to agree about what `./x.desktop` means. Now `install` writes that file, and
// it gets the path from Load: one reader, one answer, no second spelling of the
// rule to keep in step.

// Expand turns a leading ~/ into an absolute path. Anything else is returned
// unchanged; the sandbox never re-resolves it (a relative path was already
// resolved against its definition's directory by Load).
func (p Paths) Expand(s string) string {
	switch {
	case s == "~":
		return p.Home
	case strings.HasPrefix(s, "~/"):
		return filepath.Join(p.Home, s[2:])
	}
	return s
}

// Home resolves $HOME and refuses to guess. The existing hand-written wrapper
// defaulted to "/root" — inside a wrapper that is a data-loss footgun, so this
// is the one place we fail hard instead.
func Home() (string, error) {
	h := os.Getenv("HOME")
	if h == "" {
		return "", fmt.Errorf("HOME is not set — twin refuses to guess a home directory")
	}
	if !filepath.IsAbs(h) {
		return "", fmt.Errorf("HOME=%q is not an absolute path", h)
	}
	return h, nil
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateID enforces the rules the desktop entry basename must satisfy.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("instance id is empty")
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("invalid instance id %q: use letters, digits, dot, dash, underscore", id)
	}
	// The id doubles as a desktop entry basename and as the alias file name
	// inside the target directory, so these names would be shadows of each other.
	switch id {
	case ".", "..":
		return fmt.Errorf("invalid instance id %q", id)
	}
	return nil
}

// DefaultIconSize is the edge length of generated icons. 256 is the size the
// panel and the overview both scale down from without visible loss.
const DefaultIconSize = 256

// IconSizeOrDefault keeps the size out of profile.yml for now: it is derivable,
// and a persisted copy would only invite drift.
func (i *Instance) IconSizeOrDefault() int { return DefaultIconSize }

// TargetDir is the directory holding the real program (and the alias).
func (i *Instance) TargetDir() string { return filepath.Dir(i.Target) }

// TargetBase is the real program's file name.
func (i *Instance) TargetBase() string { return filepath.Base(i.Target) }

// AliasPath is the absolute path of the renamed entry point: the file twin
// creates inside the target directory, and the value both identity channels
// agree on (the window's app_id and the tray's SNI Id are both this basename).
//
// Its name is the id, and there is no key for it any more — a second spelling of
// "which instance is this" could only ever disagree with the id, and every
// disagreement is a program whose window and tray icon stop finding the desktop
// entry that names them.
func (i *Instance) AliasPath() string {
	return filepath.Join(i.TargetDir(), i.ID)
}

// hasRootMount reports whether anything in the file asks for a root. The two
// file-side answers are exact — `bwrap.root` says so by being there, and a
// `bind` entry whose two ends are `/` says it by shape — while the `extras`
// side can only be a proxy, because twin knows no bwrap option's arity and must
// not start parsing them. Every definition `gen` writes says it in the key, so
// what the last two really guard are files that predate the key and a file that
// lost its base altogether.
func (i *Instance) hasRootMount() bool {
	if i.Root != "" {
		return true
	}
	for _, b := range i.Binds {
		if b.IsRootMount() {
			return true
		}
	}
	return len(i.Extras) > 0
}

// Check performs the semantic validation the generator relies on. Every
// failure here maps to a symptom we have actually seen fail silently.
func (i *Instance) Check() error {
	if err := ValidateID(i.ID); err != nil {
		return err
	}
	if i.Target == "" {
		return fmt.Errorf("source.exec is empty (nothing to run)")
	}
	if !filepath.IsAbs(i.Target) {
		return fmt.Errorf("source.exec %q must be an absolute path", i.Target)
	}
	// The entry's text has to come from *somewhere*, and the only place it can
	// come from is that file: `install` copies it, and an empty key would send
	// the copy to the instance directory itself (a path that is a directory,
	// which fails at the last possible moment and in a way nobody recognises as
	// "you deleted a config value").
	if i.DesktopOverride == "" {
		return fmt.Errorf("override.desktop 是空的：桌面项正文必须有个文件装着（`gen` 在定义里写的是 " +
			"./<id>.desktop，与这份定义同目录；install 缺了就按源桌面项生成一份，之后逐字复制它）")
	}
	if !i.Level.Implemented() {
		return fmt.Errorf("isolation.level %q is not implemented yet (only %s)", i.Level, "none | alias | redirect")
	}
	// The alias is the id, so this is the id colliding with the real file name:
	// renaming to it would be a no-op while the desktop entry, the launcher and
	// the tray table all claim it happened.
	if i.ID == i.TargetBase() {
		return fmt.Errorf("实例 id %q 与真实程序的文件名相同 —— 改名就成了空操作（而它既是别名文件名、"+
			"又是桌面项基名），换一个 id", i.ID)
	}
	for _, u := range i.Unshare {
		if u == "net" {
			return fmt.Errorf("isolation.unshare must not contain \"net\": it cuts the network and blocks " +
				"abstract-socket single-instance locks, which bwrap cannot isolate anyway")
		}
	}
	// The three words are the bind modes, which is the whole reason there is no
	// fourth list of them to keep in step (see RootBind). Anything else is a
	// mount type somebody would then have to implement.
	switch i.Root {
	case "", "ro", "rw", "dev":
	default:
		return fmt.Errorf("bwrap.root %q 不是 ro / rw / dev 之一：ro = --ro-bind / /（整个宿主根只读，"+
			"代价是设备与 /run/user 要自己补回来），rw = --bind / /，dev = --dev-bind / /（带设备访问，"+
			"`gen` 写的就是它）", i.Root)
	}
	for n, b := range i.Binds {
		if b.Source == "" || b.Target == "" {
			return fmt.Errorf("bwrap.bind[%d] %q: 源和目标是两条路径，都要写", n, b)
		}
		// The root said twice, so which one wins would be decided by where the
		// two land in the command (the key's goes first, the entry's right
		// after it) — an answer nobody wrote down. Refused by name instead,
		// because the fix is one deletion and the symptom otherwise is "the
		// root is not what I asked for" long after this edit.
		if b.IsRootMount() && i.Root != "" {
			return fmt.Errorf("bwrap.bind[%d] %q 和 bwrap.root: %s 说的是同一处挂载 —— "+
				"两条都会发出去，后发的那条胜出，「这个程序拿到哪个根」就由发射顺序决定了；"+
				"删掉 bind 里这一条，根只用 `root` 说", n, b, i.Root)
		}
		// Both flags at once is not expressible in bubblewrap, and String()
		// would have to drop one of them when writing the file back — so it is
		// an error rather than a precedence rule. ParseBind cannot produce this
		// pair; a program that fills Bind in directly can.
		if b.ReadOnly && b.Dev {
			return fmt.Errorf("bwrap.bind[%d] %q: :ro 与 :dev 不能同时用 —— bwrap 没有只读的设备映射"+
				"（--dev-bind 天生可写）", n, b)
		}
		// The host root mounted *somewhere else* is the shape a mistyped root
		// mount takes: the read-only root is `/:/:ro`, three parts, but `/:/ro`
		// is two — and Docker's rule only reads a part as a mode when it *is* one
		// of ro/rw/dev, so the second one is a path. The two differ in the one
		// way that matters: the typo leaves the sandbox with no root at all, and
		// the program then cannot find itself (`/` is where it lives).
		if b.Source == "/" && b.Target != "/" {
			return fmt.Errorf("bwrap.bind[%d] %q：把整个宿主根挂到 %s 上多半是笔误 —— 只读的根写成 "+
				"/:/:ro（三段：源、目标、模式），写成 /:/ro（两段）会被读成这个映射，沙箱里就没有根了",
				n, b, b.Target)
		}
	}
	for n, e := range i.Env {
		// Round-tripping the one-line form is the whole check: a key carrying a
		// space cannot be told from the value when the pair is written back
		// out, and a file that reads back as something else is the failure this
		// project refuses to ship.
		if got, err := ParseEnvVar(e.String()); err != nil || got != e {
			return fmt.Errorf("bwrap.env[%d] %q 不是「键=值」—— 键名不能带空格（值里可以有 = 与冒号）", n, e)
		}
		if strings.ContainsAny(e.Value, "\x00\n") {
			return fmt.Errorf("bwrap.env[%d] %q 的值里有换行或 NUL —— bwrap 的 --setenv 收不下", n, e)
		}
	}
	// There is no "sandbox with defaults" any more: twin keeps no base of its
	// own, so this file is the only thing that could have put a root in there.
	// bwrap without one starts the program in a namespace where nothing is
	// mounted, which reads at the far end as "the program cannot find itself" —
	// a failure with no trace of twin in it.
	//
	// The root can be said in three places, so all three are read: `bwrap.root`
	// (what `gen` writes — the mount's own key), a `bind` entry whose two ends
	// are `/` (what every definition written before that key carries), or
	// `--dev-bind / /` hand-written in `extras`. Empty `extras` on its own is
	// therefore fine — that is exactly what a definition whose root is in the
	// key looks like.
	if i.Level.Renames() && !i.hasRootMount() {
		return fmt.Errorf("这份定义没有根挂载：bwrap.root 是空的，bwrap.bind 里没有 /:/:dev，" +
			"bwrap.extras 里也没有 --dev-bind / / —— bwrap 至少要有一个根挂载，twin 不再自己补一个；" +
			"请用 `twin gen` 重新生成，或照它的注释手写一份")
	}
	// The program's own arguments are not bwrap options, so nothing here reads
	// them for meaning — but a blank line is the same nothing-written-yet that
	// `extras` refuses, and it would otherwise reach the launcher as no
	// argument at all while the file looks like it asks for one.
	for n, line := range i.Args {
		if strings.TrimSpace(line) == "" {
			return fmt.Errorf("source.args[%d] 是空行：要么写一条完整参数，要么整行去掉", n)
		}
	}
	for n, line := range i.Extras {
		if strings.TrimSpace(line) == "" {
			return fmt.Errorf("bwrap.extras[%d] 是空行：要么写一条完整参数，要么整行去掉", n)
		}
		// One line is one complete argument group, so the tokens bwrap will
		// actually see are the line split on whitespace — the same split Build
		// does, spelled the same way, or this check would be inspecting a
		// string that never reaches bwrap.
		for _, tok := range strings.Fields(line) {
			if err := CheckBwrapToken(tok); err != nil {
				return fmt.Errorf("bwrap.extras[%d] %q: %w", n, line, err)
			}
		}
	}
	return nil
}

// bwrapFDTokens are the bubblewrap options that consume an already-open file
// descriptor. The launcher is exec'd by the desktop environment and owns no
// descriptors beyond stdin/stdout/stderr, so any of these would fail at launch
// time — the worst place to find out.
var bwrapFDTokens = map[string]bool{
	"--args": true, "--file": true, "--bind-data": true, "--ro-bind-data": true,
	"--bind-fd": true, "--ro-bind-fd": true, "--seccomp": true, "--add-seccomp-fd": true,
	"--block-fd": true, "--userns-block-fd": true, "--userns": true, "--userns2": true,
	"--pidns": true, "--sync-fd": true, "--info-fd": true, "--json-status-fd": true,
}

// CheckBwrapToken refuses the three token classes that must never reach the
// passthrough region (设计 §2.4). The check reads no arity: it looks at the
// option name only, and accepts the `--opt=value` spelling.
func CheckBwrapToken(tok string) error {
	name := tok
	if idx := strings.IndexByte(tok, '='); idx >= 0 {
		name = tok[:idx]
	}
	switch {
	case name == "--":
		return fmt.Errorf("`--` would truncate bubblewrap's own option parsing: everything after it " +
			"becomes the program name. twin already owns the one `--` before the program")
	case name == "--argv0":
		return fmt.Errorf("--argv0 would break the identity invariant: the window app_id follows the real " +
			"path name while the tray SNI Id follows argv[0], and twin keeps them equal on purpose " +
			"(设计 §2.4) — changing argv[0] guarantees one of the two finds no desktop entry")
	case bwrapFDTokens[name]:
		return fmt.Errorf("%s needs an already-open file descriptor, and the launcher — exec'd by the desktop "+
			"environment — does not have one", name)
	}
	return nil
}

// ------------------------------------------------------------ serialisation

// toTree renders the instance as the documented YAML layout.
//
// Every key carries the note a human needs in order to decide whether to touch
// it, because this file *is* the instance's documentation: `twin gen` prints the
// path and stops, and the terminal no longer repeats any of this.
func (i *Instance) toTree() (*yamlz.Tree, error) {
	t := yamlz.New()
	t.SetComment("id", "实例 id：桌面项基名、别名文件名、这个目录名，三者都是它。")
	t.Set("id", i.ID)

	src := t.Ensure("source")
	src.SetComment("exec", "真实程序的路径。可以绝对、~/ 开头、或相对 —— 相对时基准是这份文件所在的目录。")
	src.Set("exec", i.Target)
	src.SetComment("desktop", "源桌面项：生成下面那份正文时从这里取显示名、图标与分类等（留空也能生成，只是\n"+
		"什么都要自己填）。")
	if i.Source != "" {
		src.Set("desktop", i.Source)
	}
	src.SetComment("args", "程序的静态参数，一行一条完整命令（按空格拆开），原样接在程序路径之后、桌面环境给的\n"+
		"文件/URL 之前，不加任何引号：`--ozone-platform=wayland`、`--user-data-dir=$HOME/x`（`~` 与\n"+
		"`$HOME` 由启动它的 shell 展开）。源桌面项 Exec 里那些参数不会自动搬过来（`twin inspect` 会列出）。")
	if len(i.Args) == 0 {
		src.Set("args", []string{})
	} else {
		src.Set("args", i.Args)
	}

	// What twin is overriding, in one node. The entry's *text* is not five keys
	// here but a file of its own: it is the part of a wrap a user rewrites
	// rather than reads, and a pure-text file is where rewriting is pleasant.
	// These keys are the decisions twin makes *about* that file.
	ov := t.Ensure("override")
	ov.SetComment("desktop", "桌面项**正文**所在的文件（相对路径的基准同上，`gen` 在本文件里写的就是与它同\n"+
		"目录的 <id>.desktop）。文件不在时由 `twin install` 按源桌面项生成一份（Exec / Icon /\n"+
		"StartupWMClass 都填好）—— 改菜单里那几个字就是改它，然后 `twin reinstall <id>`。")
	ov.Set("desktop", i.DesktopOverride)
	ov.SetComment("wm_class", "生成正文时写进 StartupWMClass 的值，留空 = 用 id。别依赖它：Wayland 下认领窗口的\n"+
		"是别名基名，这一行只对 X11/XWayland 有意义；而且正文生成过一次之后，改这里不再有影响。")
	ov.Set("wm_class", i.WMClass)
	ic := ov.Ensure("icon")
	ic.SetComment("source", "源图标：png/jpeg/svg 路径，或图标主题里的名字。路径写法同 source.exec。")
	if i.IconSource != "" {
		ic.Set("source", i.IconSource)
	}
	ic.SetComment("badge", "图标上的角标文字（style = dot 不用它）。纯数字要加引号，否则会被读成数字。")
	if i.Badge != "" {
		ic.Set("badge", i.Badge)
	}
	ic.SetComment("style", "角标画法：corner 圆角方块 / number 圆形 / dot 小圆点（不带文字）；\n"+
		"三种都贴齐图标右下角，图标自己是圆角也照贴。")
	ic.Set("style", i.BadgeStyle)
	ov.SetComment("tray", "让不让 twin 管这个实例的托盘图标：false = 托盘还是程序自己的（有些程序的托盘\n"+
		"id twin 预测不了）。整机开关在 config.toml 的 defaults.tray_override，两处任一关掉都写 false。\n"+
		"走哪条路是这台机器的事：助手扩展被加载 ⇒ 写它读的文件表，否则写宿主 custom-icons 表兜底。")
	ov.Set("tray", i.TrayOverride)

	iso := t.Ensure("isolation")
	iso.SetComment("level", "none = 只写桌面项与图标、程序原样跑（连 bwrap 都不过）；alias = bwrap overlay 零拷贝改名（主力）；\n"+
		"redirect = 同 alias 改名，另加一条 L5 规则（~/.config/twin/rules.d/<id>.json）给 shell 扩展，\n"+
		"由它把窗口改判回本实例 —— 给把 app_id 写死在代码里的程序用。")
	iso.Set("level", string(i.Level))
	iso.SetComment("unshare", "额外 unshare 的命名空间；含 `net` 会被拒绝 —— 它切断网络，也打破\n"+
		"基于 abstract socket 的单实例锁。")
	if len(i.Unshare) == 0 {
		iso.Set("unshare", []string{})
	} else {
		iso.Set("unshare", i.Unshare)
	}
	// Written unconditionally. The machine-scoped half of this decision lives
	// in config.toml, so a reader of this file must be able to tell "this
	// program needs no trampoline" from "this file predates the field".
	iso.SetComment("nested_ns", "这个程序自己要不要建嵌套 user namespace（Chromium / Electron 类）；\n"+
		"用哪个跳板是机器属性，留在 config.toml 里。")
	iso.Set("nested_ns", i.NestedNS)

	// The invocation, all of it. Four keys, because a bwrap command line is four
	// kinds of thing once the root is counted on its own: the surface the
	// program runs on (`root`), mounts with two ends (`bind`), environment
	// (`env`), and everything else as the option lines themselves (`extras`).
	// The base is not in Go anywhere — `gen` writes it into these same keys
	// (see sandbox.BaseFor) — so what the program gets can be read here, and
	// which root it gets is a key in this file rather than a constant.
	bw := t.Ensure("bwrap")

	// First, and written unconditionally: it is the first mount of the command
	// and the one decision every reader looks for. "Empty" would have to mean
	// something — either "no root" (a sandbox where the program cannot find
	// itself) or "the default one" — and both readings are worse than writing
	// the value out (see inst.RootBind and Check, which refuses a definition
	// that says the root twice).
	bw.SetComment("root", "程序运行在哪个根下：ro = --ro-bind / /（整个宿主根只读，代价是设备与 /run/user\n"+
		"要自己用 extras 补回来），rw = --bind / /，dev = --dev-bind / /（带设备访问，`gen` 写的就是它）。\n"+
		"发在其余所有挂载之前（这一条的位置不由文件决定），整个沙箱里只有这一个根。")
	if i.Root == "" {
		bw.Set("root", "")
	} else {
		bw.Set("root", i.Root)
	}

	// Written unconditionally, empty case included. "twin redirected nothing" is
	// the default decision, and once the terminal stopped printing it this
	// comment became the only place the shared-state warning can live — so the
	// key has to be there even when there is nothing to list.
	bw.SetComment("bind", "隔离哪个数据目录（整段替换内置规则）：`源:目标[:ro|:rw|:dev][:try]`，同 Docker 的卷（:ro 只读、:rw 可读写、\n"+
		":dev 带设备访问，单写一条路径则挂回自己；末尾的 :try 要写全模式，源不在就整条跳过、不报错）。\n"+
		"根不在这里 —— 它是 `root` 那个键。空 = 数据目录与源程序共用。")
	binds := make([]string, 0, len(i.Binds))
	for _, b := range i.Binds {
		binds = append(binds, b.String())
	}
	bw.Set("bind", binds)

	bw.SetComment("env", "注入沙箱的环境变量（bwrap --setenv），一条一个 `键=值`，后面的同名者胜；等号只在\n"+
		"第一个处断开，所以值里可以再有等号与冒号（PATH=/a:/b）。`gen` 写的 TWIN_INSTANCE=<id> 在这一条里；\n"+
		"留空 = 一个都不注入。")
	env := make([]string, 0, len(i.Env))
	for _, e := range i.Env {
		env = append(env, e.String())
	}
	bw.Set("env", env)

	// Last of the three, and last in the command too: it is emitted after every
	// mapping, which is what `--remount-ro` needs (it only bites on a path that
	// is itself a mount, so it has to come after the mount it tightens). The
	// price is paid in the one place it shows: a `--tmpfs /tmp` written *here*
	// hides a `/tmp:/tmp` written in `bind`, so "give this program the host's
	// /tmp" is said by writing the tmpfs line you want instead — not by
	// out-mounting it afterwards.
	bw.SetComment("extras", "交给 bwrap 的其余全部参数，一行一条完整命令（选项和值同一行，按空格拆开）：`--proc /proc`、\n"+
		"`--tmpfs /dev/shm`、`--remount-ro /路径`、`--die-with-parent`。合并后的完整列表：基座 → config.toml 的\n"+
		"[bwrap] args → rules.d 的 [bwrap] args → gen 的 `--` 之后那一段；发在 bind 之后、env 之前，twin 不解析它。")
	if len(i.Extras) == 0 {
		bw.Set("extras", []string{})
	} else {
		bw.Set("extras", i.Extras)
	}

	return t, nil
}

// Role says which of the two jobs a rendered instance definition is doing.
//
// The ordinary case is one file: `twin gen` writes it into the instance's own
// directory, and `twin install` / `twin reinstall` both read that very file
// back — so the header there has to name commands that read *it*, not a copy of
// it. The archive role exists for the other arrangement: when install was
// pointed at a definition living somewhere else (-p ~/wraps/x.yml), the
// instance directory needs a record of what was installed, and that record is
// what reinstall replays. Naming the wrong command in either header would send
// a reader to something that quietly ignores the file they are looking at,
// which is the failure mode this whole design is arranged against.
type Role int

const (
	// RoleConfig is the definition the user works in: `twin gen` writes it,
	// `twin install` and `twin reinstall` read it.
	RoleConfig Role = iota
	// RoleSnapshot is the record install leaves in the instance directory when
	// the definition it read lives elsewhere. `twin reinstall` replays it.
	RoleSnapshot
)

// Render returns the document as the instance definition, at the place its id
// says it lives.
func (i *Instance) Render() ([]byte, error) { return i.RenderAs(RoleConfig) }

// RenderAs returns the document with the header for the given role.
func (i *Instance) RenderAs(role Role) ([]byte, error) { return i.render(role, "") }

// RenderAt is Render for a definition that is *not* at the instance's own place
// (`twin gen -o <文件>`). The header has to name `twin install -p <path>`, because
// a bare `twin install <id>` reads a different file entirely — the record in the
// instance directory — and a header pointing there would send the reader to a
// document that is not the one in their editor. That is the one failure mode
// this header exists to prevent.
func (i *Instance) RenderAt(path string) ([]byte, error) { return i.render(RoleConfig, path) }

func (i *Instance) render(role Role, elsewhere string) ([]byte, error) {
	t, err := i.toTree()
	if err != nil {
		return nil, err
	}
	// Only the header differs between the roles; the keys and their notes are
	// rendered once, so a key cannot be documented one way in the file the user
	// edits and another way in the file install archives.
	head := "# twin —— 实例定义。改完跑 `twin install " + i.ID + "` 按它落盘，" +
		"`twin reinstall " + i.ID + "` 重放的就是它。\n"
	if elsewhere != "" {
		head = "# twin —— 实例定义。改完跑 `twin install -p " + elsewhere + "` 按它落盘，" +
			"`twin reinstall " + i.ID + "` 重放的是实例目录里那份记录。\n"
	}
	if role == RoleSnapshot {
		head = "# twin —— 实例定义（存档记录）。`twin install` 按你指定的那份配置写出它，" +
			"改完跑 `twin reinstall " + i.ID + "` 重放。\n"
	}
	header := head +
		"# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`override.icon.badge` 那种纯数字尤其要）。\n" +
		"# 每个键的说明写在它自己上面 —— 终端不再重复一遍。\n"
	return append([]byte(header), yamlz.Encode(t)...), nil
}

// Load reads an instance definition from disk.
//
// Paths written in the file may be relative, and they are resolved here — once,
// against the directory the file itself is in. One base, resolved at the single
// point where the file meets the machine, so that every reader downstream
// (bwrap arguments, mount-point creation, icon lookup, verify) sees the same
// absolute path and none of them can invent a base of its own: a *reader* that
// resolved relative paths would be resolving them against whatever directory
// the user happened to run the command from.
//
// The file is never written back in the resolved form: what stays on disk is the
// spelling the user chose, which is what lets a bundle (the definition, the
// program and its state side by side) be moved or committed as a unit.
func Load(path string) (*Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := yamlz.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	base := filepath.Dir(path)
	// The sections that moved, refused by name. Each of these is a whole node
	// whose keys live somewhere else now, and every one of them would read as
	// "not asked for" if it were merely ignored: a definition whose entry text
	// went missing is a definition that installs half an instance, and the one
	// whose tmpfs went missing hands the program a writable /tmp it was told to
	// hide. Regenerating is one command, so the message is where to go rather
	// than an attempt to read the old shape (a second reader is a second answer).
	if root.Has("exec") {
		return nil, fmt.Errorf("%s: `exec` 这一节拆开了：真实程序现在是 `source.exec`；"+
			"`exec.alias` 与 `exec.argv0` 都取消了 —— 别名永远是 id（窗口 app_id 与托盘 SNI Id 都等于它）。"+
			"`exec.env` 换到了 `bwrap.env`，而且换成了列表：一条一个 `键=值`（等号只在第一个处断开，"+
			"所以值里可以再有等号与冒号）。请用 `twin gen` 重新生成一份", path)
	}
	if root.Has("desktop") {
		return nil, fmt.Errorf("%s: `desktop` 这一节拆成了两处：源桌面项是 `source.desktop`，"+
			"而桌面项的**正文**（显示名、说明、关键词、分类）现在装在一份纯文本文件里，由 `override.desktop` 指出 —— "+
			"install 缺了就生成它、之后逐字复制它，改它比改这里的五个键更直接。请用 `twin gen` 重新生成一份", path)
	}
	if root.Has("identity") {
		return nil, fmt.Errorf("%s: `identity` 这一节拆开了：`level` 挪到 `isolation.level`，"+
			"`wm_class` 挪到 `override.wm_class`，`notify` 取消（StartupNotify 总是 true，写不写它本来就是一个决定）。"+
			"把 level 那两行缩进到 `isolation:` 下面即可", path)
	}
	if root.Has("icon") {
		return nil, fmt.Errorf("%s: `icon` 挪到了 `override.icon` 下面 —— "+
			"`override` 这一节收集的是「twin 覆盖了什么」；把这一段整体缩进到 `override:` 下面即可", path)
	}
	if root.Has("tray") {
		return nil, fmt.Errorf("%s: `tray.override` 挪到了 `override.tray`（同一个键、同一个意思）；"+
			"把这一行缩进到 `override:` 下面即可", path)
	}
	for _, key := range []string{"name", "name_en", "comment", "comment_en", "keywords", "categories"} {
		if root.Has(key) {
			return nil, fmt.Errorf("%s: `%s` 这类文案现在写在 `override.desktop` 指的那份文件里（纯文本桌面项）"+
				"—— 那份文件由 install 生成第一份，此后 install 每次逐字复制它；直接改它比在配置里改一行更接近最终结果",
				path, key)
		}
	}
	for _, key := range []string{"proc", "tmpfs", "remountRO"} {
		if root.At("bwrap").Has(key) {
			return nil, fmt.Errorf("%s: `bwrap.%s` 没有了 —— 这一类参数现在都在 `bwrap.extras` 里，"+
				"一行一条完整命令（例如 `--tmpfs /dev/shm`、`--remount-ro /路径`）。extras 发在所有挂载之后，"+
				"`--remount-ro` 因此仍然在它收紧的那条挂载后面。请用 `twin gen` 重新生成一份", path, key)
		}
	}
	// The old keys are refused by name rather than read as "no option list".
	// Neither rename is a spelling change: `extra` was only what the user
	// appended, and `args` is the whole list including the sandbox base that twin
	// used to keep to itself. Reading any of them as the other would hand bwrap a
	// namespace with no root mounted and no hint of why — the program would
	// simply not start, with nothing anywhere naming twin. (Same reasoning as the
	// instance.toml error below: an instance twin cannot read is reported, never
	// silently dropped.)
	if root.At("bwrap").Has("extra") {
		return nil, fmt.Errorf("%s: `bwrap.extra` 现在叫 `bwrap.extras`，而且形状变了 —— "+
			"它是交给 bwrap 的**完整**参数列表（各层合并后的样子），一行一条完整命令（`--proc /proc`），"+
			"不是逐 token 排开。请用 `twin gen` 重新生成一份，再把你原来的参数补进去", path)
	}
	if root.At("bwrap").Has("args") {
		return nil, fmt.Errorf("%s: `bwrap.args` 现在叫 `bwrap.extras`（同一个键、同一个意思，只是改了名字）。"+
			"把 `args:` 改成 `extras:` 即可，注意每条要写成一整条命令（`--proc /proc`，不是 `--proc` + `/proc`）", path)
	}
	ov := root.At("override")
	it := &Instance{
		ID:         root.Str("id"),
		Target:     resolvePath(base, root.At("source").Str("exec")),
		Source:     resolvePath(base, root.At("source").Str("desktop")),
		Level:      Level(root.At("isolation").Str("level")),
		Unshare:    root.At("isolation").StrList("unshare"),
		NestedNS:   root.At("isolation").Bool("nested_ns"),
		Extras:     root.At("bwrap").StrList("extras"),
		WMClass:    ov.Str("wm_class"),
		IconSource: resolvePath(base, ov.At("icon").Str("source")),
		Badge:      ov.At("icon").Str("badge"),
		BadgeStyle: ov.At("icon").Str("style"),
		// The desktop entry's text is a path like any other path in this file,
		// and it resolves the same way: against the definition's own directory,
		// which is what makes a bundle (definition + entry text + program)
		// movable as a unit — and what Load and `gen` have to agree on, or gen
		// would write one file and install would copy another.
		DesktopOverride: resolvePath(base, ov.Str("desktop")),
	}
	// `source.args` is read strictly, like `bwrap.bind` and `bwrap.env`: a
	// single string written where a list belongs (the shape a hand-edit takes)
	// would otherwise come back as "no arguments at all", and the only symptom
	// would be a program that starts without the one switch this instance
	// exists for.
	args, err := root.At("source").StrListStrict("args")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	it.Args = args
	// `isolation.tmpfs` and `exec.env` both moved under `bwrap` for the reason
	// the shaped mounts did: each one is a bwrap option, and the node that says
	// "this is what bwrap is called with" should be the only place they live.
	// `isolation.tmpfs` went one step further and stopped being a key at all —
	// a `--tmpfs` line in the same list as every other option is the same mount,
	// with its position readable. Both are refused by name rather than read as
	// aliases: two keys emitting one argument is how a file starts disagreeing
	// with itself, and the .env one also changed shape (list of `键=值`, not a
	// mapping).
	if root.At("isolation").Has("tmpfs") {
		return nil, fmt.Errorf("%s: `isolation.tmpfs` 和它的继任者 `bwrap.tmpfs` 都没有了 —— "+
			"`--tmpfs` 现在写在 `bwrap.extras` 里，一行一条（`--tmpfs /tmp`）；"+
			"它在 extras 里的位置就是命令里的位置，所以想让它被别的挂载压住/压住别人，写对行序即可", path)
	}
	// There is no `exec.env` check of its own: the whole `exec` node was refused
	// above, and its message already says where `env` went (`bwrap.env`). A
	// second check for one key inside a node twin will not read at all could
	// only ever be dead code — `root.Has("exec")` is true for any node shape.
	envSpecs, err := root.At("bwrap").StrListStrict("env")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, spec := range envSpecs {
		e, err := ParseEnvVar(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		it.Env = append(it.Env, e)
	}
	// The root, read as the word it is: `ro` / `rw` / `dev` are the bind modes,
	// and which of them this file names is checked in Check (so a bad value is
	// reported by the same code that reports every other semantic failure, not
	// by the parser).
	it.Root = root.At("bwrap").Str("root")

	// `bind` lives under `bwrap`: it is one more thing that ends up in the same
	// invocation as `extras`, which is written right there. A top-level `bind` is
	// refused rather than ignored — the two readings do not describe the same
	// instance (one mounts the program's data, the other mounts nothing), and
	// the failure would surface as "the program cannot see its files" long after
	// the edit.
	if root.Has("bind") {
		return nil, fmt.Errorf("%s: `bind` 现在在 `bwrap` 节点里（bwrap.bind）—— 它和 bwrap.extras 一起"+
			"决定这次 bwrap 调用拿到什么，所以搬到了同一个节点下；把这一段缩进到 `bwrap:` 下面即可", path)
	}
	specs, err := root.At("bwrap").StrListStrict("bind")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, spec := range specs {
		b, err := ParseBind(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		// Both sides take the same base. The right side is a path in the
		// sandbox, and a relative one lands next to the definition like any
		// other — which is coherent, since `~` on that side is likewise turned
		// into a path under the user's home and not into something bwrap would
		// interpret on its own (bwrap resolves a relative argument against the
		// *launcher's* working directory, an answer nobody chose).
		b.Source = resolvePath(base, b.Source)
		b.Target = resolvePath(base, b.Target)
		it.Binds = append(it.Binds, b)
	}
	if it.BadgeStyle == "" {
		it.BadgeStyle = "corner"
	}
	// Absent means true: a definition that predates the key wanted the tray
	// override, and flipping it to false would silently stop maintaining
	// something that is already in the host's table.
	it.TrayOverride = true
	if ov.Has("tray") {
		it.TrayOverride = ov.Bool("tray")
	}
	if it.Level == "" {
		it.Level = LevelAlias
	}
	return it, nil
}

// LoadProfile reads one instance's snapshot, and turns "there is no
// profile.yml here" into an explanation when the pre-YAML file is what is
// actually sitting there. A missing file would otherwise read as "this instance
// never existed", which is a lie the user cannot see through.
func LoadProfile(p Paths, id string) (*Instance, error) {
	it, err := Load(p.Profile(id))
	if err == nil {
		return it, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if _, statErr := os.Stat(p.LegacyProfile(id)); statErr == nil {
		return nil, legacyProfileError(p, id)
	}
	return nil, err
}

// legacyProfileError is the one thing to say about an instance.toml: this
// version moved the snapshot to YAML, and the old parser is gone rather than
// kept around as a second code path that could disagree with the new one.
func legacyProfileError(p Paths, id string) error {
	return fmt.Errorf("%s 还是旧的 TOML 快照（这个版本起它叫 profile.yml，YAML 格式）："+
		"请把内容改写成 YAML 存到 %s（键名不变），或者 twin remove %s 之后重新 twin gen",
		p.LegacyProfile(id), p.Profile(id), id)
}

// List returns every instance found under paths, sorted by id.
func List(p Paths) ([]*Instance, error) {
	dir := filepath.Join(p.Home, ".local/share/twin")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Instance
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Not every directory here is an instance. `bin/` holds the shared
		// bwrap copy and bootstrap script (see Paths.BinDir), and a directory
		// without a snapshot is not an instance either — a half-written one
		// has nothing to load and nothing to say. Treating them as broken
		// instances made `twin list` fail on every machine that had ever
		// wrapped a Chromium program.
		path := p.Profile(e.Name())
		if _, err := os.Stat(path); err != nil {
			// No profile.yml. A pre-YAML instance.toml is a *different* thing:
			// a real instance this version cannot read, so it is reported
			// instead of skipped — silently dropping it would make the
			// instance disappear from `list` with no explanation.
			if _, legacyErr := os.Stat(p.LegacyProfile(e.Name())); legacyErr == nil {
				return nil, legacyProfileError(p, e.Name())
			}
			continue
		}
		it, err := Load(path)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}
