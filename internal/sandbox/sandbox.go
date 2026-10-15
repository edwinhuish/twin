// Package sandbox builds the bubblewrap invocation that gives an instance its
// own name and its own data directories, and renders the thin launcher script.
//
// The argument order here is not cosmetic. Two orderings were measured:
//
//   - `--tmp-overlay DIR` must precede the `--ro-bind ... DIR/alias` that
//     creates the renamed entry point, otherwise the mount point cannot be
//     created ("Can't create file: Read-only file system").
//   - `--bind /run/user /run/user` must come after `--ro-bind / /` so the
//     Wayland socket, session bus and PipeWire stay reachable.
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
)

// Sh quotes s for safe interpolation into a /bin/sh script.
func Sh(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shHome quotes a token for the launcher, collapsing a path inside the
// instance's home directory to "$HOME/...".
//
// The launcher runs long after it was generated, so baking in the
// generation-time home would make it silently wrong under any other HOME — and
// would render the `: "${HOME:?}"` guard decorative. Everything that is not
// under the home directory is quoted normally.
func shHome(home, tok string) string {
	if home != "" {
		if tok == home {
			return `"$HOME"`
		}
		if rest, ok := strings.CutPrefix(tok, home+"/"); ok {
			return `"$HOME/` + dqEscape(rest) + `"`
		}
	}
	return Sh(tok)
}

// dqEscape escapes what would still expand inside double quotes.
func dqEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"', '\\', '`', '$':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Plan is a fully built bubblewrap invocation.
//
// Arguments are kept grouped rather than flat: bubblewrap options take one or
// two values each, so only the group boundary tells a renderer where a line may
// be broken.
type Plan struct {
	Bwrap  string     // path to the bubblewrap binary ("" means no sandbox)
	Groups [][]string // each group is one option plus its values
	Prog   string     // the program to execute (the alias when level=alias)
	Paths  inst.Paths
	// Copy is set when Bwrap is a private copy twin owns and has to keep in
	// sync with the system binary. Empty means the system one is in use.
	Copy string

	// Trampoline is the binary the launcher must exec *before* bubblewrap, so
	// that this process and everything it spawns carry an AppArmor label with
	// the userns permission. Empty means "exec bubblewrap directly", which is
	// right for every program that does not build a sandbox of its own.
	Trampoline string
	// TrampolineEnv is what the trampoline itself needs in order to run a
	// script instead of opening a window — for the Electron binaries that are
	// the only usable trampolines, that is ELECTRON_RUN_AS_NODE=1. It is
	// deliberately *not* part of the sandbox environment: bootstrap.js deletes
	// it before the program starts.
	TrampolineEnv []string
	// Bootstrap is the script the trampoline runs; it spawns Bwrap.
	Bootstrap string
}

// Args flattens every group into one argv tail, inserting the `--` that ends
// bwrap's option parsing when a sandbox is in play.
func (p *Plan) Args() []string {
	n := 0
	for _, g := range p.Groups {
		n += len(g)
	}
	sep := 0
	if p.Bwrap != "" {
		sep = 1
	}
	out := make([]string, 0, n+sep)
	for _, g := range p.Groups {
		out = append(out, g...)
	}
	if p.Bwrap != "" {
		out = append(out, "--")
	}
	return out
}

// Argv is the complete argv, ready for exec.
func (p *Plan) Argv(extra ...string) []string {
	out := p.Args()
	out = append(out, p.Prog)
	return append(out, extra...)
}

// String renders the command line for humans.
func (p *Plan) String() string {
	var b strings.Builder
	if p.Trampoline != "" {
		b.WriteString("env")
		for _, kv := range p.TrampolineEnv {
			b.WriteString(" " + kv)
		}
		b.WriteString(" \\\n  " + Sh(p.Trampoline) +
			" \\\n  " + Sh(p.Bootstrap) +
			" \\\n  ")
	}
	b.WriteString(Sh(p.Bwrap))
	for _, g := range p.Groups {
		b.WriteString(" \\\n  ")
		parts := make([]string, 0, len(g))
		for _, tok := range g {
			parts = append(parts, Sh(tok))
		}
		b.WriteString(strings.Join(parts, " "))
	}
	if p.Bwrap != "" {
		b.WriteString(" \\\n  --")
	}
	b.WriteString(" \\\n  " + Sh(p.Prog))
	return b.String()
}

// Build assembles the sandbox plan for an instance.
//
// The bubblewrap binary is not hard-coded: the system one's AppArmor profile
// is enough for every program that does not build a sandbox of its own, and
// using it means the distribution's fixes arrive for free. The Chromium class
// needs a private copy instead — see Resolve, which is where that decision and
// the trampoline that makes it work are worked out.
func Build(i *inst.Instance, p inst.Paths, rt Runtime) (*Plan, error) {
	if err := i.Check(); err != nil {
		return nil, err
	}

	plan := &Plan{
		Paths:         p,
		Prog:          i.Target,
		Copy:          rt.Copy,
		Trampoline:    rt.Trampoline,
		TrampolineEnv: rt.TrampolineEnv,
		Bootstrap:     rt.Bootstrap,
	}

	// Level "none" runs the program untouched: no sandbox at all, and no
	// bubblewrap requirement. Keeping this path separate stops us from
	// pretending a rename happened when it did not.
	if i.Level == inst.LevelNone {
		return plan, nil
	}

	bwrap := rt.Bwrap
	if bwrap == "" {
		bwrap = sysx.FirstInstalled("bwrap")
	}
	if bwrap == "" {
		return nil, fmt.Errorf("bubblewrap not found — install it, or use --level none for a pure-identity wrap")
	}
	plan.Bwrap = bwrap

	plan.Groups = append(plan.Groups,
		[]string{"--ro-bind", "/", "/"},
		[]string{"--dev", "/dev"},
		[]string{"--dev-bind-try", "/dev/dri", "/dev/dri"},
		[]string{"--proc", "/proc"},
		[]string{"--bind", "/run/user", "/run/user"},
	)

	// Tmpfs entries are applied verbatim. Whether /tmp belongs in this list is
	// decided by `twin inspect`/`wrap`, because only that stage knows
	// whether the program talks to the X server (a tmpfs /tmp takes
	// /tmp/.X11-unix/X0 with it). Persisting the decision in profile.yml
	// keeps it visible to whoever reads the file.
	for _, t := range i.Tmpfs {
		plan.Groups = append(plan.Groups, []string{"--tmpfs", t})
	}

	// /tmp is a decision in *both* directions. `--ro-bind / /` does not leave
	// the host's /tmp alone — it leaves it read-only — and a program that cannot
	// create a temporary directory fails in ways that have nothing to do with
	// what the user changed. Chromium's single-instance socket is the measured
	// case: process_singleton_posix.cc does `CreateUniqueTempDir()` under
	// base::GetTempDir() ($TMPDIR, else /tmp) and, when that mkdir fails with
	// EROFS, logs "[ERROR:chrome/browser/process_singleton_posix.cc] Failed to
	// create socket directory" and gives up on the lock — a message about a
	// *socket*, printed by a program the user only just wrapped.
	//
	// So the two branches are both "writable /tmp": an empty private one when
	// the program talks Wayland, and the host's own when it does not (the tmpfs
	// would take /tmp/.X11-unix/X0 with it, which is why this list is empty for
	// the X11-capable ones). What it is not is "a /tmp that exists but cannot be
	// written to", which is what the read-only root quietly produced.
	if !tmpfsCovers(i.Tmpfs, "/tmp") {
		plan.Groups = append(plan.Groups, []string{"--bind", "/tmp", "/tmp"})
	}

	if i.Level.Renames() {
		dir := i.TargetDir()
		plan.Groups = append(plan.Groups,
			[]string{"--overlay-src", dir},
			[]string{"--tmp-overlay", dir},
			[]string{"--ro-bind", i.Target, i.AliasPath()},
		)
		plan.Prog = i.AliasPath()
	}

	for _, b := range i.Binds {
		// `:ro` is the whole reason the flag is not a constant. Read-only is for
		// handing a program somebody else's directory; writable is what the
		// private-copy isolation is for.
		flag := "--bind"
		if b.ReadOnly {
			flag = "--ro-bind"
		}
		plan.Groups = append(plan.Groups,
			[]string{flag, p.Expand(b.Source), p.Expand(b.Target)})
	}

	plan.Groups = append(plan.Groups, []string{"--setenv", "TWIN_INSTANCE", i.ID})
	for _, k := range sortedKeys(i.Env) {
		plan.Groups = append(plan.Groups, []string{"--setenv", k, i.Env[k]})
	}
	plan.Groups = append(plan.Groups, []string{"--die-with-parent"})

	// The passthrough region is copied whole and unexamined. It goes last on
	// purpose: bwrap lets a later mount shadow an earlier one on the same
	// destination, so putting the user's tokens after the base gives them the
	// final word without twin owing a single conflict rule (设计 §2.3).
	//
	// It is kept as one group because the token list carries no grouping —
	// twin deliberately does not know any option's arity (设计 §2.1), so
	// inventing line breaks here would be guessing.
	if len(i.Bwrap) > 0 {
		plan.Groups = append(plan.Groups, append([]string(nil), i.Bwrap...))
	}
	return plan, nil
}

// RenderLauncher produces the thin launcher script written to
// ~/.local/share/twin/<id>/launch.
//
// It is a shell script rather than a compiled helper on purpose: after `exec`
// it leaves no trace of its own in the process table, which is the whole point
// of a "thin" wrapper.
//
// It is also twin's only chance to speak to whoever clicked the desktop icon:
// failHelpers is emitted into every launcher, and every step that can fail
// names it before it runs. See writeExec for the one step that cannot.
func RenderLauncher(i *inst.Instance, p inst.Paths, plan *Plan) ([]byte, error) {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("# twin 生成 —— 不要手改。改 profile.yml 后跑 `twin reinstall " + i.ID + "` 重新落盘。\n")
	b.WriteString("#\n")
	b.WriteString("# 身份两条通道必须同名：窗口 app_id 取自真实路径的 basename（overlay 别名），\n")
	b.WriteString("# 托盘 SNI Id 取自 argv[0]（默认就是 exec 的那条路径）。\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString(": \"${HOME:?twin: HOME 未设置，拒绝启动}\"\n\n")
	fmt.Fprintf(&b, "readonly ID=%s\n", Sh(i.ID))
	fmt.Fprintf(&b, "readonly NAME=%s\n", Sh(displayName(i)))
	fmt.Fprintf(&b, "readonly STATE=%s\n\n", shHome(p.Home, p.StateDir(i.ID)))
	b.WriteString(failHelpers)

	// Pure-identity wrap: no sandbox, the program runs untouched. Saying so
	// explicitly beats emitting a bubblewrap call that does nothing.
	if plan.Bwrap == "" {
		writeExec(&b,
			fmt.Sprintf("%s \"$@\"", shHome(p.Home, plan.Prog)),
			Sh("无法启动程序"), shHome(p.Home, plan.Prog))
		return []byte(b.String()), nil
	}

	// Every mount point, both ends of every mapping, has to exist on the host
	// before bwrap runs: the root it gets is `--ro-bind / /`, so it cannot
	// create them itself (`Can't mkdir parents …: Read-only file system`).
	//
	// The shape, though, is a fact about the disk at *launch* time, so it is
	// decided in the script rather than while rendering: `mkdir -p` on a file
	// exits non-zero ("Already exists"), which under `set -e` kills the
	// launcher before exec — and a mapping whose ends are files (a config file
	// rather than a config directory) needs the mount point created as an empty
	// file. install's half of the same rule is mountKind/EnsureMountPoints
	// below; the two must change together.
	//
	// Each call carries its own failure line: a shell function's non-zero exit
	// does not reach an ERR trap (bash needs `set -E` for that, and the trap
	// would then report a line inside mount_point — `mkdir -p -- "$2"` — rather
	// than which mapping it was working on). The sentence here is the one a
	// human can act on; the mkdir error itself is captured in the report.
	b.WriteString(mountHelpers)
	fmt.Fprintf(&b, "mount_point %s %s || twin_fail \"$?\" %s\n",
		mountDir, shHome(p.Home, p.StateDir(i.ID)),
		Sh("建立状态目录失败："+p.RelStateDir(i.ID)))
	for _, bind := range i.Binds {
		// `:ro` travels as the third argument: the **source** of a read-only
		// mapping is somebody else's path, and manufacturing one would turn
		// "you gave the wrong path" into an empty mount that looks like success.
		ro := ""
		if bind.ReadOnly {
			ro = " " + mountRO
		}
		fmt.Fprintf(&b, "mount_pair %s %s%s || twin_fail \"$?\" %s\n",
			shHome(p.Home, p.Expand(bind.Source)),
			shHome(p.Home, p.Expand(bind.Target)), ro,
			Sh(fmt.Sprintf("建立挂载点失败：%s → %s", bind.Source, bind.Target)))
	}
	b.WriteString("\n")

	// Two shapes, depending on whether this program builds a sandbox of its
	// own. The trampoline form exists because the bubblewrap we need here is a
	// private copy with no AppArmor profile — and on a machine that restricts
	// unprivileged user namespaces, only a process already carrying a label
	// that grants userns may create one. Exec'ing the trampoline's path is
	// what installs that label on this process and every descendant.
	//
	// The command is built separately because writeExec owns the last line of
	// every shape, and the two shapes end on different lines.
	var cmd strings.Builder
	if plan.Trampoline != "" {
		cmd.WriteString("env")
		if len(plan.TrampolineEnv) > 0 {
			cmd.WriteString(" " + strings.Join(plan.TrampolineEnv, " "))
		}
		fmt.Fprintf(&cmd, " \\\n  %s \\\n  %s \\\n  %s \\\n",
			shHome(p.Home, plan.Trampoline),
			shHome(p.Home, plan.Bootstrap),
			shHome(p.Home, plan.Bwrap))
	} else {
		fmt.Fprintf(&cmd, "%s \\\n", shHome(p.Home, plan.Bwrap))
	}
	for _, g := range plan.Groups {
		parts := make([]string, 0, len(g))
		for _, tok := range g {
			parts = append(parts, shHome(p.Home, tok))
		}
		fmt.Fprintf(&cmd, "  %s \\\n", strings.Join(parts, " "))
	}
	// twin owns this `--`: it ends bwrap's option parsing so the program
	// path can never be read as an option. A passthrough token that happens to
	// be `--` was already refused by inst.CheckBwrapToken.
	fmt.Fprintf(&cmd, "  -- \\\n")
	fmt.Fprintf(&cmd, "  %s \"$@\"", shHome(p.Home, plan.Prog))

	// Which one of them exec fails on is the first word of the command, and
	// that is the one worth naming — the rest of the line is already printed.
	if plan.Trampoline != "" {
		writeExec(&b, cmd.String(), Sh("无法启动跳板程序"), shHome(p.Home, plan.Trampoline))
	} else {
		writeExec(&b, cmd.String(), Sh("无法启动 bwrap"), shHome(p.Home, plan.Bwrap))
	}
	return []byte(b.String()), nil
}

// displayName is the name a report is signed with: the instance's own name
// when it has one, its id otherwise — the id is always there, and a report
// reading "twin （wechat2）" would help nobody.
func displayName(i *inst.Instance) string {
	if i.Name != "" {
		return i.Name
	}
	return i.ID
}

// writeExec renders the launcher's last command and the two lines that speak
// when it does not run. cmd is everything after `exec`, already rendered; what
// and path are shell-quoted and name the command that could not be executed.
//
// The hand-back `2>&3` is the point of the fd dance above: fd 2 is the report
// file in the GUI branch, and the program's own output has no business in a
// report about twin — nor in a file nothing rotates. fd 3 is the stderr this
// script was started with, which is where the program's diagnostics go.
//
// The `exec` needs its own guard because a shell that cannot execute the
// command exits on the spot: non-interactive, whatever `set -e` says — that is
// the `exec` builtin's own rule, and it makes the line after it unreachable.
// `shopt -s execfail` turns the exit into a return, `set +e` keeps the returned
// status from exiting anyway, and twin_fail is then reached only when the
// program never started.
func writeExec(b *strings.Builder, cmd, what, path string) {
	b.WriteString(`# 最后一步没有"下一条命令"能接住：exec 成功时这个 shell 已经不在，失败时非交互 shell
# 会直接退出（exec 这个内建命令自己的规矩，与 set -e 无关）。所以要显式关掉这两件事 ——
# 否则"程序根本没启起来"在图形会话里就是那句"点了没反应"。走到 twin_fail 那一行，
# 就意味着上面这条命令没能执行。
shopt -s execfail
set +e
`)
	fmt.Fprintf(b, "exec %s 2>&3\n", cmd)
	fmt.Fprintf(b, "twin_fail \"$?\" %s %s\n", what, path)
}

// tmpfsCovers reports whether the tmpfs list already covers path. Entries are
// compared with trailing slashes ignored: "/tmp/" and "/tmp" are the same
// directory to everyone except a shell.
func tmpfsCovers(list []string, path string) bool {
	for _, e := range list {
		if strings.TrimRight(e, "/") == path {
			return true
		}
	}
	return false
}

// Mount shapes. The two ends of a mapping must be the same kind — bwrap refuses
// to put a file on a directory (`Can't create file at <目录>: Is a directory`)
// and cannot create the mount point on either end (the root is read-only), so
// twin has to produce the right kind in advance.
const (
	mountDir  = "dir"
	mountFile = "file"
	mountRO   = "ro"
)

// mountKind reports the shape of one mapping's two ends.
//
// The only evidence is the disk: whichever end already exists carries the fact
// (a writable source pointing at a real file, or the program's own path — which
// is a file when the program keeps its state in a config file). Neither end
// present means directory: an empty directory reads as "never started", while an
// empty *file* would read as "your configuration is empty" — a lie twin has no
// business telling.
func mountKind(p inst.Paths, b inst.Bind) string {
	if sysx.IsRegularFile(p.Expand(b.Source)) || sysx.IsRegularFile(p.Expand(b.Target)) {
		return mountFile
	}
	return mountDir
}

// EnsureMountPoints creates twin's own side of every writable mapping, in the
// shape that mapping has.
//
// It is the install-time half of what mountHelpers does in shell before every
// start, and both halves are needed: a directory created here would still be
// there at launch, and the launcher never touches a path that already exists
// (that path may be the program's own data). A `:ro` source is skipped — twin
// did not put it there, and creating it would turn "you gave the wrong path"
// into an empty read-only mount that looks like it worked.
func EnsureMountPoints(i *inst.Instance, p inst.Paths) error {
	for _, b := range i.Binds {
		if b.ReadOnly {
			continue
		}
		if mountKind(p, b) == mountFile {
			if err := sysx.EnsureFile(p.Expand(b.Source)); err != nil {
				return err
			}
			continue
		}
		// A mapping that says "directory" while a file sits there is a
		// contradiction only its owner can resolve, and MkdirAll reports it
		// right here — better than a silently skipped step and a bwrap error
		// at launch.
		if err := sysx.EnsureDir(p.Expand(b.Source)); err != nil {
			return err
		}
	}
	return nil
}

// mountHelpers is the shell half of the mount-point contract, emitted verbatim
// into every launcher that needs a sandbox. Go side: mountKind, EnsureMountPoints.
const mountHelpers = `# ── 挂载点 ────────────────────────────────────────────────────────────────
# 两端都必须先在宿主机上存在：bwrap 建不了挂载点（它看到的是 --ro-bind / / 之后的只读根）。
# 也不能一律 mkdir -p —— 打在**文件**上它会报 "Already exists" 并以非零退出，而这份脚本
# 开着 set -e，于是程序根本不会启动。而且一条映射的两端必须同形（文件挂到目录上 bwrap 会
# 拒绝：Can't create file at <目录>: Is a directory），所以形态由磁盘上已有的那一端决定，
# 两端都没在时按目录（空目录 = 还没启动过；空文件 = 配置是空的，我们不替它说这句话）。
# 已经存在的路径一个指头都不碰：那是程序自己的数据，不是我们的挂载点。
mount_point() {  # <dir|file> <路径>
  if [ -e "$2" ]; then return 0; fi
  if [ "$1" = file ]; then
    mkdir -p -- "$(dirname -- "$2")" && touch -- "$2"
  else
    mkdir -p -- "$2"
  fi
}
mount_pair() {  # <源> <目标> [ro]
  local kind=dir
  if [ -f "$1" ] || [ -f "$2" ]; then kind=file; fi
  # 这一句的 "|| return 1" 不是装饰：调用方写的是 "mount_pair ... || twin_fail ..."，而
  # set -e 在"被当作 || 左侧调用的函数"体内是关掉的（bash 的规矩），于是源端建不出来时
  # 这个函数会继续去建目标端，最后用后者的成功当自己的退出码 —— 失败就这样被吞掉，
  # 再由 bwrap 报一句跟真因无关的话（Can't find source path ...: Not a directory）。
  if [ "${3:-}" != ro ]; then
    mount_point "$kind" "$1" || return 1
  fi
  mount_point "$kind" "$2"
}
`

// failHelpers is the shell half of "a launch that cannot start has to say so" —
// the launcher's counterpart to mountHelpers above, and like it, emitted
// verbatim into every launcher.
//
// There is no Go half, and there cannot be one: this only ever runs at launch,
// on a machine twin left long ago, and a wrapped instance may not depend on the
// twin binary still being there (the same reason bootstrap.js is generated
// content — see runtime.go).
//
// Why it has to exist: a .desktop launch has no terminal, so its stderr goes to
// the journal or to /dev/null, and a launcher that dies before `exec` looks
// exactly like a click that did nothing. That is the one failure nothing else
// can repair — by the time a human could run `twin verify`, they no longer
// remember that anything happened.
const failHelpers = `# ── 启动失败 ────────────────────────────────────────────────────────────────
# 从桌面项点出来的一次启动没有终端：它的 stderr 归 journal 或 /dev/null，于是脚本在 exec
# 之前死掉，在用户眼里就是"点了没反应" —— 而这正是 twin 在别处补不回来的一种失败：等到
# 他能跑 twin verify 的时候，早就忘了刚才点过什么。
#
# 是不是图形会话启动，判据是这一刻的两件事，所以判在脚本里而不是渲染时（同一份脚本既会被
# 桌面项点，也会被人在 shell 里敲）：没有控制终端（桌面项启动的进程 stdin/stderr 都不接
# 终端）＋ 有显示（DISPLAY 或 WAYLAND_DISPLAY）。少一条都不弹：终端里跑的时候同一份错误
# 就在用户眼前，再糊一个窗口上去只会挡住它；没有显示的时候弹窗工具连不上 X，弹了也是白弹。
GUI=0
if [ ! -t 0 ] && [ ! -t 2 ] && [ -n "${WAYLAND_DISPLAY:-}${DISPLAY:-}" ]; then GUI=1; fi
readonly GUI
readonly LOG="$STATE/launch-error.log"

# twin 自己的报错要留一份给报告，程序自己的 stderr 却要原样交出去 —— 两者是同一个 fd2。
# 所以先备份到 fd3，只在图形会话里把 fd2 指向报告文件（终端里那份输出本来有人读，重定向
# 只会让它消失），最后 exec 时再用 2>&3 还回去。
exec 3>&2
if [ "$GUI" = 1 ]; then
  mkdir -p -- "$STATE" 2>/dev/null || true
  # 这一行失败（$HOME 只读之类）不中断：报告文件只是"抓到的输出"的来源，结论那句照样弹。
  exec 2>"$LOG" || true
fi

# 弹窗按"正文能选中复制"挑：这段字存在的全部意义就是让用户把它贴进 bug 报告里，复制不出来
# 等于没有。xmessage 因此不在链里（正文是一个不可选中的 Label），notify-send 也不在
# （通知里的字一样选不中）。都没有时什么也不弹：报告仍然落在 $LOG 里，journal 里留了它的
# 路径 —— 宁可什么都不弹，也不弹一个查不动的东西。
twin_dialog() {  # <标题>；正文从 stdin 读
  if command -v zenity >/dev/null 2>&1; then
    zenity --text-info --title="$1" --width=820 --height=460
  elif command -v kdialog >/dev/null 2>&1; then
    kdialog --title "$1" --textbox /dev/stdin 820 460
  elif command -v yad >/dev/null 2>&1; then
    yad --text-info --title="$1" --width=820 --height=460
  else
    cat >/dev/null   # 把正文读掉，免得上游的 printf 撞上 EPIPE 再抱怨一句
    return 1
  fi
}

# exec 失败的原因只有那两三种，与其让调用方猜，不如照着磁盘说 —— 判据仍然是回读。
twin_why() {  # <路径>
  if [ ! -e "$1" ]; then
    printf '（这个文件不在了）'
  elif [ ! -x "$1" ]; then
    printf '（没有执行位）'
  fi
}

# twin_fail <退出码> <哪一步没成> [路径]
#
# 只在失败时被调用，所以这里可以假定"不会更顺了"：set +e 是有意的 —— 报告里任何一条命令再
# 出问题（文件写不进去、没有弹窗工具），都不能把"把话说出来"这件事本身弄没了。
twin_fail() {
  set +e
  local code=$1 step=$2 path=${3:-} captured='' report
  # 终端启动：bash 与失败那条命令自己的措辞已经在用户眼前（比这句转述准），不再多说一句，
  # 也不碰报告文件 —— 那个文件只属于"没人看得见输出"的那种启动。
  [ "$GUI" = 1 ] || exit "$code"
  exec 2>&3
  if [ -n "$path" ]; then step="$step：$path$(twin_why "$path")"; fi
  if [ -s "$LOG" ]; then captured=$(cat -- "$LOG"); fi
  # 一次拼好、两处使用（落盘与弹窗），为的是屏幕上那份与文件里那份逐字相同。
  report=$(
    printf 'twin %s（%s）：启动失败（退出码 %s）\n\n哪一步没成：%s\n' "$NAME" "$ID" "$code" "$step"
    if [ -n "$captured" ]; then
      printf '\n──── 这次启动的输出 ────\n%s\n' "$captured"
    fi
    printf '\n启动器：%s\n报告文件：%s\n下一步：twin doctor 看这台机器，twin verify %s 看这个实例。\n' \
      "$0" "$LOG" "$ID"
  )
  printf '%s\n' "$report" > "$LOG"
  printf '%s' "$report" | twin_dialog "twin $NAME：启动失败"
  printf 'twin %s：启动失败，报告在 %s\n' "$ID" "$LOG" >&2
  exit "$code"
}

`

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort keeps this dependency-free; the map is always tiny.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ProbeResult is one capability check.
type ProbeResult struct {
	Name   string
	OK     bool
	Detail string
}

// ProbeUserns checks that bubblewrap can actually create a sandbox here, which
// is the difference between "it is installed" and "it works".
func ProbeUserns(bwrap string) ProbeResult {
	res := ProbeResult{Name: "bwrap 能建命名空间"}
	if bwrap == "" {
		res.Detail = "bwrap 不在 PATH"
		return res
	}
	sh := sysx.FirstInstalled("/bin/sh", "/usr/bin/sh")
	if sh == "" {
		res.Detail = "找不到 /bin/sh，无法探测"
		return res
	}
	out, err := sysx.Run(bwrap, "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--", sh, "-c", "exit 0")
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	if out.Code != 0 {
		res.Detail = firstLine(out.Stderr)
		if res.Detail == "" {
			res.Detail = fmt.Sprintf("exit %d", out.Code)
		}
		return res
	}
	res.OK = true
	res.Detail = "ok"
	return res
}

// ProbeOverlay checks the rename mechanism specifically: a directory made
// writable in-namespace via a tmpfs overlay, with a new file name created
// inside it without touching the host.
func ProbeOverlay(bwrap, workdir string) ProbeResult {
	res := ProbeResult{Name: "overlay 零拷贝改名"}
	if bwrap == "" {
		res.Detail = "bwrap 不在 PATH"
		return res
	}
	sh := sysx.FirstInstalled("/bin/sh", "/usr/bin/sh")
	if sh == "" {
		res.Detail = "找不到 /bin/sh"
		return res
	}
	if err := sysx.EnsureDir(workdir); err != nil {
		res.Detail = err.Error()
		return res
	}
	src := filepath.Join(workdir, "orig")
	if !sysx.Exists(src) {
		if err := sysx.WriteFileAtomic(src, []byte("probe\n"), 0o644); err != nil {
			res.Detail = err.Error()
			return res
		}
	}
	alias := filepath.Join(workdir, "alias-probe")
	_ = os.Remove(alias) // a stale alias would make the check meaningless
	script := fmt.Sprintf("test -f %s", Sh(alias))
	out, err := sysx.Run(bwrap,
		"--ro-bind", "/", "/",
		"--overlay-src", workdir,
		"--tmp-overlay", workdir,
		"--ro-bind", src, alias,
		"--", sh, "-c", script)
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	if out.Code != 0 {
		res.Detail = firstLine(out.Stderr)
		if res.Detail == "" {
			res.Detail = "别名未能创建（overlay 不可用）"
		}
		return res
	}
	// The host must not have learned about the alias.
	if sysx.Exists(alias) {
		res.Detail = "别名泄漏到了宿主文件系统 —— overlay 未生效"
		return res
	}
	res.OK = true
	res.Detail = "ok（别名只存在于命名空间内）"
	return res
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
