package userns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestrictedAt(t *testing.T) {
	dir := t.TempDir()
	knob := filepath.Join(dir, "knob")

	if ok, _ := restrictedAt(knob); ok {
		t.Fatal("读不到开关时应当判为「不限制」")
	}
	if err := os.WriteFile(knob, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := restrictedAt(knob); ok {
		t.Fatal("0 应当判为「不限制」")
	}
	if err := os.WriteFile(knob, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, why := restrictedAt(knob)
	if !ok {
		t.Fatal("1 应当判为「限制」")
	}
	if why == "" {
		t.Fatal("判为限制时必须给出来源，否则用户查不到是谁写的")
	}
}

func TestExpandBraces(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"/no/braces", []string{"/no/braces"}},
		{"/usr/share/code{/bin,}/code", []string{"/usr/share/code/bin/code", "/usr/share/code/code"}},
		{"/usr/bin/epiphany{,-browser}", []string{"/usr/bin/epiphany", "/usr/bin/epiphany-browser"}},
		{
			"/{usr/lib/firefox{,-esr},opt/firefox}/firefox",
			[]string{
				"/usr/lib/firefox/firefox",
				"/usr/lib/firefox-esr/firefox",
				"/opt/firefox/firefox",
			},
		},
		{"/unbalanced{/a", []string{"/unbalanced{/a"}},
	}
	for _, c := range cases {
		got := ExpandBraces(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("ExpandBraces(%q) = %v，想要 %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("ExpandBraces(%q)[%d] = %q，想要 %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

const sampleProfiles = `abi <abi/5.0>,

profile bwrap /usr/bin/bwrap flags=(attach_disconnected,mediate_deleted) {
  allow userns,
  allow pix /** -> &bwrap//&unpriv_bwrap,
}

# The unpriv_bwrap profile is used to strip capabilities within the userns
profile unpriv_bwrap flags=(attach_disconnected,mediate_deleted) {
  allow userns,
  audit deny capability,
}

profile "workbuddy" "/opt/WorkBuddy/workbuddy" flags=(unconfined) {
  userns,
}

profile vscode /usr/share/code{/bin,}/code flags=(unconfined) {
  userns,
  @{exec_path} mr,
}

profile noattach flags=(unconfined) {
  userns,
}

# 只在注释里提到 userns，不应被当成许可
profile commented /usr/bin/commented flags=(unconfined) {
  # our userns restriction notes
}
`

func TestParseProfilesAndGrants(t *testing.T) {
	decls := parseProfiles(sampleProfiles)
	got := map[string]profileDecl{}
	for _, d := range decls {
		got[d.name] = d
	}
	if len(got) != 6 {
		t.Fatalf("解析出 %d 个 profile，想要 6 个：%v", len(got), keys(got))
	}

	if d := got["bwrap"]; d.attach != "/usr/bin/bwrap" {
		t.Fatalf("bwrap 的 attach = %q", d.attach)
	}
	if d := got["unpriv_bwrap"]; d.attach != "" {
		t.Fatalf("unpriv_bwrap 无 attach，得到 %q", d.attach)
	}
	if d := got["workbuddy"]; d.attach != "/opt/WorkBuddy/workbuddy" {
		t.Fatalf("workbuddy 的 attach = %q（引号应当被剥掉）", d.attach)
	}
	if d := got["vscode"]; !strings.Contains(d.attach, "{") {
		t.Fatalf("vscode 的 attach 应当保留花括号待展开，得到 %q", d.attach)
	}

	// 真正可借标签的只有「有 attach + unconfined + userns」三个都满足的。
	want := map[string]bool{
		"bwrap":        false, // 有 userns，但 flags 不是 unconfined
		"unpriv_bwrap": false, // 没有 attach
		"workbuddy":    true,
		"vscode":       true,
		"noattach":     false, // 没有 attach
		"commented":    false, // 只在注释里出现 userns
	}
	for name, exp := range want {
		if got := got[name].grantsUserns(); got != exp {
			t.Errorf("%s.grantsUserns() = %v，想要 %v", name, got, exp)
		}
	}
}

func TestLooksElectron(t *testing.T) {
	dir := t.TempDir()

	// 同目录带 resources/app.asar ⇒ Electron
	el := filepath.Join(dir, "el", "code")
	mkFile(t, el, 0o755)
	mkFile(t, filepath.Join(dir, "el", "resources", "app.asar"), 0o644)
	if !IsElectron(el) {
		t.Fatal("resources/app.asar 应当判为 Electron")
	}

	// 解包成 resources/app 目录的也算（本机 VS Code 就是这种）
	un := filepath.Join(dir, "un", "code")
	mkFile(t, un, 0o755)
	if err := os.MkdirAll(filepath.Join(dir, "un", "resources", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsElectron(un) {
		t.Fatal("resources/app 目录应当判为 Electron")
	}

	// 包装器在 bin/ 里，bundle 在上一级
	wrapped := filepath.Join(dir, "up", "bin", "code")
	mkFile(t, wrapped, 0o755)
	mkFile(t, filepath.Join(dir, "up", "resources", "default_app.asar"), 0o644)
	if !IsElectron(wrapped) {
		t.Fatal("上一级的 resources 应当被看到")
	}

	// 纯 Chromium：没有 resources/app* ⇒ 不是 Electron
	plain := filepath.Join(dir, "plain", "chrome")
	mkFile(t, plain, 0o755)
	mkFile(t, filepath.Join(dir, "plain", "chrome-sandbox"), 0o755)
	if IsElectron(plain) {
		t.Fatal("chrome-sandbox 不是 Electron 的判据")
	}
}

func TestDiscoverAndPick(t *testing.T) {
	dir := t.TempDir()

	nonEl := filepath.Join(dir, "faketool")
	mkFile(t, nonEl, 0o755)

	elDir := filepath.Join(dir, "fakeel")
	el := filepath.Join(elDir, "fakeel")
	mkFile(t, el, 0o755)
	mkFile(t, filepath.Join(elDir, "resources", "app.asar"), 0o644)

	// 同一个 profile 还挂着一个包装脚本（本机 VS Code 的 bin/code 就是这种）。
	// 它不该顶替真二进制当选。
	wrapper := filepath.Join(elDir, "bin", "fakeel")
	mkScript(t, wrapper, 0o755)

	missing := filepath.Join(dir, "not-installed")

	profile := "abi <abi/4.0>,\n\n" +
		"profile faketool " + nonEl + " flags=(unconfined) { userns, }\n\n" +
		"profile fakeel " + elDir + "{/bin,}/fakeel flags=(unconfined) { userns, }\n\n" +
		"profile ghost " + missing + " flags=(unconfined) { userns, }\n"
	if err := os.WriteFile(filepath.Join(dir, "probe.profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Discover(dir)
	if len(got) != 2 {
		t.Fatalf("应当只发现 2 个已安装的可执行文件，得到 %d：%v", len(got), got)
	}
	if !got[0].Electron || got[0].Path != el {
		t.Fatalf("Electron 应当排在首位，得到 %+v", got[0])
	}
	if got[1].Electron {
		t.Fatalf("第二个不该是 Electron：%+v", got[1])
	}

	picked, ok := Pick(got)
	if !ok || picked.Path != el {
		t.Fatalf("Pick 应当选中 Electron 那个，得到 %+v ok=%v", picked, ok)
	}

	// 一个 Electron 都没有时，Pick 必须说不 —— 浏览器 profile 也给了
	// userns，但没有任何开关能让它去 spawn 一个 bwrap 副本。
	if _, ok := Pick([]Trampoline{{Path: "/usr/bin/firefox"}}); ok {
		t.Fatal("非 Electron 候选不该被 Pick 选中")
	}
}

func TestProfileHint(t *testing.T) {
	h := ProfileHint("/home/x/.local/share/twin/bin/bwrap")
	if !strings.Contains(h, "/etc/apparmor.d/twin-bwrap") {
		t.Fatal("提示里应当写明 profile 该装到哪")
	}
	if !strings.Contains(h, "apparmor_parser -r") {
		t.Fatal("提示里应当给出安装命令")
	}
	if !strings.Contains(h, "userns,") {
		t.Fatal("profile 里必须有 userns 许可，否则白装")
	}
}

func mkFile(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// 内容必须不以 #! 开头：那种文件会被 isScript 当成包装脚本跳过，
	// 而 Discover 只接受真正的可执行映像。
	if err := os.WriteFile(path, []byte("\x7fELF\x02\x01\x01\x00"), perm); err != nil {
		t.Fatal(err)
	}
}

// mkScript 造一个以 #! 开头的包装脚本，用来验证 isScript 会把它挡掉。
func mkScript(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/true \"$@\"\n"), perm); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]profileDecl) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
