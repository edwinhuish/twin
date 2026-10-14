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
}

// String renders the one-line form the config files carry.
func (b Bind) String() string {
	s := b.Source + ":" + b.Target
	if b.ReadOnly {
		s += ":ro"
	}
	return s
}

// ParseBind reads the one-line form: "source:target", "source:target:ro",
// "source:target:rw", or a bare "path" meaning the same directory both ways.
//
// The bare form is not an optimisation. A directory bound onto itself is what
// upgrades it to writable in place under `--ro-bind / /`, which is the one
// mapping WeChat's received-files directory has always needed.
//
// The split is Docker's: a trailing ":ro"/":rw" is the mode, then the first
// colon separates the two sides. A path that itself contains a colon is
// therefore ambiguous, and — as in Docker — the ambiguity is resolved the
// documented way rather than guessed at.
func ParseBind(spec string) (Bind, error) {
	parts := strings.Split(strings.TrimSpace(spec), ":")
	if len(parts) == 2 && (parts[1] == "ro" || parts[1] == "rw") {
		// `x:ro` has no target, only what looks like a mode. Reading it as
		// "target named ro" would build a relative mount point out of a typo,
		// so it is refused instead.
		return Bind{}, fmt.Errorf("路径映射 %q 的右侧是访问模式而不是目标路径："+
			"两侧写全（%s:%s:ro），或去掉模式（同一路径默认可写）", spec, parts[0], parts[0])
	}
	b := Bind{}
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			b.ReadOnly = true
		case "rw":
		default:
			return Bind{}, fmt.Errorf("路径映射 %q 的访问模式 %q 不认识（只有 ro 和 rw）", spec, parts[2])
		}
		parts = parts[:2]
	}
	if len(parts) > 3 {
		return Bind{}, fmt.Errorf("路径映射 %q 的冒号太多（形如 \"源:目标[:ro|:rw]\"）", spec)
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

// Profile is the instance snapshot: the file a human is meant to read and edit.
// YAML because the schema is nested (identity / exec / isolation / bwrap) and a
// nested file reads as a hierarchy instead of as a wall of headers.
func (p Paths) Profile(id string) string {
	return filepath.Join(p.InstanceDir(id), "profile.yml")
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
			return fmt.Errorf("bind[%d] %q: 源和目标是两条路径，都要写", n, b)
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
	id.SetComment("level", "none = 只写桌面项与图标、程序原样跑；alias = bwrap overlay 零拷贝改名（主力）；\n"+
		"redirect = 改名，并多写一条把窗口 app_id 改回本实例的扩展规则。")
	id.Set("level", string(i.Level))
	id.SetComment("wm_class", "写进桌面项的 StartupWMClass，默认 = id。只在会改名时才需要。")
	if i.WMClass != "" {
		id.Set("wm_class", i.WMClass)
	}
	id.SetComment("notify", "写进桌面项的 StartupNotify=。")
	id.Set("notify", i.Notify)

	ic := t.Ensure("icon")
	ic.SetComment("source", "源图标。")
	if i.IconSource != "" {
		ic.Set("source", i.IconSource)
	}
	ic.SetComment("badge", "图标上的角标文字。纯数字要加引号，否则会被读成数字。")
	if i.Badge != "" {
		ic.Set("badge", i.Badge)
	}
	ic.SetComment("style", "角标画法：corner 右下角贴文字 / number 居中大数字 / dot 圆点。")
	ic.Set("style", i.BadgeStyle)

	ex := t.Ensure("exec")
	ex.SetComment("target", "真实程序的绝对路径。")
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
	// A nested mapping rather than TOML's one-line {k = v}: with YAML the
	// hierarchy is free, and env is a hierarchy.
	ex.SetComment("env", "注入沙箱的环境变量（bwrap --setenv），写成 `键: 值`。")
	envT := yamlz.New()
	for _, k := range sortedKeys(i.Env) {
		envT.Set(k, i.Env[k])
	}
	ex.Set("env", envT)

	iso := t.Ensure("isolation")
	iso.SetComment("tmpfs", "在沙箱内被盖成空目录的路径。")
	if len(i.Tmpfs) == 0 {
		iso.Set("tmpfs", []string{})
	} else {
		iso.Set("tmpfs", i.Tmpfs)
	}
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

	// The passthrough region, stored in the same flat-token shape the CLI and
	// the (future) rules.d use, so merging stays "concatenate the arrays"
	// rather than "invent a quoting rule" (设计 §3.2).
	bw := t.Ensure("bwrap")
	bw.SetComment("extra", "逐 token 原样透传给 bwrap；twin 不解析它们（不猜 arity）。\n"+
		"拼在基座之后、程序之前，所以同目标挂载会由后写的那个胜出。")
	if len(i.Bwrap) == 0 {
		bw.Set("extra", []string{})
	} else {
		bw.Set("extra", i.Bwrap)
	}

	// Host integration. Written unconditionally: "we did not override the tray"
	// is a decision worth seeing in the file, and an absent section would make
	// it indistinguishable from a file written before this option existed.
	trayT := t.Ensure("tray")
	trayT.SetComment("override", "要不要把宿主的托盘覆盖表指向本实例。false = 这台机器上不用 twin 管托盘\n"+
		"（宿主的表是全局的，而有些程序的托盘 id twin 预测不了）。")
	trayT.Set("override", i.TrayOverride)

	// Written unconditionally too, empty case included. "twin redirected
	// nothing" is the default decision, and once the terminal stopped printing
	// it this comment became the only place the shared-state warning can live —
	// so the key has to be there even when there is nothing to list.
	t.SetComment("bind", "隔离哪个数据目录。整段替换内置规则（不是追加）。\n"+
		"写法同 Docker Compose 的卷：源:目标[:ro]，源是数据实际所在，目标是程序看到它的位置。\n"+
		"  源路径:目标路径        ⇒ 私有副本，两个实例互不干扰\n"+
		"  只写一条路径           ⇒ 该路径挂回自己，只把只读升回可写（数据不搬家）\n"+
		"  源路径:目标路径:ro     ⇒ 只读挂载，程序看得见但改不动\n"+
		"空着 = 数据目录与源程序共用；程序若有单实例锁（Electron / Qt / GTK 系常见），\n"+
		"两个实例会互相干扰。这里只影响这一个实例；要让这个程序以后新建的实例也用同样\n"+
		"的映射，写 ~/.config/twin/rules.d/<程序>.toml（twin rules --init 生成草稿）。")
	binds := make([]string, 0, len(i.Binds))
	for _, b := range i.Binds {
		binds = append(binds, b.String())
	}
	t.Set("bind", binds)
	return t, nil
}

// Role says which of the two jobs a rendered instance definition is doing.
//
// The same Instance is written to two places: the working copy `twin gen` hands
// the user, and the archive `twin install` leaves next to the instance. They
// hold the same keys, but they answer to different commands — and a header that
// named the wrong one would send a reader to a command that quietly ignores the
// file they are looking at, which is the failure mode this whole design is
// arranged against.
type Role int

const (
	// RoleConfig is the working copy: `twin gen` writes it, `twin install` reads it.
	RoleConfig Role = iota
	// RoleSnapshot is the archive: install writes it and `twin reinstall` replays it.
	RoleSnapshot
)

// Render returns the document as the working copy.
func (i *Instance) Render() ([]byte, error) { return i.RenderAs(RoleConfig) }

// RenderAs returns the document with the header for the given role.
func (i *Instance) RenderAs(role Role) ([]byte, error) {
	t, err := i.toTree()
	if err != nil {
		return nil, err
	}
	// Only the header differs between the two roles; the keys and their notes
	// are rendered once, so a key cannot be documented one way in the file the
	// user edits and another way in the file install archives.
	head := "# twin —— 实例定义（工作副本）。改完跑 `twin install` 按它落盘。\n"
	if role == RoleSnapshot {
		head = "# twin —— 实例定义（存档快照）。改完跑 `twin reinstall " + i.ID + "` 重放；\n" +
			"# `twin install` 会用工作副本那份重新写出它。\n"
	}
	header := head +
		"# 每个键的说明就写在它自己上面，终端不再重复。\n" +
		"# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`badge` 那种纯数字尤其要）。\n"
	return append([]byte(header), yamlz.Encode(t)...), nil
}

// Load reads an instance definition from disk.
func Load(path string) (*Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := yamlz.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
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
	specs, err := root.StrListStrict("bind")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, spec := range specs {
		b, err := ParseBind(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
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
	return fmt.Errorf("%s 还是旧的 TOML 快照（这个版本起快照叫 profile.yml，YAML 格式）："+
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

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
