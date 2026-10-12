package generate

import (
	"bytes"
	"os"
)

// Traits are the few facts about a program that change how we wrap it.
//
// This is a byte scan, not a dependency walk: a statically linked toolkit (and
// anything loaded with dlopen) is invisible to `ldd`, which is exactly how the
// "WeChat is a hand-drawn Wayland client" misreading happened on 2026-10-11.
type Traits struct {
	Wayland bool
	X11     bool
	Qt      bool
	GTK     bool
	// MultiCall marks a byte-identical multi-call binary (BusyBox / coreutils).
	// The program picks which tool to be from argv[0], so renaming its path
	// (LevelAlias) makes it stop recognising itself — measured on 2026-10-12:
	// `/usr/lib/cargo/bin/coreutils/env` renamed to `envprobe` printed
	// "coreutils: unknown program 'envprobe'". LevelAlias is unusable there.
	MultiCall bool
	// NeedsNestedNS marks the Chromium/Electron class: the program creates a
	// user namespace of its own at run time to build its sandbox, so it
	// cannot run under a bubblewrap whose descendants lost capabilities.
	//
	// This is detected from the strings every Chromium build carries rather
	// than from the Electron layout check in internal/userns, because the two
	// answer different questions: that one asks "can this binary be a
	// trampoline?", this one asks "does this program need one?". Plain
	// Chrome/Edge need the treatment without being Electron.
	NeedsNestedNS bool
	Notes         []string
}

type needle struct {
	key  string
	text string
}

var traitNeedles = []needle{
	{"wayland", "libwayland-client"},
	{"wayland", "wl_display_connect"},
	{"wayland", "xdg_toplevel"},
	{"x11", "libX11.so"},
	{"x11", "XOpenDisplay"},
	{"x11", "QXcbIntegration"},
	{"qt", "QtWaylandClient"},
	{"qt", "libQt5Core.so"},
	{"qt", "libQt6Core.so"},
	{"gtk", "libgtk-3.so"},
	{"gtk", "libgtk-4.so"},
	// Both BusyBox (" multi-call binary.") and coreutils ("(multi-call binary)")
	// carry this phrase verbatim.
	{"multicall", "multi-call binary"},
	// Chromium 系共有的三个字符串。第一条在 Chrome/Edge/Electron 里必然存在
	// （它们都要 spawn 自家沙箱助手），第二条只有 Electron 才有，第三条是
	// Electron 之前的 CEF 打包。
	{"nestedns", "chrome-sandbox"},
	{"nestedns", "ELECTRON_RUN_AS_NODE"},
	{"nestedns", "libcef.so"},
}

// ScanTraits classifies a binary by the strings it carries.
func ScanTraits(path string) (Traits, error) {
	f, err := os.Open(path)
	if err != nil {
		return Traits{}, err
	}
	defer f.Close()

	maxLen := 0
	for _, n := range traitNeedles {
		if len(n.text) > maxLen {
			maxLen = len(n.text)
		}
	}

	hit := map[string]bool{}
	buf := make([]byte, 1<<20)
	var carry []byte
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			seg := make([]byte, 0, len(carry)+n)
			seg = append(seg, carry...)
			seg = append(seg, buf[:n]...)
			for _, nd := range traitNeedles {
				if !hit[nd.key] && bytes.Contains(seg, []byte(nd.text)) {
					hit[nd.key] = true
				}
			}
			if keep := maxLen - 1; len(seg) > keep {
				carry = append(carry[:0], seg[len(seg)-keep:]...)
			} else {
				carry = append(carry[:0], seg...)
			}
		}
		if rerr != nil {
			break
		}
	}

	t := Traits{
		Wayland:       hit["wayland"],
		X11:           hit["x11"],
		Qt:            hit["qt"],
		GTK:           hit["gtk"],
		MultiCall:     hit["multicall"],
		NeedsNestedNS: hit["nestedns"],
	}
	switch {
	case t.Wayland && t.Qt:
		t.Notes = append(t.Notes, "Wayland 原生 + 静态链接的 Qt：窗口 app_id 由 Qt 从可执行文件真实路径派生（已实测）")
	case t.Wayland:
		t.Notes = append(t.Notes, "内含 Wayland 客户端代码（运行时 dlopen，所以 ldd 看不到）")
	case t.X11:
		t.Notes = append(t.Notes, "只看到 X11：身份走 WM_CLASS，且不能用 --tmpfs /tmp")
	}
	if t.Wayland && t.X11 {
		t.Notes = append(t.Notes, "同时含 Wayland 与 X11 平台插件：本会话有 WAYLAND_DISPLAY 时按 Wayland 走")
	}
	if t.NeedsNestedNS {
		t.Notes = append(t.Notes,
			"Chromium / Electron 系：运行时自己会建一个嵌套 user namespace 来搭沙箱。"+
				"系统 /usr/bin/bwrap 的后代被叠上了 unpriv_bwrap（内含 audit deny capability），"+
				"在里面建不起来 —— 所以这个实例必须走私有 bwrap 副本，并可能还需要一个跳板"+
				"（`twin doctor` 会报告本机有没有）")
	}
	if t.MultiCall {
		t.Notes = append(t.Notes,
			"这是多合一二进制（BusyBox / coreutils 那种靠 argv[0] 决定「自己是哪个工具」的程序）："+
				"alias 改名后它会不认识自己，identity.level=alias 用不了")
	}
	return t, nil
}
