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
	fmt.Fprintf(&b, "readonly STATE=%s\n\n", shHome(p.Home, p.StateDir(i.ID)))

	// Pure-identity wrap: no sandbox, the program runs untouched. Saying so
	// explicitly beats emitting a bubblewrap call that does nothing.
	if plan.Bwrap == "" {
		fmt.Fprintf(&b, "exec %s \"$@\"\n", shHome(p.Home, plan.Prog))
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
	b.WriteString(mountHelpers)
	fmt.Fprintf(&b, "mount_point %s %s\n", mountDir, shHome(p.Home, p.StateDir(i.ID)))
	for _, bind := range i.Binds {
		// `:ro` travels as the third argument: the **source** of a read-only
		// mapping is somebody else's path, and manufacturing one would turn
		// "you gave the wrong path" into an empty mount that looks like success.
		ro := ""
		if bind.ReadOnly {
			ro = " " + mountRO
		}
		fmt.Fprintf(&b, "mount_pair %s %s%s\n",
			shHome(p.Home, p.Expand(bind.Source)),
			shHome(p.Home, p.Expand(bind.Target)), ro)
	}
	b.WriteString("\n")

	// Two shapes, depending on whether this program builds a sandbox of its
	// own. The trampoline form exists because the bubblewrap we need here is a
	// private copy with no AppArmor profile — and on a machine that restricts
	// unprivileged user namespaces, only a process already carrying a label
	// that grants userns may create one. Exec'ing the trampoline's path is
	// what installs that label on this process and every descendant.
	if plan.Trampoline != "" {
		fmt.Fprintf(&b, "exec")
		if len(plan.TrampolineEnv) > 0 {
			fmt.Fprintf(&b, " env %s", strings.Join(plan.TrampolineEnv, " "))
		}
		fmt.Fprintf(&b, " \\\n  %s \\\n  %s \\\n  %s \\\n",
			shHome(p.Home, plan.Trampoline),
			shHome(p.Home, plan.Bootstrap),
			shHome(p.Home, plan.Bwrap))
	} else {
		fmt.Fprintf(&b, "exec %s \\\n", shHome(p.Home, plan.Bwrap))
	}
	for _, g := range plan.Groups {
		parts := make([]string, 0, len(g))
		for _, tok := range g {
			parts = append(parts, shHome(p.Home, tok))
		}
		fmt.Fprintf(&b, "  %s \\\n", strings.Join(parts, " "))
	}
	// twin owns this `--`: it ends bwrap's option parsing so the program
	// path can never be read as an option. A passthrough token that happens to
	// be `--` was already refused by inst.CheckBwrapToken.
	fmt.Fprintf(&b, "  -- \\\n")
	fmt.Fprintf(&b, "  %s \"$@\"\n", shHome(p.Home, plan.Prog))
	return []byte(b.String()), nil
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
  if [ "${3:-}" != ro ]; then
    mount_point "$kind" "$1"
  fi
  mount_point "$kind" "$2"
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
