// Package doctor answers one question: is this machine able to produce a
// working wrap right now, and if not, which single thing is missing?
//
// Every item reports explicitly. The failures this tool exists to prevent were
// all of the quiet kind, so a check that cannot be run is reported as skipped
// rather than assumed to be fine.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sandbox"
	"github.com/edwinhuish/twin/internal/style"
	"github.com/edwinhuish/twin/internal/sysx"
	"github.com/edwinhuish/twin/internal/tray"
	"github.com/edwinhuish/twin/internal/userns"
	"github.com/edwinhuish/twin/internal/verify"
)

// Kind classifies a check result.
type Kind int

const (
	// KindOK passed.
	KindOK Kind = iota
	// KindWarn is degraded but not blocking.
	KindWarn
	// KindFail blocks the main path.
	KindFail
	// KindSkip could not be evaluated here.
	KindSkip
)

func (k Kind) String() string {
	switch k {
	case KindOK:
		return "OK  "
	case KindWarn:
		return "WARN"
	case KindFail:
		return "FAIL"
	default:
		return "SKIP"
	}
}

// Check is one diagnostic item.
type Check struct {
	Name   string
	Kind   Kind
	Detail string
	Fix    string
}

// Report is the full diagnostic run.
type Report struct {
	Checks []Check
	// Graphical records whether a graphical session was detected, and BusKind
	// the outcome of the session-bus probe. Both decide the closing advice; they
	// live here so NextSteps does not have to re-probe the world.
	Graphical bool
	BusKind   Kind
}

// Blocking reports whether anything failed outright.
func (r *Report) Blocking() bool {
	for _, c := range r.Checks {
		if c.Kind == KindFail {
			return true
		}
	}
	return false
}

// Run performs every check.
func Run(p inst.Paths) *Report {
	rep := &Report{}
	add := func(name string, k Kind, detail, fix string) {
		rep.Checks = append(rep.Checks, Check{Name: name, Kind: k, Detail: detail, Fix: fix})
	}

	// 1. HOME — the hand-written wrapper used to fall back to /root.
	home, err := inst.Home()
	if err != nil {
		add("HOME", KindFail, err.Error(), "以真实用户身份运行 twin（图形会话里的终端）")
		return rep
	}
	add("HOME", KindOK, home, "")

	// 2. session type — decides the /tmp strategy and whether app_id even exists.
	rep.Graphical = os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
	session := os.Getenv("XDG_SESSION_TYPE")
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		add("会话类型", KindOK, "Wayland（"+os.Getenv("WAYLAND_DISPLAY")+"）—— app_id 改名这条路成立", "")
	case os.Getenv("DISPLAY") != "":
		add("会话类型", KindWarn,
			"只有 X11（DISPLAY="+os.Getenv("DISPLAY")+"）—— 窗口身份走 WM_CLASS，别用 --tmpfs /tmp", "")
	default:
		add("会话类型", KindWarn,
			"既没有 WAYLAND_DISPLAY 也没有 DISPLAY：非图形上下文。"+
				"生成产物没问题，但自检和托盘写入必须在图形会话里跑", "")
	}
	if session != "" {
		rep.Checks[len(rep.Checks)-1].Detail += " [XDG_SESSION_TYPE=" + session + "]"
	}

	// 3. bubblewrap.
	bwrap := sysx.FirstInstalled("bwrap")
	if bwrap == "" {
		add("bubblewrap", KindFail, "不在 PATH",
			"sudo apt install bubblewrap（或改用 --level none 做纯身份包裹）")
	} else {
		detail := bwrap
		if res, err := sysx.Run(bwrap, "--version"); err == nil && res.Code == 0 {
			detail += " (" + strings.TrimSpace(res.Stdout) + ")"
		}
		add("bubblewrap", KindOK, detail, "")
	}

	// 4 & 5. capability probes — installed is not the same as working.
	if bwrap != "" {
		nsProbe := sandbox.ProbeUserns(bwrap)
		kind := KindOK
		fix := ""
		if !nsProbe.OK {
			kind = KindFail
			fix = "检查 AppArmor：/usr/lib/sysctl.d/10-apparmor.conf 里的 " +
				"kernel.apparmor_restrict_unprivileged_userns"
		}
		add(nsProbe.Name, kind, nsProbe.Detail, fix)

		work := filepath.Join(p.Home, ".cache/twin/probe")
		ov := sandbox.ProbeOverlay(bwrap, work)
		kind = KindOK
		fix = ""
		if !ov.OK {
			kind = KindFail
			fix = "overlay 不可用 ⇒ 无法零拷贝改名；退路是复制一份程序目录再改名"
		}
		add(ov.Name, kind, ov.Detail, fix)

		// The machine's user-namespace situation. The restriction itself is
		// not the verdict — what matters is whether there is a way around it,
		// because that is the difference between "Chromium and Electron wrap
		// fine" and "they cannot start at all". There are two ways: a profile
		// attached to the private copy (durable, and what `twin install`
		// installs itself), or a trampoline borrowed from an Electron app.
		restricted, why := userns.Restricted()
		if !restricted {
			detail := "内核未限制非特权 user namespace"
			if why != "" {
				detail += "（" + why + "）"
			}
			add("非特权 userns", KindOK,
				detail+"：Chromium / Electron 可以直接包裹", "")
		} else {
			cands := userns.Discover(userns.DefaultDir)
			electron := 0
			for _, c := range cands {
				if c.Electron {
					electron++
				}
			}
			covered, how := userns.ProfileCovers(p.BwrapCopy())
			switch {
			case covered:
				add("非特权 userns", KindOK,
					fmt.Sprintf("内核在限制（%s），但 %s —— 私有 bwrap 副本可以直接建 user namespace",
						why, how), "")
			case electron > 0:
				pick, _ := userns.Pick(cands)
				add("非特权 userns", KindOK,
					fmt.Sprintf("内核在限制（%s），但发现 %d 个 Electron 跳板可用，例如 %s —— "+
						"Chromium / Electron 会走「私有 bwrap 副本 + 跳板」这条路", why, electron, pick.Path),
					"")
			case len(cands) > 0:
				add("非特权 userns", KindWarn,
					fmt.Sprintf("内核在限制，%s 下 %d 个候选里没有一个像 Electron —— "+
						"它们拿不到「只跑脚本、不开窗口」的驱动方式，Chromium / Electron 包裹会起不来",
						userns.DefaultDir, len(cands)),
					"装一个 Electron 应用（VS Code 即可），然后 twin reinstall <id> 让它当跳板")
			default:
				add("非特权 userns", KindWarn,
					"内核在限制，且没有找到任何自带 userns 许可的已安装程序：Chromium / Electron 包裹会起不来",
					"twin install 会自己装一份 AppArmor profile（要 sudo）；也可以装一个 Electron 应用"+
						"（VS Code 即可）当跳板")
			}
		}
	} else {
		add("bwrap 能建命名空间", KindSkip, "bubblewrap 不可用，跳过", "")
		add("overlay 零拷贝改名", KindSkip, "bubblewrap 不可用，跳过", "")
	}

	// 6. GLib desktop load. We exercise the checker on two throwaway entries
	//    before trusting it: one whose Exec is executable (must load) and one
	//    pointing at a file *without* execute bits (must be rejected). If the
	//    second one loads, our strongest silent-failure detector is blind.
	probeDir := filepath.Join(p.Home, ".cache/twin/probe")
	sh := sysx.FirstInstalled("/bin/sh", "/usr/bin/sh")
	switch {
	case sh == "":
		add("GLib 桌面项自检可用", KindWarn, "找不到 /bin/sh，无法构造探针", "")
	default:
		if err := sysx.EnsureDir(probeDir); err != nil {
			add("GLib 桌面项自检可用", KindWarn, "探针目录不可写："+err.Error(), "")
			break
		}
		notExec := filepath.Join(probeDir, "not-executable")
		good := filepath.Join(probeDir, "twin-probe-good.desktop")
		bad := filepath.Join(probeDir, "twin-probe-bad.desktop")
		_ = sysx.WriteFileAtomic(notExec, []byte("#!/bin/sh\n"), 0o644) // deliberately no +x
		_ = sysx.WriteFileAtomic(good, []byte(desktopBody("twin probe good", sh)), 0o644)
		_ = sysx.WriteFileAtomic(bad, []byte(desktopBody("twin probe bad", notExec)), 0o644)

		goodLoaded, goodDetail := verify.DesktopLoads(good)
		badLoaded, badDetail := verify.DesktopLoads(bad)
		switch {
		case goodDetail == "no-gi" || badDetail == "no-gi":
			add("GLib 桌面项自检可用", KindWarn,
				"本机没有 python3-gi —— 会少一道最能抓「静默失败」的自检",
				"sudo apt install python3-gi")
		case goodLoaded && !badLoaded:
			add("GLib 桌面项自检可用", KindOK,
				"python3-gi 可用，且能识别「Exec 无执行位 ⇒ 整个桌面项被 GLib 丢弃」这一类", "")
		case goodLoaded && badLoaded:
			add("GLib 桌面项自检可用", KindWarn,
				"探针显示 GLib 接受了不可执行的 Exec —— 判据与 2026-10-11 的实测不符，请以现场实测为准", "")
		default:
			add("GLib 桌面项自检可用", KindWarn, "好探针也没装载成功："+goodDetail, "")
		}
	}

	// 7. icon rasterisation.
	rsvg := sysx.Which("rsvg-convert")
	magick := sysx.FirstInstalled("magick", "convert")
	switch {
	case gdkPixbufAvailable():
		add("SVG/其他格式光栅化", KindOK,
			"python3-gi GdkPixbuf（PNG/JPEG 走内置解码，不需要外部工具）", "")
	case rsvg != "":
		add("SVG 光栅化", KindOK, "rsvg-convert", "")
	case magick != "":
		add("SVG/其他格式光栅化", KindOK, magick, "")
	default:
		add("SVG/其他格式光栅化", KindWarn,
			"没有可用的外部光栅器 —— 只有 PNG/JPEG 源图标能生成", "")
	}

	// 8. the tray override table: is there one, and is it readable?
	//    Reading goes through dconf's mmap path and needs no bus, which is why
	//    twin can always report the truth about the table even when it
	//    cannot write it.
	if hs := tray.Host(); !hs.OK {
		add("托盘宿主", KindOK, "本机没有 appindicator 托盘宿主（跳过图标覆盖）："+hs.Why, "")
	} else if raw, err := tray.ReadRaw(); err != nil {
		add("托盘宿主", KindWarn, "读不到 "+tray.Key+"："+err.Error(), "")
	} else {
		add("托盘宿主", KindOK, tray.Schema+" / "+tray.Key+" = "+truncate(raw, 72), "")
	}

	// 9. the shell-side half of the tray override: are the helper's files in
	//    place, and has the shell been told to load them? It is machine-wide
	//    and optional — it only changes how big a custom tray icon is drawn —
	//    so a machine that never wants it can stay this way. Not installed and
	//    installed-but-not-enabled are both worth naming with the command that
	//    fixes them, because neither one is a state twin will leave on its own.
	st := tray.CheckExt(p)
	const installExt = "twin install --ext"
	switch {
	case !st.Installed:
		why := ""
		if st.Why != "" {
			why = "；启用列表也读不到：" + st.Why
		}
		add("托盘尺寸扩展", KindWarn,
			"未安装（自定义图标会比面板图标小一圈）"+why, installExt)
	case !st.Enabled && st.Why == "":
		add("托盘尺寸扩展", KindWarn,
			tray.ExtUUID+" 已装入但未启用（自定义图标按主题的 16px 画）",
			tray.EnableHint())
	case !st.Enabled:
		add("托盘尺寸扩展", KindWarn,
			tray.ExtUUID+" 已装入，但读不到 shell 的启用列表："+st.Why,
			"在图形会话的终端里跑一次 "+installExt)
	default:
		add("托盘尺寸扩展", KindOK, tray.ExtUUID+" 已启用", "")
	}

	// 10. session bus — the write channel. A failure here is exactly the
	//    "gsettings set exits 0 but writes nothing" trap.
	busName, busKind, busDetail, busFix := busCheck()
	rep.BusKind = busKind
	add(busName, busKind, busDetail, busFix)

	// 11. writability of every destination.
	for _, d := range []string{
		filepath.Join(p.Home, ".local/share/applications"),
		filepath.Join(p.Home, ".local/share/appicons"),
		filepath.Join(p.Home, ".local/share/twin"),
		filepath.Join(p.Home, ".local/state/twin"),
		filepath.Join(p.Home, ".config/twin/rules.d"),
	} {
		if where, ok := writable(d); ok {
			add("可写 "+shrinkHome(p.Home, d), KindOK, "经由 "+shrinkHome(p.Home, where), "")
		} else {
			add("可写 "+shrinkHome(p.Home, d), KindFail,
				"最近的存在目录不可写："+shrinkHome(p.Home, where), "")
		}
	}

	return rep
}

// desktopBody renders a minimal desktop entry used by the self-check probes.
func desktopBody(name, exec string) string {
	return "[Desktop Entry]\nType=Application\nName=" + name + "\nExec=" + exec +
		"\nIcon=application-x-executable\n"
}

func gdkPixbufAvailable() bool {
	py := sysx.PythonWithGI()
	if py == "" {
		return false
	}
	res, err := sysx.Run(py, "-c",
		"import gi;gi.require_version('GdkPixbuf','2.0');from gi.repository import GdkPixbuf")
	return err == nil && res.Code == 0
}

// busCheck reports whether the session bus is reachable.
func busCheck() (string, Kind, string, string) {
	timeout := sysx.Which("timeout")
	if timeout == "" {
		return "会话总线（写 dconf 的前提）", KindSkip,
			"没有 timeout 命令，为避免卡住不探测", ""
	}
	gdbus := sysx.Which("gdbus")
	if gdbus == "" {
		return "会话总线（写 dconf 的前提）", KindSkip, "没有 gdbus 命令", ""
	}
	res, err := sysx.Run(timeout, "5", gdbus, "call", "--session",
		"--dest", "org.freedesktop.DBus", "--object-path", "/org/freedesktop/DBus",
		"--method", "org.freedesktop.DBus.ListNames")
	if err != nil {
		return "会话总线（写 dconf 的前提）", KindWarn, err.Error(), ""
	}
	if res.Code != 0 {
		return "会话总线（写 dconf 的前提）", KindWarn,
			"连不上：" + firstLine(res.Stderr) +
				" ⇒ gsettings set 会「假成功」（exit 0 但什么也没写）",
			"在真实图形会话的终端里执行写入，或交给 twin 的会话启动重放"
	}
	return "会话总线（写 dconf 的前提）", KindOK, "可连接", ""
}

// writable finds the nearest existing ancestor of path and tests it.
func writable(path string) (string, bool) {
	cur := path
	for {
		if st, err := os.Stat(cur); err == nil && st.IsDir() {
			f, err := os.CreateTemp(cur, ".twin-write-probe*")
			if err != nil {
				return cur, false
			}
			name := f.Name()
			f.Close()
			_ = os.Remove(name)
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur, false
		}
		cur = parent
	}
}

func shrinkHome(home, p string) string {
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Format renders the report for a terminal.
//
// The style is handed in rather than resolved here: whether colour is allowed
// is a property of the stream the caller is about to write to, and only the
// caller knows which stream that is.
func (r *Report) Format(st style.Style) string {
	var b strings.Builder
	fmt.Fprintf(&b, "   %-28s %s\n", "检查项", "详情")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%s %-28s %s\n", st.Mark(levelOf(c.Kind)), c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, "   %-28s → %s\n", "", c.Fix)
		}
	}
	var ok, warn, fail, skip int
	for _, c := range r.Checks {
		switch c.Kind {
		case KindOK:
			ok++
		case KindWarn:
			warn++
		case KindFail:
			fail++
		default:
			skip++
		}
	}
	// Counts are coloured only when non-zero: painting "0 失败" red would train
	// the reader to ignore the colour.
	fmt.Fprintf(&b, "\n%s / %s / %s / %s\n",
		count(st.Green, ok, "通过"), count(st.Yellow, warn, "警告"),
		count(st.Red, fail, "失败"), count(st.Dim, skip, "跳过"))
	return b.String()
}

// levelOf maps this package's Kind onto the shared verdict vocabulary, so the
// table's glyphs match the ones `twin gen` prints.
func levelOf(k Kind) style.Level {
	switch k {
	case KindOK:
		return style.LevelOK
	case KindWarn:
		return style.LevelWarn
	case KindFail:
		return style.LevelFail
	default:
		return style.LevelSkip
	}
}

func count(paint func(string) string, n int, label string) string {
	if n == 0 {
		return fmt.Sprintf("%d %s", n, label)
	}
	return paint(fmt.Sprintf("%d %s", n, label))
}

// NextSteps is the closing advice: what to actually type next.
//
// A diagnosis that ends in a table makes the reader do the translation, and the
// reason to run `doctor` first is precisely to be told where to go. So the
// findings are turned into either the next command or the one thing to fix.
func (r *Report) NextSteps() []string {
	if r.Blocking() {
		return []string{"先修掉上面的 FAIL 项（→ 后面就是修法），再回来跑 twin doctor。"}
	}
	out := []string{
		"看一眼某个程序会被包成什么样：twin inspect wechat",
		"生成配置（默认值会推导出来并打印来源）：twin gen wechat",
		"再安装（会先让你过目配置；需要 root 的那一步会问你要密码）：twin install",
		"装完自检身份链路：twin verify wechat2",
	}
	if !r.Graphical {
		out = append(out,
			"⚠️ 当前不是图形会话：产物照样能生成，但自检与托盘写入要在真实图形会话的终端里跑。")
	}
	if r.BusKind != KindOK && r.BusKind != KindSkip {
		out = append(out,
			"⚠️ 会话总线连不上 ⇒ 托盘图标那一步要在图形会话里执行（否则 gsettings set 会「假成功」）。")
	}
	return out
}
