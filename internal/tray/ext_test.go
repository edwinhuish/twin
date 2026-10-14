package tray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
)

// fakeShell puts stand-in `gsettings` and `gnome-extensions` on PATH.
//
// This feature talks to two different ends of a session — one reads dconf, the
// other asks the running shell to do something — and both are faked here,
// because what needs testing is the decision each answer leads to
// (installed / enabled / "log out once"), not the tools themselves.
//
// mode:
//
//	"known"   enable succeeds and appends the uuid — the normal second install
//	"unknown" enable fails: the shell has never scanned this uuid
//	"noext"   there is no gnome-extensions command at all
//	"nolist"  gsettings cannot read the enable list
func fakeShell(t *testing.T, mode, initialList string) string {
	t.Helper()
	dir := t.TempDir()
	list := filepath.Join(dir, "enabled")
	if err := os.WriteFile(list, []byte(initialList), 0o644); err != nil {
		t.Fatal(err)
	}

	gs := `#!/bin/sh
case "$3" in
enabled-extensions)
  case "` + mode + `" in
  nolist) echo "找不到“org.gnome.shell”的设置架构" >&2; exit 1 ;;
  esac
  cat "` + list + `"
  ;;
*)
  echo "unsupported key: $3" >&2; exit 2 ;;
esac
`
	writeScript(t, dir, "gsettings", gs)

	if mode != "noext" {
		ge := `#!/bin/sh
if [ "$1" != "enable" ]; then echo "unsupported: $1" >&2; exit 2; fi
case "` + mode + `" in
unknown) echo "Extension $2 does not exist" >&2; exit 1 ;;
esac
printf "['ubuntu-dock@ubuntu.com', '%s']" "$2" > "` + list + `"
`
		writeScript(t, dir, "gnome-extensions", ge)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return list
}

func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readList(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ------------------------------------------------------------- the two halves

// A first install cannot enable what the shell has never scanned. That is not a
// failure to work around: it is the fact the message has to carry.
func TestEnsureExtInstallsButCannotEnableAnUnknownUUID(t *testing.T) {
	fakeShell(t, "unknown", "['ubuntu-dock@ubuntu.com']")
	p := inst.Paths{Home: t.TempDir()}

	oc, err := EnsureExt(p)
	if err != nil {
		t.Fatal(err)
	}
	if !oc.Installed {
		t.Error("文件应当被写出去")
	}
	if oc.Enabled || !oc.NeedsRelogin {
		t.Errorf("shell 不认识的 uuid 不可能被启用：%+v", oc)
	}
	if oc.Detail == "" {
		t.Error("启用失败必须带出原因，否则用户不知道下一步是什么")
	}

	for _, name := range extNames {
		if !sysx.Exists(filepath.Join(ExtDir(p), name)) {
			t.Errorf("%s 没落盘", name)
		}
	}
}

// 第二次装：文件内容一样就不该碰它 —— 扩展文件的 mtime 是"有东西变了"的唯一信号。
func TestEnsureExtIsIdempotent(t *testing.T) {
	fakeShell(t, "unknown", "[]")
	p := inst.Paths{Home: t.TempDir()}

	if oc, err := EnsureExt(p); err != nil || !oc.Installed {
		t.Fatalf("第一次应当写入：%+v %v", oc, err)
	}
	before, err := os.Stat(filepath.Join(ExtDir(p), "extension.js"))
	if err != nil {
		t.Fatal(err)
	}

	oc, err := EnsureExt(p)
	if err != nil {
		t.Fatal(err)
	}
	if oc.Installed {
		t.Error("内容相同不该重写")
	}
	after, err := os.Stat(filepath.Join(ExtDir(p), "extension.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("没变化的一次调用动了文件的 mtime")
	}
}

func TestEnsureExtEnablesOnceTheShellKnowsTheUUID(t *testing.T) {
	list := fakeShell(t, "known", "['ubuntu-dock@ubuntu.com']")
	p := inst.Paths{Home: t.TempDir()}

	oc, err := EnsureExt(p)
	if err != nil {
		t.Fatal(err)
	}
	if !oc.Enabled || oc.NeedsRelogin {
		t.Errorf("shell 认得这个 uuid 时应当直接启用：%+v", oc)
	}
	if !strings.Contains(readList(t, list), ExtUUID) {
		t.Errorf("启用列表里没有 %s：%s", ExtUUID, readList(t, list))
	}

	// 已在列表里：什么都不用做。
	oc, err = EnsureExt(p)
	if err != nil {
		t.Fatal(err)
	}
	if !oc.Enabled || oc.Installed {
		t.Errorf("已经启用之后应当无事可做：%+v", oc)
	}
}

func TestEnsureExtWithoutTheCommandStillInstallsTheFiles(t *testing.T) {
	fakeShell(t, "noext", "[]")
	p := inst.Paths{Home: t.TempDir()}

	oc, err := EnsureExt(p)
	if err != nil {
		t.Fatal(err)
	}
	if !oc.Installed || oc.Enabled || !oc.NeedsRelogin {
		t.Errorf("没有 gnome-extensions 只影响启用，不影响落盘：%+v", oc)
	}
	if oc.Detail == "" {
		t.Error("要说清是哪一步做不了")
	}
}

// ------------------------------------------------------------- reading back

func TestCheckExtKeepsInstalledAndEnabledApart(t *testing.T) {
	fakeShell(t, "known", "['ubuntu-dock@ubuntu.com']")
	p := inst.Paths{Home: t.TempDir()}

	if st := CheckExt(p); st.Installed || st.Enabled || st.Why != "" {
		t.Errorf("什么都没装时：%+v", st)
	}
	if _, err := EnsureExt(p); err != nil {
		t.Fatal(err)
	}
	st := CheckExt(p)
	if !st.Installed || !st.Enabled {
		t.Errorf("装完应当是 installed ＋ enabled：%+v", st)
	}
	// 只删掉一个文件就算没装：少了 metadata.json 的扩展目录对 shell 是不存在的。
	if err := os.Remove(filepath.Join(ExtDir(p), "metadata.json")); err != nil {
		t.Fatal(err)
	}
	if st := CheckExt(p); st.Installed {
		t.Error("缺文件不能算已安装")
	}
}

// 读不到列表不等于"没启用"：那会把用户送到错的地方去修。
func TestCheckExtSaysWhyItCannotReadTheList(t *testing.T) {
	fakeShell(t, "nolist", "[]")
	p := inst.Paths{Home: t.TempDir()}

	st := CheckExt(p)
	if st.Enabled {
		t.Error("读不到列表时不能声称已启用")
	}
	if st.Why == "" {
		t.Error("读不到的原因必须带出来")
	}
}

func TestParseStringListReadsWhatGSettingsPrints(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"未设置（host 自己的输出）", "@as []", nil},
		{"裸空数组", "[]", nil},
		{"空串", "", nil},
		{"单个", "['a@b']", []string{"a@b"}},
		{"多个", "['a@b', 'c@d']", []string{"a@b", "c@d"}},
		{"双引号与空格的 uuid", `["a b@c", 'd@e']`, []string{"a b@c", "d@e"}},
		{"末尾换行", "['a@b']\n", []string{"a@b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseStringList(c.in)
			if err != nil {
				t.Fatalf("parseStringList(%q) 报错：%v", c.in, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("parseStringList(%q) = %#v，期望 %#v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("第 %d 项 = %q，期望 %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// 目录名与 metadata.json 里的 uuid 必须一致：shell 把启用列表、每扩展的设置、
// 报错信息全部按这一个字符串索引，两边漂了就是一个装得上但永远加载不了的扩展。
func TestEmbeddedExtensionNamesItselfOnce(t *testing.T) {
	meta, err := extFiles.ReadFile("ext/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		UUID         string   `json:"uuid"`
		ShellVersion []string `json:"shell-version"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		t.Fatalf("metadata.json 不是合法 JSON：%v", err)
	}
	if m.UUID != ExtUUID {
		t.Errorf("metadata.json 的 uuid = %q，ExtUUID = %q", m.UUID, ExtUUID)
	}
	if len(m.ShellVersion) == 0 {
		t.Error("shell-version 为空：shell 会拒绝加载")
	}
	if _, err := extFiles.ReadFile("ext/extension.js"); err != nil {
		t.Errorf("扩展入口读不到：%v", err)
	}
}
