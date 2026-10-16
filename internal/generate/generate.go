// Package generate is the pipeline from the design doc: inspect the source
// program, decide how to give it an identity, then write the artefacts.
package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edwinhuish/twin/internal/config"
	"github.com/edwinhuish/twin/internal/desk"
	"github.com/edwinhuish/twin/internal/icon"
	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sandbox"
	"github.com/edwinhuish/twin/internal/sysx"
)

// Options are the user's choices for one wrap.
type Options struct {
	Source     string
	ID         string
	Name       string
	NameEn     string
	Comment    string
	CommentEn  string
	Keywords   string
	Categories string
	Icon       string
	Badge      string
	BadgeStyle string
	Level      inst.Level
	Tmpfs      []string
	// Cfg is the environment layer. A nil value means "built-in defaults",
	// which is what `twin reinstall` uses: replaying a snapshot must not depend on a
	// file the user may have edited since (设计 §3.5 rule three).
	Cfg *config.Config
	// Args is the CLI `--` region: bubblewrap option tokens forwarded verbatim
	// (设计 §2). They are appended to the instance's own list, behind its base
	// and its global/rule layers, and they land in the file from then on — this
	// is a seed, not a launch-time override. Nothing here is parsed; inst.Check
	// only refuses the three token classes that cannot work.
	Args    []string
	NoTmpfs bool
	// NoTray opts this one instance out of the host tray override. The
	// machine-wide switch is config.toml's defaults.tray_override; either one
	// turning it off is enough.
	NoTray bool
	// There is no icon size here on purpose: it is environment, not intent
	// (设计 §3.6), and another machine has another DPI. It is read straight
	// from config.toml at install time, so it is never frozen into a
	// configuration that gets copied to a second machine.
}

// Inspection is what we learned about the source program.
type Inspection struct {
	SourceDesktop string
	Entry         *desk.Entry
	Argv          []string
	Target        string // symlink-resolved real binary
	IconSource    string
	Traits        Traits
	Presets       []inst.Bind
	// Rule is the application knowledge that applied to this program, already
	// merged built-in + rules.d. Its Binds are pre-expanded into Presets.
	Rule *config.Rule
	// RuleKey is the name Rule was found under, empty when there is no rule.
	// The CLI prints it as the file name to write when the user corrects a path
	// map, so it has to be the key FindRule actually matched: re-deriving it
	// from the desktop basename could name a different one of the two keys that
	// RuleKeys tries, and a rule filed under the other name would be shadowed.
	RuleKey string
	Notes   []string
}

// Result lists what was written.
type Result struct {
	Desktop string
	Icon    string
	Launch  string
	Profile string
	State   string
	// Rule is the L5 redirection rule; empty unless level=redirect.
	Rule  string
	Notes []string
}

// Expansion placeholders. `{id}` is the documented spelling (设计 §3.3);
// `<id>` is accepted because the built-in table used it before rules.d
// existed, and a user may have copied a line out of an older instance file.
func expandID(s, id string) string {
	s = strings.ReplaceAll(s, "{id}", id)
	return strings.ReplaceAll(s, "<id>", id)
}

// ResolveDesktop finds the source desktop entry: a direct path, a bare id, or
// an application/binary name.
func ResolveDesktop(p inst.Paths, source string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("no source given")
	}
	if sysx.Exists(source) && strings.HasSuffix(source, ".desktop") {
		return source, nil
	}
	candidates := []string{source}
	if !strings.HasSuffix(source, ".desktop") {
		candidates = append(candidates, source+".desktop")
	}
	if bin := sysx.FirstInstalled(source); bin != "" {
		base := filepath.Base(bin)
		candidates = append(candidates, base+".desktop")
	}
	for _, dir := range p.SystemDesktopDirs() {
		for _, c := range candidates {
			cand := filepath.Join(dir, filepath.Base(c))
			if sysx.Exists(cand) {
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("找不到 %q 对应的 .desktop（可以直接给出绝对路径）", source)
}

// Inspect reads the source program and works out what we are dealing with.
func Inspect(p inst.Paths, source string) (*Inspection, error) {
	desktopPath, err := ResolveDesktop(p, source)
	if err != nil {
		return nil, err
	}
	entry, err := desk.Parse(desktopPath)
	if err != nil {
		return nil, err
	}
	argv, err := entry.Command()
	if err != nil {
		return nil, err
	}
	prog := argv[0]
	abs := prog
	if !filepath.IsAbs(abs) {
		abs = sysx.Which(prog)
		if abs == "" {
			return nil, fmt.Errorf("%s: Exec 程序 %q 在 PATH 上找不到", desktopPath, prog)
		}
	}
	if !sysx.Exists(abs) {
		return nil, fmt.Errorf("Exec 目标 %s 不存在", abs)
	}
	// /proc/self/exe reports the resolved path, and that basename is what the
	// window app_id is derived from — so the alias must sit next to the real
	// file, not next to the symlink.
	target := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		target = resolved
	}

	insp := &Inspection{
		SourceDesktop: desktopPath,
		Entry:         entry,
		Argv:          argv,
		Target:        target,
	}
	if target != abs {
		insp.Notes = append(insp.Notes,
			fmt.Sprintf("%s 是指向 %s 的链接 —— 别名要建在 %s 里（$ORIGIN 与 /proc/self/exe 都看真实路径）",
				abs, target, filepath.Dir(target)))
	}

	if entry.Icon != "" {
		if resolved, note := icon.ResolveSource(p.Home, entry.Icon); resolved != "" {
			insp.IconSource = resolved
			if note != "" {
				insp.Notes = append(insp.Notes, note)
			}
		} else {
			insp.Notes = append(insp.Notes, fmt.Sprintf("源桌面项的 Icon=%q 解析不到文件 —— 需要 --icon 指定", entry.Icon))
		}
	}

	traits, err := ScanTraits(target)
	if err != nil {
		insp.Notes = append(insp.Notes, "二进制扫描失败："+err.Error())
	}
	insp.Traits = traits
	insp.Notes = append(insp.Notes, traits.Notes...)

	key := strings.TrimSuffix(filepath.Base(desktopPath), ".desktop")
	rule, matched, err := config.FindRule(p, config.RuleKeys(key, filepath.Base(target))...)
	if err != nil {
		return nil, err
	}
	insp.Rule = rule
	insp.RuleKey = matched
	if rule != nil {
		// The bind list is materialised here rather than in Plan so that
		// `inspect` can show the same paths a real run would use, while the
		// concrete id is still a placeholder.
		for _, b := range rule.Binds {
			insp.Presets = append(insp.Presets, inst.Bind{
				Source:   expandID(b.Source, "<id>"),
				Target:   expandID(b.Target, "<id>"),
				ReadOnly: b.ReadOnly,
			})
		}
		where := "内置规则"
		if !rule.Builtin {
			where = rule.Path
		}
		insp.Notes = append(insp.Notes, fmt.Sprintf(
			"应用规则 %s：%s（%d 条路径映射 ⇒ bwrap.bind）", matched, where, len(insp.Presets)))
	} else {
		insp.Notes = append(insp.Notes,
			"没有该程序的状态路径知识：本实例只做身份包裹。若它有单实例锁或共享配置，"+
				"在 "+p.RulesDir()+"/"+key+".toml 里写一条 bind（twin rules --init "+key+" 生成草稿），"+
				"或只给这一个实例改那份定义里的 bwrap.bind")
	}
	return insp, nil
}

// Derivation records where a defaulted value came from.
//
// Deriving defaults is only safe when the derivation is visible: the risk is
// not guessing wrong, it is guessing unnoticed (设计 §1.2). Every value Plan
// fills in from the source program gets one of these, and the CLI prints them.
type Derivation struct {
	Key    string
	Value  string
	Source string
}

// Plan turns options plus an inspection into an instance definition, deriving
// every default it can from the source program and recording the reason.
func Plan(p inst.Paths, o Options, insp *Inspection) (*inst.Instance, EntryBody, []Derivation, error) {
	base := strings.TrimSuffix(filepath.Base(insp.SourceDesktop), ".desktop")

	var der []Derivation
	note := func(key, val, src string) {
		der = append(der, Derivation{Key: key, Value: val, Source: src})
	}

	cfg := o.Cfg
	if cfg == nil {
		cfg = config.Builtin()
	}
	rule := insp.Rule

	id := o.ID
	if id == "" {
		id = freeID(p, base)
		note("id", id, fmt.Sprintf("源桌面项基名 %s + 最小空闲序号", base))
	}
	if err := inst.ValidateID(id); err != nil {
		return nil, EntryBody{}, nil, err
	}
	seq := seqOf(id)

	// Level: CLI > rules.d > config.toml > built-in. Each layer names its
	// origin when it wins, because "which file set this to redirect?" is the
	// first question a broken identity raises.
	level := o.Level
	if level == "" {
		switch {
		case rule != nil && rule.Level != "":
			lvl, err := inst.ParseLevel(rule.Level)
			if err != nil {
				return nil, EntryBody{}, nil, err
			}
			level = lvl
			note("level", string(level), "应用规则 "+ruleOrigin(rule))
		case cfg.Defaults.Level != "":
			lvl, err := inst.ParseLevel(cfg.Defaults.Level)
			if err != nil {
				return nil, EntryBody{}, nil, err
			}
			level = lvl
			note("level", string(level), "config.toml defaults.level")
		default:
			level = inst.LevelAlias
		}
	}
	if !level.Implemented() {
		return nil, EntryBody{}, nil, fmt.Errorf("level=%s 还没实现：阶梯上只有 none 与 alias 通过了实测", level)
	}

	// The prose of the entry — the part that ends up inside the generated
	// desktop file rather than in this definition. It is derived here (and
	// derived the same way at install time, when the file is missing) so that a
	// definition and its entry text can never disagree about what the program
	// is called.
	body, bodyDer := DeriveBody(insp, id, o)
	der = append(der, bodyDer...)

	badge := o.Badge
	if badge == "" && seq != "" {
		badge = seq
		note("badge", badge, "从 id 的尾部数字提取")
	}

	it := &inst.Instance{
		ID:     id,
		Target: insp.Target,
		Source: insp.SourceDesktop,
		// One file per id, beside the definition. The spelling is relative on
		// purpose: the text is configuration, and a bundle (definition + entry
		// text + program) then moves or gets committed as a unit. `gen` writes
		// it, Load resolves it, and both do it with inst.ResolveAgainst — the
		// file gen creates and the file install copies have to be one file.
		DesktopOverride: "./" + id + ".desktop",
		// Left empty on purpose. The entry twin generates fills StartupWMClass
		// with the id when the level renames the program (and only then), and
		// writing the same value here as well would give a reader two places to
		// change one line.
		WMClass:    "",
		IconSource: firstNonEmpty(o.Icon, insp.IconSource),
		Badge:      badge,
		BadgeStyle: firstNonEmpty(o.BadgeStyle, cfg.Defaults.BadgeStyle, icon.StyleCorner),
		Level:      level,
		NestedNS:   insp.Traits.NeedsNestedNS,
	}

	// A program that builds a sandbox of its own needs a bubblewrap that can
	// still create a user namespace inside, which the system one cannot (see
	// internal/userns). Recording the need in the snapshot matters because
	// `twin reinstall` deliberately does not re-scan the binary: without this line a
	// replay would quietly fall back to the system bubblewrap and produce a
	// launcher that dies at startup.
	if it.NestedNS {
		note("isolation.nested_ns", "true",
			"二进制含 Chromium 沙箱特征（chrome-sandbox / ELECTRON_RUN_AS_NODE / libcef.so）")
	}

	// The tray override table is host state, and the decision to touch it is
	// recorded in the snapshot so `twin reinstall` reproduces it without reading
	// config.toml (设计 §3.5). Either switch turning it off wins.
	it.TrayOverride = cfg.Defaults.TrayOverride && !o.NoTray
	switch {
	case o.NoTray:
		note("override.tray", "false", "--no-tray")
	case !cfg.Defaults.TrayOverride:
		note("override.tray", "false", "config.toml defaults.tray_override")
	}

	// Refuse a missing icon *before* anything is written. A desktop entry
	// without a resolvable Icon= shows up as the gear placeholder, which is
	// exactly the bug this tool exists to fix. Checking here rather than in
	// Materialize matters: Materialize writes the launcher and profile.yml
	// first, so failing at the icon would leave a half-built instance behind
	// (and the next wrap would then refuse with "建议先 unwrap").
	if it.IconSource == "" {
		return nil, EntryBody{}, nil, fmt.Errorf("找不到可用的源图标：请用 --icon <png|svg> 指定（拒绝静默生成齿轮占位图）")
	}

	// A multi-call binary cannot survive a rename: it dispatches on argv[0],
	// which under LevelAlias is the alias path. Refusing beats shipping an
	// instance that prints "unknown program '<alias>'" every launch.
	if level.Renames() && insp.Traits.MultiCall {
		return nil, EntryBody{}, nil, fmt.Errorf("目标是多合一二进制（BusyBox / coreutils），靠 argv[0] 分派子命令：" +
			"alias 改名后它会不认识自己（实测报 unknown program）。这个程序暂时没有可用的身份级别")
	}

	// /tmp policy. A tmpfs /tmp takes /tmp/.X11-unix/X0 with it, so it is only
	// safe for a program that actually talks Wayland. `tmpfs_unless_x11` in
	// config.toml turns the guess off for machines where the trait scan is
	// known to be wrong.
	var tmpfs []string
	switch {
	case o.NoTmpfs:
		tmpfs = nil
	case len(o.Tmpfs) > 0:
		tmpfs = append([]string(nil), o.Tmpfs...)
	case rule != nil && len(rule.Tmpfs) > 0:
		tmpfs = append([]string(nil), rule.Tmpfs...)
		note("bwrap.extras", strings.Join(tmpfsLines(tmpfs), " / "), "应用规则 "+ruleOrigin(rule))
	case insp.Traits.Wayland && cfg.Defaults.TmpfsUnlessX11:
		tmpfs = []string{"/tmp"}
	default:
		tmpfs = nil
	}
	// There is no branch to take when there is no sandbox: `level none` runs the
	// program untouched, so a line here would describe a mount that never
	// happens — and SessionWarning reads the same lines to warn about X11
	// sessions.
	if !level.Renames() {
		tmpfs = withoutPath(tmpfs, "/tmp")
	}
	// The two branches of the /tmp decision are two different lines, and the
	// base owns the other one (see sandbox.BaseFor): a private /tmp is a tmpfs
	// line — already in the list above — while the host's /tmp is a
	// `--bind /tmp /tmp` line the base adds *after* every tmpfs line. Both leave
	// /tmp writable, and the file says which one this instance got.
	privateTmp := tmpfsCovers(tmpfs, "/tmp")

	// Path maps come from the rule and from nowhere else. There is deliberately
	// no CLI flag for them: a mapping is the one thing in a wrap that varies
	// per program and is silently wrong when wrong, so it is written into a
	// file the user reads before anything is installed (设计 §3.3).
	for _, pr := range insp.Presets {
		it.Binds = append(it.Binds, inst.Bind{
			Source:   expandID(pr.Source, id),
			Target:   expandID(pr.Target, id),
			ReadOnly: pr.ReadOnly,
			Dev:      pr.Dev,
			Try:      pr.Try,
		})
	}

	// bwrap.extras, low priority first (设计 §3.5 rule two): the base twin writes
	// for this instance, then the global default, then the rule, then the
	// command line's `--` region. Later tokens shadow earlier mounts on the same
	// destination, so no conflict-resolution code is owed — and the root is
	// first for exactly that reason: everything that has to be able to overrule
	// it (a path mapping, the alias overlay) is emitted after it, wherever it
	// sits in the file (see inst.Bind.IsRootMount).
	//
	// The base goes into the file rather than staying in the sandbox builder.
	// Which root a program gets is the decision that varies most per program
	// (a program that touches devices cannot live under a read-only root), and
	// the only way to change a hard-coded one was the `--` region — which lands
	// at the *end*, where binding the root again shadows every mapping before
	// it. Writing it here is what makes the option list the file's business.
	//
	// Its pieces are interleaved on purpose: the host's /tmp line only does
	// anything if it comes after every tmpfs line, and this is the one place
	// that knows both lists. Base-first inside every list too, so the layers
	// above can still overrule the base.
	if level.Renames() {
		base := sandbox.BaseFor(id, privateTmp)
		it.Binds = append(base.Binds, it.Binds...)
		it.Extras = append(it.Extras, base.Head...)
		it.Extras = append(it.Extras, tmpfsLines(base.Tmpfs)...)
		it.Extras = append(it.Extras, tmpfsLines(tmpfs)...)
		hostTmp := ""
		if base.HostTmp != "" {
			hostTmp = "--bind " + base.HostTmp + " " + base.HostTmp
			it.Extras = append(it.Extras, hostTmp)
		}
		it.Extras = append(it.Extras, base.Tail...)
		it.Env = append(base.Env, it.Env...)
		note("bwrap.bind", base.Binds[0].String(),
			"gen 写的沙箱基座：根挂载（发在其余所有挂载之前，写在第几行都一样）")
		note("bwrap.env", base.Env[0].String(), "gen 写的沙箱基座")
		own := append([]string{}, base.Head...)
		own = append(own, tmpfsLines(base.Tmpfs)...)
		if hostTmp != "" {
			// A line of its own, sitting where nothing distinguishes it from a
			// line the user wrote — so it is named here, where the lines the
			// user did not write are already named.
			own = append(own, hostTmp)
		}
		own = append(own, base.Tail...)
		why := "gen 写的沙箱基座：proc、私有 /dev/shm"
		if hostTmp != "" {
			why += "，以及宿主 /tmp 原地可写（排在 tmpfs 之后，压得住那里的一条 /tmp）"
		}
		why += "；config.toml / rules.d / `--` 的参数也并进这一份"
		note("bwrap.extras", strings.Join(own, " / "), why)
	}
	it.Extras = append(it.Extras, cfg.Args...)
	if rule != nil {
		it.Extras = append(it.Extras, rule.Args...)
	}
	it.Extras = append(it.Extras, o.Args...)
	if len(it.Extras) == 0 {
		it.Extras = nil
	}
	for n, line := range it.Extras {
		for _, tok := range strings.Fields(line) {
			if err := inst.CheckBwrapToken(tok); err != nil {
				return nil, EntryBody{}, nil, fmt.Errorf("%s（bwrap.extras[%d] %q）：%w",
					bwrapSource(o, rule, cfg), n, line, err)
			}
		}
	}

	if err := it.Check(); err != nil {
		return nil, EntryBody{}, nil, err
	}
	return it, body, der, nil
}

// tmpfsLines renders paths as the `--tmpfs` lines the definition carries.
//
// The paths are written the way the file writes paths, `~` included: Build
// expands a leading `~` in every token it hands over, so a rule's
// `~/.cache/x` stays the user's own answer to "which home" instead of being
// frozen into this machine's home directory the moment it is generated.
func tmpfsLines(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, t := range paths {
		out = append(out, "--tmpfs "+t)
	}
	return out
}

// ruleOrigin names where a rule came from, for the derivation log.
func ruleOrigin(r *config.Rule) string {
	if r == nil {
		return "内置"
	}
	if r.Path != "" {
		return r.Path
	}
	return "内置规则 " + r.App
}

// bwrapSource guesses which layer a rejected token came from, so the error can
// point at the file to edit instead of the whole merge.
func bwrapSource(o Options, r *config.Rule, c *config.Config) string {
	if len(o.Args) > 0 {
		for _, tok := range o.Args {
			if inst.CheckBwrapToken(tok) != nil {
				return "命令行 `--` 传的 " + tok
			}
		}
	}
	if r != nil {
		for _, tok := range r.Args {
			if inst.CheckBwrapToken(tok) != nil {
				return ruleOrigin(r) + " 里的 " + tok
			}
		}
	}
	for _, tok := range c.Args {
		if inst.CheckBwrapToken(tok) != nil {
			return c.Path + " [bwrap] args 里的 " + tok
		}
	}
	return "bwrap.extras 里的 token"
}

// freeID returns the smallest unused name of the form <base><n>, n >= 2.
//
// "Unused" means nothing already answers to it — not an twin instance, not a
// user desktop entry, not a system one. Reusing an existing name would create a
// duplicate desktop id, which GNOME resolves by silently shadowing one of them.
func freeID(p inst.Paths, base string) string {
	for n := 2; n < 1000; n++ {
		if cand := fmt.Sprintf("%s%d", base, n); !idTaken(p, cand) {
			return cand
		}
	}
	return base + "2"
}

func idTaken(p inst.Paths, id string) bool {
	if sysx.Exists(p.Desktop(id)) || sysx.Exists(p.Profile(id)) {
		return true
	}
	for _, dir := range []string{"/usr/local/share/applications", "/usr/share/applications"} {
		if sysx.Exists(filepath.Join(dir, id+".desktop")) {
			return true
		}
	}
	return false
}

// seqOf returns the trailing run of digits of s — the sequence number shared by
// the id, the display name and the badge (wechat2 -> "2", myapp -> "").
func seqOf(s string) string {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	return s[i:]
}

// Materialize writes the artefacts. It is idempotent: running it twice only
// overwrites twin's own output, never the source program.
//
// definitionIsHere reports whether the file the caller read the instance from
// is already that instance's own definition (~/.local/share/twin/<id>/profile.yml).
//
// It is what decides whether an install writes a definition at all. Writing it
// would be a round trip through the parser, which drops every comment the user
// added and re-orders what they wrote, and nothing short of re-running
// `twin gen` would bring them back. The archive role exists for the other
// arrangement: install was pointed at a file elsewhere (-p ~/wraps/x.yml), and
// the instance directory still needs the record `twin reinstall` replays.
func definitionIsHere(p inst.Paths, id, definition string) bool {
	return definition == p.Profile(id)
}

// iconSize is passed in rather than read from the instance because it is
// environment: it must not be frozen into the snapshot (设计 §3.6). A
// non-positive value falls back to the built-in default.
//
// definition is the file the caller just read the instance from. When that file
// already *is* the instance definition (the ordinary case: `twin gen` wrote it
// into the instance directory), it is left alone — see step 3 below.
func Materialize(p inst.Paths, it *inst.Instance, insp *Inspection, plan *sandbox.Plan,
	iconSize int, definition string) (*Result, error) {
	if iconSize <= 0 {
		iconSize = it.IconSizeOrDefault()
	}
	res := &Result{
		Desktop: p.Desktop(it.ID),
		Icon:    p.Icon(it.ID),
		Launch:  p.Launch(it.ID),
		Profile: p.Profile(it.ID),
		State:   p.StateDir(it.ID),
	}

	// 0. the machine-wide run-time helpers, when this instance needs them.
	//    One bubblewrap copy and one bootstrap script serve every instance, so
	//    they live in BinDir rather than under the instance. Writing them
	//    first also means a failure here leaves nothing instance-shaped behind.
	if plan.Copy != "" {
		if err := syncBwrapCopy(plan.Copy); err != nil {
			return nil, err
		}
		res.Notes = append(res.Notes, "bwrap 私有副本 "+plan.Copy)
	}
	if plan.Bootstrap != "" {
		if err := writeBootstrap(p.Bootstrap()); err != nil {
			return nil, err
		}
		res.Notes = append(res.Notes, "跳板脚本 "+p.Bootstrap())
	}

	// 1. private state directories. The state root is always a directory; a
	//    mapping's private side is whatever shape that mapping has (a config
	//    file stays a file) — sandbox.EnsureMountPoints owns that rule, and the
	//    launcher repeats it in shell before every start.
	if err := sysx.EnsureDir(p.StateDir(it.ID)); err != nil {
		return nil, err
	}
	if err := sandbox.EnsureMountPoints(it, p); err != nil {
		return nil, err
	}

	// 2. the launcher. Written before the desktop entry because GLib validates
	//    that Exec exists *and* is executable.
	launch, err := sandbox.RenderLauncher(it, p, plan)
	if err != nil {
		return nil, err
	}
	if err := sysx.WriteFileAtomic(p.Launch(it.ID), launch, 0o755); err != nil {
		return nil, err
	}

	// 3. the instance definition — but only when the file we were handed is not
	//    already the one that lives here (see definitionIsHere).
	if !definitionIsHere(p, it.ID, definition) {
		cfg, err := it.RenderAs(inst.RoleSnapshot)
		if err != nil {
			return nil, err
		}
		if err := sysx.WriteFileAtomic(p.Profile(it.ID), cfg, 0o644); err != nil {
			return nil, err
		}
	}

	// 4. the entry's own text. It is a file of its own (override.desktop), and
	//    from the first byte on it is the user's document: generated once —
	//    usually by `twin gen` — and then copied verbatim at every install, so
	//    that editing what the menu shows is editing a text file rather than
	//    five keys in a config. A missing one is regenerated here, because a
	//    bundle may carry the definition without it — with the defaults derived
	//    from the source entry, which is all install has.
	//
	//    This runs before the icon and the copy below, and the icon is why:
	//    the fallback glyph is the display name, and that name is in here.
	txt := it.DesktopOverride
	if txt == "" {
		return nil, fmt.Errorf("这份定义没有 override.desktop：install 不知道桌面项的正文在哪")
	}
	if !sysx.Exists(txt) {
		body, _ := DeriveBody(insp, it.ID, Options{})
		if err := sysx.WriteFileAtomic(txt, EntryBytes(p, it, body), 0o644); err != nil {
			return nil, err
		}
		res.Notes = append(res.Notes, "桌面项正文（按源桌面项新生成）"+txt)
	} else {
		res.Notes = append(res.Notes, "桌面项正文 "+txt+"（install 逐字复制它）")
	}
	entry, err := desk.Parse(txt)
	if err != nil {
		return nil, fmt.Errorf("桌面项正文 %s 读不了：%w", txt, err)
	}
	// Not an error, because the file is the user's: they may have pointed Exec
	// somewhere on purpose. But an entry whose Exec is not our launcher is
	// either a hand-edit or a file generated on another machine (its Exec and
	// Icon carry that machine's home), and both end the same way — a menu entry
	// that does not run this instance — so it is said out loud.
	if !strings.Contains(entry.Exec, p.Launch(it.ID)) {
		res.Notes = append(res.Notes, "⚠️ 这份正文里的 Exec= 不是本实例的启动器（"+p.Launch(it.ID)+
			"）—— 装出来的桌面项会去跑别的程序；换过家目录的话，删掉 "+txt+" 让 twin 重新生成")
	}

	// 5. the icon.
	iconSpec := icon.Spec{
		Source: it.IconSource,
		Name:   entry.Name,
		Badge:  it.Badge,
		Style:  it.BadgeStyle,
		Home:   p.Home,
	}
	if iconSpec.Source == "" {
		return nil, fmt.Errorf("找不到可用的源图标：请用 --icon <png|svg> 指定（拒绝静默生成齿轮占位图）")
	}
	iconRep, err := icon.Compose(iconSpec, p.Icon(it.ID), iconSize)
	if err != nil {
		return nil, err
	}
	res.Notes = append(res.Notes, iconRep.Notes...)

	// 6. the desktop entry — the anchor of the identity. A byte-for-byte copy of
	//    the text above, twins' own lines included: `--dry-run` shows those very
	//    bytes, and what gets installed is what the user read.
	body, err := os.ReadFile(txt)
	if err != nil {
		return nil, err
	}
	if err := sysx.WriteFileAtomic(p.Desktop(it.ID), body, 0o644); err != nil {
		return nil, err
	}

	// 7. the L5 rule. Written last so a rule can never outlive the desktop
	//    entry it points at: the extension would keep redirecting windows to
	//    an app that no longer exists.
	if it.Level == inst.LevelRedirect {
		rule, err := RuleJSON(it, p)
		if err != nil {
			return nil, err
		}
		if err := sysx.WriteFileAtomic(p.RuleFile(it.ID), rule, 0o644); err != nil {
			return nil, err
		}
		res.Rule = p.RuleFile(it.ID)
	}
	return res, nil
}

// Keywords resolves the search keywords a new instance should carry.
//
// The chain is explicit > inherited > synthesised:
//
//  1. an explicit --keywords wins outright (the caller's responsibility);
//  2. otherwise the source's own Keywords are inherited verbatim, a localised
//     Keywords[<lang>] beating the generic one;
//  3. otherwise they are synthesised from the names the program already
//     answers to (see DerivedKeywords).
//
// `name` is the display name the instance will carry and `nameExplicit` says
// whether the user typed it or twin derived it. That distinction drives the
// only deliberate deviation from verbatim inheritance in step 2 (see
// inheritKeywords).
//
// Synthesising (step 3) is not a guess about meaning: it only reuses strings the
// source entry or the program file already carries, so the worst case is a
// redundant keyword, never a wrong one. That is what lets it be a default.
func Keywords(insp *Inspection, explicit, name string, nameExplicit bool) (value, source string) {
	if explicit != "" {
		return explicit, ""
	}
	if e := insp.Entry; e != nil {
		tags := desk.LocaleTags()
		src, why := "", ""
		if v, tag := e.LocalizedKeywords(tags...); v != "" {
			src, why = v, "源 Keywords["+tag+"]"
		} else if v := strings.Trim(e.Keywords, "; \t"); v != "" {
			// A bare ";" is a declared-but-empty list; treat it as absent
			// rather than emitting Keywords=; into the generated entry.
			src, why = e.Keywords, "源 Keywords"
		}
		if src != "" {
			return inheritKeywords(src, name, nameExplicit, why)
		}
	}
	return DerivedKeywords(insp)
}

// inheritKeywords hands back an inherited list, adding the instance's own name
// at the head in the one case where that is justified.
//
// A source list is the program's own statement about how it wants to be found,
// so it is inherited verbatim — vlc.desktop's `Player;Capture;DVD;…` stays
// English-only even though Name[zh_CN]=VLC 媒体播放器. The exception is a name
// the *user* typed: that is a term they have declared they want searchable, and
// it is the only way an instance of an English-keyworded program becomes
// findable under its own Chinese name. twin still never invents the term
// itself — it only reuses one a human put in the command line.
//
// It is skipped when the inherited list already speaks Chinese (nothing to add)
// or already contains the name (nothing new).
func inheritKeywords(src, name string, nameExplicit bool, why string) (value, source string) {
	if name == "" || !nameExplicit || hasCJK(src) || hasKeyword(src, name) {
		return src, why
	}
	out := name + ";" + src
	if !strings.HasSuffix(out, ";") {
		// The spec recommends a trailing separator for list values; a source
		// that omits it is kept as it was until we are the ones editing.
		out += ";"
	}
	return out, why + "；前置你显式指定的名字 " + name
}

// hasKeyword reports whether a ';'-separated list already contains kw.
func hasKeyword(list, kw string) bool {
	for _, k := range strings.Split(list, ";") {
		if strings.EqualFold(strings.TrimSpace(k), kw) {
			return true
		}
	}
	return false
}

// hasCJK reports whether s carries a CJK character — Han, kana or hangul.
//
// It answers "does this inherited keyword list already have a Chinese term in
// it?" A list mixing scripts (Keywords[zh_CN]=聊天;chat;) counts as yes, which
// is what keeps the prepend rule from firing twice on the same list.
func hasCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x3400 && r <= 0x4DBF, // CJK unified ideographs, extension A
			r >= 0x4E00 && r <= 0x9FFF, // CJK unified ideographs
			r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
			r >= 0x3040 && r <= 0x30FF, // hiragana + katakana
			r >= 0xAC00 && r <= 0xD7AF: // hangul syllables
			return true
		}
	}
	return false
}

// DerivedKeywords builds a keyword list from what the source program already
// calls itself: the localised and ASCII names, the GenericName, and the
// executable's basename — in that order, de-duplicated case-insensitively.
//
// For wechat.desktop (no Keywords, Name=wechat, Name[zh_CN]=微信) on a zh_CN
// desktop this yields "微信;wechat;" — precisely the value the hand-written
// 微信2 had to be told by hand.
func DerivedKeywords(insp *Inspection) (value, source string) {
	type cand struct{ val, why string }
	var cands []cand
	if e := insp.Entry; e != nil {
		tags := desk.LocaleTags()
		if v, tag := e.LocalizedName(tags...); v != "" {
			cands = append(cands, cand{v, "Name[" + tag + "]"})
		}
		if e.Name != "" {
			cands = append(cands, cand{e.Name, "Name"})
		}
		if v, tag := e.LocalizedGenericName(tags...); v != "" {
			cands = append(cands, cand{v, "GenericName[" + tag + "]"})
		} else if e.GenericName != "" {
			cands = append(cands, cand{e.GenericName, "GenericName"})
		}
	}
	if insp.Target != "" {
		cands = append(cands, cand{filepath.Base(insp.Target), "程序名"})
	}

	var kept, used []string
	seen := map[string]bool{}
	for _, c := range cands {
		v := strings.TrimSpace(c.val)
		// A keyword may not contain the list separator, and an empty one is
		// pointless noise in the search index.
		if v == "" || strings.ContainsRune(v, ';') {
			continue
		}
		if k := strings.ToLower(v); !seen[k] {
			seen[k] = true
			kept = append(kept, v)
			used = append(used, c.why)
		}
	}
	if len(kept) == 0 {
		return "", ""
	}
	// The trailing ';' is the form the Desktop Entry spec recommends for lists.
	return strings.Join(kept, ";") + ";",
		"源无 Keywords；由 " + strings.Join(used, "、") + " 推得"
}

// EntryBody is the prose of a generated desktop entry: what the program is
// called, what it says about itself, and how it wants to be found.
//
// It is a value of its own rather than six fields on inst.Instance because it is
// not part of the definition: the definition says *where* the entry's text lives
// (override.desktop), and these are the words twin puts in it when it has to
// write that file. Keeping them off the instance is what stops a reader from
// thinking "edit profile.yml to rename the program" — that file is the text.
type EntryBody struct {
	Name       string
	NameEn     string
	Comment    string
	CommentEn  string
	Keywords   string
	Categories string
}

// DeriveBody fills in the entry's prose for one id, recording where each part
// came from. It is the single derivation, used by `gen` (which then writes the
// file) and by `install` when the file has gone missing — two derivations would
// eventually disagree, and the disagreement would be a program whose menu entry
// renames itself the next time it is installed.
//
// The flags are consulted only when they are given: at install time there are no
// flags, so what comes back is the source entry's own words plus the sequence
// number. Anything a user typed at `gen` time lives on in the file itself.
func DeriveBody(insp *Inspection, id string, o Options) (EntryBody, []Derivation) {
	var der []Derivation
	// The key is the file *and* the line inside it: the prose moved from six
	// config keys into a file of its own, so "where does this value go" is now
	// two answers, and a table of five identical rows would hide the one the
	// reader needs (which line to edit in that file).
	note := func(key, val, src string) {
		der = append(der, Derivation{Key: "override.desktop " + key, Value: val, Source: src})
	}
	seq := seqOf(id)
	tags := desk.LocaleTags()
	body := EntryBody{
		CommentEn:  o.CommentEn,
		Categories: o.Categories,
	}
	entry := insp.Entry
	if entry == nil {
		entry = &desk.Entry{}
	}

	// The display name: the source's localised name when it has one for this
	// locale, otherwise its ASCII Name — either way with the sequence number, so
	// the instance reads as a sibling of the original rather than a stranger.
	body.Name = o.Name
	if body.Name == "" {
		if loc, tag := entry.LocalizedName(tags...); loc != "" {
			body.Name = loc + seq
			note("Name", body.Name, fmt.Sprintf("源 Name[%s]=%q + 序号 %s", tag, loc, seq))
		} else {
			body.Name = entry.Name + seq
			note("Name", body.Name, "源 Name + 序号 "+seq)
		}
	}

	body.NameEn = o.NameEn
	if body.NameEn == "" {
		body.NameEn = entry.Name + seq
		note("Name[en]", body.NameEn, "源 Name + 序号 "+seq)
	}
	if body.NameEn == body.Name {
		body.NameEn = "" // Name[en] identical to Name would only add noise
	}

	body.Comment = o.Comment
	if body.Comment == "" {
		if loc, tag := entry.LocalizedComment(tags...); loc != "" {
			body.Comment = loc
			note("Comment", body.Comment, "源 Comment["+tag+"]")
		} else if entry.Comment != "" {
			body.Comment = entry.Comment
			note("Comment", body.Comment, "源 Comment")
		} else {
			body.Comment = "由 twin 生成的第二个 " + entry.Name
		}
	}

	// Search keywords: explicit flag > rules.d > the source entry's own
	// Keywords > a list synthesised from the names the program already answers
	// to. Deriving them is the difference between `twin gen wechat` working
	// as-is and the user having to retype "微信;wechat" on every instance.
	//
	// o.Name != "" is passed separately from body.Name because the two are not
	// the same thing: an inherited list only gains the instance's own name when
	// the user typed that name, never when twin derived it (see Keywords).
	kwExplicit := o.Keywords
	kwWhy := ""
	if kwExplicit == "" && insp.Rule != nil && insp.Rule.Keywords != "" {
		kwExplicit = insp.Rule.Keywords
		kwWhy = "应用规则 " + ruleOrigin(insp.Rule) + " 里的 keywords"
	}
	keywords, why := Keywords(insp, kwExplicit, body.Name, o.Name != "")
	if kwWhy != "" {
		why = kwWhy
	}
	body.Keywords = keywords
	if o.Keywords == "" && keywords != "" {
		note("Keywords", keywords, why)
	}

	if body.Categories == "" && insp.Rule != nil && insp.Rule.Categories != "" {
		body.Categories = insp.Rule.Categories
		note("Categories", body.Categories, "应用规则 "+ruleOrigin(insp.Rule))
	}
	if body.Categories == "" && entry.Categories != "" {
		body.Categories = entry.Categories
	}
	return body, der
}

// EntryBytes renders the desktop entry twin generates for an instance — the
// text of the file `override.desktop` points at. Exported because `--dry-run`
// has to show byte-for-byte what would land on disk: a preview rendered by
// different code than the real thing is a lie.
//
// The whole entry is here, twins' own lines included (Exec, Icon, StartupWMClass,
// StartupNotify): it is generated once and copied verbatim from then on, so a
// file that is missing cannot be completed by patching it up at install time.
func EntryBytes(p inst.Paths, it *inst.Instance, body EntryBody) []byte {
	return RenderEntry(p, it, body).Bytes()
}

// RenderEntry is EntryBytes as the structured form.
func RenderEntry(p inst.Paths, it *inst.Instance, body EntryBody) desk.Render {
	wmClass := it.WMClass
	if wmClass == "" && it.Level.Renames() {
		// StartupWMClass is harmless and does matter on X11/XWayland, but the
		// basename is what actually claims a Wayland window. Empty in the
		// definition means "the id", so the default lives here rather than being
		// written out twice.
		wmClass = it.ID
	}
	return desk.Render{
		Name:      body.Name,
		NameEn:    body.NameEn,
		Comment:   body.Comment,
		CommentEn: body.CommentEn,
		Exec:      desk.ExecLine(p.Launch(it.ID), "%U"),
		Icon:      p.Icon(it.ID),
		Categ:     body.Categories,
		Keywords:  body.Keywords,
		WMClass:   wmClass,
		Notify:    true,
	}
}

// CheckCollision refuses to overwrite something that is not ours.
func CheckCollision(p inst.Paths, id string, force bool) error {
	if force {
		return nil
	}
	ours := p.Desktop(id)
	if sysx.Exists(ours) {
		if _, err := inst.Load(p.Profile(id)); err == nil {
			return nil // our own previous output; regenerating is the point
		}
		return fmt.Errorf("%s 已存在，但不是 twin 生成的 —— 加 --force 才会覆盖", ours)
	}
	for _, dir := range []string{"/usr/share/applications", "/usr/local/share/applications"} {
		if cand := filepath.Join(dir, id+".desktop"); sysx.Exists(cand) {
			return fmt.Errorf("系统的 %s 已占用这个 id —— 换一个 --as", cand)
		}
	}
	if sysx.Exists(p.Profile(id)) {
		// The definition's presence is not a half-finished install by itself:
		// `twin gen` writes it and stops there, and that is the ordinary way an
		// id ends up occupied (this branch is reached through an explicit --as).
		return fmt.Errorf("%s 已被占用（这个 id 的定义在，桌面项不在）：落盘用 twin install %s；"+
			"要重新来过就先 twin remove %s，或换一个 --as",
			p.Profile(id), id, id)
	}
	return nil
}

// CheckInstall guards the installing half.
//
// The definition is already ours by construction, so the only thing that can be
// occupied is the desktop entry: ours from an earlier install (reinstalling is
// the point) or somebody else's. Ownership is read back out of the file rather
// than assumed from the definition's existence — the desktop entry is the
// identity anchor, and guessing wrong here silently shadows another program.
func CheckInstall(p inst.Paths, id string, force bool) error {
	if force {
		return nil
	}
	if d := p.Desktop(id); sysx.Exists(d) && !desktopIsOurs(p, id, d) {
		return fmt.Errorf("%s 已存在，但不是 twin 生成的 —— 加 --force 才会覆盖", d)
	}
	for _, dir := range []string{"/usr/share/applications", "/usr/local/share/applications"} {
		if cand := filepath.Join(dir, id+".desktop"); sysx.Exists(cand) {
			return fmt.Errorf("系统的 %s 已占用这个 id —— 换一个 --as，或用 --force 让本实例遮蔽它", cand)
		}
	}
	return nil
}

// desktopIsOurs asks whether a desktop entry launches this instance. The Exec
// line naming our own launcher is the evidence; a third party's entry, a
// hand-written one, or a file that does not parse is not ours to overwrite.
func desktopIsOurs(p inst.Paths, id, path string) bool {
	e, err := desk.Parse(path)
	if err != nil {
		return false
	}
	return strings.Contains(e.Exec, p.Launch(id))
}

// Unwrap removes everything twin generated for an id.
type Removed struct {
	Paths []string
	Kept  []string
}

// Unwrap deletes the generated files. Private state is kept unless purge.
func Unwrap(p inst.Paths, id string, purge bool) (*Removed, error) {
	if err := inst.ValidateID(id); err != nil {
		return nil, err
	}
	out := &Removed{}
	// The tray table row goes with the instance: it is a claim about a program
	// that is about to stop existing, and the helper reads it every time it
	// draws an icon.
	for _, f := range []string{p.Desktop(id), p.Icon(id), p.InstanceDir(id), p.RuleFile(id), p.TrayTable(id)} {
		if !sysx.Exists(f) {
			continue
		}
		if err := os.RemoveAll(f); err != nil {
			return out, err
		}
		out.Paths = append(out.Paths, f)
	}
	state := p.StateDir(id)
	if sysx.Exists(state) {
		if purge {
			if err := os.RemoveAll(state); err != nil {
				return out, err
			}
			out.Paths = append(out.Paths, state)
		} else {
			out.Kept = append(out.Kept,
				state+"（保留：里面是程序的私有数据，确认不再需要后再删）")
		}
	}
	sort.Strings(out.Paths)
	return out, nil
}

// Refresh re-reads the config layers and appends what a snapshot is missing,
// returning one human-readable line per change.
//
// What that can be is now only the bwrap option lines: the entry's prose moved
// into a file of its own, where "empty" is not a state a key can be in — the
// file either exists or is regenerated whole (see Materialize).
//
// It never overwrites a value the snapshot already holds. `twin reinstall` exists to
// make the derived files agree with the snapshot; a "helpful" rewrite of a
// hand-edited field would undo exactly that, and would do it silently — the
// one failure mode this tool is built to avoid (设计 §3.5).
func Refresh(it *inst.Instance, insp *Inspection, cfg *config.Config) ([]string, error) {
	var notes []string
	rule := insp.Rule

	// The entry's prose is not refreshed any more, and cannot be: it lives in
	// the file `override.desktop` points at, which from the first byte on is the
	// user's document (install copies it verbatim). Filling in a "missing"
	// Keywords there would mean rewriting a file twin does not own — the same
	// round trip through the parser that loses hand-made comments. A missing
	// file is regenerated wholesale instead, by install, where an absent file is
	// the only signal there is.
	//
	// The bwrap region is additive by nature, so refreshed tokens are appended
	// rather than substituted; a token that is already in the list is skipped,
	// which keeps the operation idempotent. The base is not touched: it is part
	// of the definition, and `--refresh` only ever adds what the environment
	// layers say (设计 §3.5).
	var want []string
	want = append(want, cfg.Args...)
	if rule != nil {
		want = append(want, rule.Args...)
	}
	for _, tok := range want {
		if containsToken(it.Extras, tok) {
			continue
		}
		if err := inst.CheckBwrapToken(tok); err != nil {
			return notes, err
		}
		it.Extras = append(it.Extras, tok)
		notes = append(notes, "bwrap.extras += "+tok)
	}

	if len(notes) > 0 {
		if err := it.Check(); err != nil {
			return notes, err
		}
	}
	return notes, nil
}

// SessionWarning flags a snapshot whose sandbox decision no longer matches the
// session it is about to run in. It is a warning, not a rewrite: the snapshot
// records intent, and silently changing it here is the thing reinstall must not do.
func SessionWarning(it *inst.Instance) string {
	if os.Getenv("WAYLAND_DISPLAY") != "" || !tmpfsHidesTmp(it) {
		return ""
	}
	return "定义里给 /tmp 盖了 tmpfs，但当前会话没有 WAYLAND_DISPLAY：" +
		"程序若退到 X11 就连不上 X（/tmp/.X11-unix/X0 已被屏蔽）"
}

// tmpfsHidesTmp asks whether the definition hides /tmp behind an empty tmpfs.
//
// There is exactly one place it can be said now — a `--tmpfs /tmp` line in
// bwrap.extras — so the line is split on whitespace and the option's own value
// looked at. Matching that one literal pair is not the arity table the
// passthrough region refuses to build (设计 §2.1): the pattern is one option
// name twin itself writes, nothing is derived from the match, and the only
// thing it feeds is the session warning above.
func tmpfsHidesTmp(it *inst.Instance) bool {
	for _, line := range it.Extras {
		toks := strings.Fields(line)
		if len(toks) < 2 {
			continue
		}
		if toks[0] == "--tmpfs" && strings.TrimRight(toks[1], "/") == "/tmp" {
			return true
		}
	}
	return false
}

// tmpfsCovers reports whether a tmpfs list covers path. Entries are compared
// with trailing slashes ignored: "/tmp/" and "/tmp" are the same directory to
// everyone except a shell.
func tmpfsCovers(list []string, path string) bool {
	for _, e := range list {
		if strings.TrimRight(e, "/") == path {
			return true
		}
	}
	return false
}

// withoutPath returns list minus every entry that names path (same comparison),
// and keeps "no list" and "empty list" apart the way profile.yml does.
func withoutPath(list []string, path string) []string {
	var out []string
	for _, e := range list {
		if strings.TrimRight(e, "/") == path {
			continue
		}
		out = append(out, e)
	}
	return out
}

func containsToken(list []string, tok string) bool {
	for _, t := range list {
		if t == tok {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
