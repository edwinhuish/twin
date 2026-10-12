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
	// which is what `regen` uses: replaying a snapshot must not depend on a
	// file the user may have edited since (设计 §3.5 rule three).
	Cfg *config.Config
	// Bwrap is the CLI `--` passthrough region: bubblewrap option tokens that
	// twin forwards verbatim (设计 §2). It is not parsed here; inst.Check
	// only refuses the three token classes that cannot work.
	Bwrap   []string
	NoTmpfs bool
	// NoTray opts this one instance out of the host tray override. The
	// machine-wide switch is config.toml's defaults.tray_override; either one
	// turning it off is enough.
	NoTray bool
	// IconSize is environment, not intent, so it is resolved here but never
	// written to the snapshot (设计 §3.6): another machine has another DPI.
	IconSize int
	Force    bool
}

// IconSizeFor resolves the icon edge length from the CLI, then config.toml,
// then the built-in default. Kept next to the other precedence rules so the
// order lives in one place.
func (o Options) IconSizeFor(cfg *config.Config) int {
	if o.IconSize > 0 {
		return o.IconSize
	}
	if cfg != nil && cfg.Defaults.IconSize > 0 {
		return cfg.Defaults.IconSize
	}
	return inst.DefaultIconSize
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
			"应用规则 %s：%s（%d 条路径映射 ⇒ bind）", matched, where, len(insp.Presets)))
	} else {
		insp.Notes = append(insp.Notes,
			"没有该程序的状态路径知识：本实例只做身份包裹。若它有单实例锁或共享配置，"+
				"在 "+p.RulesDir()+"/"+key+".toml 里写一条 bind（twin rules --init "+key+" 生成草稿），"+
				"或只给这一个实例改快照里的 bind")
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
func Plan(p inst.Paths, o Options, insp *Inspection) (*inst.Instance, []Derivation, error) {
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
		return nil, nil, err
	}
	seq := seqOf(id)
	tags := desk.LocaleTags()

	// Level: CLI > rules.d > config.toml > built-in. Each layer names its
	// origin when it wins, because "which file set this to redirect?" is the
	// first question a broken identity raises.
	level := o.Level
	if level == "" {
		switch {
		case rule != nil && rule.Level != "":
			lvl, err := inst.ParseLevel(rule.Level)
			if err != nil {
				return nil, nil, err
			}
			level = lvl
			note("level", string(level), "应用规则 "+ruleOrigin(rule))
		case cfg.Defaults.Level != "":
			lvl, err := inst.ParseLevel(cfg.Defaults.Level)
			if err != nil {
				return nil, nil, err
			}
			level = lvl
			note("level", string(level), "config.toml defaults.level")
		default:
			level = inst.LevelAlias
		}
	}
	if !level.Implemented() {
		return nil, nil, fmt.Errorf("level=%s 还没实现：阶梯上只有 none 与 alias 通过了实测", level)
	}

	// The display name: the source's localised name when it has one for this
	// locale, otherwise its ASCII Name — either way with the sequence number, so
	// the instance reads as a sibling of the original rather than a stranger.
	name := o.Name
	if name == "" {
		if loc, tag := insp.Entry.LocalizedName(tags...); loc != "" {
			name = loc + seq
			note("name", name, fmt.Sprintf("源 Name[%s]=%q + 序号 %s", tag, loc, seq))
		} else {
			name = insp.Entry.Name + seq
			note("name", name, "源 Name + 序号 "+seq)
		}
	}

	nameEn := o.NameEn
	if nameEn == "" {
		nameEn = insp.Entry.Name + seq
		note("name_en", nameEn, "源 Name + 序号 "+seq)
	}
	if nameEn == name {
		nameEn = "" // Name[en] identical to Name would only add noise
	}

	comment := o.Comment
	if comment == "" {
		if loc, tag := insp.Entry.LocalizedComment(tags...); loc != "" {
			comment = loc
			note("comment", comment, "源 Comment["+tag+"]")
		} else if insp.Entry.Comment != "" {
			comment = insp.Entry.Comment
			note("comment", comment, "源 Comment")
		} else {
			comment = "由 twin 生成的第二个 " + insp.Entry.Name
		}
	}

	// Search keywords: explicit flag > rules.d > the source entry's own
	// Keywords > a list synthesised from the names the program already answers
	// to. Deriving them is the difference between `twin new wechat` working
	// as-is and the user having to retype "微信;wechat" on every instance.
	//
	// o.Name != "" is passed separately from name because the two are not the
	// same thing: an inherited list only gains the instance's own name when the
	// user typed that name, never when twin derived it (see Keywords).
	kwExplicit := o.Keywords
	kwWhy := ""
	if kwExplicit == "" && rule != nil && rule.Keywords != "" {
		kwExplicit = rule.Keywords
		kwWhy = "应用规则 " + ruleOrigin(rule) + " 里的 keywords"
	}
	keywords, why := Keywords(insp, kwExplicit, name, o.Name != "")
	if kwWhy != "" {
		why = kwWhy
	}
	if o.Keywords == "" && keywords != "" {
		note("keywords", keywords, why)
	}

	categories := o.Categories
	if categories == "" && rule != nil && rule.Categories != "" {
		categories = rule.Categories
		note("categories", categories, "应用规则 "+ruleOrigin(rule))
	}

	badge := o.Badge
	if badge == "" && seq != "" {
		badge = seq
		note("badge", badge, "从 id 的尾部数字提取")
	}

	it := &inst.Instance{
		ID:         id,
		Name:       name,
		NameEn:     nameEn,
		Comment:    comment,
		CommentEn:  o.CommentEn,
		Keywords:   keywords,
		Categories: categories,
		Source:     insp.SourceDesktop,
		Level:      level,
		WMClass:    "",
		Notify:     true,
		IconSource: firstNonEmpty(o.Icon, insp.IconSource),
		Badge:      badge,
		BadgeStyle: firstNonEmpty(o.BadgeStyle, cfg.Defaults.BadgeStyle, icon.StyleCorner),
		Target:     insp.Target,
		Alias:      id,
		Env:        map[string]string{},
		NestedNS:   insp.Traits.NeedsNestedNS,
	}
	if level.Renames() {
		// StartupWMClass is harmless and does matter on X11/XWayland, but the
		// basename is what actually claims a Wayland window.
		it.WMClass = id
	}

	// A program that builds a sandbox of its own needs a bubblewrap that can
	// still create a user namespace inside, which the system one cannot (see
	// internal/userns). Recording the need in the snapshot matters because
	// `regen` deliberately does not re-scan the binary: without this line a
	// replay would quietly fall back to the system bubblewrap and produce a
	// launcher that dies at startup.
	if it.NestedNS {
		note("isolation.nested_ns", "true",
			"二进制含 Chromium 沙箱特征（chrome-sandbox / ELECTRON_RUN_AS_NODE / libcef.so）")
	}

	// The tray override table is host state, and the decision to touch it is
	// recorded in the snapshot so `regen` reproduces it without reading
	// config.toml (设计 §3.5). Either switch turning it off wins.
	it.TrayOverride = cfg.Defaults.TrayOverride && !o.NoTray
	switch {
	case o.NoTray:
		note("tray.override", "false", "--no-tray")
	case !cfg.Defaults.TrayOverride:
		note("tray.override", "false", "config.toml defaults.tray_override")
	}

	// Refuse a missing icon *before* anything is written. A desktop entry
	// without a resolvable Icon= shows up as the gear placeholder, which is
	// exactly the bug this tool exists to fix. Checking here rather than in
	// Materialize matters: Materialize writes the launcher and profile.yml
	// first, so failing at the icon would leave a half-built instance behind
	// (and the next wrap would then refuse with "建议先 unwrap").
	if it.IconSource == "" {
		return nil, nil, fmt.Errorf("找不到可用的源图标：请用 --icon <png|svg> 指定（拒绝静默生成齿轮占位图）")
	}

	// A multi-call binary cannot survive a rename: it dispatches on argv[0],
	// which under LevelAlias is the alias path. Refusing beats shipping an
	// instance that prints "unknown program '<alias>'" every launch.
	if level.Renames() && insp.Traits.MultiCall {
		return nil, nil, fmt.Errorf("目标是多合一二进制（BusyBox / coreutils），靠 argv[0] 分派子命令：" +
			"alias 改名后它会不认识自己（实测报 unknown program）。这个程序暂时没有可用的身份级别")
	}

	// /tmp policy. A tmpfs /tmp takes /tmp/.X11-unix/X0 with it, so it is only
	// safe for a program that actually talks Wayland. `tmpfs_unless_x11` in
	// config.toml turns the guess off for machines where the trait scan is
	// known to be wrong.
	switch {
	case o.NoTmpfs:
		it.Tmpfs = nil
	case len(o.Tmpfs) > 0:
		it.Tmpfs = append([]string(nil), o.Tmpfs...)
	case rule != nil && len(rule.Tmpfs) > 0:
		it.Tmpfs = append([]string(nil), rule.Tmpfs...)
		note("tmpfs", strings.Join(rule.Tmpfs, " "), "应用规则 "+ruleOrigin(rule))
	case insp.Traits.Wayland && cfg.Defaults.TmpfsUnlessX11:
		it.Tmpfs = []string{"/tmp"}
	default:
		it.Tmpfs = nil
	}

	// Path maps come from the rule and from nowhere else. There is deliberately
	// no CLI flag for them: a mapping is the one thing in a wrap that varies
	// per program and is silently wrong when wrong, so it is written into a
	// file the user reads before anything is installed (设计 §3.3).
	for _, pr := range insp.Presets {
		it.Binds = append(it.Binds, inst.Bind{
			Source:   expandID(pr.Source, id),
			Target:   expandID(pr.Target, id),
			ReadOnly: pr.ReadOnly,
		})
	}

	// Passthrough tail, low priority first (设计 §3.5 rule two). Later tokens
	// shadow earlier mounts on the same destination, so putting the global
	// default first is enough to make the CLI win — no conflict resolution
	// code is owed.
	it.Bwrap = append(it.Bwrap, cfg.Bwrap...)
	if rule != nil {
		it.Bwrap = append(it.Bwrap, rule.Bwrap...)
	}
	it.Bwrap = append(it.Bwrap, o.Bwrap...)
	if len(it.Bwrap) == 0 {
		it.Bwrap = nil
	}
	for _, tok := range it.Bwrap {
		if err := inst.CheckBwrapToken(tok); err != nil {
			return nil, nil, fmt.Errorf("%s：%w", bwrapSource(o, rule, cfg), err)
		}
	}

	if err := it.Check(); err != nil {
		return nil, nil, err
	}
	return it, der, nil
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
	if len(o.Bwrap) > 0 {
		for _, tok := range o.Bwrap {
			if inst.CheckBwrapToken(tok) != nil {
				return "命令行 `--` 透传的 " + tok
			}
		}
	}
	if r != nil {
		for _, tok := range r.Bwrap {
			if inst.CheckBwrapToken(tok) != nil {
				return ruleOrigin(r) + " 里的 " + tok
			}
		}
	}
	for _, tok := range c.Bwrap {
		if inst.CheckBwrapToken(tok) != nil {
			return c.Path + " [bwrap] extra 里的 " + tok
		}
	}
	return "bwrap 透传 token"
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
// iconSize is passed in rather than read from the instance because it is
// environment: it must not be frozen into the snapshot (设计 §3.6). A
// non-positive value falls back to the built-in default.
func Materialize(p inst.Paths, it *inst.Instance, insp *Inspection, plan *sandbox.Plan, iconSize int) (*Result, error) {
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

	// 1. private state directories. A `:ro` source is skipped: it is somebody
	//    else's directory, and creating it would turn "you gave the wrong path"
	//    into an empty read-only mount that looks like it worked.
	if err := sysx.EnsureDir(p.StateDir(it.ID)); err != nil {
		return nil, err
	}
	for _, b := range it.Binds {
		if b.ReadOnly {
			continue
		}
		if err := sysx.EnsureDir(p.Expand(b.Source)); err != nil {
			return nil, err
		}
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

	// 3. the instance definition (the human-editable source of truth).
	cfg, err := it.Render()
	if err != nil {
		return nil, err
	}
	if err := sysx.WriteFileAtomic(p.Profile(it.ID), cfg, 0o644); err != nil {
		return nil, err
	}

	// 4. the icon.
	iconSpec := icon.Spec{
		Source: it.IconSource,
		Name:   it.Name,
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

	// 5. the desktop entry — the anchor of the identity.
	render := DesktopEntry(p, it, insp)
	if err := sysx.WriteFileAtomic(p.Desktop(it.ID), render.Bytes(), 0o644); err != nil {
		return nil, err
	}

	// 6. the L5 rule. Written last so a rule can never outlive the desktop
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

// WriteSnapshot writes only profile.yml — the first half of a two-phase wrap.
//
// It shares Render with Materialize, so the file the user is invited to edit is
// byte for byte the one a one-step wrap would have written: two renderers would
// be two chances to disagree. Nothing else is touched, and that is the point of
// the split — until `--publish`, the only thing on disk is a file meant to be
// edited.
func WriteSnapshot(p inst.Paths, it *inst.Instance) (string, error) {
	body, err := it.Render()
	if err != nil {
		return "", err
	}
	path := p.Profile(it.ID)
	if err := sysx.WriteFileAtomic(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Snapshot is one profile.yml found by scanning, plus the one piece of state
// a two-phase wrap needs in order to interpret it.
type Snapshot struct {
	Instance *inst.Instance
	// Path is the snapshot file itself.
	Path string
	// Published says the desktop entry is on disk, so this instance is live.
	// A snapshot without one is a configuration somebody wrote and has not
	// installed yet — the normal middle of a two-phase wrap, which is why the
	// state has a name instead of being reported as a missing file.
	Published bool
}

// SnapshotsFor returns every instance configured from this source desktop entry.
//
// The two halves of `twin new` use it to agree on which instance they mean
// without re-deriving the id: once phase one has written the snapshot, that id
// is taken (freeID checks for exactly that), so a re-derivation would quietly
// move on to wechat3 and install something nobody asked for. Matching on the
// *resolved* source path is what makes `twin new wechat`, `twin new wechat
// --publish` and `twin new /usr/share/applications/wechat.desktop --publish`
// all mean the same instance.
func SnapshotsFor(p inst.Paths, sourceDesktop string) ([]Snapshot, error) {
	all, err := inst.List(p)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, it := range all {
		if it.Source != sourceDesktop {
			continue
		}
		out = append(out, Snapshot{
			Instance:  it,
			Path:      p.Profile(it.ID),
			Published: sysx.Exists(p.Desktop(it.ID)),
		})
	}
	return out, nil
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

// DesktopEntry builds the desktop entry for an instance. Exported because
// `--dry-run` has to show byte-for-byte what would land on disk — a preview
// rendered by different code than the real thing is a lie.
func DesktopEntry(p inst.Paths, it *inst.Instance, insp *Inspection) desk.Render {
	var categ, kw string
	if insp.Entry != nil {
		categ, kw = insp.Entry.Categories, insp.Entry.Keywords
	}
	return desk.Render{
		Name:      it.Name,
		NameEn:    it.NameEn,
		Comment:   it.Comment,
		CommentEn: it.CommentEn,
		Exec:      desk.ExecLine(p.Launch(it.ID), "%U"),
		Icon:      p.Icon(it.ID),
		Categ:     firstNonEmpty(it.Categories, categ),
		Keywords:  firstNonEmpty(it.Keywords, kw),
		WMClass:   it.WMClass,
		Notify:    it.Notify,
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
		return fmt.Errorf("%s 已存在、桌面项却不在：这是一份写好但还没落盘的配置 —— "+
			"落盘用 twin new <source> --as %s --publish；要按新参数重写它，先 twin remove %s",
			p.Profile(id), id, id)
	}
	return nil
}

// CheckInstall guards the publish half of a two-phase wrap.
//
// The snapshot is already ours by construction, so the only thing that can be
// occupied is the desktop entry: ours from an earlier publish (republishing is
// the point) or somebody else's. Ownership is read back out of the file rather
// than assumed from the snapshot's existence — the desktop entry is the
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
	for _, f := range []string{p.Desktop(id), p.Icon(id), p.InstanceDir(id), p.RuleFile(id)} {
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

// Refresh re-reads the config layers and fills in the fields a snapshot left
// empty, returning one human-readable line per change.
//
// It never overwrites a value the snapshot already holds. `regen` exists to
// make the derived files agree with the snapshot; a "helpful" rewrite of a
// hand-edited field would undo exactly that, and would do it silently — the
// one failure mode this tool is built to avoid (设计 §3.5).
func Refresh(it *inst.Instance, insp *Inspection, cfg *config.Config) ([]string, error) {
	var notes []string
	rule := insp.Rule

	if it.Keywords == "" {
		var kw, why string
		switch {
		case rule != nil && rule.Keywords != "":
			kw, why = rule.Keywords, "keywords（应用规则 "+ruleOrigin(rule)+"）"
		default:
			kw, why = Keywords(insp, "", it.Name, false)
			if why != "" {
				why = "keywords（" + why + "）"
			} else {
				why = "keywords"
			}
		}
		if kw != "" {
			it.Keywords = kw
			notes = append(notes, why+" = "+kw)
		}
	}

	if it.Categories == "" {
		switch {
		case rule != nil && rule.Categories != "":
			it.Categories = rule.Categories
			notes = append(notes, "categories（应用规则 "+ruleOrigin(rule)+"）= "+rule.Categories)
		case insp.Entry != nil && insp.Entry.Categories != "":
			it.Categories = insp.Entry.Categories
			notes = append(notes, "categories（源桌面项）= "+insp.Entry.Categories)
		}
	}

	// The bwrap region is additive by nature, so refreshed tokens are appended
	// rather than substituted; a token that is already in the list is skipped,
	// which keeps the operation idempotent.
	var want []string
	want = append(want, cfg.Bwrap...)
	if rule != nil {
		want = append(want, rule.Bwrap...)
	}
	for _, tok := range want {
		if containsToken(it.Bwrap, tok) {
			continue
		}
		if err := inst.CheckBwrapToken(tok); err != nil {
			return notes, err
		}
		it.Bwrap = append(it.Bwrap, tok)
		notes = append(notes, "bwrap.extra += "+tok)
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
// records intent, and silently changing it here is the thing regen must not do.
func SessionWarning(it *inst.Instance) string {
	if len(it.Tmpfs) == 0 || os.Getenv("WAYLAND_DISPLAY") != "" {
		return ""
	}
	return "快照里有 --tmpfs " + strings.Join(it.Tmpfs, " ") +
		"，但当前会话没有 WAYLAND_DISPLAY：程序若退到 X11 就连不上 X（/tmp/.X11-unix/X0 已被屏蔽）"
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
