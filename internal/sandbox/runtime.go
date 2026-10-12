package sandbox

import (
	"github.com/edwinhuish/twin/internal/config"
	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
	"github.com/edwinhuish/twin/internal/userns"
)

// Runtime is how *this machine* has to launch bubblewrap for one instance.
//
// Everything here is environment, so it is resolved on every `wrap` and every
// `regen` instead of being stored: another machine has another bubblewrap,
// another set of installed trampolines and another kernel setting. The one
// fact that does travel — whether the program needs a nested user namespace at
// all — lives in the snapshot as inst.NestedNS.
type Runtime struct {
	// Bwrap is the binary the launcher execs. Empty means "whatever is on
	// PATH", resolved later by Build.
	Bwrap string
	// Copy is non-empty when Bwrap is a private copy twin owns and must
	// keep in sync with the system binary.
	Copy string
	// Trampoline is the binary to exec before bubblewrap so that this process
	// carries an AppArmor label granting userns. Empty means "exec bubblewrap
	// directly", which is right whenever the copy can create one on its own.
	Trampoline    string
	TrampolineEnv []string
	Bootstrap     string
	// NeedsProfile is set when the copy cannot create a user namespace and no
	// trampoline exists to borrow a label from. The wrap is still allowed to
	// succeed — the caller prints userns.ProfileHint and exits 3, the same
	// code that already means "identity is in place, one human step remains".
	NeedsProfile bool
	// Notes explain the choice, for the derivation log.
	Notes []string
}

// Resolve picks the bubblewrap to exec and, for a program that builds its own
// sandbox, works out how to give it a working user namespace.
//
// The decision tree, and why each branch exists:
//
//	程序不需要嵌套 ns
//	    系统 bwrap 够用。不复制、不动它 —— 这样发行版修 bwrap 时，这个实例
//	    自动跟着受益，而不是守着一份旧副本。
//
//	需要嵌套 ns
//	    必须换成私有副本。/usr/bin/bwrap 的 profile 有
//	    `allow pix /** -> &bwrap//&unpriv_bwrap`，而 unpriv_bwrap 带
//	    `audit deny capability`：后代建不起嵌套 user namespace（2026-10-12
//	    实测 unshare(NEWUSER|NEWNS) 直接 EPERM），Chromium 只会 FATAL。
//
//	副本也建不了 ns
//	    内核在限制非特权 userns 时（Ubuntu 26.04 默认开），无 profile 的副本
//	    同样建不了。此时唯一的零注册出路是"跳板"：先 exec 一个自带
//	    `flags=(unconfined) { userns, }` profile 的 Electron 程序，让内核把标签
//	    换掉，再由它 spawn 副本。找不到跳板就如实报 NeedsProfile。
func Resolve(p inst.Paths, cfg *config.Config, needNestedNS bool) Runtime {
	rt := Runtime{}
	system := cfg.Tools.Bwrap // "" = Build 再去 PATH 里找

	if !needNestedNS {
		rt.Bwrap = system
		return rt
	}

	switch cfg.Tools.BwrapCopy {
	case "":
		// The user turned copies off. Say what that costs instead of silently
		// producing a launcher that cannot start the program.
		rt.Bwrap = system
		rt.Notes = append(rt.Notes,
			"程序需要嵌套 user namespace，但 config.toml 里 tools.bwrap_copy = \"\"："+
				"系统 bwrap 的后代会丢掉 capability，这个程序很可能起不来")
		return rt
	case "auto":
		rt.Copy = p.BwrapCopy()
	default:
		rt.Copy = cfg.Tools.BwrapCopy
	}
	rt.Bwrap = rt.Copy

	restricted, why := userns.Restricted()
	if !restricted {
		note := "内核没有限制非特权 user namespace"
		if why != "" {
			note += "（" + why + "）"
		}
		rt.Notes = append(rt.Notes, note+"：私有副本可以直接建")
		return rt
	}

	switch cfg.USerns.Trampoline {
	case "off":
		rt.NeedsProfile = true
		rt.Notes = append(rt.Notes,
			"config.toml 里 userns.trampoline = \"off\"：按打印出来的 profile 自己装一份")
	case "", "auto":
		if t, ok := userns.Pick(userns.Discover(userns.DefaultDir)); ok {
			rt.useElectronTrampoline(p, t.Path, t.Describe())
			return rt
		}
		rt.NeedsProfile = true
		rt.Notes = append(rt.Notes,
			"没找到可用跳板："+userns.DefaultDir+" 下没有「既像 Electron、profile 又带 userns 许可」的已安装程序")
	default:
		tp := cfg.USerns.Trampoline
		if !sysx.IsExecutableFile(tp) {
			rt.NeedsProfile = true
			rt.Notes = append(rt.Notes, "config.toml 指定的跳板不可执行："+tp)
			return rt
		}
		if !userns.IsElectron(tp) {
			// A non-Electron binary also carries a userns-granting profile,
			// but nothing short of its own command line makes it spawn our
			// copy instead of opening a window. Refusing beats generating a
			// launcher that launches the trampoline's GUI.
			rt.NeedsProfile = true
			rt.Notes = append(rt.Notes,
				"config.toml 指定的跳板不是 Electron 程序（没有 resources/app*）："+
					"它拿不到「只跑脚本、不开窗口」的驱动方式，用不了")
			return rt
		}
		rt.useElectronTrampoline(p, tp, tp+"（config.toml userns.trampoline）")
	}
	return rt
}

// useElectronTrampoline fills in the three pieces an Electron trampoline
// needs. ELECTRON_RUN_AS_NODE=1 goes to the exec itself only — bootstrap.js
// removes it again so the wrapped program never sees it, which would otherwise
// start a second WorkBuddy-class app in Node mode with no window.
func (rt *Runtime) useElectronTrampoline(p inst.Paths, path, why string) {
	rt.Trampoline = path
	rt.TrampolineEnv = []string{"ELECTRON_RUN_AS_NODE=1"}
	rt.Bootstrap = p.Bootstrap()
	rt.Notes = append(rt.Notes, "跳板 "+why)
}
