package inst

import (
	"os"
	"path/filepath"
	"reflect"
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
		Proc:       "/proc",
		Tmpfs:      []string{"/tmp"},
		RemountRO:  []string{"~/.ssh"},
		// Two pairs, and the second one has a colon in its *value*: the file
		// spelling has to survive that (`PATH:/a:/b` splits at the first colon
		// and nowhere else).
		Env:  []EnvVar{{Key: "TWIN_FOO", Value: "bar"}, {Key: "PATH", Value: "/a:/b"}},
		Args: []string{"--dev-bind", "/", "/", "--unshare-pid", "--setenv", "TWIN_FOO", "bar"},
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
	if strings.Join(out.Args, " ") != strings.Join(in.Args, " ") {
		t.Errorf("bwrap args lost: %q", out.Args)
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
// lives, ~/.xwechat is where the program looks for it. The entries live under
// `bwrap`, next to the option list they are emitted with — a mapping is another
// argument of the same invocation, not a separate subject.
func TestRenderWritesBindsAsDockerSpecs(t *testing.T) {
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "\n  bind:\n") {
		t.Errorf("bind 应当是 bwrap 节点下的一个键：\n%s", s)
	}
	if !strings.Contains(s, "\n    - ~/.local/state/twin/wechat2/xwechat:~/.xwechat\n") {
		t.Errorf("bind 条目没写进 bwrap 节点：\n%s", s)
	}
}

// An instance with an empty option list still has to say so, or a reader cannot
// tell "there is nothing to pass" from "the key was forgotten".
func TestRenderAlwaysWritesBwrapArgs(t *testing.T) {
	it := base()
	it.Args = nil
	it.Level = LevelNone // an empty list is only legitimate without a sandbox
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "args: []") {
		t.Errorf("empty option list is not visible in the snapshot:\n%s", data)
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
	if len(out.Args) != 0 {
		t.Errorf("empty option list came back as %q", out.Args)
	}
}

// twin no longer keeps a sandbox base of its own, so an empty option list is a
// namespace with nothing mounted: the program would not find itself, and
// nothing in the failure would name twin. Refusing here is the only place that
// can say which file to fix.
func TestCheckRefusesEmptyArgsWithASandbox(t *testing.T) {
	it := base()
	it.Args = nil
	err := it.Check()
	if err == nil || !strings.Contains(err.Error(), "bwrap.args") {
		t.Fatalf("空 bwrap.args 必须被拒绝并指名那个键，得到 %v", err)
	}
	// Level none runs no bubblewrap at all, so an empty list there is honest.
	plain := base()
	plain.Level = LevelNone
	plain.Args = nil
	if err := plain.Check(); err != nil {
		t.Errorf("level=none 不过 bwrap，空列表不该是错误：%v", err)
	}
}

// `bwrap.extra` was only what the user appended; `bwrap.args` is the whole
// list, base included. Reading one as the other would hand bwrap a namespace
// with no root — a launch that fails for reasons that never mention twin.
func TestLoadRefusesTheOldExtraKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProfileName)
	body := `id: demo
exec:
  target: /opt/demo/demo
bwrap:
  extra:
    - --unshare-pid
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "bwrap.args") {
		t.Fatalf("旧写法必须指名报错（它现在是另一个键、另一套语义），得到 %v", err)
	}
}

// `bind` 从顶层搬进 `bwrap` 之后，一份老定义里的顶层 bind 不能再被当成空列表读掉：
// 前者描述"程序的数据挂在哪"，后者描述"什么都没挂"，而后者会一直到程序看不见自己的文件
// 才暴露，那时离这次编辑已经很远了。
func TestLoadRefusesTheTopLevelBindKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProfileName)
	body := `id: demo
exec:
  target: /opt/demo/demo
bind:
  - ~/.local/state/twin/demo/x:~/.x
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "bwrap.bind") {
		t.Fatalf("顶层 bind 必须指名报错并给出新位置，得到 %v", err)
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

// The three token classes that must never reach bwrap (设计 §2.4).
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
		it.Args = []string{c.tok}
		if err := it.Check(); err == nil {
			t.Errorf("CheckBwrapToken(%q) must be refused: %s", c.tok, c.why)
		}
	}
}

func TestCheckAcceptsOrdinaryPassthrough(t *testing.T) {
	it := base()
	it.Args = []string{"--dev-bind", "/", "/", "--unshare-pid", "--setenv", "TWIN_FOO", "bar", "--chdir", "/tmp"}
	if err := it.Check(); err != nil {
		t.Fatalf("ordinary bwrap tokens must pass: %v", err)
	}
}

// 实例定义就住在实例自己的目录里（`twin gen` 写到那儿、`twin install` 读那一份），
// 所以它必须跟着 $HOME 走 —— 当前目录只是当时站的地方，换个 shell 就找不回来了。
// 这也是"一份定义、一个 id"的落点：两个 id 的定义天然不会互相覆盖。
func TestProfileIsTheDefinitionInsideTheInstanceDirectory(t *testing.T) {
	p := Paths{Home: "/home/someone"}
	want := "/home/someone/.local/share/twin/x/" + ProfileName
	if got := p.Profile("x"); got != want {
		t.Errorf("Profile = %q, want %q", got, want)
	}
	if p.Profile("x") == p.Profile("y") {
		t.Error("两个实例不能共用一份定义")
	}
}

// 相对路径按"那份定义自己所在的目录"算，而且算在 Load 这一步：下游那些读路径的人
// （bwrap 参数、建挂载点、图标解析、verify）拿到的是同一个绝对路径，谁也编不出第二个
// 基准 —— 在读者那一侧解析，等于按"用户当时站在哪个目录"解析。定义文件本身保持你写的
// 那个相对形式，一个 bundle（配置 + 程序 + 状态目录并排）才能整体搬走。
func TestRelativePathsResolveAgainstTheDefinitionDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProfileName)
	body := `id: demo
exec:
  target: ./bin/demo
icon:
  source: assets/demo.png
bwrap:
  proc: proc-mirror
  tmpfs:
    - cache
    - /tmp
  bind:
    - ./state/demo:~/.demo
    - /abs/data:~/abs
    - ~/data:~/data-in-home
  remountRO:
    - secrets
    - ~/.ssh
  env:
    - DEMO_MODE:on
    - PATH:/a:/b
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "bin/demo"); it.Target != want {
		t.Errorf("exec.target = %q, want %q", it.Target, want)
	}
	if want := filepath.Join(dir, "assets/demo.png"); it.IconSource != want {
		t.Errorf("icon.source = %q, want %q", it.IconSource, want)
	}
	if len(it.Tmpfs) != 2 || it.Tmpfs[0] != filepath.Join(dir, "cache") || it.Tmpfs[1] != "/tmp" {
		t.Errorf("bwrap.tmpfs = %v（/tmp 这类绝对路径不该被动）", it.Tmpfs)
	}
	if want := filepath.Join(dir, "proc-mirror"); it.Proc != want {
		t.Errorf("bwrap.proc = %q, want %q", it.Proc, want)
	}
	if len(it.RemountRO) != 2 || it.RemountRO[0] != filepath.Join(dir, "secrets") || it.RemountRO[1] != "~/.ssh" {
		t.Errorf("bwrap.remountRO = %v（~ 留给 Expand，绝对路径不该被动）", it.RemountRO)
	}
	wantEnv := []EnvVar{{Key: "DEMO_MODE", Value: "on"}, {Key: "PATH", Value: "/a:/b"}}
	if !reflect.DeepEqual(it.Env, wantEnv) {
		t.Errorf("bwrap.env = %+v, want %+v（冒号只在第一个处断开）", it.Env, wantEnv)
	}
	if len(it.Binds) != 3 {
		t.Fatalf("binds = %v", it.Binds)
	}
	if want := filepath.Join(dir, "state/demo"); it.Binds[0].Source != want {
		t.Errorf("bind 源 = %q, want %q", it.Binds[0].Source, want)
	}
	// 目标那一侧同样有基准：它是沙箱里那个路径，`~` 那一侧本来就由 twin 折叠成
	// $HOME 下的路径，相对的一侧落在定义旁边是一样的道理。
	if it.Binds[0].Target != "~/.demo" {
		t.Errorf("bind 目标 = %q，~ 形式必须原样留着（它在用的时候才折叠成本机的 home）", it.Binds[0].Target)
	}
	if it.Binds[1].Source != "/abs/data" || it.Binds[2].Source != "~/data" {
		t.Errorf("绝对路径与 ~ 形式都不该被动：%v", it.Binds)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("Load 不得改写那份定义（相对形式要留在盘上）：\n%s", after)
	}
}

// 没有路径的字段（空字符串）不能被解析成一个目录：那会让一份没写 icon.source 的
// 定义得到 "<定义目录>" 这个"图标"。
func TestResolvePathLeavesEmptyAndAnchoredPaths(t *testing.T) {
	if got := resolvePath("/base", ""); got != "" {
		t.Errorf("空字符串不该变成路径：%q", got)
	}
	for _, s := range []string{"/abs", "~", "~/x"} {
		if got := resolvePath("/base", s); got != s {
			t.Errorf("resolvePath(%q) = %q，应当原样返回", s, got)
		}
	}
	if got := resolvePath("/base", "rel/x"); got != "/base/rel/x" {
		t.Errorf("resolvePath = %q", got)
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
		"/src:/dst:dev",
		"/dev/dri:/dev/dri:dev",
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
	// `:dev` is a mode of its own, not a second word for read-only: it is what
	// asks for `--dev-bind` (a directory of device nodes, once the base has
	// stopped handing the whole host over).
	if b, err := ParseBind("/dev/dri:/dev/dri:dev"); err != nil || !b.Dev || b.ReadOnly {
		t.Errorf(":dev：%+v / %v", b, err)
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
		"/x:dev",     // 同上：设备路径也要把两侧写全
		"/x:/y:rx",   // 不认识的模式
		"/x:/y:ro:9", // 冒号太多
	} {
		if got, err := ParseBind(spec); err == nil {
			t.Errorf("ParseBind(%q) 应当报错，得到 %+v", spec, got)
		}
	}
}

// 一行写法是盘上的完整契约，`键:值` 也一样：写出去要能原样读回来，读不回来就是改了
// 用户写下的东西。这里同时钉住那个必然会被写出来的形状 —— 值里带冒号（PATH:/a:/b）。
func TestParseEnvVarRoundTrips(t *testing.T) {
	for _, spec := range []string{
		"TWIN_FOO:bar",
		"PATH:/a:/b",
		"LC_ALL:", // 空值：程序分得清"设了但为空"和"没设"
		"URL:https://example.com:8443/x",
	} {
		got, err := ParseEnvVar(spec)
		if err != nil {
			t.Errorf("ParseEnvVar(%q): %v", spec, err)
			continue
		}
		if again, err := ParseEnvVar(got.String()); err != nil || again != got {
			t.Errorf("%q → %q → %+v（%v）", spec, got, again, err)
		}
	}
}

// 键名是 bwrap 的一个参数，值可以是任何东西 —— 所以不能有冒号的只有冒号左边那一半。
func TestParseEnvVarRefusesUnusableKeys(t *testing.T) {
	for _, spec := range []string{
		"TWIN_FOO",     // 少了冒号：那是一个键，不是一个值
		":bar",         // 少了键名
		"TWIN FOO:bar", // 键名里的空格会变成一个谁也查不到的名字
		"TWIN_FOO=bar", // shell 的写法；默认成"设了但没生效"比拒掉更糟
	} {
		if got, err := ParseEnvVar(spec); err == nil {
			t.Errorf("ParseEnvVar(%q) 应当报错，得到 %+v", spec, got)
		}
	}
}

// 两个键从别的节点搬进了 `bwrap`（tmpfs 来自 isolation、env 来自 exec）。读成"没设置"
// 会让两份定义各表一套：一份把目录盖空、另一份什么都没盖，而终端上一个字都不说。
func TestLoadRefusesTheMovedTmpfsAndEnvKeys(t *testing.T) {
	cases := map[string]string{
		"isolation.tmpfs": `id: demo
exec:
  target: /opt/demo/demo
isolation:
  tmpfs:
    - /var/cache/demo
`,
		"exec.env": `id: demo
exec:
  target: /opt/demo/demo
  env:
    FOO: bar
`,
	}
	for key, body := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, ProfileName)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "bwrap.") {
			t.Errorf("旧键 %s 必须指名报错并给出新位置，得到 %v", key, err)
		}
	}
}

// ro 与 dev 同时写不是"更严格"，而是 bwrap 表达不出来的东西（--dev-bind 天生可写）。
// String() 也会在其中丢一个，所以必须在 Check 那一层被拒，而不是让其中一个悄悄赢。
func TestCheckRefusesReadOnlyDeviceBind(t *testing.T) {
	i := &Instance{
		ID:    "x",
		Level: LevelNone,
		Binds: []Bind{{Source: "/dev/dri", Target: "/dev/dri", ReadOnly: true, Dev: true}},
	}
	if err := i.Check(); err == nil {
		t.Errorf(":ro + :dev 应当报错，得到 %+v", i.Binds[0])
	}
}

// The root mount's read-only spelling is three parts. Two of them is the shape a
// typo takes, and it is worth refusing rather than running: `/:/ro` reads as
// "mount the host root at /ro", which leaves the sandbox with no root at all —
// the program then cannot find itself, and nothing on the way says why.
func TestCheckRefusesRootMountedSomewhereElse(t *testing.T) {
	i := &Instance{ID: "x", Level: LevelNone, Target: "/bin/true",
		Binds: []Bind{{Source: "/", Target: "/ro"}}}
	if err := i.Check(); err == nil {
		t.Errorf("把根挂到别处应当报错，得到 %+v", i.Binds[0])
	}
	// Both real spellings pass, and both are the root: `/:/:ro` is the same
	// mount point, tightened.
	for _, b := range []Bind{
		{Source: "/", Target: "/", Dev: true},
		{Source: "/", Target: "/", ReadOnly: true},
	} {
		if !b.IsRootMount() {
			t.Errorf("%s 就是根挂载", b)
		}
		i.Binds = []Bind{b}
		if err := i.Check(); err != nil {
			t.Errorf("%s 应当通过 Check：%v", b, err)
		}
	}
}
