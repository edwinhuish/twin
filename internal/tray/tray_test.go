package tray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --------------------------------------------------------------------- parsing

func TestParseAcceptsTheFormsTheHostPrints(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Entry
	}{
		{"未设置（host 自己的输出）", "@a(sss) []", nil},
		{"裸空数组", "[]", nil},
		{"空串", "", nil},
		{"一行", "[('wechat2', '/home/e/.local/share/appicons/wechat2.png', '/home/e/.local/share/appicons/wechat2.png')]",
			[]Entry{{"wechat2", "/home/e/.local/share/appicons/wechat2.png", "/home/e/.local/share/appicons/wechat2.png"}}},
		{"两行", "[('a', '/a.png', '/a.png'), ('b', '/b.png', '/b.png')]",
			[]Entry{{"a", "/a.png", "/a.png"}, {"b", "/b.png", "/b.png"}}},
		{"带类型标注", "@a(sss) [('a', '/a.png', '/a.png')]",
			[]Entry{{"a", "/a.png", "/a.png"}}},
		{"双引号", `[("a", "/a b.png", "/a b.png")]`,
			[]Entry{{"a", "/a b.png", "/a b.png"}}},
		{"转义的单引号与反斜杠", `[('a\'b', '/x\\y.png', '/x\\y.png')]`,
			[]Entry{{`a'b`, `/x\y.png`, `/x\y.png`}}},
		{"末尾多余空白与换行", "[('a', '/a.png', '/a.png')]\n",
			[]Entry{{"a", "/a.png", "/a.png"}}},
		{"空表带空白", "@a(sss) [ ]", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Parse(c.in)
			if err != nil {
				t.Fatalf("Parse(%q) 报错：%v", c.in, err)
			}
			if !sameEntries(got, c.want) {
				t.Errorf("Parse(%q)\n got %#v\nwant %#v", c.in, got, c.want)
			}
		})
	}
}

func TestParseRejectsGarbageInsteadOfGuessing(t *testing.T) {
	for _, in := range []string{
		"@a(sss)",             // 只有标注
		"[('a', '/b')]",       // 两字段
		"[('a', '/b', '/c'",   // 没闭合
		"('a', '/b', '/c')",   // 不是数组
		"[('a', '/b', '/c')]", // 没问题？—— 这条在下面单独断言
	} {
		_, err := Parse(in)
		ok := in == "[('a', '/b', '/c')]"
		if ok && err != nil {
			t.Errorf("Parse(%q) 不该报错：%v", in, err)
		}
		if !ok && err == nil {
			t.Errorf("Parse(%q) 应当报错", in)
		}
	}
}

func TestRenderIsTheHostsOwnForm(t *testing.T) {
	if got := Render(nil); got != "@a(sss) []" {
		t.Errorf("空表应当渲染成带标注的空数组，得到 %q", got)
	}
	one := Render([]Entry{{"a", "/a.png", "/a.png"}})
	if one != "[('a', '/a.png', '/a.png')]" {
		t.Errorf("单行渲染 = %q", one)
	}
	// 引号与反斜杠必须被转义，否则整条 key 会被 gsettings 拒掉。
	esc := Render([]Entry{{"a", `/we'ird\path.png`, "/x.png"}})
	if !strings.Contains(esc, `\'`) {
		t.Errorf("单引号没转义：%q", esc)
	}
	if !strings.Contains(esc, `\\`) {
		t.Errorf("反斜杠没转义：%q", esc)
	}
	back, err := Parse(esc)
	if err != nil {
		t.Fatalf("自己渲染出来的东西解析不了：%v", err)
	}
	if back[0].Normal != `/we'ird\path.png` {
		t.Errorf("转义往返丢了字符：%q", back[0].Normal)
	}
}

// ---------------------------------------------------------------- table edits

func table(entries ...Entry) *Table { return &Table{Entries: entries} }

func TestWithReplacesInPlaceAndFoldsDuplicates(t *testing.T) {
	// 位置保持：手工排过序的表不该因为一次覆盖就跳位。
	cur := table(
		Entry{"keep1", "/1.png", "/1.png"},
		Entry{"mine", "/old.png", "/old.png"},
		Entry{"mine", "/older.png", "/older.png"}, // 历史遗留的重复行
		Entry{"keep2", "/2.png", "/2.png"},
	)
	got := cur.With(Entry{"mine", "/new.png", "/new.png"})
	want := []Entry{
		{"keep1", "/1.png", "/1.png"},
		{"mine", "/new.png", "/new.png"},
		{"keep2", "/2.png", "/2.png"},
	}
	if !sameEntries(got, want) {
		t.Errorf("With\n got %#v\nwant %#v", got, want)
	}
}

func TestWithAppendsWhenAbsent(t *testing.T) {
	got := table(Entry{"other", "/o.png", "/o.png"}).With(Entry{"mine", "/m.png", "/m.png"})
	if len(got) != 2 || got[1].ID != "mine" {
		t.Errorf("新行应当追加在末尾：%#v", got)
	}
}

func TestWithoutLeavesEverythingElse(t *testing.T) {
	got := table(
		Entry{"other", "/o.png", "/o.png"},
		Entry{"mine", "/m.png", "/m.png"},
	).Without("mine")
	if len(got) != 1 || got[0].ID != "other" {
		t.Errorf("不该动别的行：%#v", got)
	}
	if len(table(Entry{"other", "/o.png", "/o.png"}).Without("mine")) != 1 {
		t.Error("删一个不存在的 id 不该影响任何行")
	}
}

// ---------------------------------------------------- against a fake gsettings
//
// Everything above tests the text. The part that actually bites is the sequence
// read → merge → write → read back, so the tests below put a stand-in
// `gsettings` on PATH and drive the real code through it.

// fakeGSettings installs a script named gsettings and points its state at a
// file. mode: "ok" writes, "silent" exits 0 without writing, "warn" prints a
// dconf complaint, "noschema" fails the read.
func fakeGSettings(t *testing.T, mode, initial string) string {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(state, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
state="` + state + `"
case "$1" in
get)
  case "` + mode + `" in
  noschema) echo "找不到“org.gnome.shell.extensions.appindicator”的设置架构" >&2; exit 1 ;;
  esac
  cat "$state"
  ;;
set)
  case "` + mode + `" in
  silent) exit 0 ;;
  warn) echo "dconf-WARNING: failed to commit changes to dconf: 无法连接" >&2; exit 0 ;;
  esac
  printf '%s' "$4" > "$state"
  ;;
*)
  echo "unsupported: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gsettings"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", dir) // 让 sessionBusPath() 指向一个空目录
	return state
}

func readState(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnsureMergesIntoTheGlobalKey(t *testing.T) {
	// 这张 key 是全局单值：写它必须合并，不能覆盖别的程序的条目。
	state := fakeGSettings(t, "ok",
		"[('vscode-panel', '/v.png', '/v.png')]")

	oc, err := Ensure("wechat2", "/home/e/.local/share/appicons/wechat2.png")
	if err != nil {
		t.Fatal(err)
	}
	if oc.Action != ActionWritten {
		t.Fatalf("action = %v，期望 Written", oc.Action)
	}
	got, err := Parse(readState(t, state))
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"vscode-panel", "/v.png", "/v.png"},
		{"wechat2", "/home/e/.local/share/appicons/wechat2.png", "/home/e/.local/share/appicons/wechat2.png"},
	}
	if !sameEntries(got, want) {
		t.Errorf("别的程序的条目被动了：\n got %#v\nwant %#v", got, want)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	state := fakeGSettings(t, "ok", "[]")
	if _, err := Ensure("app2", "/a.png"); err != nil {
		t.Fatal(err)
	}
	first := readState(t, state)

	oc, err := Ensure("app2", "/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if oc.Action != ActionUnchanged {
		t.Errorf("action = %v，期望 Unchanged", oc.Action)
	}
	if readState(t, state) != first {
		t.Error("没有变化的第二次调用不该重写这张表")
	}

	// 换了图标路径就必须重写（reinstall 之后图标可能变）。
	if oc, err := Ensure("app2", "/b.png"); err != nil || oc.Action != ActionWritten {
		t.Errorf("图标变了应当重写：action=%v err=%v", oc.Action, err)
	}
}

// 「假成功」是这个工具最不能相信的一件事：没有会话总线时 gsettings set 退 0
// 却什么也没写。回读是唯一的判据。
func TestEnsureRefusesToBelieveASilentWrite(t *testing.T) {
	fakeGSettings(t, "silent", "[]")
	_, err := Ensure("app2", "/a.png")
	if err == nil {
		t.Fatal("静默失败的写入必须报错，否则用户以为托盘图标已经换好了")
	}
	if !strings.Contains(err.Error(), "没有落盘") {
		t.Errorf("错误要说清是没落盘，得到：%v", err)
	}
}

func TestEnsureReportsADconfRefusal(t *testing.T) {
	fakeGSettings(t, "warn", "[]")
	_, err := Ensure("app2", "/a.png")
	if err == nil || !strings.Contains(err.Error(), "dconf") {
		t.Errorf("dconf 的拒绝必须原样带出来，得到：%v", err)
	}
}

func TestRemoveKeepsOtherRows(t *testing.T) {
	state := fakeGSettings(t, "ok",
		"[('other', '/o.png', '/o.png'), ('mine', '/m.png', '/m.png')]")

	oc, err := Remove("mine")
	if err != nil {
		t.Fatal(err)
	}
	if oc.Action != ActionRemoved {
		t.Fatalf("action = %v，期望 Removed", oc.Action)
	}
	got, _ := Parse(readState(t, state))
	if len(got) != 1 || got[0].ID != "other" {
		t.Errorf("删完剩下 %#v", got)
	}

	// 再删一次：没有变化。
	if oc, err := Remove("mine"); err != nil || oc.Action != ActionUnchanged {
		t.Errorf("删一个不在表里的条目应当报 Unchanged：action=%v err=%v", oc.Action, err)
	}
}

// 没有托盘宿主的机器（别的发行版 / 没装 appindicator）不是故障。
func TestNoHostIsASkipNotAFailure(t *testing.T) {
	fakeGSettings(t, "noschema", "")
	if hs := Host(); hs.OK {
		t.Fatal("读不到表就不该说这台机器有托盘宿主")
	} else if !strings.Contains(hs.Why, "设置架构") {
		t.Errorf("要说清原因，得到 %q", hs.Why)
	}
	for _, oc := range []Outcome{mustEnsure(t, "app2", "/a.png"), mustRemove(t, "app2")} {
		if oc.Action != ActionNoHost {
			t.Errorf("action = %v，期望 NoHost", oc.Action)
		}
		if oc.Detail == "" {
			t.Error("跳过也要给出原因")
		}
	}
}

func mustEnsure(t *testing.T, id, icon string) Outcome {
	t.Helper()
	oc, err := Ensure(id, icon)
	if err != nil {
		t.Fatalf("没有托盘宿主不算错：%v", err)
	}
	return oc
}

func mustRemove(t *testing.T, id string) Outcome {
	t.Helper()
	oc, err := Remove(id)
	if err != nil {
		t.Fatalf("没有托盘宿主不算错：%v", err)
	}
	return oc
}

// 一个已存在的条目若指向别的路径，Find 必须能看见，好让 verify 报「表里指的不是
// 本实例的图标」而不是笼统的「没有条目」。
func TestFindSeesAnExistingRow(t *testing.T) {
	tb := table(Entry{"a", "/old.png", "/old.png"})
	e, ok := tb.Find("a")
	if !ok || e.Normal != "/old.png" {
		t.Errorf("Find = %#v, %v", e, ok)
	}
	if _, ok := tb.Find("b"); ok {
		t.Error("不存在的 id 不该被找到")
	}
}
