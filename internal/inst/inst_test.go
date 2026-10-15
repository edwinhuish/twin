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
			{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"},
		},
	}
}

// List must report instances, not everything that happens to live under
// ~/.local/share/twin. `bin/` holds the shared bwrap copy and bootstrap script
// (Paths.BinDir), so a Chromium wrap puts a directory there — and treating it
// as an instance with a missing snapshot broke `twin list` on any machine that
// had ever wrapped one.
func TestListSkipsDirectoriesThatAreNotInstances(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.BinDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.BwrapCopy(), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory someone half-created: no snapshot, nothing to report.
	if err := os.MkdirAll(p.InstanceDir("halfmade"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.InstanceDir("wechat2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Profile("wechat2"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := List(p)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "wechat2" {
		t.Errorf("List = %+v，期望只有 wechat2", got)
	}
}

func TestRenderLoadRoundTrip(t *testing.T) {
	in := base()
	data, err := in.Render()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yml")
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
	if len(out.Binds) != 1 || out.Binds[0].Source != in.Binds[0].Source {
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

// The generated entry's text is one `desktop:` section. Seven keys at the root
// answered the same question seven lines apart, and every one of them means
// "inherit from the source" when empty — a group reads as one decision.
func TestSnapshotPutsTheEntryTextInOneSection(t *testing.T) {
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "\ndesktop:\n") {
		t.Errorf("没有 desktop: 一节：\n%s", text)
	}
	// Anchored on a newline so the indented copies inside `desktop:` do not
	// count: the assertion is about what is at the *root*.
	for _, key := range []string{"name", "name_en", "comment", "comment_en", "keywords", "categories", "source"} {
		if strings.Contains(text, "\n"+key+":") {
			t.Errorf("%s 还留在根上：\n%s", key, text)
		}
		if !strings.Contains(text, "\n  "+key+":") {
			t.Errorf("%s 没有缩进进 desktop: 一节：\n%s", key, text)
		}
	}
}

// A snapshot written before the keys moved must keep loading. The old spelling
// is not ambiguous — there is nowhere else a root-level `name` could have
// belonged — and refusing it would take every installed instance offline at
// once, over a change the file itself absorbs on the next install.
func TestLoadReadsThePreSectionSpelling(t *testing.T) {
	legacy := "id: wechat2\n" +
		"name: 微信2\n" +
		"comment: 第二个微信\n" +
		"keywords: wechat;微信;\n" +
		"source: /usr/share/applications/wechat.desktop\n" +
		"exec:\n" +
		"  target: /opt/wechat/wechat\n"
	path := filepath.Join(t.TempDir(), "profile.yml")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "微信2" || out.Comment != "第二个微信" || out.Keywords != "wechat;微信;" ||
		out.Source != "/usr/share/applications/wechat.desktop" {
		t.Errorf("旧写法的桌面项文案没读回来：%+v", out)
	}
}

// The Docker Compose spelling, source first: the private copy is where the data
// lives, ~/.xwechat is where the program looks for it.
func TestRenderWritesBindsAsDockerSpecs(t *testing.T) {
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "- ~/.local/state/twin/wechat2/xwechat:~/.xwechat") {
		t.Errorf("bind 条目没写进快照：\n%s", data)
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
	if !strings.Contains(string(data), "extra: []") {
		t.Errorf("empty passthrough is not visible in the snapshot:\n%s", data)
	}
	// And it must round-trip as empty, not as a one-element list.
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yml")
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

// The default working copy has to be findable from the next shell, which the
// directory the user happens to be standing in is not. It also has to stay a
// file: List reads that directory and would otherwise try to load it as an
// instance (see TestListSkipsDirectoriesThatAreNotInstances).
func TestWorkingCopyFollowsHomeAndIsNotAnInstance(t *testing.T) {
	p := Paths{Home: "/home/someone"}
	want := "/home/someone/.local/share/twin/" + ProfileName
	if got := p.WorkingCopy(); got != want {
		t.Errorf("WorkingCopy = %q, want %q", got, want)
	}
	if dir := filepath.Dir(p.WorkingCopy()); dir != filepath.Dir(p.BinDir()) {
		t.Errorf("工作副本与实例目录应当同级，得到 %q", dir)
	}
	if filepath.Base(p.WorkingCopy()) != ProfileName {
		t.Errorf("工作副本与存档快照必须同名：%q", filepath.Base(p.WorkingCopy()))
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
	if !strings.Contains(string(data), "override: false") {
		t.Fatalf("关掉的状态必须看得见：\n%s", data)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yml")
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

// 没有 tray 段的老快照等于"要覆盖"：它们全都写于这个选项存在之前，
// 把它们的语义翻成"不管"会让已经写进宿主表的条目悄悄失去维护。
func TestTrayOverrideDefaultsToTrueForOldSnapshots(t *testing.T) {
	it := base()
	it.TrayOverride = true
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	old := dropBlock(t, string(data), "tray")
	if old == string(data) {
		t.Fatalf("测试前提不成立：快照里没有 tray 段\n%s", data)
	}
	// The section must be gone with its keys *and* its comment — a file that
	// still carried the `override:` line would not be an old file at all.
	if strings.Contains(old, "override:") {
		t.Fatalf("tray 段没删干净：\n%s", old)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yml")
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.TrayOverride {
		t.Error("缺 tray 段应当按 true 处理")
	}
}

// dropBlock returns doc without the top-level `key:` mapping and everything
// indented under it — the shape a file written before that section existed has.
// It walks lines rather than matching text because the section now carries
// multi-line comments of its own.
func dropBlock(t *testing.T, doc, key string) string {
	t.Helper()
	var out []string
	dropping := false
	for _, ln := range strings.Split(doc, "\n") {
		if ln == key+":" {
			dropping = true
			continue
		}
		if dropping && (ln == "" || ln[0] == ' ' || ln[0] == '#') {
			continue
		}
		dropping = false
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// 只有旧的 instance.toml 时必须报"这个实例读不了"，不能读成"这个实例不存在" ——
// 一个真实例从所有命令里悄悄消失，正是 twin 存在的意义所在的反面。
func TestLoadProfileExplainsLegacyTomlSnapshot(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	if err := os.MkdirAll(p.InstanceDir("wechat2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LegacyProfile("wechat2"), []byte("id = \"wechat2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfile(p, "wechat2")
	if err == nil {
		t.Fatal("只有旧的 instance.toml 时必须报错，而不是当作实例不存在")
	}
	if !strings.Contains(err.Error(), "profile.yml") {
		t.Errorf("报错要点名新的快照文件：%v", err)
	}
}

// 两个文件都不在是"没有这个实例"，这个语义要保留：CLI 靠 os.IsNotExist 区分
// "没写过"和"文件坏了"。
func TestLoadProfileKeepsNotExistMeaning(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	if _, err := LoadProfile(p, "nope"); !os.IsNotExist(err) {
		t.Fatalf("两个文件都不在时应保持 os.IsNotExist，得到 %v", err)
	}
}

// 旧快照也不能从 list 里消失：跳过它就等于这个实例从来没存在过。
func TestListReportsLegacyTomlSnapshot(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	if err := os.MkdirAll(p.InstanceDir("wechat2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LegacyProfile("wechat2"), []byte("id = \"wechat2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(p); err == nil {
		t.Fatal("旧快照不能让实例从 list 里悄悄消失")
	}
}

// 快照现在是实例的说明书：`twin new` 只报路径，逐键的解释全在这份文件里。
// 所以"每个带值的键上面都有一行说明"是它必须守住的性质 —— 删掉注释就等于把
// 文件变回一堆没人知道含义的键。
//
// 两类行不在断言里：小节标题（`identity:`）的含义由它下面的键给出；序列项
// （`- 源:目标` 这种 bind 条目）由它们所属键的那条说明统管。
func TestEverySnapshotKeyIsExplained(t *testing.T) {
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case trimmed == "",
			strings.HasPrefix(trimmed, "#"),
			strings.HasPrefix(trimmed, "- "),
			strings.HasSuffix(ln, ":"):
			continue
		}
		if i == 0 || !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "#") {
			t.Errorf("键 %q 上面没有说明", trimmed)
		}
	}
}

// "一条映射都没有"是默认决定，而且终端不再重复它了 —— 这句话只剩下文件里这一个
// 落点，所以 bind 必须无条件写出，空的时候写成 bind: []。
func TestSnapshotWritesBindEvenWhenEmpty(t *testing.T) {
	it := base()
	it.Binds = nil
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "bind: []") {
		t.Errorf("空映射要写成 bind: []，不能整段消失：\n%s", data)
	}
	if !strings.Contains(string(data), "数据目录与源程序共用") {
		t.Errorf("空的 bind 必须把共享状态的后果写在文件里：\n%s", data)
	}
	// 而且空映射必须能原样读回来（往返稳定是 `regen` 不做无谓 diff 的前提）。
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Binds) != 0 {
		t.Errorf("空映射读回来变成 %+v", out.Binds)
	}
	again, err := out.Render()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("空映射的往返不稳定：\n--- 第一次 ---\n%s\n--- 第二次 ---\n%s", data, again)
	}
}

// The one-line form is the whole on-disk contract for a mapping, so every shape
// it accepts has to survive being written back out: `regen` re-renders from the
// same value, and a spelling that drifts would show up as a diff the user did
// not make.
func TestParseBindRoundTrips(t *testing.T) {
	for _, spec := range []string{
		"~/.local/state/twin/wechat2/xwechat:~/.xwechat",
		"~/.xwechat",
		"/src:/dst:ro",
		"/src:/dst:rw",
		"/src:/dst",
	} {
		got, err := ParseBind(spec)
		if err != nil {
			t.Errorf("ParseBind(%q): %v", spec, err)
			continue
		}
		again, err := ParseBind(got.String())
		if err != nil {
			t.Errorf("%q → %q 解析不回去：%v", spec, got, err)
			continue
		}
		if again != got {
			t.Errorf("%q → %q → %+v", spec, got, again)
		}
	}

	// The bare form means the same directory both ways — the in-place writable
	// upgrade WeChat's shared directory needs.
	if b, err := ParseBind("~/.xwechat"); err != nil || b.Source != "~/.xwechat" || b.Target != "~/.xwechat" {
		t.Errorf("单路径：%+v / %v", b, err)
	}
	if b, err := ParseBind("/src:/dst:ro"); err != nil || !b.ReadOnly {
		t.Errorf(":ro：%+v / %v", b, err)
	}
}

// Every shape that would otherwise be read as something it is not.
func TestParseBindRefusesAmbiguousShapes(t *testing.T) {
	for _, spec := range []string{
		"",           // 空
		"/x:",        // 缺一侧
		":/y",        // 缺一侧
		"/x:ro",      // 右侧看着像模式，其实是漏写了目标
		"/x:rw",      // 同上
		"/x:/y:rx",   // 不认识的模式
		"/x:/y:ro:9", // 冒号太多
	} {
		if got, err := ParseBind(spec); err == nil {
			t.Errorf("ParseBind(%q) 应当报错，得到 %+v", spec, got)
		}
	}
}
