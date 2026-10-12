package yamlz

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// sample mirrors the shape profile.yml actually has: flat keys, nested
// mappings, scalar sequences and a sequence of mappings.
func sample() *Tree {
	t := New()
	t.Set("id", "wechat2")
	t.Set("name", "微信2")
	t.Set("badge", "2")
	t.Set("notify", true)

	id := t.Ensure("identity")
	id.Set("level", "alias")
	id.Set("wm_class", "wechat2")

	ex := t.Ensure("exec")
	ex.Set("target", "/opt/wechat/wechat")
	ex.Set("alias", "wechat2")
	env := New()
	env.Set("ELECTRON_OZONE_PLATFORM_HINT", "wayland")
	ex.Set("env", env)

	iso := t.Ensure("isolation")
	iso.Set("tmpfs", []string{})
	iso.Set("unshare", []string{"pid", "ipc"})
	iso.Set("nested_ns", false)

	bw := t.Ensure("bwrap")
	bw.Set("extra", []string{"--setenv", "FOO", "bar"})

	t.Ensure("tray").Set("override", true)

	e := t.AddTable("map.bind")
	e.Set("canonical", "~/.xwechat")
	e.Set("private", "~/.local/state/twin/wechat2/xwechat")
	e2 := t.AddTable("map.bind")
	e2.Set("canonical", "~/Documents/xwechat_files # 共享")
	e2.Set("private", "~/Documents/xwechat_files")
	return t
}

// A round trip is the contract that matters: the file is emitted, hand-edited,
// and read back, so encode(parse(encode(t))) has to be encode(t).
func TestRoundTrip(t *testing.T) {
	first := string(Encode(sample()))
	tree, err := Parse([]byte(first))
	if err != nil {
		t.Fatalf("Parse: %v\n--- 原文 ---\n%s", err, first)
	}
	again := string(Encode(tree))
	if again != first {
		t.Errorf("往返之后不一致：\n--- 第一次 ---\n%s\n--- 第二次 ---\n%s", first, again)
	}
	if !reflect.DeepEqual(loadSample(tree), loadSample(sample())) {
		t.Errorf("往返之后取出的值不一样：%+v", loadSample(tree))
	}
}

// loadSample reads the tree back through the accessors, which is how the
// instance layer will use it — the point being that a hand-edited file reaches
// the same fields.
func loadSample(t *Tree) map[string]any {
	return map[string]any{
		"id":       t.Str("id"),
		"name":     t.Str("name"),
		"badge":    t.Str("badge"),
		"notify":   t.Bool("notify"),
		"level":    t.At("identity").Str("level"),
		"env":      t.At("exec").At("env").Str("ELECTRON_OZONE_PLATFORM_HINT"),
		"tmpfs":    t.At("isolation").StrList("tmpfs"),
		"unshare":  t.At("isolation").StrList("unshare"),
		"nested":   t.At("isolation").Bool("nested_ns"),
		"extra":    t.At("bwrap").StrList("extra"),
		"override": t.Tree("tray").Bool("override"),
		"binds":    len(t.TablesAt("map.bind")),
		"bind0":    t.TablesAt("map.bind")[0].Str("canonical"),
	}
}

// Plain where it reads well, quoted where plain would mean something else. A
// digit string and a `#` in a path are the two that bite.
func TestQuoteOnlyWhenNeeded(t *testing.T) {
	cases := []struct{ in, want string }{
		{"wechat2", "wechat2"},
		{"微信2", "微信2"},
		{"~/.local/state/twin/wechat2", "~/.local/state/twin/wechat2"},
		{"2", `"2"`},
		{"true", `"true"`},
		{"yes", `"yes"`},
		{"", `""`},
		{"a: b", `"a: b"`},
		{"a #b", `"a #b"`},
		{"-x", `"-x"`},
		{"  padded  ", `"  padded  "`},
	}
	for _, c := range cases {
		if got := quote(c.in); got != c.want {
			t.Errorf("quote(%q) = %s，期望 %s", c.in, got, c.want)
		}
	}
}

// The document is untyped, so a bare number has to come back as text: reading
// `badge: 2` as an integer and then returning "" for the string field would be
// the silent failure this package exists to prevent.
func TestStrCoercesScalars(t *testing.T) {
	tree, err := Parse([]byte("badge: 2\nnotify: \"true\"\nsize: 256\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.Str("badge"); got != "2" {
		t.Errorf("Str(badge) = %q，期望 \"2\"", got)
	}
	if !tree.Bool("notify") {
		t.Error(`Bool("notify") 对引号里的 "true" 也应当为真`)
	}
	if got := tree.Int("size"); got != 256 {
		t.Errorf("Int(size) = %d，期望 256", got)
	}
}

// A note attached to a key is written directly above it, at that key's own
// column — and never through the `- ` that opens a sequence item, because a
// dash followed by `#` would turn the item into a comment.
//
// Parse then drops it. That asymmetry is the point: comments are what the file
// says to its reader, so a hand-edited one must not be able to change what a
// later render writes.
func TestCommentsSitAboveTheirKey(t *testing.T) {
	tree := New()
	tree.SetComment("id", "实例 id。")
	tree.Set("id", "wechat2")
	sub := tree.Ensure("identity")
	sub.SetComment("level", "none / alias / redirect。\n第二行。")
	sub.Set("level", "alias")

	got := string(Encode(tree))
	want := "# 实例 id。\n" +
		"id: wechat2\n" +
		"identity:\n" +
		"  # none / alias / redirect。\n" +
		"  # 第二行。\n" +
		"  level: alias\n"
	if got != want {
		t.Fatalf("注释没有落在键上方：\n--- 得到 ---\n%s\n--- 期望 ---\n%s", got, want)
	}

	parsed, err := Parse([]byte(got))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if again := string(Encode(parsed)); strings.Contains(again, "#") {
		t.Errorf("Parse 之后重新编码不该还带注释：\n%s", again)
	}
}

// A comment inside a sequence-of-mappings item still lands on its own line
// rather than after the dash.
func TestCommentsInsideSequenceItems(t *testing.T) {
	tree := New()
	e := tree.AddTable("map.bind")
	e.SetComment("canonical", "程序看到的路径")
	e.Set("canonical", "~/.xwechat")
	e.Set("private", "~/.local/state/twin/wechat2/xwechat")

	got := string(Encode(tree))
	if want := "map:\n  bind:\n    # 程序看到的路径\n    - canonical: ~/.xwechat\n" +
		"      private: ~/.local/state/twin/wechat2/xwechat\n"; got != want {
		t.Errorf("序列项里的注释位置不对：\n--- 得到 ---\n%s\n--- 期望 ---\n%s", got, want)
	}
}

// `#` only starts a comment at the start of a line or after a space, so a path
// that happens to contain one survives.
func TestCommentsAndHashes(t *testing.T) {
	tree, err := Parse([]byte("# 头部注释\nsource: /opt/we/chat#1.desktop\nname: a # 注释\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.Str("source"); got != "/opt/we/chat#1.desktop" {
		t.Errorf("source = %q，路径里的 # 被当成注释了", got)
	}
	if got := tree.Str("name"); got != "a" {
		t.Errorf("name = %q，行尾注释没去掉", got)
	}
}

// Everything outside the subset has to be refused with a line number: a
// half-understood identity file is worse than one that will not load.
func TestParseRefusesTheRest(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"Tab 缩进", "identity:\n\tlevel: alias\n", "Tab"},
		{"文档标记", "---\nid: x\n", "文档标记"},
		{"顶层序列", "- a\n- b\n", "顶层必须是映射"},
		{"重复键", "id: a\nid: b\n", "两次"},
		{"空值", "id:\nname: x\n", "没有值"},
		{"有歧义的布尔", "notify: yes\n", "歧义"},
		{"null", "id: null\n", "歧义"},
		{"锚点", "id: &x y\n", "不支持"},
		{"块标量", "id: |\n  x\n", "不支持"},
		{"flow 映射", "exec: {target: /x}\n", "flow 映射"},
		{"引号后面还有东西", `id: "x" y` + "\n", "引号后面"},
		{"引号键", `"id": x` + "\n", "key: value"},
		{"嵌套序列", "a:\n  -\n    - x\n", "嵌套序列"},
		{"序列缩进跳级", "map:\n  bind:\n    - canonical: a\n        private: b\n", "缩进"},
		{"顶层缩进", "  id: x\n", "缩进"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.in))
		if err == nil {
			t.Errorf("%s：居然解析成功了\n%s", c.name, c.in)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：报错 %q，期望包含 %q", c.name, err.Error(), c.want)
		}
		if !strings.Contains(err.Error(), "line ") {
			t.Errorf("%s：报错没带行号：%v", c.name, err)
		}
	}
}

// A mapping item may also be written with the dash alone and the keys below it.
// Refusing that would be refusing YAML that a user will reasonably type.
func TestDashAloneItem(t *testing.T) {
	tree, err := Parse([]byte("map:\n  bind:\n    -\n      canonical: /a\n      private: /b\n"))
	if err != nil {
		t.Fatal(err)
	}
	binds := tree.TablesAt("map.bind")
	if len(binds) != 1 || binds[0].Str("canonical") != "/a" {
		t.Errorf("拿到 %d 项：%+v", len(binds), binds)
	}
}

// A sequence of scalars is the other half of the schema (tmpfs / unshare /
// bwrap.extra), and it has to come back as []any so StrList can read it.
func TestScalarSequence(t *testing.T) {
	tree, err := Parse([]byte("isolation:\n  unshare:\n    - pid\n    - ipc\n  tmpfs: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := tree.At("isolation").StrList("unshare")
	if !reflect.DeepEqual(got, []string{"pid", "ipc"}) {
		t.Errorf("unshare = %v", got)
	}
	if l := tree.At("isolation").StrList("tmpfs"); len(l) != 0 {
		t.Errorf("tmpfs = %v，期望空", l)
	}
	if _, err := Parse([]byte("a:\n  - x\n  - k: v\n")); err == nil {
		t.Error("混合的序列必须被拒绝")
	}
}

// The encoder's output has to be stable across runs: the file is committed to
// git and diffed by humans.
func TestEncodeIsOrdered(t *testing.T) {
	var first string
	for i := 0; i < 5; i++ {
		got := string(Encode(sample()))
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("第 %d 次编码结果不同", i)
		}
	}
	// Values first, nested blocks after: that is the shape a human writes.
	lines := strings.Split(first, "\n")
	var idIdx, execIdx int
	for i, l := range lines {
		if strings.HasPrefix(l, "id:") {
			idIdx = i
		}
		if strings.HasPrefix(l, "exec:") {
			execIdx = i
		}
	}
	if idIdx > execIdx {
		t.Errorf("标量应当排在嵌套块之前：\n%s", fmt.Sprintf("%v", lines))
	}
}
