package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/inst"
)

// write creates a file under home, making parent directories first.
func write(t *testing.T, home, rel, body string) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithoutFileUsesBuiltins(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Exists {
		t.Error("Exists 应为 false —— 用户没写过 config.toml")
	}
	if c.Defaults.Level != "alias" || c.Defaults.IconSize != inst.DefaultIconSize {
		t.Errorf("内置默认不对：%+v", c.Defaults)
	}
	if !c.Defaults.TmpfsUnlessX11 {
		t.Error("tmpfs_unless_x11 默认应为 true")
	}
	if c.Tools.Bwrap != "" {
		t.Errorf("bwrap 默认应为空（走 PATH），得到 %q", c.Tools.Bwrap)
	}
	if got, want := c.Path, filepath.Join(p.Home, ".config/twin/config.toml"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestLoadReadsEveryField(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	write(t, p.Home, ".config/twin/config.toml", `
[defaults]
level       = "none"
icon_size   = 128
badge_style = "dot"
tmpfs_unless_x11 = false

[tools]
bwrap  = "/usr/lib/app-multi/bin/bwrap"
python = "/usr/bin/python3"

[icon]
theme = "Adwaita"

[bwrap]
extra = ["--unshare-pid", "--setenv", "FOO", "bar"]
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Exists {
		t.Fatal("Exists 应为 true")
	}
	if c.Defaults.Level != "none" || c.Defaults.IconSize != 128 || c.Defaults.BadgeStyle != "dot" {
		t.Errorf("defaults 没读全：%+v", c.Defaults)
	}
	if c.Defaults.TmpfsUnlessX11 {
		t.Error("tmpfs_unless_x11 = false 没生效")
	}
	if c.Tools.Bwrap != "/usr/lib/app-multi/bin/bwrap" || c.Tools.Python != "/usr/bin/python3" {
		t.Errorf("tools 没读全：%+v", c.Tools)
	}
	if c.Icon.Theme != "Adwaita" {
		t.Errorf("icon.theme = %q", c.Icon.Theme)
	}
	// The flat token array must survive verbatim, including a value that looks
	// like an option.
	want := []string{"--unshare-pid", "--setenv", "FOO", "bar"}
	if len(c.Bwrap) != len(want) {
		t.Fatalf("[bwrap] extra = %#v, want %#v", c.Bwrap, want)
	}
	for i := range want {
		if c.Bwrap[i] != want[i] {
			t.Fatalf("[bwrap] extra = %#v, want %#v", c.Bwrap, want)
		}
	}
}

// A typo in config.toml must be loud. Falling back to the built-in defaults
// would leave the user editing a file that silently does nothing.
func TestLoadRejectsBadValues(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"级别没实现", "[defaults]\nlevel = \"argument\"\n", "没实现"},
		{"级别是垃圾", "[defaults]\nlevel = \"nonsense\"\n", "level"},
		{"图标尺寸为零", "[defaults]\nicon_size = 0\n", "正数"},
		{"语法错", "[defaults\nlevel = \"alias\"\n", "config.toml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := inst.Paths{Home: t.TempDir()}
			write(t, p.Home, ".config/twin/config.toml", tc.body)
			_, err := Load(p)
			if err == nil {
				t.Fatal("应当报错")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息应含 %q，得到 %v", tc.want, err)
			}
		})
	}
}

func TestFindRuleReturnsBuiltinWhenNoFile(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	rule, matched, err := FindRule(p, RuleKeys("wechat.desktop", "wechat")...)
	if err != nil {
		t.Fatal(err)
	}
	if rule == nil {
		t.Fatal("wechat 应有内置规则")
	}
	if matched != "wechat" {
		t.Errorf("matched = %q", matched)
	}
	if !rule.Builtin || rule.Path != "" {
		t.Errorf("内置规则不该有 Path：%+v", rule)
	}
	if len(rule.Binds) != 3 {
		t.Fatalf("内置 wechat 应有 3 条路径，得到 %d", len(rule.Binds))
	}
	// ~/Documents/xwechat_files is the in-place writable upgrade: binding it to
	// itself is not a no-op under --ro-bind / / (设计 §7).
	if b := rule.Binds[1]; b.Source != b.Target {
		t.Errorf("共享目录应原地升级可写，得到 %s", b)
	}
	if b := rule.Binds[0]; !strings.Contains(b.Source, "{id}") {
		t.Errorf("私有目录应带 {id} 占位，得到 %q", b.Source)
	}
}

func TestUnknownProgramHasNoRule(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	rule, _, err := FindRule(p, "vlc")
	if err != nil {
		t.Fatal(err)
	}
	if rule != nil {
		t.Fatalf("vlc 不该有内置规则，得到 %+v", rule)
	}
}

// A user file wins field by field, so adding one keyword list does not drop
// the state paths that ship with the tool.
func TestRuleFileOverlaysBuiltinFieldByField(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	write(t, p.Home, ".config/twin/rules.d/wechat.toml", `
app      = "wechat"
keywords = "wechat;微信;im"
`)
	rule, _, err := FindRule(p, "wechat")
	if err != nil {
		t.Fatal(err)
	}
	if rule.Keywords != "wechat;微信;im" {
		t.Errorf("文件里的 keywords 没生效：%q", rule.Keywords)
	}
	if len(rule.Binds) != 3 {
		t.Errorf("没写 bind 时应继承内置的 3 条，得到 %d", len(rule.Binds))
	}
	if rule.Builtin {
		t.Error("来源是文件时 Builtin 应为 false")
	}
	if want := filepath.Join(p.RulesDir(), "wechat.toml"); rule.Path != want {
		t.Errorf("Path = %q, want %q", rule.Path, want)
	}
}

// Writing bind means "this is the whole mapping": half-inheriting would make
// the file a lie.
func TestRuleFileBindsReplaceBuiltin(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	write(t, p.Home, ".config/twin/rules.d/wechat.toml", `
bind = [
  "~/.local/state/twin/{id}/xwechat:~/.xwechat",
  "~/.cache/wechat",
]
`)
	rule, _, err := FindRule(p, "wechat")
	if err != nil {
		t.Fatal(err)
	}
	if len(rule.Binds) != 2 {
		t.Fatalf("Binds 应被文件整体替换，得到 %d 条", len(rule.Binds))
	}
	// A one-path entry means the in-place writable upgrade, spelled the same
	// way the hand-written wrapper did with `--bind A A`.
	if b := rule.Binds[1]; b.Source != "~/.cache/wechat" || b.Target != "~/.cache/wechat" {
		t.Errorf("单路径应回填两侧，得到 %s", b)
	}
}

// rules.d 里的相对路径被指名拒绝：`profile.yml` 是唯一有"自己所在目录"这个基准的文件
// （inst.Load 按那份文件算），而规则是给这个程序的所有实例用的共享知识 —— 它的相对路径
// 该相对规则目录还是相对每个实例，没有答案。猜一个，就等于把一条 bwrap 按启动器工作目录
// 解析的路径塞进去，而包好的东西看起来一切正常。
func TestRuleFileRefusesRelativePaths(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	write(t, p.Home, ".config/twin/rules.d/demo.toml", `
bind = ["state/demo:~/.demo"]
`)
	_, _, err := FindRule(p, "demo")
	if err == nil {
		t.Fatal("rules.d 里的相对路径必须报错，而不是塞进 bwrap 由它按工作目录解析")
	}
	if !strings.Contains(err.Error(), "相对路径") || !strings.Contains(err.Error(), "state/demo") {
		t.Errorf("报错要说清是哪一条、为什么：%v", err)
	}
}

// The pre-string-list spelling is refused by name. Reading `[[bind]]` as "no
// mappings" would turn a file that says where the data is into a file that says
// the data is shared.
func TestRuleFileOldTableFormIsAnError(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	write(t, p.Home, ".config/twin/rules.d/wechat.toml", `
[[bind]]
canonical = "~/.xwechat"
private   = "~/.local/state/twin/{id}/xwechat"
`)
	_, _, err := FindRule(p, "wechat")
	if err == nil || !strings.Contains(err.Error(), "bind") {
		t.Fatalf("旧的 [[bind]] 表应当报错，得到 %v", err)
	}
}

// A version of the same mistake one step subtler: right key, wrong shape.
func TestRuleFileBindMustBeAList(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	write(t, p.Home, ".config/twin/rules.d/wechat.toml", "bind = \"/a:/b\"\n")
	_, _, err := FindRule(p, "wechat")
	if err == nil {
		t.Fatal("bind 写成单个字符串时应当报错，而不是被读成没有映射")
	}
}

func TestRuleKeys(t *testing.T) {
	cases := []struct {
		desktop, target string
		want            []string
	}{
		{"wechat.desktop", "wechat", []string{"wechat"}},
		{"wechat.desktop", "wechat-beta", []string{"wechat", "wechat-beta"}},
		{"", "wechat", []string{"wechat"}},
		{"..", "wechat", []string{"wechat"}},
		{"/usr/share/applications/vlc.desktop", "", []string{"vlc"}},
	}
	for _, tc := range cases {
		got := RuleKeys(tc.desktop, tc.target)
		if len(got) != len(tc.want) {
			t.Errorf("RuleKeys(%q,%q) = %v, want %v", tc.desktop, tc.target, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("RuleKeys(%q,%q) = %v, want %v", tc.desktop, tc.target, got, tc.want)
				break
			}
		}
	}
}
