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
		ID:              "wechat2",
		Target:          "/opt/wechat/wechat",
		Source:          "/usr/share/applications/wechat.desktop",
		DesktopOverride: "./wechat2.desktop",
		WMClass:         "",
		IconSource:      "/usr/share/icons/hicolor/512x512/apps/wechat.png",
		Badge:           "2",
		BadgeStyle:      "corner",
		TrayOverride:    true,
		Level:           LevelAlias,
		// The program's own arguments: one line per argument group, like
		// `bwrap.extras`, and one token keeps a `~`. Nothing resolves it —
		// `~` and `$HOME` are left for the launching shell — so what matters
		// here is that the spelling comes back untouched.
		Args: []string{
			"--ozone-platform=wayland",
			"~/.config/wechat2/x",
		},
		NestedNS: true,
		Unshare:  []string{"ipc"},
		// The values carry the two things a separator must not touch: a colon
		// (`PATH=/a:/b`) and another `=` (`OPTS=--a=b`) both have to come back
		// as written — the split is at the first `=` and nowhere else.
		Env: []EnvVar{
			{Key: "TWIN_FOO", Value: "bar"},
			{Key: "PATH", Value: "/a:/b"},
			{Key: "OPTS", Value: "--a=b"},
		},
		// One line per argument group, and the fixture keeps a `~` in one of
		// them: that spelling has to survive the round trip untouched (Build is
		// what folds it into this machine's home, at launch time).
		Extras: []string{
			"--proc /proc",
			"--tmpfs /dev/shm",
			"--tmpfs ~/.cache/wechat",
			"--die-with-parent",
		},
		// The root is the key of its own (`gen` writes `dev` there), so the
		// fixture carries it that way and `bind` holds nothing but data.
		Root: "dev",
		Binds: []Bind{
			{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"},
			{Source: "/usr/share/wechat/theme.conf", Target: "~/.config/wechat/theme.conf", ReadOnly: true},
			{Source: "~/.config/wechat/pending.conf", Target: "~/pending.conf", ReadOnly: true, Try: true},
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
	out, first := loadRender(t, dir, data)
	if out.ID != in.ID || out.Level != in.Level || out.NestedNS != in.NestedNS {
		t.Errorf("scalars lost: %+v", out)
	}
	if out.Target != in.Target || out.Source != in.Source {
		t.Errorf("source lost: exec=%q desktop=%q", out.Target, out.Source)
	}
	if !reflect.DeepEqual(out.Args, in.Args) {
		t.Errorf("source.args lost: %q", out.Args)
	}
	// The entry's text is the one field the round trip is *supposed* to move:
	// the file keeps `./wechat2.desktop` — the spelling that lets a bundle be
	// picked up and moved to another machine — while every reader downstream
	// gets the absolute path, because Load resolves relative paths once, against
	// the definition's own directory, and nobody resolves them again (see Load).
	if want := filepath.Join(dir, "wechat2.desktop"); out.DesktopOverride != want {
		t.Errorf("override.desktop = %q, want %q", out.DesktopOverride, want)
	}
	if out.WMClass != in.WMClass || out.IconSource != in.IconSource || out.Badge != in.Badge ||
		out.BadgeStyle != in.BadgeStyle || out.TrayOverride != in.TrayOverride {
		t.Errorf("override lost: %+v", out)
	}
	// The root is one word and has to come back as that word: it decides which
	// mount the whole sandbox is laid on, and reading it back as "nothing said"
	// would silently drop the mount (Check then refuses the definition, so the
	// instance would stop installing for a reason nobody can see in the file).
	if out.Root != in.Root {
		t.Errorf("bwrap.root = %q, want %q", out.Root, in.Root)
	}
	if !reflect.DeepEqual(out.Binds, in.Binds) {
		t.Errorf("binds lost: %+v", out.Binds)
	}
	if !reflect.DeepEqual(out.Unshare, in.Unshare) {
		t.Errorf("isolation.unshare lost: %+v", out.Unshare)
	}
	if !reflect.DeepEqual(out.Env, in.Env) {
		t.Errorf("bwrap.env lost: %+v", out.Env)
	}
	if !reflect.DeepEqual(out.Extras, in.Extras) {
		t.Errorf("bwrap extras lost: %q", out.Extras)
	}
	// Byte-stability is what keeps a rewrite from drifting, and it starts at the
	// *second* pass: the first one is the pass that resolved the relative path
	// above, and nothing writes that resolved form back anywhere.
	_, second := loadRender(t, t.TempDir(), first)
	if string(second) != string(first) {
		t.Errorf("re-render is not stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// loadRender does the two steps a human does: it writes a definition into the
// given directory, reads it back, and renders it again. It returns both the
// value and the second document, so a test can say which bytes must not move
// between the two.
func loadRender(t *testing.T, dir string, data []byte) (*Instance, []byte) {
	t.Helper()
	path := filepath.Join(dir, ProfileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := Load(path)
	if err != nil {
		t.Fatalf("Load:\n%s\n%v", data, err)
	}
	again, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	return it, again
}

// The entry's text is not in the definition at all any more: it is a file of
// its own, named by `override.desktop`. Five keys here answered "what does the
// menu say?", which is a question about a text file — and every one of them
// stopped being changeable the moment the file existed.
func TestSnapshotKeepsTheEntryTextOutOfTheDefinition(t *testing.T) {
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "\n  desktop: ./wechat2.desktop\n") {
		t.Errorf("override.desktop 没有写进 override 节点：\n%s", text)
	}
	for _, key := range []string{"name", "name_en", "comment", "comment_en", "keywords", "categories"} {
		if strings.Contains(text, "\n"+key+":") || strings.Contains(text, "\n  "+key+":") {
			t.Errorf("%s 还在定义里 —— 它属于 override.desktop 那份正文：\n%s", key, text)
		}
	}
	// wm_class is the one knob about that text that stays here, and it is
	// written even when empty: "the id" is a decision, not a missing value.
	if !strings.Contains(text, "\n  wm_class:") {
		t.Errorf("override.wm_class 应当无条件写出（留空 = 用 id）：\n%s", text)
	}
	if !strings.Contains(text, "\n  tray: true\n") {
		t.Errorf("override.tray 应当在 override 节点里：\n%s", text)
	}
}

// A snapshot written in the old shape does not load, and that is deliberate:
// the keys that moved are refused *by name*, with the place they moved to. Read
// as "not asked for", a definition whose entry text went missing installs half
// an instance — and the half it loses is the identity.
func TestLoadRefusesTheOldSections(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		// The name the reader has to be given: every one of these keys lives
		// somewhere else now, and a message that only said "unknown key" would
		// leave them to guess which of the two halves of the node it went to.
		want string
	}{
		// One stale node per case, everything else already in the new spelling:
		// reading only ever reports the first thing it finds, so a body with the
		// whole old schema in it would test the *order* of these messages
		// instead of each message.
		{"desktop", "id: wechat2\ndesktop:\n  name: 微信2\n  source: /usr/share/applications/wechat.desktop\nsource:\n  exec: /opt/wechat/wechat\n", "source.desktop"},
		{"identity", "id: wechat2\nidentity:\n  level: alias\n  wm_class: wechat2\nsource:\n  exec: /opt/wechat/wechat\n", "isolation.level"},
		{"icon", "id: wechat2\nicon:\n  source: /usr/share/icons/x.png\nsource:\n  exec: /opt/wechat/wechat\n", "override.icon"},
		{"exec", "id: wechat2\nexec:\n  target: /opt/wechat/wechat\n", "source.exec"},
		{"tray", "id: wechat2\ntray:\n  override: false\nsource:\n  exec: /opt/wechat/wechat\n", "override.tray"},
	} {
		path := filepath.Join(t.TempDir(), ProfileName)
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("旧写法 %s 必须指名报错并给出新位置 %s，得到 %v", c.name, c.want, err)
		}
	}
	// The pre-section spelling of the entry text — the same keys at the root —
	// is refused for the same reason, not read as "inherit from the source".
	legacy := "id: wechat2\nname: 微信2\nkeywords: wechat;微信;\nsource:\n  exec: /opt/wechat/wechat\n"
	path := filepath.Join(t.TempDir(), ProfileName)
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "override.desktop") {
		t.Errorf("根上的 name 必须指向 override.desktop，得到 %v", err)
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
	// And the root is its own key right above it — a word, not a `/:/:dev`
	// entry pretending the file's order means something (see Instance.Root).
	if !strings.Contains(s, "\n  root: dev\n") {
		t.Errorf("bwrap.root 应当是 bwrap 节点里的第一个键：\n%s", s)
	}
	if i, j := strings.Index(s, "\n  root:"), strings.Index(s, "\n  bind:"); i > j {
		t.Errorf("root 应当写在 bind 前面（它就是命令里的第一条挂载）：\n%s", s)
	}
}

// An instance with an empty option list still has to say so, or a reader cannot
// tell "there is nothing to pass" from "the key was forgotten".
func TestRenderAlwaysWritesBwrapExtras(t *testing.T) {
	it := base()
	it.Extras = nil
	it.Level = LevelNone // an empty list is only legitimate without a sandbox
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "extras: []") {
		t.Errorf("empty option list is not visible in the snapshot:\n%s", data)
	}
	// And it is written last in the node, which is the one place its position
	// does not mean anything (Build emits it right after the root).
	if i, j := strings.Index(string(data), "\n  extras:"), strings.Index(string(data), "\n  env:"); i < j {
		t.Errorf("extras 应当写在 bwrap 节点的最后一个键：\n%s", data)
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
	if len(out.Extras) != 0 {
		t.Errorf("empty option list came back as %q", out.Extras)
	}
}

// `source.args` 是程序的参数，不是 bwrap 的选项，所以它没有"基座"那种默认值 —— 但
// "一条都没有"仍然是一个决定，而且这个键的注释是它唯一的说明书（终端不再解释任何键）。
func TestRenderAlwaysWritesSourceArgs(t *testing.T) {
	it := base()
	it.Args = nil
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "\n  args: []\n") {
		t.Errorf("空参数列表要写成 args: []，否则读者分不清「没有参数」和「忘了这个键」：\n%s", s)
	}
	// It lives in `source`, right behind `exec`: the two lines answer one
	// question — which program, started with what.
	if i, j := strings.Index(s, "\n  exec:"), strings.Index(s, "\n  args:"); i < 0 || j < i {
		t.Errorf("args 应当写在 source.exec 之后：\n%s", s)
	}
	path := filepath.Join(t.TempDir(), ProfileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Args) != 0 {
		t.Errorf("空参数列表读回来变成 %q", out.Args)
	}
}

// 一条空行会让文件看起来写了参数，而启动器里一个 token 都不会出现 —— 和 extras
// 拒绝它的是同一个理由：静默地少一节，比报错难查得多。
func TestCheckRefusesABlankSourceArgLine(t *testing.T) {
	it := base()
	it.Args = []string{"--foo", "   "}
	err := it.Check()
	if err == nil || !strings.Contains(err.Error(), "source.args[1]") {
		t.Fatalf("空行必须指名报错，得到 %v", err)
	}
}

// 手写时把列表写成一行字符串是很自然的笔误；读成"没有参数"会让程序带着错的行为
// 启动，而文件里看起来它明明写了。
func TestLoadRefusesSourceArgsWrittenAsAScalar(t *testing.T) {
	path := filepath.Join(t.TempDir(), ProfileName)
	body := "id: demo2\nsource:\n  exec: /opt/demo/demo\n  args: --foo\n" +
		"override:\n  desktop: ./demo2.desktop\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "args") {
		t.Fatalf("一行字符串必须被拒绝，得到 %v", err)
	}
}

// Deleting the whole list under a key leaves the bare key behind, and that is
// what the file's own comments mean by "留空" — so it has to read as the empty
// list, not as a syntax error. Two things must survive that leniency: the keys
// after it, and the one thing an empty list can really mean (a definition with
// no root mount, which is refused by name).
func TestLoadReadsABareKeyAsNoEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProfileName)
	// The id has to differ from the real program's file name, or Check refuses
	// it as a no-op rename and this test would be reading the wrong failure.
	body := `id: demo2
source:
  exec: /opt/demo/demo
override:
  desktop: ./demo2.desktop
  tray: false
bwrap:
  bind:
    - /:/:dev
  extras:
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := Load(path)
	if err != nil {
		t.Fatalf("空的选项列表必须能读（它就是「留空 = 一个都不传」）：%v", err)
	}
	if len(it.Extras) != 0 {
		t.Errorf("Extras = %q，期望空", it.Extras)
	}
	if it.TrayOverride {
		t.Error("`extras:` 之后那个键没读到：空值把文件的其余部分吃掉了")
	}
	if err := it.Check(); err != nil {
		t.Errorf("根挂载在 bind 里，空 extras 不该被拒绝：%v", err)
	}

	// 空 bind 与漏写 bind 给出的是同一个事实：这份定义什么都没有挂。
	bare := filepath.Join(dir, "no-root.yml")
	noRootBody := `id: demo2
source:
  exec: /opt/demo/demo
override:
  desktop: ./demo2.desktop
bwrap:
  bind:
`
	if err := os.WriteFile(bare, []byte(noRootBody), 0o644); err != nil {
		t.Fatal(err)
	}
	noRoot, err := Load(bare)
	if err != nil {
		t.Fatalf("空的 bind 也必须能读：%v", err)
	}
	if err := noRoot.Check(); err == nil || !strings.Contains(err.Error(), "根挂载") {
		t.Errorf("空 bind 就是没有根挂载，仍然必须指名报错，得到 %v", err)
	}
}

// twin keeps no sandbox base of its own, so a definition with no root mount is a
// namespace with nothing mounted: the program would not find itself, and nothing
// in the failure would name twin. Refusing here is the only place that can say
// which file to fix.
//
// The root can be said in three spellings and any one of them counts:
// `bwrap.root` (what `gen` writes), the `/:/:dev` entry every definition written
// before that key carries, and `--dev-bind / /` typed by hand into `extras`.
// What is refused is having none of them.
func TestCheckRefusesADefinitionWithoutARootMount(t *testing.T) {
	noRoot := base()
	// All three places that can hold it have to be cleared before this means
	// anything.
	noRoot.Root = ""
	noRoot.Binds = nil
	noRoot.Extras = nil
	if err := noRoot.Check(); err == nil || !strings.Contains(err.Error(), "根挂载") {
		t.Fatalf("没有根挂载必须被拒绝并指名那个键，得到 %v", err)
	}
	viaKey := base()
	if err := viaKey.Check(); err != nil {
		t.Errorf("bwrap.root: dev 就该通过（这是 gen 写的那一份）：%v", err)
	}
	viaBind := base()
	viaBind.Root = ""
	viaBind.Binds = append([]Bind{{Source: "/", Target: "/", Dev: true}}, viaBind.Binds...)
	if err := viaBind.Check(); err != nil {
		t.Errorf("老写法 bind 里那条 /:/:dev 也该通过：%v", err)
	}
	viaExtras := base()
	viaExtras.Root = ""
	viaExtras.Binds = nil
	viaExtras.Extras = []string{"--dev-bind / /", "--proc /proc"}
	if err := viaExtras.Check(); err != nil {
		t.Errorf("旧写法的 --dev-bind / / 也该通过：%v", err)
	}
	// Level none runs no bubblewrap at all, so it owes no root.
	plain := base()
	plain.Level = LevelNone
	plain.Extras = nil
	if err := plain.Check(); err != nil {
		t.Errorf("level=none 不过 bwrap，没有根挂载不该是错误：%v", err)
	}
}

// 根挂载只允许说一遍。两处都说时，两条挂载都会发进命令里，而"程序拿到哪个根"
// 就由发射顺序决定了 —— 那是一个谁都没写下来的答案（而且第一条会让 `--explain`
// 的路径映射那一节多出一条 `/`），所以这里指名报错而不是挑一个。
func TestCheckRefusesARootSaidTwice(t *testing.T) {
	i := base()
	i.Root = "ro"
	i.Binds = append([]Bind{{Source: "/", Target: "/", Dev: true}}, i.Binds...)
	err := i.Check()
	if err == nil || !strings.Contains(err.Error(), "bwrap.root") {
		t.Fatalf("bwrap.root 与 bind 里那条根挂载同时存在必须被拒绝，得到 %v", err)
	}
	// `rw` 那两个词里没有模式，那份 bind 里的 `/:/` 也就是普通的自映射 —— 但它
	// 仍然是根，仍然只能出现一次。
	i = base()
	i.Root = "rw"
	i.Binds = append([]Bind{{Source: "/", Target: "/"}}, i.Binds...)
	if err := i.Check(); err == nil {
		t.Fatal("不带模式的 /:/ 同样是根挂载，两处都在时也要拒绝")
	}
}

// 三个词就是 bind 的三个模式，别的拼法不是"再一种根"，而是另一种挂载 ——
// 没有对应的实现，所以在这里拦下，而不是让 Build 把它当成 dev 悄悄落盘。
func TestCheckRefusesAnUnknownRoot(t *testing.T) {
	for _, bad := range []string{"readonly", "read-only", "root", "RO", "ro:try"} {
		i := base()
		i.Root = bad
		if err := i.Check(); err == nil || !strings.Contains(err.Error(), "bwrap.root") {
			t.Errorf("root=%q 必须指名报错并给出可取值，得到 %v", bad, err)
		}
	}
}

// 那个键读进来是那个词：它决定整个沙箱架在哪儿，读错（比如当成空值）会让这份定义
// 在 Check 那里以"没有根挂载"被拒 —— 一个文件里看不出来的理由。
func TestLoadReadsTheRootKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProfileName)
	body := `id: demo2
source:
  exec: /opt/demo/demo
override:
  desktop: ./demo2.desktop
isolation:
  level: alias
bwrap:
  root: ro
  bind:
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if it.Root != "ro" {
		t.Fatalf("bwrap.root = %q，want ro", it.Root)
	}
	if got, ok := it.RootBind(); !ok || !got.ReadOnly || got.Source != "/" || got.Target != "/" {
		t.Errorf("root: ro 应当读成 --ro-bind / /，得到 %+v（ok=%v）", got, ok)
	}
	if err := it.Check(); err != nil {
		t.Errorf("bwrap.root 单独说根挂载就该通过：%v", err)
	}
	// 空的那一个读回来是"这个键什么都没说"，不是 `dev`：根挂载的缺省值由 Check
	// 按事实报出来（这份定义没有根挂载），解析器不替文件猜一个。
	empty := filepath.Join(dir, "empty-root.yml")
	emptyBody := strings.Replace(body, "  root: ro", `  root: ""`, 1)
	if err := os.WriteFile(empty, []byte(emptyBody), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err = Load(empty)
	if err != nil {
		t.Fatal(err)
	}
	if it.Root != "" {
		t.Errorf("空值读成了 %q，它必须就是「什么都没说」", it.Root)
	}
	if err := it.Check(); err == nil || !strings.Contains(err.Error(), "根挂载") {
		t.Errorf("空的 root 等于没有根挂载（这份文件也没有别的写法），应当被拒绝，得到 %v", err)
	}
}

// `bwrap.extra` was only what the user appended, while the key that replaced it
// is the whole merged list. Reading one as the other would hand bwrap a
// namespace with no root — a launch that fails for reasons that never mention
// twin.
func TestLoadRefusesTheOldExtraKey(t *testing.T) {
	for key, why := range map[string]string{
		"extra": "只装用户追加的 token，和现在的键不是一回事",
		"args":  "同一个键改了名字（现在叫 extras）",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, ProfileName)
		body := "id: demo\nsource:\n  exec: /opt/demo/demo\noverride:\n  desktop: ./demo.desktop\n" +
			"bwrap:\n  " + key + ":\n    - --unshare-pid\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "bwrap.extras") {
			t.Errorf("旧键 %s（%s）必须指名报错并给出新名字，得到 %v", key, why, err)
		}
	}
}

// `proc` / `tmpfs` / `remountRO` 是三个折进 `extras` 的键。读成"没有这些挂载"会让
// 一份写明了「把这里盖空」的定义装出一个什么都没盖的实例 —— 而终端上一个字都不说。
func TestLoadRefusesTheFoldedBwrapKeys(t *testing.T) {
	head := "id: demo\nsource:\n  exec: /opt/demo/demo\noverride:\n  desktop: ./demo.desktop\nbwrap:\n"
	for _, key := range []string{"proc", "tmpfs", "remountRO"} {
		path := filepath.Join(t.TempDir(), ProfileName)
		body := head + "  " + key + ":\n    - /var/cache/demo\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "bwrap.extras") {
			t.Errorf("旧键 bwrap.%s 必须指名报错并给出新位置，得到 %v", key, err)
		}
	}
}

// `bind` 从顶层搬进 `bwrap` 之后，一份老定义里的顶层 bind 不能再被当成空列表读掉：
// 前者描述"程序的数据挂在哪"，后者描述"什么都没挂"，而后者会一直到程序看不见自己的文件
// 才暴露，那时离这次编辑已经很远了。
func TestLoadRefusesTheTopLevelBindKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProfileName)
	body := `id: demo
source:
  exec: /opt/demo/demo
override:
  desktop: ./demo.desktop
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

// The alias is the id, so the identity cannot split any more — what is left to
// refuse is the id colliding with the real file name: renaming to it would be a
// no-op while the desktop entry, the launcher and the tray table all claim it
// happened.
func TestCheckRejectsIDEqualToRealName(t *testing.T) {
	it := base()
	it.ID = "wechat"
	if err := it.Check(); err == nil {
		t.Fatal("id == target basename is a no-op and must be refused")
	}
}

// The entry's text has to come from somewhere, and the only somewhere is the
// file override.desktop names: an empty value would send the copy at the
// instance directory itself, failing at the last possible moment.
func TestCheckRejectsAMissingDesktopOverride(t *testing.T) {
	it := base()
	it.DesktopOverride = ""
	if err := it.Check(); err == nil || !strings.Contains(err.Error(), "override.desktop") {
		t.Fatalf("空 override.desktop 必须被拒绝并指名那个键，得到 %v", err)
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
	// The alias is the id, and the id is the first line of the definition —
	// which is all a replay needs to know which name to build. Nothing else
	// may spell it: a second spelling is a second answer.
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "id: wechat2\n") {
		t.Errorf("id 没写进快照：\n%s", data)
	}
	if strings.Contains(string(data), "alias:") {
		t.Errorf("别名还是 id，定义里不该有第二个键说它：\n%s", data)
	}
	if !LevelRedirect.Implemented() || !LevelRedirect.Renames() {
		t.Error("redirect 必须同时是「已实现」与「会改名」")
	}
	if LevelNone.Renames() {
		t.Error("none 不改名")
	}
}

// wrap 是"沙箱但不改名"：它要根挂载（它真的过 bwrap），不建别名，所以 id 与真实
// 文件名相同也不再是空操作 —— 这条以前对 none 也是拒的，而那正是 wrap 要拆开的两个轴。
func TestCheckAcceptsWrap(t *testing.T) {
	it := base()
	it.Level = LevelWrap
	if err := it.Check(); err != nil {
		t.Errorf("wrap 应当被接受：%v", err)
	}
	if !LevelWrap.Implemented() || !LevelWrap.Sandboxed() || LevelWrap.Renames() {
		t.Error("wrap 必须同时是「已实现」「过 bwrap」「不改名」")
	}

	// The id naming the program's own file would be a no-op rename; there is
	// no rename here, so it is legitimate.
	it = base()
	it.Level = LevelWrap
	it.ID = it.TargetBase()
	it.Binds = nil
	if err := it.Check(); err != nil {
		t.Errorf("wrap 不建别名，id 与程序同名不该被拒：%v", err)
	}

	// The root check follows the sandbox, not the rename: a wrap definition with
	// nothing mounted would start the program in an empty namespace.
	it = base()
	it.Level = LevelWrap
	it.Root = ""
	it.Binds = nil
	it.Extras = nil
	if err := it.Check(); err == nil || !strings.Contains(err.Error(), "根挂载") {
		t.Errorf("没有根挂载的 wrap 定义必须被拒，得到 %v", err)
	}

	// ...while none still needs nothing, because it builds no sandbox.
	it = base()
	it.Level = LevelNone
	it.Root = ""
	it.Binds = nil
	it.Extras = nil
	if err := it.Check(); err != nil {
		t.Errorf("none 不过 bwrap，没有根挂载是正常的：%v", err)
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
		// The root is kept, so the refusal cannot come from "no root mount" and
		// quietly pass for the wrong reason.
		it.Extras = []string{"--dev-bind / /", c.tok}
		err := it.Check()
		if err == nil {
			t.Errorf("CheckBwrapToken(%q) must be refused: %s", c.tok, c.why)
			continue
		}
		if !strings.Contains(err.Error(), "bwrap.extras") {
			t.Errorf("拒绝 %q 的错误应当点出它在哪：%v", c.tok, err)
		}
	}
}

func TestCheckAcceptsOrdinaryPassthrough(t *testing.T) {
	it := base()
	it.Extras = []string{"--dev-bind / /", "--unshare-pid", "--setenv TWIN_FOO bar", "--chdir /tmp"}
	if err := it.Check(); err != nil {
		t.Fatalf("ordinary bwrap option lines must pass: %v", err)
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
source:
  exec: ./bin/demo
  desktop: ./share/demo.desktop
override:
  desktop: ./demo.desktop
  icon:
    source: assets/demo.png
bwrap:
  bind:
    - ./state/demo:~/.demo
    - /abs/data:~/abs
    - ~/data:~/data-in-home
  env:
    - DEMO_MODE=on
    - PATH=/a:/b
  extras:
    - --proc proc-mirror
    - --tmpfs cache
    - --tmpfs /tmp
    - --remount-ro secrets
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "bin/demo"); it.Target != want {
		t.Errorf("source.exec = %q, want %q", it.Target, want)
	}
	if want := filepath.Join(dir, "share/demo.desktop"); it.Source != want {
		t.Errorf("source.desktop = %q, want %q", it.Source, want)
	}
	if want := filepath.Join(dir, "assets/demo.png"); it.IconSource != want {
		t.Errorf("override.icon.source = %q, want %q", it.IconSource, want)
	}
	// The entry's text resolves the same way, and that is what makes a bundle
	// (definition + entry text) movable: `gen` and Load have to agree about
	// `./demo.desktop`, or gen writes one file and install copies another.
	if want := filepath.Join(dir, "demo.desktop"); it.DesktopOverride != want {
		t.Errorf("override.desktop = %q, want %q", it.DesktopOverride, want)
	}
	// Nothing inside `extras` is resolved: it is a bwrap option list, and a
	// relative path in it is a path relative to the *launcher's* working
	// directory — an answer nobody chose. Only `~` is folded, at launch time.
	wantExtras := []string{"--proc proc-mirror", "--tmpfs cache", "--tmpfs /tmp", "--remount-ro secrets"}
	if !reflect.DeepEqual(it.Extras, wantExtras) {
		t.Errorf("bwrap.extras = %q, want %q（twin 不解析它）", it.Extras, wantExtras)
	}
	wantEnv := []EnvVar{{Key: "DEMO_MODE", Value: "on"}, {Key: "PATH", Value: "/a:/b"}}
	if !reflect.DeepEqual(it.Env, wantEnv) {
		t.Errorf("bwrap.env = %+v, want %+v（等号只在第一个处断开）", it.Env, wantEnv)
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

// TWIN_INSTANCE moved out of `bwrap.env` into what Build always emits（值就是
// id）：老定义里那一行等于 twin 自己会加的那条，于是读进来直接丢掉 —— 一份 `gen`
// 写出来的文件不该因为这次搬家而装不上。值不一致是另一回事：文件在要求一个 twin
// 会覆盖掉的值，而那正是"终端上什么都不说"的那类分歧。
func TestLoadDropsTheOldTwinInstanceLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ProfileName)
	body := `id: demo2
source:
  exec: /opt/demo/demo
override:
  desktop: ./demo2.desktop
bwrap:
  bind:
    - /:/:dev
  env:
    - TWIN_INSTANCE=demo2
    - FOO=bar
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := Load(path)
	if err != nil {
		t.Fatalf("值等于 id 的老定义必须还能读：%v", err)
	}
	if len(it.Env) != 1 || it.Env[0].Key != "FOO" {
		t.Errorf("bwrap.env = %+v，那条 TWIN_INSTANCE 应当被丢掉", it.Env)
	}
	if err := it.Check(); err != nil {
		t.Errorf("读进来的实例应当能通过校验（TWIN_INSTANCE 由 Build 加）：%v", err)
	}
}

func TestLoadRefusesAMismatchedTwinInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), ProfileName)
	body := `id: demo2
source:
  exec: /opt/demo/demo
override:
  desktop: ./demo2.desktop
bwrap:
  bind:
    - /:/:dev
  env:
    - TWIN_INSTANCE=other
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "TWIN_INSTANCE") {
		t.Fatalf("值不一致必须指名报错（它会被 twin 覆盖），得到 %v", err)
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

// 托盘覆盖是"这台机器上要不要碰宿主那张表"的决定，必须进快照：reinstall 只读快照
// （不读 config.toml），少了它，一次 --refresh 就会在明确关掉它的机器上把条目
// 又写回去。
func TestTrayOverrideIsPartOfTheSnapshot(t *testing.T) {
	it := base()
	it.TrayOverride = false
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\n  tray: false\n") {
		t.Fatalf("关掉的状态必须看得见（override.tray）：\n%s", data)
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
		t.Error("override.tray = false 读回来变成了 true")
	}
}

// 没有 override.tray 那份键的老快照等于"要覆盖"：它们全都写于这个选项存在之前，
// 把它们的语义翻成"不管"会让已经写进宿主表的条目悄悄失去维护。
func TestTrayOverrideDefaultsToTrueForOldSnapshots(t *testing.T) {
	it := base()
	it.TrayOverride = true
	data, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	old := dropLine(t, string(data), "  tray: true")
	if old == string(data) {
		t.Fatalf("测试前提不成立：快照里没有 override.tray\n%s", data)
	}
	if strings.Contains(old, "  tray:") {
		t.Fatalf("tray 行没删干净：\n%s", old)
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
		t.Error("缺 override.tray 应当按 true 处理")
	}
}

// dropLine returns doc without one indented line — the shape a file written
// before that key existed has. It matches the whole line, so a comment that
// mentions the key does not count as the key.
func dropLine(t *testing.T, doc, line string) string {
	t.Helper()
	var out []string
	for _, ln := range strings.Split(doc, "\n") {
		if ln == line {
			continue
		}
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

// 快照现在是实例的说明书：`twin gen` 只报路径，逐键的解释全在这份文件里。
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

// 每个键上面那段说明最多三行。这份文件是给人当场读的：注释一旦淹过内容，读者就跳过它，
// 而它是这些键**唯一**的说明书（终端不再重复任何键，见设计 §4）。
//
// 头那一段与 id 的说明连在一起（它们之间没有别的行），所以整段放行：header 自己是固定的
// 三行，id 一行。
func TestEveryNoteFitsInThreeLines(t *testing.T) {
	data, err := base().Render()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	run, start, header := 0, 0, true
	for n, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if run == 0 {
				start = n + 1
			}
			run++
			continue
		}
		if run > 3 && !header {
			t.Errorf("第 %d 行起那段说明有 %d 行，守着 %q：读的人会跳过它", start, run, trimmed)
		}
		header, run = false, 0
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
	// 而且空映射必须能原样读回来：定义里那份相对路径（override.desktop）在 Load
	// 那一步被解析成绝对路径，所以往返的稳定性从第二份起成立 —— 谁也不会把解析后
	// 的形式写回盘上（见 Load）。
	out, first := loadRender(t, t.TempDir(), data)
	if len(out.Binds) != 0 {
		t.Errorf("空映射读回来变成 %+v", out.Binds)
	}
	_, second := loadRender(t, t.TempDir(), first)
	if string(second) != string(first) {
		t.Errorf("空映射的往返不稳定：\n--- 第一次 ---\n%s\n--- 第二次 ---\n%s", first, second)
	}
}

// The one-line form is the whole on-disk contract for a mapping, so every shape
// it accepts has to survive being written back out: `twin reinstall` re-renders from the
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
		// `:try` is written after the mode, and the mode is spelled out on the
		// way back — a bare `源:目标:try` would be a second spelling of one
		// thing (see Bind.String).
		"/src:/dst:ro:try",
		"/src:/dst:rw:try",
		"~/.local/state/twin/wechat2/vlcrc:~/vlcrc:rw:try",
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
	// `:try` combines with every mode: it says what to do when the source is
	// missing, which is a question independent of how the mount is made.
	if b, err := ParseBind("/src:/dst:ro:try"); err != nil || !b.ReadOnly || !b.Try {
		t.Errorf(":ro:try：%+v / %v", b, err)
	}
	if b, err := ParseBind("/src:/dst:rw:try"); err != nil || b.ReadOnly || b.Dev || !b.Try {
		t.Errorf(":rw:try：%+v / %v", b, err)
	}
	if b, err := ParseBind("/dev/dri:/dev/dri:dev:try"); err != nil || !b.Dev || !b.Try {
		t.Errorf(":dev:try：%+v / %v", b, err)
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
		// `:try` 必须跟在写全的模式后面：三段里的 try 会被读成模式，
		// 而两段的 `x:try` 从第一天起就是"目标叫 try"。
		"/x:/y:try",
	} {
		if got, err := ParseBind(spec); err == nil {
			t.Errorf("ParseBind(%q) 应当报错，得到 %+v", spec, got)
		}
	}
}

// 一行写法是盘上的完整契约，`键=值` 也一样：写出去要能原样读回来，读不回来就是改了
// 用户写下的东西。这里同时钉住那两个必然会被写出来的形状 —— 值里带冒号（PATH=/a:/b）
// 和值里再有一个等号（OPTS=--a=b）。
func TestParseEnvVarRoundTrips(t *testing.T) {
	for _, spec := range []string{
		"TWIN_FOO=bar",
		"PATH=/a:/b",
		"LC_ALL=", // 空值：程序分得清"设了但为空"和"没设"
		"URL=https://example.com:8443/x",
		"OPTS=--a=b",
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

// 键名是 bwrap 的一个参数，值可以是任何东西 —— 所以不能有问题的只有等号左边那一半。
func TestParseEnvVarRefusesUnusableKeys(t *testing.T) {
	for _, spec := range []string{
		"TWIN_FOO",     // 少了等号：那是一个键，不是一个值
		"=bar",         // 少了键名
		"TWIN FOO=bar", // 键名里的空格会变成一个谁也查不到的名字
		"TWIN_FOO:bar", // 冒号那一版的老写法：它现在是一个没有等号的字符串
	} {
		if got, err := ParseEnvVar(spec); err == nil {
			t.Errorf("ParseEnvVar(%q) 应当报错，得到 %+v", spec, got)
		}
	}
}

// 两个键从别的节点搬进了 `bwrap`（--tmpfs 来自 isolation、env 来自 exec），而
// `isolation.tmpfs` 更是连键都没有了。读成"没设置"会让两份定义各表一套：一份把目录
// 盖空、另一份什么都没盖，而终端上一个字都不说 —— 所以每一处都指名报错，并且要说清
// 搬到哪去了，否则读者只知道"它不在这儿"。
func TestLoadRefusesTheMovedTmpfsAndEnvKeys(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"isolation.tmpfs": {`id: demo
source:
  exec: /opt/demo/demo
isolation:
  tmpfs:
    - /var/cache/demo
`, "bwrap.extras"},
		"exec.env": {`id: demo
source:
  exec: /opt/demo/demo
exec:
  env:
    FOO: bar
`, "bwrap.env"},
	}
	for key, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, ProfileName)
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("旧键 %s 必须指名报错并给出新位置（%s），得到 %v", key, c.want, err)
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
	i := &Instance{ID: "x", Level: LevelNone, Target: "/bin/true", DesktopOverride: "./x.desktop",
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
