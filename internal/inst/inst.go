// Package inst holds the twin instance schema — the single source of truth
// that `instance.toml` reads and writes.
package inst

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/edwinhuish/twin/internal/tomlz"
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

// Bind is one path mapping applied inside the sandbox.
type Bind struct {
	Canonical string // the path the program sees (may start with ~)
	Private   string // where the data really lives (may start with ~)
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
	Env    map[string]string

	Tmpfs   []string
	Unshare []string
	// Bwrap holds verbatim bubblewrap option tokens collected from the CLI's
	// `--` region. twin never parses or rewrites them (设计 §2.1): the
	// arity of a bwrap option is bwrap's business, and re-deriving it here
	// would be a table that rots on every bwrap release.
	Bwrap []string

	Binds []Bind
}

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
func (p Paths) Config(id string) string { return filepath.Join(p.InstanceDir(id), "instance.toml") }
func (p Paths) StateDir(id string) string {
	return filepath.Join(p.Home, ".local/state/twin", id)
}

// RelStateDir is the state directory written with a leading ~ instead of an
// absolute home. instance.toml is meant to be human-edited and replayed on
// another machine (设计 §5.1), so it must not bake in today's home; Expand
// turns this back into a real path at use time.
func (p Paths) RelStateDir(id string) string {
	return "~/.local/state/twin/" + id
}
func (p Paths) RulesDir() string {
	return filepath.Join(p.Home, ".config/twin/rules.d")
}

// GlobalConfig is the environment layer: machine-scoped defaults. It is not
// the same file as Config(id), which is one instance's snapshot.
func (p Paths) GlobalConfig() string {
	return filepath.Join(p.Home, ".config/twin/config.toml")
}
func (p Paths) RuleFile(id string) string {
	return filepath.Join(p.RulesDir(), id+".json")
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

// Expand turns a leading ~/ into an absolute path. Anything else is returned
// unchanged; the sandbox never re-resolves it.
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

// IconSizeOrDefault keeps the size out of instance.toml for now: it is
// derivable, and a persisted copy would only invite drift.
func (i *Instance) IconSizeOrDefault() int { return DefaultIconSize }

// TargetDir is the directory holding the real program (and the alias).
func (i *Instance) TargetDir() string { return filepath.Dir(i.Target) }

// TargetBase is the real program's file name.
func (i *Instance) TargetBase() string { return filepath.Base(i.Target) }

// AliasPath is the absolute path of the renamed entry point.
func (i *Instance) AliasPath() string {
	return filepath.Join(i.TargetDir(), i.EffectiveAlias())
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
		if b.Canonical == "" || b.Private == "" {
			return fmt.Errorf("map.bind[%d]: both canonical and private are required", n)
		}
	}
	for n, tok := range i.Bwrap {
		if err := CheckBwrapToken(tok); err != nil {
			return fmt.Errorf("bwrap.extra[%d]: %w", n, err)
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

// toTree renders the instance as the documented TOML layout.
func (i *Instance) toTree() (*tomlz.Tree, error) {
	t := tomlz.New()
	t.Set("id", i.ID)
	t.Set("name", i.Name)
	if i.NameEn != "" {
		t.Set("name_en", i.NameEn)
	}
	if i.Comment != "" {
		t.Set("comment", i.Comment)
	}
	if i.CommentEn != "" {
		t.Set("comment_en", i.CommentEn)
	}
	if i.Keywords != "" {
		t.Set("keywords", i.Keywords)
	}
	if i.Categories != "" {
		t.Set("categories", i.Categories)
	}
	if i.Source != "" {
		t.Set("source", i.Source)
	}

	id := t.Ensure("identity")
	id.Set("level", string(i.Level))
	if i.WMClass != "" {
		id.Set("wm_class", i.WMClass)
	}
	id.Set("notify", i.Notify)

	ic := t.Ensure("icon")
	if i.IconSource != "" {
		ic.Set("source", i.IconSource)
	}
	if i.Badge != "" {
		ic.Set("badge", i.Badge)
	}
	ic.Set("style", i.BadgeStyle)

	ex := t.Ensure("exec")
	ex.Set("target", i.Target)
	if i.Level.Renames() {
		ex.Set("alias", i.EffectiveAlias())
	}
	if i.Argv0 != "" {
		ex.Set("argv0", i.Argv0)
	}
	envT := tomlz.New()
	for _, k := range sortedKeys(i.Env) {
		envT.Set(k, i.Env[k])
	}
	ex.Set("env", tomlz.Inline{T: envT})

	iso := t.Ensure("isolation")
	if len(i.Tmpfs) == 0 {
		iso.Set("tmpfs", []string{})
	} else {
		iso.Set("tmpfs", i.Tmpfs)
	}
	if len(i.Unshare) == 0 {
		iso.Set("unshare", []string{})
	} else {
		iso.Set("unshare", i.Unshare)
	}
	// Written unconditionally. The machine-scoped half of this decision lives
	// in config.toml, so a reader of this file must be able to tell "this
	// program needs no trampoline" from "this file predates the field".
	iso.Set("nested_ns", i.NestedNS)

	// The passthrough region, stored in the same flat-token shape the CLI and
	// the (future) rules.d use, so merging stays "concatenate the arrays"
	// rather than "invent a quoting rule" (设计 §3.2).
	bw := t.Ensure("bwrap")
	if len(i.Bwrap) == 0 {
		bw.Set("extra", []string{})
	} else {
		bw.Set("extra", i.Bwrap)
	}

	// Host integration. Written unconditionally: "we did not override the tray"
	// is a decision worth seeing in the file, and an absent section would make
	// it indistinguishable from a file written before this option existed.
	t.Ensure("tray").Set("override", i.TrayOverride)

	for _, b := range i.Binds {
		e := t.AddTable("map.bind")
		e.Set("canonical", b.Canonical)
		e.Set("private", b.Private)
	}
	return t, nil
}

// Render returns the TOML document for the instance.
func (i *Instance) Render() ([]byte, error) {
	t, err := i.toTree()
	if err != nil {
		return nil, err
	}
	header := "# twin —— 真源文件。改完跑 `twin regen " + i.ID + "` 重新落盘。\n" +
		"# level=alias 时窗口 app_id 取自 exec.target 的真实路径名，托盘 SNI Id 取自 argv[0]，\n" +
		"# 两者都等于 exec.alias —— 所以别让它们分叉。\n" +
		"# [bwrap] extra 是逐 token 原样透传给 bwrap 的选项，twin 不解析它们；\n" +
		"# 它拼在基座之后、程序之前，所以同目标挂载会由后写的那个胜出。\n"
	return append([]byte(header), tomlz.Encode(t)...), nil
}

// Load reads an instance definition from disk.
func Load(path string) (*Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := tomlz.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	it := &Instance{
		ID:         root.Str("id"),
		Name:       root.Str("name"),
		NameEn:     root.Str("name_en"),
		Comment:    root.Str("comment"),
		CommentEn:  root.Str("comment_en"),
		Keywords:   root.Str("keywords"),
		Categories: root.Str("categories"),
		Source:     root.Str("source"),
		Level:      Level(root.At("identity").Str("level")),
		WMClass:    root.At("identity").Str("wm_class"),
		Notify:     root.At("identity").Bool("notify"),
		IconSource: root.At("icon").Str("source"),
		Badge:      root.At("icon").Str("badge"),
		BadgeStyle: root.At("icon").Str("style"),
		Target:     root.At("exec").Str("target"),
		Alias:      root.At("exec").Str("alias"),
		Argv0:      root.At("exec").Str("argv0"),
		Tmpfs:      root.At("isolation").StrList("tmpfs"),
		Unshare:    root.At("isolation").StrList("unshare"),
		NestedNS:   root.At("isolation").Bool("nested_ns"),
		Bwrap:      root.At("bwrap").StrList("extra"),
	}
	env := root.At("exec").At("env")
	it.Env = make(map[string]string)
	for _, k := range env.Keys() {
		it.Env[k] = env.Str(k)
	}
	for _, b := range root.TablesAt("map.bind") {
		it.Binds = append(it.Binds, Bind{Canonical: b.Str("canonical"), Private: b.Str("private")})
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
		it, err := Load(p.Config(e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
