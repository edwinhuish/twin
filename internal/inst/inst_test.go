package inst

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func base() *Instance {
	return &Instance{
		ID:         "wechat2",
		Name:       "微信2",
		NameEn:     "WeChat 2",
		Comment:    "第二个微信",
		CommentEn:  "Second WeChat",
		Keywords:   "wechat;微信;im",
		Categories: "Network;InstantMessaging;",
		Source:     "/usr/share/applications/wechat.desktop",
		Level:      LevelAlias,
		WMClass:    "wechat2",
		Notify:     true,
		IconSource: "/usr/share/icons/hicolor/512x512/apps/wechat.png",
		Badge:      "2",
		BadgeStyle: "corner",
		Target:     "/opt/wechat/wechat",
		Alias:      "wechat2",
		Env:        map[string]string{},
		Tmpfs:      []string{"/tmp"},
		Bwrap:      []string{"--unshare-pid", "--setenv", "TWIN_FOO", "bar"},
		Binds: []Bind{
			{Canonical: "~/.xwechat", Private: "~/.local/state/twin/wechat2/xwechat"},
		},
	}
}

func TestRenderLoadRoundTrip(t *testing.T) {
	in := base()
	data, err := in.Render()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.toml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load:\n%s\n%v", data, err)
	}
	if out.ID != in.ID || out.Name != in.Name || out.Level != in.Level {
		t.Errorf("scalars lost: %+v", out)
	}
	if out.Target != in.Target || out.Alias != in.Alias {
		t.Errorf("exec lost: %q / %q", out.Target, out.Alias)
	}
	if len(out.Binds) != 1 || out.Binds[0].Canonical != in.Binds[0].Canonical {
		t.Errorf("binds lost: %+v", out.Binds)
	}
	if len(out.Tmpfs) != 1 || out.Tmpfs[0] != "/tmp" {
		t.Errorf("tmpfs lost: %+v", out.Tmpfs)
	}
	if strings.Join(out.Bwrap, " ") != strings.Join(in.Bwrap, " ") {
		t.Errorf("bwrap passthrough lost: %q", out.Bwrap)
	}
	if out.CommentEn != in.CommentEn || out.Keywords != in.Keywords || out.Categories != in.Categories {
		t.Errorf("desktop overrides lost: comment_en=%q keywords=%q categories=%q",
			out.CommentEn, out.Keywords, out.Categories)
	}
	// Re-rendering must be byte-identical, otherwise `regen` would produce a
	// diff on every run.
	again, err := out.Render()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("re-render is not stable:\n--- first ---\n%s\n--- second ---\n%s", data, again)
	}
}

func TestRenderHasNoEmptyParentSections(t *testing.T) {
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[map]\n") {
		t.Errorf("[[map.bind]] should not drag an empty [map] header along:\n%s", data)
	}
	if !strings.Contains(string(data), "[[map.bind]]") {
		t.Errorf("[[map.bind]] missing:\n%s", data)
	}
}

// An instance with no passthrough still has to say so, or a reader cannot tell
// "nothing extra" from "the key was forgotten".
func TestRenderAlwaysWritesBwrapExtra(t *testing.T) {
	it := base()
	it.Bwrap = nil
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[bwrap]") || !strings.Contains(string(data), "extra = []") {
		t.Errorf("empty passthrough is not visible in the snapshot:\n%s", data)
	}
	// And it must round-trip as empty, not as a one-element list.
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.toml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Bwrap) != 0 {
		t.Errorf("empty passthrough came back as %q", out.Bwrap)
	}
}

func TestCheckRejectsSplitIdentity(t *testing.T) {
	it := base()
	it.Argv0 = "somethingelse"
	err := it.Check()
	if err == nil {
		t.Fatal("argv0 differing from the alias must be refused: the window app_id follows " +
			"the real path while the tray id follows argv[0]")
	}
	if !strings.Contains(err.Error(), "argv0") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestCheckRejectsAliasEqualToRealName(t *testing.T) {
	it := base()
	it.Alias = "wechat"
	if err := it.Check(); err == nil {
		t.Fatal("alias == target basename is a no-op and must be refused")
	}
}

func TestCheckRejectsUnshareNet(t *testing.T) {
	it := base()
	it.Unshare = []string{"net"}
	err := it.Check()
	if err == nil || !strings.Contains(err.Error(), "net") {
		t.Fatalf("--unshare-net must be refused, got %v", err)
	}
}

func TestCheckRejectsRelativeTarget(t *testing.T) {
	it := base()
	it.Target = "wechat"
	if err := it.Check(); err == nil {
		t.Fatal("a relative target must be refused")
	}
}

func TestCheckRejectsUnimplementedLevel(t *testing.T) {
	for _, lvl := range []Level{LevelArgument, LevelEnv} {
		it := base()
		it.Level = lvl
		if err := it.Check(); err == nil {
			t.Errorf("level %q is not implemented and must be refused, not approximated", lvl)
		}
	}
}

// redirect is alias plus a rule file: it renames for real, so Check must accept
// it exactly where alias is accepted.
func TestCheckAcceptsRedirect(t *testing.T) {
	it := base()
	it.Level = LevelRedirect
	if err := it.Check(); err != nil {
		t.Errorf("redirect 应当被接受：%v", err)
	}
	// redirect 也要写 exec.alias：改名是它拿到进程级证据的前提，少了这一行
	// 快照回放时就会退化成「目标原名」（这个坑真实存在过）。
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "alias") {
		t.Errorf("redirect 的 exec.alias 没写进快照：\n%s", data)
	}
	if !LevelRedirect.Implemented() || !LevelRedirect.Renames() {
		t.Error("redirect 必须同时是「已实现」与「会改名」")
	}
	if LevelNone.Renames() {
		t.Error("none 不改名")
	}
}

// The three passthrough classes that must never reach bwrap (设计 §2.4).
func TestCheckRefusesForbiddenPassthrough(t *testing.T) {
	cases := []struct{ tok, why string }{
		{"--", "it would truncate bwrap's own option parsing"},
		{"--argv0", "it would split the two identity channels"},
		{"--argv0=FOO", "the =value spelling must be caught too"},
		{"--seccomp", "it needs a file descriptor"},
		{"--add-seccomp-fd", "it needs a file descriptor"},
		{"--userns", "it needs a file descriptor"},
		{"--json-status-fd", "it needs a file descriptor"},
	}
	for _, c := range cases {
		it := base()
		it.Bwrap = []string{c.tok}
		if err := it.Check(); err == nil {
			t.Errorf("CheckBwrapToken(%q) must be refused: %s", c.tok, c.why)
		}
	}
}

func TestCheckAcceptsOrdinaryPassthrough(t *testing.T) {
	it := base()
	it.Bwrap = []string{"--unshare-pid", "--unshare-uts", "--setenv", "TWIN_FOO", "bar", "--chdir", "/tmp"}
	if err := it.Check(); err != nil {
		t.Fatalf("ordinary bwrap tokens must pass: %v", err)
	}
}

func TestPathsUseExplicitHome(t *testing.T) {
	p := Paths{Home: "/home/someone"}
	if got := p.Desktop("x"); got != "/home/someone/.local/share/applications/x.desktop" {
		t.Errorf("Desktop = %q", got)
	}
	if got := p.Expand("~/a/b"); got != "/home/someone/a/b" {
		t.Errorf("Expand = %q", got)
	}
	if got := p.Expand("/abs"); got != "/abs" {
		t.Errorf("Expand should leave absolute paths alone, got %q", got)
	}
}

// HOME must never be guessed. The hand-written wrapper defaulted to /root, and
// quietly writing a second account's data into /root is the worse failure.
func TestHomeRefusesToGuess(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := Home(); err == nil {
		t.Fatal("empty HOME must be an error")
	}
	t.Setenv("HOME", "relative/path")
	if _, err := Home(); err == nil {
		t.Fatal("relative HOME must be an error")
	}
	t.Setenv("HOME", "/home/ok")
	h, err := Home()
	if err != nil || h != "/home/ok" {
		t.Fatalf("Home = %q, %v", h, err)
	}
}

func TestValidateID(t *testing.T) {
	for _, bad := range []string{"", "a/b", "a b", "-lead", ".", "..", "wechat desktop"} {
		if err := ValidateID(bad); err == nil {
			t.Errorf("ValidateID(%q) should fail", bad)
		}
	}
	for _, good := range []string{"wechat2", "wechat-zc", "a.b", "App_2"} {
		if err := ValidateID(good); err != nil {
			t.Errorf("ValidateID(%q) = %v", good, err)
		}
	}
}

// 托盘覆盖是"这台机器上要不要碰宿主那张表"的决定，必须进快照：regen 只读快照
// （不读 config.toml），少了它，一次 --refresh 就会在明确关掉它的机器上把条目
// 又写回去。
func TestTrayOverrideIsPartOfTheSnapshot(t *testing.T) {
	it := base()
	it.TrayOverride = false
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[tray]") || !strings.Contains(string(data), "override = false") {
		t.Fatalf("关掉的状态必须看得见：\n%s", data)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "instance.toml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.TrayOverride {
		t.Error("override = false 读回来变成了 true")
	}
}

// 没有 [tray] 段的老快照等于"要覆盖"：它们全都写于这个选项存在之前，
// 把它们的语义翻成"不管"会让已经写进宿主表的条目悄悄失去维护。
func TestTrayOverrideDefaultsToTrueForOldSnapshots(t *testing.T) {
	it := base()
	it.TrayOverride = true
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(data), "[tray]\noverride = true\n", "", 1)
	if old == string(data) {
		t.Fatalf("测试前提不成立：快照里没有 [tray] 段\n%s", data)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "instance.toml")
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.TrayOverride {
		t.Error("缺 [tray] 段应当按 true 处理")
	}
}
