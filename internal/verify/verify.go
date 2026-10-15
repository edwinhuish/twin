// Package verify is what separates twin from a hand-assembled wrapper: after
// writing anything it looks at the result and says whether the identity chain
// actually closed.
//
// Every check here maps to a failure we have watched happen silently on this
// machine — a desktop entry discarded by GLib, a gear instead of an icon, a
// tray override that never got written.
package verify

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/desk"
	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
	"github.com/edwinhuish/twin/internal/tray"
	"github.com/edwinhuish/twin/internal/userns"
)

// Status is how badly a check failed.
type Status int

const (
	// StatusOK means the check passed.
	StatusOK Status = iota
	// StatusWarn means something is degraded but the instance still works.
	StatusWarn
	// StatusFail means the identity chain is broken.
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK  "
	case StatusWarn:
		return "WARN"
	default:
		return "FAIL"
	}
}

// Finding is one check result.
type Finding struct {
	Name   string
	Status Status
	Detail string
	Fix    string // a single command the user can run, when there is one
	// Manual marks a fix that cannot be done from a script: it needs a real
	// graphical session (a dconf write has to go through the session bus).
	// The CLI turns this into exit code 3 so a script can tell "retry me from
	// the desktop" apart from "you are broken" (设计 §5.3).
	Manual bool
}

// Report is the whole verification.
type Report struct {
	ID       string
	Findings []Finding
}

// OK reports whether nothing failed.
func (r *Report) OK() bool {
	for _, f := range r.Findings {
		if f.Status == StatusFail {
			return false
		}
	}
	return true
}

// NeedsHuman reports whether a check passed only partly and the missing step
// can only be taken inside a graphical session.
func (r *Report) NeedsHuman() bool {
	for _, f := range r.Findings {
		if f.Manual && f.Status != StatusOK {
			return true
		}
	}
	return false
}

// Run verifies an instance end to end.
func Run(i *inst.Instance, p inst.Paths) *Report {
	rep := &Report{ID: i.ID}
	add := func(name string, st Status, detail, fix string) {
		rep.Findings = append(rep.Findings, Finding{Name: name, Status: st, Detail: detail, Fix: fix})
	}

	// ---- 1. the source program -------------------------------------------------
	if !sysx.Exists(i.Target) {
		add("exec.target 存在", StatusFail, i.Target+" 不存在", "")
	} else if !sysx.IsExecutableFile(i.Target) {
		add("exec.target 可执行", StatusFail, i.Target+" 没有执行位", "")
	} else {
		add("exec.target", StatusOK, i.Target, "")
	}

	if i.Level.Renames() {
		if sysx.Exists(i.AliasPath()) {
			add("别名只在命名空间内", StatusWarn,
				fmt.Sprintf("%s 在宿主上真实存在 —— 它本应只由 overlay 在命名空间内提供。"+
					"请删掉它，否则它可能遮蔽真实程序", i.AliasPath()), "")
		} else {
			add("别名只在命名空间内", StatusOK, filepath.Base(i.AliasPath())+" 未污染宿主", "")
		}
	}

	// ---- 1b. the nested-namespace route ----------------------------------------
	//
	// Only the Chromium class has one. It needs its own check because a
	// trampoline that has been uninstalled since the wrap is a startup failure
	// that nothing else here would catch: every path the snapshot names still
	// exists, the desktop entry still loads, and the launcher still parses.
	if i.NestedNS {
		checkNestedNS(i, p, add)
	}

	// ---- 2. the desktop entry --------------------------------------------------
	desktopPath := p.Desktop(i.ID)
	if !sysx.Exists(desktopPath) {
		add("桌面项存在", StatusFail, desktopPath+" 不存在", "")
	} else {
		issues := desk.Validate(desktopPath, p.Home)
		var fatal, soft []string
		for _, is := range issues {
			if is.Fatal {
				fatal = append(fatal, is.Msg)
			} else {
				soft = append(soft, is.Msg)
			}
		}
		switch {
		case len(fatal) > 0:
			add("桌面项可用", StatusFail, strings.Join(fatal, "; "),
				fmt.Sprintf("twin reinstall %s", i.ID))
		case len(soft) > 0:
			add("桌面项可用", StatusWarn, strings.Join(soft, "; "), "")
		default:
			add("桌面项可用", StatusOK, filepath.Base(desktopPath), "")
		}

		base := strings.TrimSuffix(filepath.Base(desktopPath), ".desktop")
		if base != i.ID {
			add("桌面项基名 == id", StatusFail,
				fmt.Sprintf("基名是 %q，必须是 %q —— 基名就是身份", base, i.ID), "")
		}

		if loaded, detail := DesktopLoads(desktopPath); loaded {
			add("GLib 接受该桌面项", StatusOK, detail, "")
		} else if detail == errNoGI {
			add("GLib 接受该桌面项", StatusWarn, "本机没有 python3-gi，跳过（安装 python3-gi 可启用这项）", "")
		} else {
			add("GLib 接受该桌面项", StatusFail,
				"GLib 返回 NULL（"+detail+"）—— 整个桌面项会被静默丢弃，面板只会显示齿轮",
				fmt.Sprintf("twin reinstall %s", i.ID))
		}
	}

	// ---- 3. the icon -----------------------------------------------------------
	iconPath := p.Icon(i.ID)
	if !sysx.Exists(iconPath) {
		add("图标存在", StatusFail, iconPath+" 不存在", fmt.Sprintf("twin reinstall %s", i.ID))
	} else if w, h, err := PNGSize(iconPath); err != nil {
		add("图标可解码", StatusFail, err.Error(), fmt.Sprintf("twin reinstall %s", i.ID))
	} else {
		add("图标", StatusOK, fmt.Sprintf("%s (%d×%d)", filepath.Base(iconPath), w, h), "")
	}

	// ---- 4. the launcher -------------------------------------------------------
	launch := p.Launch(i.ID)
	switch {
	case !sysx.Exists(launch):
		add("启动器存在", StatusFail, launch+" 不存在", fmt.Sprintf("twin reinstall %s", i.ID))
	case !sysx.IsExecutableFile(launch):
		// This is the same trap as a non-executable Exec target: GLib throws
		// the entry away without a sound.
		add("启动器可执行", StatusFail, launch+" 没有执行位 —— GLib 会丢弃整个桌面项",
			fmt.Sprintf("chmod +x %s", launch))
	default:
		add("启动器", StatusOK, launch, "")
	}

	// ---- 5. the private state directory ---------------------------------------
	state := p.StateDir(i.ID)
	if sysx.Exists(state) {
		add("私有状态目录", StatusOK, state, "")
	} else {
		add("私有状态目录", StatusWarn, state+" 不存在（首次启动会自动建）", "")
	}

	// ---- 6. the tray override: whichever of the two mechanisms is in force ----
	rep.Findings = append(rep.Findings, TrayFinding(p, i, iconPath))
	rep.Findings = append(rep.Findings, TrayExtFinding(p, i))

	// ---- 7. the L5 rule (only for level=redirect) -----------------------------
	//
	// A stale rule is worse than a missing one: it keeps redirecting windows to
	// a desktop entry that may no longer match, and nothing on screen says why.
	// So this check looks in both directions.
	rulePath := p.RuleFile(i.ID)
	switch {
	case i.Level == inst.LevelRedirect && !sysx.Exists(rulePath):
		add("L5 规则文件", StatusFail, rulePath+" 不存在", fmt.Sprintf("twin reinstall %s", i.ID))
	case i.Level == inst.LevelRedirect:
		body, err := os.ReadFile(rulePath)
		if err != nil {
			add("L5 规则文件", StatusFail, err.Error(), fmt.Sprintf("twin reinstall %s", i.ID))
			break
		}
		rule, err := inst.ParseRedirectRule(body)
		if err != nil {
			add("L5 规则文件", StatusFail, filepath.Base(rulePath)+" 不是合法 JSON："+err.Error(),
				fmt.Sprintf("twin reinstall %s", i.ID))
			break
		}
		switch err := rule.Validate(i.ID); {
		case err != nil:
			add("L5 规则文件", StatusFail, err.Error(), fmt.Sprintf("twin reinstall %s", i.ID))
		case !rule.Carries(i.AliasPath()):
			// The evidence no longer names this instance's alias — the usual
			// cause is a hand-edited profile.yml that moved the target, with
			// no regen since.
			add("L5 规则文件", StatusFail,
				fmt.Sprintf("规则的证据里找不到别名路径 %s", i.AliasPath()),
				fmt.Sprintf("twin reinstall %s", i.ID))
		default:
			add("L5 规则文件", StatusOK,
				fmt.Sprintf("target=%s，扩展会读 %s", rule.Target, rulePath), "")
		}
	case sysx.Exists(rulePath):
		// level is not redirect but the file is still there: it was left by an
		// earlier level and will keep redirecting.
		add("L5 规则文件", StatusWarn,
			rulePath+" 还在，但本实例 level="+string(i.Level)+" 已不用它 —— 扩展仍会读它",
			fmt.Sprintf("rm %s", rulePath))
	}

	return rep
}

// errNoGI marks "the checker is unavailable", which is not a failure.
const errNoGI = "no-gi"

const gioLoadScript = `import sys
import gi
gi.require_version("Gio", "2.0")
from gi.repository import Gio
info = Gio.DesktopAppInfo.new_from_filename(sys.argv[1])
if info is None:
    print("NULL")
else:
    print("OK\t%s\t%s" % (info.get_id(), info.get_name()))
`

// checkNestedNS verifies the route a Chromium/Electron instance needs to create
// its own user namespace: the private bubblewrap copy, and — on a machine that
// restricts unprivileged namespaces — something that lends the copy the
// permission. There are two such things: a trampoline recorded in the launcher,
// or a profile attached to the copy itself, which is the one `twin install`
// installs.
//
// The profile is reported as evidence rather than as a verdict. It is decided
// by reading /etc/apparmor.d, and a check whose outcome depends on what happens
// to be installed on the machine running the tests cannot be tested — so the
// status stays about the launcher, and the detail gets sharper when the profile
// is actually there.
func checkNestedNS(i *inst.Instance, p inst.Paths, add func(string, Status, string, string)) {
	copyPath := p.BwrapCopy()
	if !sysx.Exists(copyPath) {
		add("嵌套 user namespace", StatusWarn,
			"定义里标了 isolation.nested_ns，但私有副本 "+copyPath+" 不存在 —— 这个实例很可能起不来",
			fmt.Sprintf("twin reinstall %s", i.ID))
		return
	}
	tr := trampolineInLauncher(p.Launch(i.ID))
	switch {
	case tr == "":
		detail := "走私有 bwrap 副本 " + copyPath + "，没有用跳板"
		if covered, how := userns.ProfileCovers(copyPath); covered {
			detail += "：" + how
		}
		add("嵌套 user namespace", StatusOK, detail, "")
	case !sysx.Exists(tr):
		add("嵌套 user namespace", StatusFail,
			"启动器里的跳板 "+tr+" 已经不存在了（那个应用被卸载或改名了？）—— 这个实例起不来",
			fmt.Sprintf("twin reinstall %s", i.ID))
	case !userns.IsElectron(tr):
		add("嵌套 user namespace", StatusWarn,
			"启动器里的跳板 "+tr+" 现在不像 Electron 程序了，Node 模式可能驱动不了它",
			fmt.Sprintf("twin reinstall %s", i.ID))
	default:
		add("嵌套 user namespace", StatusOK, "跳板 "+tr+" → 副本 "+copyPath, "")
	}
}

// trampolineInLauncher digs the trampoline path out of a generated launcher.
//
// Reading a shell script to answer a question is normally a smell. Here it is
// the point: the launcher is the artefact that actually runs, so a trampoline
// removed since the wrap gets noticed even though every path the snapshot
// records still exists.
func trampolineInLauncher(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "exec env ") {
			continue
		}
		// Shape is `exec env VAR=1 \` with the trampoline on the next line.
		for _, next := range lines[i+1:] {
			t := strings.TrimSuffix(strings.TrimSpace(next), "\\")
			fields := strings.Fields(t)
			if len(fields) == 0 {
				continue
			}
			return strings.Trim(fields[0], "'\"")
		}
	}
	return ""
}

// DesktopLoads asks GLib whether it accepts a desktop entry. GLib returning
// NULL is the silent failure mode: desktop-file-validate still says the file is
// fine. The second return value is errNoGI when no interpreter is available.
func DesktopLoads(path string) (bool, string) {
	py := sysx.PythonWithGI()
	if py == "" {
		return false, errNoGI
	}
	res, err := sysx.Run(py, "-c", gioLoadScript, path)
	if err != nil {
		return false, err.Error()
	}
	if res.Code != 0 {
		return false, firstLine(res.Stderr)
	}
	if strings.HasPrefix(res.Stdout, "OK") {
		fields := strings.Split(strings.TrimSpace(res.Stdout), "\t")
		if len(fields) >= 3 {
			return true, fmt.Sprintf("id=%s name=%s", fields[1], fields[2])
		}
		return true, "ok"
	}
	return false, "constructor returned NULL"
}

// PNGSize decodes just enough of a PNG to report its dimensions.
func PNGSize(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	img, format, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if format != "png" {
		return 0, 0, fmt.Errorf("%s is %s, want png", filepath.Base(path), format)
	}
	return img.Width, img.Height, nil
}

// TrayFinding reports how this instance's tray icon is being drawn.
//
// There are two mechanisms and the install picked one of them, so the check
// has to read the *same* switch (tray.ExtState.Usable) and then look at what
// that mechanism actually holds. It reports both directions, like the L5 rule
// check: a row that should not be there is as much of a finding as a row that
// is missing, because it means two mechanisms are claiming one tray item.
//
// It stays Manual — and therefore exit 3 — because the fallback's write needs
// the session bus: run from an installer, a script or a headless session the
// host table does not change, and the honest answer is "not done yet, here is
// the command" rather than a FAIL that reads like a broken instance.
//
// It is a separate function because every branch here is a decision about what
// the user is told, and getting one wrong means either a false FAIL or a task
// nobody knows is still outstanding.
func TrayFinding(p inst.Paths, i *inst.Instance, iconPath string) Finding {
	f := Finding{Name: "托盘图标", Status: StatusOK}
	if !i.TrayOverride {
		f.Detail = "按设置跳过（tray.override = false）"
		return f
	}
	// A replay is the one command that fixes every branch below: it re-takes
	// the switch and re-writes whichever side it lands on.
	replay := fmt.Sprintf("twin reinstall %s", i.ID)

	st := tray.CheckExt(p)
	if st.Usable() {
		return trayFileFinding(p, i, iconPath, replay)
	}

	// The fix is a replay rather than a hand-typed gsettings line: the write is
	// a read-modify-write of a global key (tray.Ensure), so a literal `set`
	// copied out of here would replace the whole table — someone else's rows
	// included. One command that does it right beats a shorter one that does
	// the wrong thing.
	f.Fix = replay
	why := st.ShortWhy()

	cur, err := tray.Snapshot()
	if err != nil {
		if hs := tray.Host(); !hs.OK {
			// No gsettings, or no appindicator extension: a normal state on a
			// machine that is not this one, and not the instance's fault.
			f.Detail = "本机没有可用的托盘宿主，跳过：" + hs.Why
			return f
		}
		f.Status, f.Manual = StatusWarn, true
		f.Detail = "读不到：" + err.Error()
		return f
	}

	switch e, found := cur.Find(i.ID); {
	case !found:
		f.Status, f.Manual = StatusWarn, true
		f.Detail = fmt.Sprintf("%s 里没有 %q 的条目（托盘会继续显示应用自己的图标；%s）",
			tray.Key, i.ID, why)
	case e.Normal != iconPath:
		f.Status, f.Manual = StatusWarn, true
		f.Detail = fmt.Sprintf("%s 里 %q 指向 %s，不是本实例的图标", tray.Key, i.ID, e.Normal)
	default:
		f.Detail = fmt.Sprintf("%s → %s（%s；比面板图标小一圈是这条路线的上限）", tray.Key, i.ID, why)
	}
	return f
}

// trayFileFinding is the helper's branch of TrayFinding: the row the extension
// reads, and the host-table row that must no longer be there.
func trayFileFinding(p inst.Paths, i *inst.Instance, iconPath, replay string) Finding {
	f := Finding{Name: "托盘图标", Status: StatusOK}
	table := p.TrayTable(i.ID)

	row, found, err := tray.ReadOverride(p, i.ID)
	switch {
	case err != nil:
		return warnFinding(f, table+" 读不了："+err.Error(), replay)
	case !found:
		return warnFinding(f, table+" 不存在：助手扩展没有图标可画，托盘会显示应用自己的图标", replay)
	case row.ID != i.ID || row.Icon != iconPath:
		return warnFinding(f, fmt.Sprintf("%s 里是 %s → %s，不是本实例的图标", table, row.ID, row.Icon), replay)
	}

	// A row left in the host's table is not a second icon, it is a second
	// *source*. Both point at the same file today, which is why this is a WARN
	// and not a FAIL — but it is the row that keeps pointing at an icon after
	// the instance is gone, and it is why the install deletes it.
	if cur, err := tray.Snapshot(); err == nil {
		if _, found := cur.Find(i.ID); found {
			return warnFinding(f, fmt.Sprintf(
				"宿主表 %s 里还留着 %q 的一行（助手扩展已经在画这个图标了；同一个图标不留两个来源）",
				tray.Key, i.ID), replay)
		}
	}
	f.Detail = "助手扩展读 " + table + " → " + iconPath
	return f
}

// warnFinding is the shape every not-OK branch of the tray checks has: WARN,
// Manual (hence exit 3 — something here may need a human), and the command
// that fixes it.
func warnFinding(f Finding, detail, fix string) Finding {
	f.Status, f.Manual = StatusWarn, true
	f.Detail, f.Fix = detail, fix
	return f
}

// TrayExtFinding checks the shell-side half of the tray override.
//
// It answers one question — is the tray icon being drawn by the helper, or by
// the host's table — because that is the same question the install answered,
// and the two have to agree. Three facts have to hold for the helper: the files
// are in the user extension directory, the shell has the uuid in its enable
// list, and the shell has not refused it.
//
// It stays a WARN rather than a FAIL: the fallback path draws the icon fine, it
// is just pinned to the theme's size. And it is deliberately *not* Manual: the
// helper only makes that icon bigger, so a machine that never installs it is
// not missing a step. Marking it Manual put "twin install --ext" in the same
// block as the tray-table write that really does need a desktop session, and
// made a plain `twin install` exit 3 over something nobody has to do.
func TrayExtFinding(p inst.Paths, i *inst.Instance) Finding {
	f := Finding{Name: "托盘扩展", Status: StatusOK}
	if !i.TrayOverride {
		f.Detail = "按设置跳过（tray.override = false）"
		return f
	}
	st := tray.CheckExt(p)
	switch {
	case st.Usable():
		f.Detail = tray.ExtUUID + " 已启用 → 托盘图标由它画（" + tray.ExtDir(p) + "）"
	case !st.Installed:
		// The files are not there at all, so the command is unambiguous —
		// whether or not the extension lists could be read. Installing the
		// helper is its own command now: it is machine-wide, so an
		// instance-level reinstall is the wrong verb for it.
		f.Status = StatusWarn
		f.Detail = tray.ExtUUID + " 不在 " + tray.ExtDir(p) + "：托盘图标走宿主表兜底" +
			"（可选增强：装了才和面板图标一样大）"
		if st.Why != "" {
			f.Detail += "；扩展列表也读不到：" + st.Why
		}
		f.Fix = "twin install --ext"
	case st.Why != "":
		// Installed, but the lists themselves could not be read. Saying "not
		// enabled" here would send the reader to fix the wrong thing — the
		// lists are unreadable, which is a session-bus question.
		f.Status = StatusWarn
		f.Detail = tray.ExtUUID + " 已装入，但读不到 shell 的扩展列表：" + st.Why +
			"（现在托盘图标走宿主表兜底）"
		f.Fix = "在图形会话的终端里跑一次 twin install --ext（它会重新问一遍 shell）"
	case st.Blocked:
		f.Status = StatusWarn
		f.Detail = tray.ExtUUID + " 在 shell 的禁用列表里（被关掉过，shell 不会加载它）：托盘图标走宿主表兜底"
		f.Fix = "twin install --ext"
	default:
		f.Status = StatusWarn
		f.Detail = tray.ExtUUID + " 已装入但未启用：托盘图标走宿主表兜底" +
			"（可选增强：装了才和面板图标一样大）"
		f.Fix = tray.EnableHint()
	}
	return f
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
