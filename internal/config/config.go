// Package config reads the two layers a user owns: the machine-scoped
// config.toml and the per-application rules.d/<app>.toml knowledge files.
//
// The split is not decoration. The two layers change for different reasons and
// at different rates (设计 §3.1): config.toml changes when you move to another
// machine, rules.d changes when you learn a new program's habits. Merging them
// would copy WeChat's knowledge into every instance file, and a global default
// would then leak into existing instances on the next regeneration.
//
// Nothing here is authoritative on its own. Precedence is resolved by
// generate.Plan, which sees the CLI too:
//
//	CLI > profile.yml (snapshot) > rules.d/<app> > config.toml > built-in
//
// and array-shaped values (bwrap extra, tmpfs, binds) are concatenated low
// priority first, so a later mount shadows an earlier one on the same
// destination — bubblewrap already implements the conflict rule (设计 §3.5).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/icon"
	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/tomlz"
)

// Defaults are the machine-scoped knobs. They describe "this computer", never
// "this instance" — which is exactly why they never reach profile.yml.
type Defaults struct {
	Level          string
	IconSize       int
	BadgeStyle     string
	TmpfsUnlessX11 bool
	// TrayOverride says whether twin should point the host's tray override
	// table at each new instance's icon. It lives here rather than in a flag
	// because the reason to turn it off is a property of the machine, not of
	// one program: a different tray host, or a session that already handles
	// tray icons some other way.
	TrayOverride bool
}

// Tools are the programs twin shells out to. Their paths are environment,
// not intent: another machine will have them elsewhere.
type Tools struct {
	Bwrap  string // "" means "whatever is on PATH"
	Python string
	// BwrapCopy is the private bubblewrap copy a program that needs a nested
	// user namespace must run under. /usr/bin/bwrap's AppArmor profile stacks
	// unpriv_bwrap onto every descendant and unpriv_bwrap carries
	// `audit deny capability`, so Chromium's sandbox cannot start inside it —
	// measured 2026-10-12: a nested unshare(CLONE_NEWUSER|CLONE_NEWNS) fails
	// with EPERM. A copy at a path nobody profiles escapes that, at the price
	// of needing a trampoline (see USerns).
	//
	//	""     follow whatever Bwrap resolves to (no copy)
	//	"auto" copy to Paths.BwrapCopy() when an instance needs it
	//	else   use this path as the copy
	BwrapCopy string
}

// USerns is this machine's user-namespace situation and how twin works
// around it.
type USerns struct {
	// Trampoline names the binary twin execs to acquire an AppArmor label
	// that grants userns, before that process spawns the bubblewrap copy.
	//
	//	"auto" (or "") discover the best one — the normal case, and what keeps
	//	              a wrap portable: run twin again on another machine and
	//	              it picks *that* machine's trampoline
	//	"off"         never use one; print the profile to install instead
	//	else          use exactly this path
	Trampoline string
}

// IconStyle says where to look when the source entry names a theme icon
// instead of a file.
type IconStyle struct {
	Theme string
}

// Config is the environment layer.
type Config struct {
	Path     string // ~/.config/twin/config.toml
	Exists   bool   // false when the user never wrote one
	Defaults Defaults
	Tools    Tools
	USerns   USerns
	Icon     IconStyle
	// Bwrap is the global passthrough tail, appended to every instance's
	// bwrap option region. One element is one token (设计 §3.2).
	Bwrap []string
}

// Rule is the application-knowledge layer: what is true about a *program*,
// independent of how many instances of it you make. `{id}` is substituted at
// instantiation time so one rule serves any number of instances.
type Rule struct {
	Path       string // the file it came from ("" for a built-in rule)
	Builtin    bool   // true when this is the compiled-in fallback
	App        string
	Level      string
	Keywords   string
	Categories string
	Tmpfs      []string
	Binds      []inst.Bind
	Bwrap      []string
}

// Builtin is the set of defaults compiled into twin. Every field is
// overridable from config.toml; the values here are exactly what the tool did
// before the file existed.
func Builtin() *Config {
	return &Config{
		Defaults: Defaults{
			Level:          string(inst.LevelAlias),
			IconSize:       inst.DefaultIconSize,
			BadgeStyle:     icon.StyleCorner,
			TmpfsUnlessX11: true,
			TrayOverride:   true,
		},
		Tools: Tools{
			Python: "python3",
			// "auto" copies only when an instance actually needs a nested
			// user namespace, so a plain GTK program still runs off the
			// system bubblewrap and inherits any fix the distribution ships.
			BwrapCopy: "auto",
		},
		USerns: USerns{Trampoline: "auto"},
		Icon:   IconStyle{Theme: "hicolor"},
	}
}

// Load reads config.toml, falling back to the built-in values.
//
// A missing file is normal and produces no error. A file that exists but does
// not parse is a hard error: silently ignoring a typo here would mean the user
// edits config.toml, sees nothing change, and has no way to find out why.
func Load(p inst.Paths) (*Config, error) {
	c := Builtin()
	c.Path = p.GlobalConfig()

	data, err := os.ReadFile(c.Path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读不到 %s：%w", c.Path, err)
	}
	root, err := tomlz.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Path, err)
	}
	c.Exists = true

	d := root.Tree("defaults")
	setStr(d, "level", &c.Defaults.Level)
	if d.Has("icon_size") {
		c.Defaults.IconSize = int(d.Int("icon_size"))
	}
	setStr(d, "badge_style", &c.Defaults.BadgeStyle)
	if d.Has("tmpfs_unless_x11") {
		c.Defaults.TmpfsUnlessX11 = d.Bool("tmpfs_unless_x11")
	}
	if d.Has("tray_override") {
		c.Defaults.TrayOverride = d.Bool("tray_override")
	}

	t := root.Tree("tools")
	setStr(t, "bwrap", &c.Tools.Bwrap)
	setStr(t, "python", &c.Tools.Python)
	setStr(t, "bwrap_copy", &c.Tools.BwrapCopy)

	setStr(root.Tree("userns"), "trampoline", &c.USerns.Trampoline)

	setStr(root.Tree("icon"), "theme", &c.Icon.Theme)

	c.Bwrap = root.Tree("bwrap").StrList("extra")

	if lvl, err := inst.ParseLevel(c.Defaults.Level); err != nil {
		return nil, fmt.Errorf("%s: defaults.level = %q：%w", c.Path, c.Defaults.Level, err)
	} else if !lvl.Implemented() {
		return nil, fmt.Errorf("%s: defaults.level = %q 还没实现（只有 none / alias / redirect 通过实测）", c.Path, c.Defaults.Level)
	}
	if c.Defaults.IconSize <= 0 {
		return nil, fmt.Errorf("%s: defaults.icon_size = %d，必须是正数", c.Path, c.Defaults.IconSize)
	}
	return c, nil
}

func setStr(t *tomlz.Tree, key string, dst *string) {
	if t.Has(key) {
		*dst = t.Str(key)
	}
}

// ------------------------------------------------------------ application rules

// RuleKeys are the names a rule file may be filed under, most specific first.
//
// Two names are tried because they answer different questions: the desktop
// basename is how the user thinks of the program ("wechat"), while the real
// binary's basename is what survives a repackaged source entry. Either is a
// valid file name for rules.d.
func RuleKeys(desktopBase, targetBase string) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSuffix(filepath.Base(s), ".desktop")
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\x00") {
			return
		}
		for _, seen := range out {
			if seen == s {
				return
			}
		}
		out = append(out, s)
	}
	add(desktopBase)
	add(targetBase)
	return out
}

// FindRule returns the first rule that applies, plus the key it was found
// under. A nil rule means "we know nothing about this program" — which is a
// legitimate answer and produces the bare identity wrap.
func FindRule(p inst.Paths, keys ...string) (*Rule, string, error) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		r, err := loadRule(p, key)
		if err != nil {
			return nil, "", err
		}
		if r != nil {
			return r, key, nil
		}
	}
	return nil, "", nil
}

// loadRule merges the built-in entry for an app with the user's file of the
// same name. The file wins field by field, so overriding one keyword list does
// not silently drop the state paths that ship with the tool.
//
// Array-shaped fields (binds, tmpfs, bwrap extra) replace rather than append
// when the file sets them: a user who writes bind = [...] is describing the
// whole set of paths they want, and half-inheriting would make the file a lie.
func loadRule(p inst.Paths, key string) (*Rule, error) {
	builtin, hasBuiltin := BuiltinRules()[key]
	out := &Rule{App: key}
	if hasBuiltin {
		out.Level = builtin.Level
		out.Keywords = builtin.Keywords
		out.Categories = builtin.Categories
		out.Tmpfs = builtin.Tmpfs
		out.Binds = builtin.Binds
		out.Bwrap = builtin.Bwrap
		out.Builtin = true
	}

	path := filepath.Join(p.RulesDir(), key+".toml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if !hasBuiltin {
			return nil, nil
		}
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读不到 %s：%w", path, err)
	}
	root, err := tomlz.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out.Path = path
	out.Builtin = false

	if root.Has("app") {
		out.App = root.Str("app")
	}
	setStr(root, "level", &out.Level)
	setStr(root, "keywords", &out.Keywords)
	setStr(root, "categories", &out.Categories)

	if root.Has("tmpfs") {
		out.Tmpfs = root.StrList("tmpfs")
	}
	// The pre-string-list `[[bind]]` form is refused by name. Reading it as "no
	// mappings" would turn a file that says where the data is into a file that
	// says the data is shared, and nothing on screen would say so — the exact
	// silent failure rules.d exists to prevent.
	if len(root.TableList("bind")) > 0 {
		return nil, fmt.Errorf("%s: bind 现在是一句式字符串列表（\"源:目标[:ro]\"），"+
			"不再是 [[bind]] 表 —— 请改成 bind = [\"源:目标\", …]", path)
	}
	if specs, err := root.StrListStrict("bind"); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	} else if specs != nil {
		out.Binds = nil
		for _, spec := range specs {
			b, err := inst.ParseBind(spec)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			out.Binds = append(out.Binds, b)
		}
	}
	if root.Tree("bwrap").Has("extra") {
		out.Bwrap = root.Tree("bwrap").StrList("extra")
	}

	if out.Level != "" {
		lvl, err := inst.ParseLevel(out.Level)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !lvl.Implemented() {
			return nil, fmt.Errorf("%s: level = %q 还没实现（只有 none / alias / redirect 通过实测）", path, out.Level)
		}
	}
	return out, nil
}

// BuiltinRules is the small knowledge base of state paths that ships with
// twin, stored in the same shape a user's rules.d file would be.
//
// It is deliberately short: guessing a wrong path silently is worse than
// asking the user to write one down. WeChat is here because it is the tested
// case.
//
// The split is not arbitrary. WeChat keeps per-account data in
// ~/Documents/xwechat_files/<wxid>/ and machine-wide provider state in
// .../all_users. The hand-written 微信2 shares the former (received files stay
// common to both logins) and isolates the latter — all_users is what actually
// collides between two accounts. A source equal to its target is the in-place
// writable upgrade, not a no-op (设计 §7).
func BuiltinRules() map[string]*Rule {
	return map[string]*Rule{
		"wechat": {
			App: "wechat",
			Binds: []inst.Bind{
				{Source: "~/.local/state/twin/{id}/xwechat", Target: "~/.xwechat"},
				{Source: "~/Documents/xwechat_files", Target: "~/Documents/xwechat_files"},
				{Source: "~/.local/state/twin/{id}/all_users", Target: "~/Documents/xwechat_files/all_users"},
			},
		},
	}
}

// Sample renders a commented config.toml for `twin doctor` to offer.
func Sample() string {
	return `# twin 全局默认 —— 只描述"这台机器"，不描述"哪个实例"

[defaults]
level       = "alias"
icon_size   = 256
badge_style = "corner"        # corner | number | dot
tmpfs_unless_x11 = true
# 生成实例时是否把宿主的托盘图标覆盖表指向本实例的图标。
# twin 只在真的跑在图形会话里时才写得进去（热重载，立刻生效）；
# 无会话总线时它会打印一条命令并以退出码 3 结束。本机不用 appindicator
# 托盘宿主时，把这项关掉，自检就不会再报那一项。
tray_override = true

[tools]
# 留空则用 PATH 里的 bwrap。
bwrap  = ""
# 私有副本。系统 /usr/bin/bwrap 的 AppArmor profile 会给它的每个后代叠上
# unpriv_bwrap（内含 audit deny capability），于是 Chromium / Electron 在沙箱里
# 建不出嵌套 user namespace，只能 FATAL。副本放在没人挂 profile 的路径上就绕开
# 了这一点 —— 代价是需要一个"跳板"（见下面的 [userns]）。
#   ""     = 不做副本，直接用上面的 bwrap（Electron 类会起不来）
#   "auto" = 需要时复制到 ~/.local/share/twin/bin/bwrap
#   路径   = 用你指定的那一份
bwrap_copy = "auto"
python = "python3"            # 仅用于 SVG 光栅化与 GLib 桌面项自检

[userns]
# 跳板：kernel.apparmor_restrict_unprivileged_userns=1 时，只有 profile 里写了
# userns 许可的程序才能建非特权 user namespace。twin 会找一个已安装的、带
# "路径附着 + flags=(unconfined) + userns" profile 的程序（通常是某个 Electron
# 应用），先 exec 它拿到标签，再由它 spawn 上面的 bwrap 副本。这正是换一台机器
# 只要重跑一次 twin 就能用的原因 —— 跳板由它当场重新挑。
#   "auto" = 自动探测（推荐）
#   "off"  = 不探测；需要时打印一份 profile 让你 sudo 装上
#   路径   = 指定跳板
trampoline = "auto"

[icon]
theme = "hicolor"             # 源 Icon= 是主题名时的查找主题

[bwrap]
# 追加到**所有**实例 bwrap 选项区的尾部。一个元素 = 一个 token，原样拼接。
extra = []
`
}
