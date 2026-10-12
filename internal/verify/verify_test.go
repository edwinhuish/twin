package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/inst"
)

// stubGSettings puts a stand-in `gsettings` on PATH. mode: "ok" reads and
// writes, "noschema" fails the read the way a machine without the appindicator
// extension does.
func stubGSettings(t *testing.T, mode, initial string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(state, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
get)
  case "` + mode + `" in
  noschema) echo "找不到“org.gnome.shell.extensions.appindicator”的设置架构" >&2; exit 1 ;;
  esac
  cat "` + state + `"
  ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gsettings"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func iconInstance() *inst.Instance {
	return &inst.Instance{ID: "wechat2", TrayOverride: true}
}

const iconPath = "/home/e/.local/share/appicons/wechat2.png"

func TestTrayFindingWhenTheRowIsThere(t *testing.T) {
	stubGSettings(t, "ok", "[('wechat2', '"+iconPath+"', '"+iconPath+"')]")
	f := TrayFinding(iconInstance(), iconPath)
	if f.Status != StatusOK || f.Manual {
		t.Errorf("表里已经有正确的条目，不该报问题：%+v", f)
	}
}

func TestTrayFindingWhenTheRowIsMissing(t *testing.T) {
	stubGSettings(t, "ok", "[]")
	f := TrayFinding(iconInstance(), iconPath)
	if f.Status != StatusWarn || !f.Manual {
		t.Fatalf("缺条目应当是 WARN + Manual（退出码 3）：%+v", f)
	}
	if f.Fix == "" {
		t.Error("Manual 的条目必须给出那条命令，否则用户不知道该跑什么")
	}
	if !strings.Contains(f.Fix, "wechat2") {
		t.Errorf("命令里要带实例 id：%q", f.Fix)
	}
}

// 图标换了位置（regen 后换过图标目录）时，「有这条但指错」要说清指的是哪儿，
// 不能笼统地说「没有条目」。
func TestTrayFindingWhenTheRowPointsElsewhere(t *testing.T) {
	stubGSettings(t, "ok", "[('wechat2', '/old/other.png', '/old/other.png')]")
	f := TrayFinding(iconInstance(), iconPath)
	if f.Status != StatusWarn || !f.Manual {
		t.Fatalf("指错也要报出来：%+v", f)
	}
	if !strings.Contains(f.Detail, "/old/other.png") {
		t.Errorf("要说清现在指的是哪个图标：%q", f.Detail)
	}
}

// 没有托盘宿主的机器不是故障：这不是 WARN，更不是 FAIL，也不该进「下一步」。
func TestTrayFindingOnAMachineWithoutATrayHost(t *testing.T) {
	stubGSettings(t, "noschema", "")
	f := TrayFinding(iconInstance(), iconPath)
	if f.Status != StatusOK {
		t.Errorf("没有托盘宿主不该算问题：%+v", f)
	}
	if f.Manual {
		t.Error("没有托盘宿主不是「差一步人工」，不该让 wrap 退出 3")
	}
	if !strings.Contains(f.Detail, "跳过") {
		t.Errorf("要说清是跳过以及为什么：%q", f.Detail)
	}
}

func TestTrayFindingHonoursTheOptOut(t *testing.T) {
	// 故意让宿主有一张读得到的空表：跳过必须是设置说了算，不是读失败说了算。
	stubGSettings(t, "ok", "[]")
	it := iconInstance()
	it.TrayOverride = false
	f := TrayFinding(it, iconPath)
	if f.Status != StatusOK || f.Manual {
		t.Errorf("显式关掉不该报问题：%+v", f)
	}
	if !strings.Contains(f.Detail, "tray.override") {
		t.Errorf("要说清是按设置跳过的：%q", f.Detail)
	}
}

// ---- 嵌套 user namespace（Chromium / Electron 那条路） ----------------------

// sink 收集 checkNestedNS 吐出来的条目，免得每个用例都重写一遍闭包。
type sink struct{ got []Finding }

func (s *sink) add(name string, st Status, detail, fix string) {
	s.got = append(s.got, Finding{Name: name, Status: st, Detail: detail, Fix: fix})
}

func (s *sink) only(t *testing.T) Finding {
	t.Helper()
	if len(s.got) != 1 {
		t.Fatalf("这条路应当恰好一条结论，得到 %d 条：%+v", len(s.got), s.got)
	}
	return s.got[0]
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// nestedEnv 造一份 home ＋ 一个标了 nested_ns 的实例。
func nestedEnv(t *testing.T) (inst.Paths, *inst.Instance) {
	t.Helper()
	return inst.Paths{Home: t.TempDir()}, &inst.Instance{ID: "waachat2", NestedNS: true}
}

// 启动器按生成器里那个形状写：`exec env VAR=1 \` 换行，跳板在下一行。
func writeLauncher(t *testing.T, p inst.Paths, id, trampoline string) {
	t.Helper()
	body := "#!/bin/bash\nset -euo pipefail\n"
	if trampoline != "" {
		body += "exec env ELECTRON_RUN_AS_NODE=1 \\\n  " + trampoline + " \\\n" +
			"  " + p.Bootstrap() + " \\\n  " + p.BwrapCopy() + " \\\n" +
			"  --ro-bind / / \\\n  -- /opt/wechat/wechat \"$@\"\n"
	} else {
		body += "exec " + p.BwrapCopy() + " \\\n  --ro-bind / / \\\n  -- /opt/wechat/wechat \"$@\"\n"
	}
	writeFile(t, p.Launch(id), body, 0o755)
}

func makeElectron(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "electron")
	writeFile(t, bin, "\x7fELF", 0o755)
	writeFile(t, filepath.Join(dir, "resources", "app.asar"), "asar", 0o644)
	return bin
}

// 快照说要嵌套命名空间，副本却不在 —— 这个实例起不来，必须给 reinstall。
func TestNestedNSWithoutTheCopy(t *testing.T) {
	p, it := nestedEnv(t)
	var s sink
	checkNestedNS(it, p, s.add)
	f := s.only(t)
	if f.Status != StatusWarn {
		t.Errorf("缺副本应当是 WARN：%+v", f)
	}
	if !strings.Contains(f.Fix, "reinstall") {
		t.Errorf("要给一条能跑的命令：%q", f.Fix)
	}
}

// 副本在、也没用跳板（内核没限制的机器）—— 正常，不该报任何问题。
func TestNestedNSWithCopyAndNoTrampoline(t *testing.T) {
	p, it := nestedEnv(t)
	writeFile(t, p.BwrapCopy(), "\x7fELF", 0o755)
	writeLauncher(t, p, it.ID, "")
	var s sink
	checkNestedNS(it, p, s.add)
	f := s.only(t)
	if f.Status != StatusOK {
		t.Errorf("没有限制的机器不该报问题：%+v", f)
	}
	if !strings.Contains(f.Detail, "副本") {
		t.Errorf("要说清走的是哪条路：%q", f.Detail)
	}
}

// 跳板被卸载/改名（快照里每条路径都还在，别的检查抓不到）—— 这条要 FAIL。
func TestNestedNSWhenTheTrampolineVanished(t *testing.T) {
	p, it := nestedEnv(t)
	writeFile(t, p.BwrapCopy(), "\x7fELF", 0o755)
	writeLauncher(t, p, it.ID, "/opt/gone/electron")
	var s sink
	checkNestedNS(it, p, s.add)
	f := s.only(t)
	if f.Status != StatusFail {
		t.Errorf("跳板没了就是起不来，应当 FAIL：%+v", f)
	}
	if !strings.Contains(f.Fix, "reinstall") {
		t.Errorf("要给一条能跑的命令：%q", f.Fix)
	}
}

// 那个路径还在，但已经不是 Electron 了（换成了别的程序）—— 降级为 WARN。
func TestNestedNSWhenTheTrampolineIsNotElectron(t *testing.T) {
	p, it := nestedEnv(t)
	writeFile(t, p.BwrapCopy(), "\x7fELF", 0o755)
	plain := filepath.Join(t.TempDir(), "somebinary")
	writeFile(t, plain, "\x7fELF", 0o755)
	writeLauncher(t, p, it.ID, plain)
	var s sink
	checkNestedNS(it, p, s.add)
	f := s.only(t)
	if f.Status != StatusWarn {
		t.Errorf("驱动不了 Node 模式应当是 WARN：%+v", f)
	}
}

// 齐全：副本在、跳板在、还是 Electron —— OK 且要说清是哪个跳板。
func TestNestedNSFullyWired(t *testing.T) {
	p, it := nestedEnv(t)
	writeFile(t, p.BwrapCopy(), "\x7fELF", 0o755)
	tr := makeElectron(t)
	writeLauncher(t, p, it.ID, tr)
	var s sink
	checkNestedNS(it, p, s.add)
	f := s.only(t)
	if f.Status != StatusOK {
		t.Fatalf("这条链是通的，不该报问题：%+v", f)
	}
	if !strings.Contains(f.Detail, tr) {
		t.Errorf("要说清用的是哪个跳板：%q", f.Detail)
	}
}

// 读启动器是为了回答「实际跑起来的东西还在不在」，这里把两种形状钉住。
func TestTrampolineInLauncher(t *testing.T) {
	p, it := nestedEnv(t)
	writeLauncher(t, p, it.ID, "/opt/fake/electron")
	if got := trampolineInLauncher(p.Launch(it.ID)); got != "/opt/fake/electron" {
		t.Errorf("应当挖出跳板路径，得到 %q", got)
	}
	writeLauncher(t, p, it.ID, "")
	if got := trampolineInLauncher(p.Launch(it.ID)); got != "" {
		t.Errorf("没有跳板就该返回空，得到 %q", got)
	}
	if got := trampolineInLauncher(filepath.Join(t.TempDir(), "nope")); got != "" {
		t.Errorf("文件不存在也是空，得到 %q", got)
	}
}

// 退出码 3 只由 Manual 驱动，这里顺带把这个约定钉住。
func TestManualDrivesExitCodeThree(t *testing.T) {
	stubGSettings(t, "ok", "[]")
	rep := &Report{Findings: []Finding{TrayFinding(iconInstance(), iconPath)}}
	if !rep.NeedsHuman() {
		t.Error("缺条目 ⇒ 必须有一步人工，wrap 才会退出 3")
	}
	if rep.OK() != true {
		t.Error("Manual 的 WARN 不是 FAIL：实例本身是好的")
	}

	stubGSettings(t, "ok", "[('wechat2', '"+iconPath+"', '"+iconPath+"')]")
	rep = &Report{Findings: []Finding{TrayFinding(iconInstance(), iconPath)}}
	if rep.NeedsHuman() {
		t.Error("写好了就不该再要求人工步骤")
	}
}
