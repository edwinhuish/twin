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
	// root a program runs under stopped being twin's constant: the root mount
	// itself is `/:/:dev` (that *is* `--dev-bind / /`), and a definition that
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
}

// IsRootMount reports whether this entry is the base's root mount: the whole
// host root bound onto itself — `/:/:dev`, what `twin gen` writes, or `/:/:ro`
// when a definition tightens it.
//
// It is recognised by shape rather than by a flag of its own, and three places
// depend on that: Build emits it *before* every other mount (it is the surface
// the rest is laid on — bound after the alias overlay it puts the host's
// directory back over the copy the program is supposed to run from), the
// reports that read `bind` as "the program's data directories" leave it out
// (the root is not one of them), and the launcher's mount points skip it (both
// of its ends are `/`, which is already there).
func (b Bind) IsRootMount() bool { return b.Source == "/" && b.Target == "/" }

// String renders the one-line form the config files carry.
func (b Bind) String() string {
	s := b.Source + ":" + b.Target
	switch {
	case b.ReadOnly:
		s += ":ro"
	case b.Dev:
		s += ":dev"
	}
	return s
}

// ParseBind reads the one-line form: "source:target", "source:target:ro",
// "source:target:rw", "source:target:dev", or a bare "path" meaning the same
// directory both ways.
//
// The bare form is not an optimisation. A directory bound onto itself is what
// upgrades it to writable in place under `--ro-bind / /`, which is the one
// mapping WeChat's received-files directory has always needed.
//
// The split is Docker's: a trailing ":ro"/":rw"/":dev" is the mode, then the
// first colon separates the two sides. A path that itself contains a colon is
// therefore ambiguous, and — as in Docker — the ambiguity is resolved the
// documented way rather than guessed at.
func ParseBind(spec string) (Bind, error) {
	parts := strings.Split(strings.TrimSpace(spec), ":")
	if len(parts) == 2 && isBindMode(parts[1]) {
		// `x:ro` has no target, only what looks like a mode. Reading it as
		// "target named ro" would build a relative mount point out of a typo,
		// so it is refused instead. `:dev` lands here too — and the advice below
		// covers it, since `x:x:dev` is exactly how a device path bound onto
		// itself is written.
		return Bind{}, fmt.Errorf("路径映射 %q 的右侧是访问模式而不是目标路径："+
			"两侧写全（%s:%s:%s），或去掉模式（同一路径默认可写）", spec, parts[0], parts[0], parts[1])
	}
	b := Bind{}
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
		return Bind{}, fmt.Errorf("路径映射 %q 的冒号太多（形如 \"源:目标[:ro|:rw|:dev]\"）；"+
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

// EnvVar is one `--setenv` pair, written on one line as "键:值".
//
// It is a pair rather than a YAML mapping because the file's job is to say what
// bwrap will be called with, and `--setenv KEY VALUE` is two arguments: writing
// `键: 值` would put a second, differently-shaped spelling of the same thing in
// the same file. The split is at the *first* colon, so a value may contain as
// many as it likes (`PATH:/a:/b`, a URL) — the same rule `bind` uses, and for
// the same reason.
type EnvVar struct {
	Key   string
	Value string
}

// String renders the one-line form the definition carries.
func (e EnvVar) String() string { return e.Key + ":" + e.Value }

// ParseEnvVar reads the one-line form: "键:值". The value may be empty (a
// program that treats "set but empty" differently from "unset" needs that),
// which is why the colon is not optional.
func ParseEnvVar(spec string) (EnvVar, error) {
	key, value, ok := strings.Cut(strings.TrimSpace(spec), ":")
	if !ok {
		return EnvVar{}, fmt.Errorf("环境变量 %q 少了冒号：写成 `键:值`（值可以是空的，键不行）", spec)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return EnvVar{}, fmt.Errorf("环境变量 %q 没有键名：写成 `键:值`", spec)
	}
	// A key with a space in it would reach bwrap as one argument that no
	// program can look up; `=` is here because KEY=VALUE is the shell spelling
	// and someone will write it — silently exporting nothing is worse than a
	// refused line.
	if strings.ContainsAny(key, " \t=") {
		return EnvVar{}, fmt.Errorf("环境变量 %q 的键名里有空格或 =：键名得是一个词，写法是 `键:值`", spec)
	}
	return EnvVar{Key: key, Value: value}, nil
}

// Instance is one wrapped program.
type Instance struct {
	ID        string
	Name      string
	NameEn    string
	Comment   string
	CommentEn string
	// Keywords and Categories override what the source entry provides. Empty
	// means "inherit from the source": the hand-written 微信2 added its own
	// values because the real wechat.desktop carries neither.
	Keywords   string
	Categories string
	Source     string // source .desktop file, informational

	Level   Level
	WMClass string
	Notify  bool

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

	// TrayOverride says whether this instance asks twin to point the host's
	// tray override table at its own icon. It is true by default and is only
	// interesting when false: the table is global, so a program whose tray id
	// twin cannot predict (level=none) or a machine whose tray host is
	// something else leaves it alone.
	TrayOverride bool

	IconSource string
	Badge      string
	BadgeStyle string

	Target string // absolute path of the real program
	Alias  string // file name created next to Target inside the sandbox
	Argv0  string // reserved; empty means "let argv[0] default to the alias"

	// The bwrap mounts that have a shape of their own, as opposed to the raw
	// token list in Args. They are separate keys because each one is a *kind* of
	// mount — a proc, a throwaway tmpfs, a path re-tightened to read-only —
	// which is exactly what `bind`'s `:ro` and `:dev` say for a mapping, and
	// nothing in a flat token list can say it. Empty means "not asked for": an
	// empty one emits no argument at all, so a reader can tell a mount they
	// deleted from a mount twin never made.
	Proc      string   // `--proc DEST`; empty = no proc mount at all
	Tmpfs     []string // `--tmpfs DEST` each: hidden behind an empty directory
	RemountRO []string // `--remount-ro DEST` each: re-tightened to read-only
	// Env is `--setenv` pairs, in the order written (later pairs win, as bwrap
	// does it). The instance's own TWIN_INSTANCE is one of them: `twin gen`
	// writes it here, because it is a variable twin sets and has no business
	// being invisible in the definition.
	Env []EnvVar

	Unshare []string
	// Args is the bubblewrap option list, verbatim, in the order written. twin
	// adds nothing ahead of it — the sandbox base lives in the file, put there
	// by `twin gen` and readable, because which root a program needs is a
	// property of that program. It used to be hard-coded here as `--ro-bind / /`:
	// a program that has to touch devices then had no way to ask for
	// `--dev-bind / /`, and asking for it through the old passthrough region put
	// it *after* every mapping, where binding the root again shadows all of them.
	//
	// What is left in it is what has no node of its own: the host's /tmp when
	// that is the branch, and --die-with-parent. Everything else the base carries
	// is written into the key that names its kind (Binds / Proc / Tmpfs / Env),
	// where a reader can see what a mount *is* without knowing bubblewrap's
	// options. It is still emitted ahead of every shaped mount; the one thing
	// that goes before it is the root, which is a mapping itself now (see
	// Bind.IsRootMount).
	//
	// twin never parses or rewrites these tokens (设计 §2.1): the arity of a
	// bwrap option is bwrap's business, and re-deriving it here would be a table
	// that rots on every bwrap release.
	Args []string

	// Binds is `--bind` / `--ro-bind` / `--dev-bind`, in the order written. The
	// first entry `twin gen` writes is the root mount (`/:/:dev`); Build emits it
	// before every other mount wherever it sits in the file, and the reports that
	// read this list as "the program's data directories" leave it out — see
	// Bind.IsRootMount.
	Binds []Bind
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
// YAML because the schema is nested (identity / exec / isolation / bwrap) and a
// nested file reads as a hierarchy instead of as a wall of headers.
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

// resolvePaths is resolvePath over a list, preserving nil (nil means "no list",
// and [] means "an empty one" — the two are different answers in profile.yml).
func resolvePaths(base string, list []string) []string {
	if list == nil {
		return nil
	}
	out := make([]string, len(list))
	for n, s := range list {
		out[n] = resolvePath(base, s)
	}
	return out
}

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

// EffectiveAlias is the file name the program is exec'd through. It is the
// single value that both identity channels must agree on.
func (i *Instance) EffectiveAlias() string {
	if i.Alias != "" {
		return i.Alias
	}
	return i.ID
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

// AliasPath is the absolute path of the renamed entry point.
func (i *Instance) AliasPath() string {
	return filepath.Join(i.TargetDir(), i.EffectiveAlias())
}

// hasRootMount reports whether anything in the file asks for a root. The `bind`
// side is exact — `/:/` is the root by shape — while the `args` side can only
// be a proxy, because twin knows no bwrap option's arity and must not start
// parsing them. Every definition `gen` writes keeps its root in `bind`, so what
// this really guards is a file that lost its base altogether.
func (i *Instance) hasRootMount() bool {
	for _, b := range i.Binds {
		if b.IsRootMount() {
			return true
		}
	}
	return len(i.Args) > 0
}

// Check performs the semantic validation the generator relies on. Every
// failure here maps to a symptom we have actually seen fail silently.
func (i *Instance) Check() error {
	if err := ValidateID(i.ID); err != nil {
		return err
	}
	if i.Target == "" {
		return fmt.Errorf("exec.target is empty (nothing to run)")
	}
	if !filepath.IsAbs(i.Target) {
		return fmt.Errorf("exec.target %q must be an absolute path", i.Target)
	}
	if !i.Level.Implemented() {
		return fmt.Errorf("identity.level %q is not implemented yet (only %s)", i.Level, "none | alias | redirect")
	}
	alias := i.EffectiveAlias()
	if err := ValidateID(alias); err != nil {
		return fmt.Errorf("exec.alias: %w", err)
	}
	if alias == i.TargetBase() {
		return fmt.Errorf("exec.alias %q equals the real file name — that is a no-op, pick a different name", alias)
	}
	if i.Level.Renames() && i.Argv0 != "" && i.Argv0 != alias {
		return fmt.Errorf("exec.argv0 %q must equal exec.alias %q: "+
			"the window app_id follows the real path while the tray id follows argv[0], "+
			"so a mismatch guarantees one of them finds no desktop entry", i.Argv0, alias)
	}
	for _, u := range i.Unshare {
		if u == "net" {
			return fmt.Errorf("isolation.unshare must not contain \"net\": it cuts the network and blocks " +
				"abstract-socket single-instance locks, which bwrap cannot isolate anyway")
		}
	}
	for n, b := range i.Binds {
		if b.Source == "" || b.Target == "" {
			return fmt.Errorf("bwrap.bind[%d] %q: 源和目标是两条路径，都要写", n, b)
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
	// The shaped mounts. An empty value means "not asked for" and emits nothing,
	// so only a *present but empty* path is an error — that one reaches bwrap as
	// an empty argument, which it reads as a relative path with no name.
	for n, dest := range i.RemountRO {
		if strings.TrimSpace(dest) == "" {
			return fmt.Errorf("bwrap.remountRO[%d] 是空的：要收紧哪条路径就写哪条，不然整条去掉", n)
		}
	}
	for n, e := range i.Env {
		// Round-tripping the one-line form is the whole check: a key carrying a
		// colon (or a space, or an =) cannot be told from the value when the
		// pair is written back out, and a file that reads back as something else
		// is the failure this project refuses to ship.
		if got, err := ParseEnvVar(e.String()); err != nil || got != e {
			return fmt.Errorf("bwrap.env[%d] %q 不是「键:值」—— 键名不能带冒号、空格或 =（值里可以有冒号）", n, e)
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
	// The root can be said in two places, so both are read: the base writes it
	// into `bind` as `/:/:dev` (a mount belongs in the node for mounts), while a
	// hand-written or older definition keeps `--dev-bind / /` in `args`. Empty
	// `args` on its own is therefore fine now — that is exactly what a
	// definition whose root is in `bind` looks like.
	if i.Level.Renames() && !i.hasRootMount() {
		return fmt.Errorf("这份定义没有根挂载：bwrap.bind 里没有 /:/:dev，bwrap.args 也是空的 —— " +
			"bwrap 至少要有一个根挂载（/:/:dev 或 --dev-bind / /），twin 不再自己补一个；" +
			"请用 `twin gen` 重新生成，或照它的注释手写一份")
	}
	for n, tok := range i.Args {
		if err := CheckBwrapToken(tok); err != nil {
			return fmt.Errorf("bwrap.args[%d]: %w", n, err)
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

	// The generated entry's text is one section, not seven keys at the root.
	// They all answer the same question — what does the .desktop say? — and
	// every one of them means "inherit it from the source" when left empty:
	// that is a group, and it reads as one. Spread out at the root they were
	// the widest thing in the file and the part a reader had to re-derive.
	dt := t.Ensure("desktop")
	dt.SetComment("name", "显示名（窗口标题与菜单里看到的）。name_en / comment_en 是可选英文版。")
	dt.Set("name", i.Name)
	dt.SetComment("name_en", "英文显示名。")
	if i.NameEn != "" {
		dt.Set("name_en", i.NameEn)
	}
	dt.SetComment("comment", "说明（菜单里那行副标题）。")
	if i.Comment != "" {
		dt.Set("comment", i.Comment)
	}
	dt.SetComment("comment_en", "英文说明。")
	if i.CommentEn != "" {
		dt.Set("comment_en", i.CommentEn)
	}
	dt.SetComment("keywords", "搜索关键词，`;` 分隔。留空则沿用源桌面项。")
	if i.Keywords != "" {
		dt.Set("keywords", i.Keywords)
	}
	dt.SetComment("categories", "桌面分类，`;` 分隔。留空则沿用源桌面项。")
	if i.Categories != "" {
		dt.Set("categories", i.Categories)
	}
	dt.SetComment("source", "源桌面项。`twin install` / `twin reinstall` 会重读它取 Categories / Keywords。")
	if i.Source != "" {
		dt.Set("source", i.Source)
	}

	id := t.Ensure("identity")
	id.SetComment("level", "none = 只写桌面项与图标、程序原样跑（连 bwrap 都不过）；alias = bwrap overlay 零拷贝改名（主力）；\n"+
		"redirect = 同 alias 改名，另加一条 L5 规则（~/.config/twin/rules.d/<id>.json）给 shell 扩展，\n"+
		"由它把窗口改判回本实例 —— 给把 app_id 写死在代码里的程序用。")
	id.Set("level", string(i.Level))
	id.SetComment("wm_class", "写进桌面项的 StartupWMClass，默认 = id，且只在 level 会改名时才写。\n"+
		"别依赖它：Wayland 下认领窗口的是别名基名，这一行只对 X11/XWayland 有意义。")
	if i.WMClass != "" {
		id.Set("wm_class", i.WMClass)
	}
	id.SetComment("notify", "写进桌面项的 StartupNotify=。默认 true，而且总是写出来（写不写本身就是一个决定）。")
	id.Set("notify", i.Notify)

	ic := t.Ensure("icon")
	ic.SetComment("source", "源图标：png/jpeg/svg 路径，或图标主题里的名字。路径可以绝对、可以 ~/ 开头，\n"+
		"也可以相对 —— 相对的话基准是这份 profile.yml 自己所在的目录。")
	if i.IconSource != "" {
		ic.Set("source", i.IconSource)
	}
	ic.SetComment("badge", "图标上的角标文字（style = dot 不用它）。纯数字要加引号，否则会被读成数字。")
	if i.Badge != "" {
		ic.Set("badge", i.Badge)
	}
	ic.SetComment("style", "角标画法：corner 右下角圆角方块 / number 右下角圆形 / dot 右下角小圆点（不带文字）。\n"+
		"三种都贴齐图标的右边缘与下边缘 —— 图标自己是圆角也照贴，角标不让位。")
	ic.Set("style", i.BadgeStyle)

	ex := t.Ensure("exec")
	ex.SetComment("target", "真实程序的路径。可以绝对、可以 ~/ 开头，也可以相对 ——\n"+
		"相对的话基准是这份 profile.yml 自己所在的目录（下同：icon.source、bwrap 里那几个路径键）。")
	ex.Set("target", i.Target)
	ex.SetComment("alias", "沙箱内建在 target 同目录下的别名文件名。窗口 app_id 取自真实路径名，\n"+
		"托盘 SNI Id 取自 argv[0]，两者都等于它 —— 让它们分叉，就有一个找不到桌面项。")
	if i.Level.Renames() {
		ex.Set("alias", i.EffectiveAlias())
	}
	ex.SetComment("argv0", "保留字段：留空即让 argv[0] 自动取 alias；填了就必须等于 alias。")
	if i.Argv0 != "" {
		ex.Set("argv0", i.Argv0)
	}

	iso := t.Ensure("isolation")
	iso.SetComment("unshare", "额外 unshare 的命名空间。含 `net` 会被拒绝：它切断网络，\n"+
		"也打破基于 abstract socket 的单实例锁。")
	if len(i.Unshare) == 0 {
		iso.Set("unshare", []string{})
	} else {
		iso.Set("unshare", i.Unshare)
	}
	// Written unconditionally. The machine-scoped half of this decision lives
	// in config.toml, so a reader of this file must be able to tell "this
	// program needs no trampoline" from "this file predates the field".
	iso.SetComment("nested_ns", "这个程序自己要不要建嵌套 user namespace（Chromium / Electron 类）。\n"+
		"它跟着程序走；用哪个跳板是机器属性，留在 config.toml 里。")
	iso.Set("nested_ns", i.NestedNS)

	// The option list, stored in the same flat-token shape the CLI and rules.d
	// use, so merging stays "concatenate the arrays" rather than "invent a
	// quoting rule" (设计 §3.2).
	//
	// The base is part of the file. What is left of it in this list is what names
	// no kind of mount and so has no key of its own — the host's /tmp and
	// --die-with-parent; the rest of it is written under `bind` / `proc` /
	// `tmpfs` / `env` below, where a reader can tell a throwaway tmpfs from a
	// mapping without knowing bubblewrap's options. twin used to keep the whole
	// base in Go and let this list only *append*, which made the one decision
	// that actually varies per program — which root the program sees — the one
	// decision the file could not express.
	bw := t.Ensure("bwrap")
	bw.SetComment("args", "交给 bwrap 的完整选项列表，逐 token 原样排列；twin 不解析它们（不猜每个选项的元数）。\n"+
		"它的位置：根挂载（下面 bind 里那条 /:/:dev）之后、其余所有挂载之前 —— 根是底座，必须在它下面；\n"+
		"而同目标挂载由后写的那个胜出，所以想覆盖它或别的映射，就写进这份列表靠后的位置。\n"+
		"`twin gen` 写进来的基座只剩下面这几条 —— 根挂载在 bind 里，其余各在下面与自己同类的键里：\n"+
		"  --bind /tmp /tmp 宿主那个 /tmp（可写）—— 程序走 X11、不要私有 /tmp 时才有这一条；\n"+
		"                   要私有的那种写法在 bwrap.tmpfs 里。它是「哪个根配哪个 /tmp」的一部分，\n"+
		"                   不是这个程序的数据映射，所以不写成 bwrap.bind 里的一行\n"+
		"  --die-with-parent\n"+
		"这里是合并后的最终列表，低优先级在前：上面这份基座 → config.toml → rules.d → gen 的 -- 透传区；\n"+
		"手写进来的照旧保留。空列表本身没问题（根挂载在 bind 里）；两种写法里都没有根挂载的定义会被拒绝。")
	if len(i.Args) == 0 {
		bw.Set("args", []string{})
	} else {
		bw.Set("args", i.Args)
	}

	// The shaped mounts, in the order Build emits them: the root (out of `bind`,
	// hoisted), then `args`, then these, then the rest of `bind`, then remountRO
	// (which has to come after whatever it tightens). The file reads
	// top-to-bottom like the command line it turns into, which is the only
	// reason these have keys of their own at all: each one names a *kind* of
	// mount that a flat token list cannot name — and that is where the base's own
	// mounts are written too, so the file says what each of them is (see
	// sandbox.BaseFor).
	//
	// All four are written even when empty, so that a reader discovers them
	// where they belong; an empty one emits no argument, which is what "not
	// asked for" has to mean for a knob as opposed to a decision (a decision is
	// something like `bind: []`, which says the program shares its data).
	bw.SetComment("proc", "挂一个 proc 到这条路径（bwrap --proc）。`gen` 写的是 /proc；留空 = 一个都不挂。\n"+
		"路径可以绝对、可以 ~/ 开头，也可以相对（基准是这份 profile.yml 所在的目录）。")
	if i.Proc == "" {
		bw.Set("proc", "")
	} else {
		bw.Set("proc", i.Proc)
	}

	bw.SetComment("tmpfs", "盖成空目录的路径（bwrap --tmpfs）：写后即弃，宿主看不见，\n"+
		"代价是那里原本的东西也一并看不见 —— 比如 X11 的 /tmp/.X11-unix/X0。\n"+
		"`gen` 写的是 /dev/shm（空的私有共享内存）与 /tmp（程序走 Wayland 时的私有 /tmp）；\n"+
		"只走 X11 的程序不能要后者，那时 `gen` 改成在 args 里写一条 --bind /tmp /tmp 把宿主那个挂回来\n"+
		"（两条路都保证 /tmp 可写，只读的 /tmp 会让程序在别处报错）。留空 = 不盖。")
	if len(i.Tmpfs) == 0 {
		bw.Set("tmpfs", []string{})
	} else {
		bw.Set("tmpfs", i.Tmpfs)
	}

	// Written unconditionally too, empty case included. "twin redirected
	// nothing" is the default decision, and once the terminal stopped printing
	// it this comment became the only place the shared-state warning can live —
	// so the key has to be there even when there is nothing to list.
	//
	// It lives under `bwrap` because that is what it becomes: more of the same
	// invocation the list above fills, emitted in the order written here. Two
	// keys that have to agree about one command line belong next to each other
	// in the file, not in two halves of it.
	bw.SetComment("bind", "隔离哪个数据目录。整段替换内置规则（不是追加）。\n"+
		"`gen` 写的第一条是根挂载 /:/:dev —— 它就是 --dev-bind / /，把整个宿主根挂进来\n"+
		"（设备可用、根可写）。它永远排在所有挂载之前，写在第几行都无所谓：排在 overlay\n"+
		"别名之后会把程序的真名还回来。收紧成只读就是把它改成 /:/:ro。\n"+
		"它是底座，不是这个程序的数据目录 —— 所以它不算在「路径映射」那几行里。\n"+
		"写法同 Docker Compose 的卷：源:目标[:ro|:dev]，源是数据实际所在，目标是程序看到它的位置。\n"+
		"路径可以绝对、可以 ~/ 开头，也可以相对 —— 相对的话基准是这份 profile.yml 自己所在的目录。\n"+
		"（同层那个 args 例外：那里逐字照搬，twin 不认、也解析不了里面的路径。）\n"+
		"  源路径:目标路径        ⇒ 私有副本，两个实例互不干扰\n"+
		"  只写一条路径           ⇒ 该路径挂回自己（数据不搬家）；根挂载是可写的 /:/:dev 时\n"+
		"                            它不改变什么，把根换成 /:/:ro 之后它才是「把只读升回可写」\n"+
		"  源路径:目标路径:ro     ⇒ 只读挂载，程序看得见但改不动\n"+
		"  源路径:目标路径:dev    ⇒ 带上设备访问（--dev-bind），给设备节点用。\n"+
		"                            bwrap 没有只读的设备映射，所以 dev 不能与 ro 同用；\n"+
		"                            根挂载 /:/:dev 已让整个宿主带设备可用，这一条是\n"+
		"                            给把根收紧成 /:/:ro 的定义留着表达这件事用的\n"+
		"两端也可以是同一个文件（单个配置文件）：形态照磁盘上已经存在的那一端，都没在时按目录。\n"+
		"空着 = 数据目录与源程序共用；程序若有单实例锁（Electron / Qt / GTK 系常见），\n"+
		"两个实例会互相干扰。这里只影响这一个实例；要让这个程序以后新建的实例也用同样\n"+
		"的映射，写 ~/.config/twin/rules.d/<程序>.toml（twin rules --init <程序名> 生成草稿）。")
	binds := make([]string, 0, len(i.Binds))
	for _, b := range i.Binds {
		binds = append(binds, b.String())
	}
	bw.Set("bind", binds)

	// After the mappings, on purpose. `--remount-ro` re-mounts a *destination*
	// read-only, and a destination is something a mapping above (or the base in
	// args) made writable — put it anywhere earlier and the mount it was meant
	// to tighten would be laid down on top of it, writable again. This is the
	// counterpart of the writable root the base binds: `--dev-bind / /` hands
	// over the whole host, so taking a path back is a per-path decision.
	bw.SetComment("remountRO", "把哪些路径收回只读（bwrap --remount-ro），写在所有挂载之后，所以能压住\n"+
		"bind 给出的那种可写。⚠️ 只能作用在「自己就是一条挂载」的路径上：bind 的**目标端**、\n"+
		"overlay 或 tmpfs 那条路径、或者整个 `/`。宿主本来就属于那一条大根挂载的路径（比如\n"+
		"~ 底下某个目录）底下没有单独的挂载，bwrap 会拒绝启动：Can't remount readonly on …:\n"+
		"Unable to find … in mount table（实测）。整根要收紧就把基座那行换成 --ro-bind / /。\n"+
		"留空 = 不收回。路径的基准同 bind（可以 ~/、可以相对这份 profile.yml）。")
	remount := make([]string, 0, len(i.RemountRO))
	remount = append(remount, i.RemountRO...)
	bw.Set("remountRO", remount)

	bw.SetComment("env", "注入沙箱的环境变量（bwrap --setenv），一条一个 `键:值`，后面的同名者胜。\n"+
		"冒号只在第一个处断开，所以值里可以再有冒号（PATH:/a:/b）。值可以是空的（写 `键:`），\n"+
		"键名不能带冒号、空格或 =。`gen` 写的 TWIN_INSTANCE:<id> 就在这一条里；留空 = 一个都不注入。")
	env := make([]string, 0, len(i.Env))
	for _, e := range i.Env {
		env = append(env, e.String())
	}
	bw.Set("env", env)

	// Host integration. Written unconditionally: "we did not override the tray"
	// is a decision worth seeing in the file, and an absent section would make
	// it indistinguishable from a file written before this option existed.
	trayT := t.Ensure("tray")
	trayT.SetComment("override", "要不要让 twin 管这个实例的托盘图标。false = 不管：托盘还是程序自己的图标\n"+
		"（有些程序的托盘 id twin 预测不了，只能交给它自己）；整机开关是 config.toml 的\n"+
		"defaults.tray_override，两处任一关掉都写 false。\n"+
		"走哪条路是这台机器的事、不在这里选：助手扩展被 shell 加载了 ⇒ 写它读的那份文件表\n"+
		"（~/.local/share/twin/tray.d/<id>.json）；没加载（未装／未启用／被禁用列表挡住／\n"+
		"读不到列表）⇒ 写宿主的 custom-icons 覆盖表兜底。")
	trayT.Set("override", i.TrayOverride)

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
	head := "# twin —— 实例定义。改完跑 `twin install " + i.ID + "` 按它落盘；\n" +
		"# `twin reinstall " + i.ID + "` 重放的就是这一份。\n"
	if elsewhere != "" {
		head = "# twin —— 实例定义。改完跑 `twin install -p " + elsewhere + "` 按它落盘；\n" +
			"# `twin reinstall " + i.ID + "` 重放的是实例目录里那份记录。\n"
	}
	if role == RoleSnapshot {
		head = "# twin —— 实例定义（存档记录）。`twin install` 按你指定的那份配置写出它；\n" +
			"# 改完跑 `twin reinstall " + i.ID + "` 重放。\n"
	}
	header := head +
		"# 每个键的说明就写在它自己上面，终端不再重复。\n" +
		"# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`badge` 那种纯数字尤其要）。\n"
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
	// The old key is refused by name rather than read as "no option list". The
	// rename is not a spelling change: `extra` was only what the user appended,
	// while `args` is the whole list including the sandbox base that twin used
	// to keep to itself. Reading one as the other would hand bwrap a namespace
	// with no root mounted and no hint of why — the program would simply not
	// start, with nothing anywhere naming twin. (Same reasoning as the
	// instance.toml error below: an instance twin cannot read is reported, never
	// silently dropped.)
	if root.At("bwrap").Has("extra") {
		return nil, fmt.Errorf("%s: `bwrap.extra` 现在叫 `bwrap.args`，而且语义变了 —— "+
			"它是交给 bwrap 的**完整**选项列表，基座（--dev-bind / /、--proc /proc、--tmpfs /tmp 等）也在里面，"+
			"twin 不再自己补。请用 `twin gen` 重新生成一份，再把你原来的 token 补进去", path)
	}
	// The entry's text lives under `desktop:`. The spelling that predates the
	// section — the same keys at the root — is still read: it is not ambiguous
	// (there is nowhere else a root-level `name` could have belonged), and a
	// snapshot written before the move has to keep working until the next
	// install rewrites it in the new shape. Refusing it would take every
	// installed instance offline at once over a change the file can absorb.
	dt := root.At("desktop")
	desktop := func(key string) string {
		if v := dt.Str(key); v != "" {
			return v
		}
		return root.Str(key)
	}
	it := &Instance{
		ID:         root.Str("id"),
		Name:       desktop("name"),
		NameEn:     desktop("name_en"),
		Comment:    desktop("comment"),
		CommentEn:  desktop("comment_en"),
		Keywords:   desktop("keywords"),
		Categories: desktop("categories"),
		Source:     desktop("source"),
		Level:      Level(root.At("identity").Str("level")),
		WMClass:    root.At("identity").Str("wm_class"),
		Notify:     root.At("identity").Bool("notify"),
		IconSource: resolvePath(base, root.At("icon").Str("source")),
		Badge:      root.At("icon").Str("badge"),
		BadgeStyle: root.At("icon").Str("style"),
		Target:     resolvePath(base, root.At("exec").Str("target")),
		Alias:      root.At("exec").Str("alias"),
		Argv0:      root.At("exec").Str("argv0"),
		Unshare:    root.At("isolation").StrList("unshare"),
		NestedNS:   root.At("isolation").Bool("nested_ns"),
		Args:       root.At("bwrap").StrList("args"),
		// The shaped mounts resolve like every other path in this file: they are
		// known to *be* paths, which is exactly what cannot be said about the
		// tokens in `args` (hence the passthrough rule).
		Proc:      resolvePath(base, root.At("bwrap").Str("proc")),
		Tmpfs:     resolvePaths(base, root.At("bwrap").StrList("tmpfs")),
		RemountRO: resolvePaths(base, root.At("bwrap").StrList("remountRO")),
	}
	// `isolation.tmpfs` and `exec.env` moved under `bwrap`, for the reason
	// `bind` did: each one is a bwrap option, and the node that says "this is
	// what bwrap is called with" should be the only place they live. They are
	// refused by name rather than read as aliases — two keys emitting one
	// argument is how a file starts disagreeing with itself, and the .env one
	// also changed shape (list of `键:值`, not a mapping).
	if root.At("isolation").Has("tmpfs") {
		return nil, fmt.Errorf("%s: `isolation.tmpfs` 现在在 `bwrap` 节点里（bwrap.tmpfs）—— "+
			"它就是 `--tmpfs`，和 bwrap.args 一起决定这次调用；把它缩进到 `bwrap:` 下面即可", path)
	}
	if root.At("exec").Has("env") {
		return nil, fmt.Errorf("%s: `exec.env` 现在是 `bwrap.env`，而且换成了列表：一条一个 `键:值`"+
			"（冒号只在第一个处断开，所以值里可以再有冒号）。请把 `键: 值` 那几行改写进 `bwrap:` 下面", path)
	}
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
	// `bind` lives under `bwrap`: it is one more thing that ends up in the same
	// invocation as `args`, which is written right there. A top-level `bind` is
	// refused rather than ignored — the two readings do not describe the same
	// instance (one mounts the program's data, the other mounts nothing), and
	// the failure would surface as "the program cannot see its files" long after
	// the edit.
	if root.Has("bind") {
		return nil, fmt.Errorf("%s: `bind` 现在在 `bwrap` 节点里（bwrap.bind）—— 它和 bwrap.args 一起"+
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
	// Absent means true: files written before the option existed all wanted the
	// tray override, and flipping them to false would silently stop maintaining
	// something that is already in the host's table.
	it.TrayOverride = true
	if trayT := root.Tree("tray"); trayT.Has("override") {
		it.TrayOverride = trayT.Bool("override")
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
