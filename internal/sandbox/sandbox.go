// Package sandbox builds the bubblewrap invocation that gives an instance its
// own name and its own data directories, and renders the thin launcher script.
//
// twin owns no sandbox base at launch time: every option comes from the
// definition, and `twin gen` writes the base into it with BaseFor below — the
// root into `bind`, the other mounts and flags into `bwrap.extras` as the option
// lines they are. What this package still emits is only what those keys ask for:
// the alias overlay (which follows from the id), the mappings from `bind`, the
// option lines from `extras`, and the variables from `env`.
//
// Three positions are decided here rather than by the file, and each one was
// measured before it was written down:
//
//   - the root mount goes down before everything, the other `bind` entries
//     included — bind the root after the alias overlay and the host's directory
//     is laid back over the copy the program is supposed to run from, alias and
//     program together;
//   - the alias overlay goes down after the root but *before* the rest of
//     `bind`: an overlay hides what was mounted under the directory it covers
//     (its lower layer does not follow submounts — measured), so a mapping onto
//     a path inside the target directory has to come after it;
//   - `extras` goes down after every mount and before `env`. That is what lets
//     `--remount-ro` live in it at all: it only bites on a path that is itself a
//     mount, so it has to come after the mount it tightens.
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
)

// Sh quotes s for safe interpolation into the launcher — and leaves it bare
// when a bare word already *is* that string.
//
// Quoting every token is not the safe side of this choice, it is the unreadable
// one: the launcher is read exactly when something went wrong, and a wall of
// `'--dev-bind' '/' '/'` makes the reader strip quotes before they can find the
// mapping they came for. The quotes stay on the tokens that would mean
// something else without them — a path with a space is the case that made them
// necessary — and on the empty string, which as a bare word would vanish
// instead of arriving as an argument.
func Sh(s string) string {
	if s != "" && !strings.ContainsAny(s, shSpecial) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shSpecial is everything a bare word would be read as instead of itself: the
// shell's metacharacters, the ones that expand (`$`, backtick, glob characters,
// and bash's braces — the launcher is bash, `{a,b}` would split into two words)
// and the quoting characters themselves. desk.quoteExecArg keeps its own copy of
// nearly this list for the Exec= line; that import edge does not exist in either
// direction, so the two are kept equal by hand.
const shSpecial = " \t\n\"'\\><~|&;$*?#()`[]{}!"

// shHome quotes a token for the launcher, collapsing a path inside the
// instance's home directory to "$HOME/...".
//
// The launcher runs long after it was generated, so baking in the
// generation-time home would make it silently wrong under any other HOME — and
// would render the `: "${HOME:?}"` guard decorative. Everything that is not
// under the home directory goes through Sh, which quotes it only if it has to.
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
	// ProgArgs is `source.args` — what goes after Prog and before the
	// arguments the desktop environment passes in (GLib's `%U`/`%F`). It is
	// not a group: those are bwrap options, and these belong to the program on
	// both shapes of the launcher (inside a sandbox and level=none alike), so
	// keeping them apart is what lets one renderer emit them once.
	ProgArgs []string
	Paths    inst.Paths
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
//
// The order is the launcher's own: bwrap options, the separator, the program,
// the program's `source.args`, and only then whatever the caller adds (the
// desktop environment's files/URLs). `extra` after the args is the point — a
// switch belongs in front of an operand, and `%U` expands to operands.
func (p *Plan) Argv(extra ...string) []string {
	out := p.Args()
	out = append(out, p.Prog)
	out = append(out, p.ProgArgs...)
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
	// Not through Sh: the preview has to be the line the launcher will run
	// (see progArgs), and a quote shown here but absent there — or the other
	// way round — is the kind of preview that teaches the wrong thing.
	for _, a := range p.ProgArgs {
		b.WriteString(" " + a)
	}
	return b.String()
}

// progArgs renders `source.args` for the launcher's last command: the tokens
// exactly as the definition writes them, one space apart.
//
// This is the one place in the file that adds no quotes, and the reason is
// what the key means: these are the program's own arguments, written the way
// its `--help` writes them, and the launcher line is supposed to read as what
// it does. Quoting them here would show up as `'--user-data-dir=~/x'` — and,
// worse than ugly, it would stop the shell from expanding `~` and `$HOME`,
// which is the only way such a token can mean anything at launch time. The
// other side of the same rule is the user's: a value with a space in it has
// to be quoted in the definition itself, exactly as one would type it in a
// terminal (whitespace splits the line into tokens first, and the quotes ride
// along inside a token).
//
// It is a function rather than inline code because there are two shapes of
// launcher — with bubblewrap and without it — and both end on the program. The
// arguments have to come out the same way in both, or one definition would
// produce two different commands depending on the level it happens to have.
func progArgs(args []string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(" " + a)
	}
	return b.String()
}

// Base is the sandbox base `twin gen` writes into a definition, in the pieces
// Plan has to interleave: the root, the mounts and the option lines.
//
// It lives in Go only because it has to be written *once*, into the file where
// the user can read and change it; Build never adds it (see Build below). Each
// piece is a line of the command that calls bwrap, and the reason they are not
// one flat list is ordering alone — Plan has to place the caller's own `--tmpfs`
// lines between Head and HostTmp, and everything else after them.
type Base struct {
	// Root is the value `gen` writes into `bwrap.root`: `dev`, because an
	// identity wrapper needs the whole host including its devices (twin is not
	// a security boundary). It is the mount's own key rather than an entry in
	// the mounts, and Build puts it in front of everything wherever the file
	// would have placed it — see inst.Instance.RootBind.
	Root string
	// Head is the option lines that go before the caller's tmpfs lines.
	Head []string
	// Tmpfs is what the base itself hides behind an empty directory.
	Tmpfs []string
	// HostTmp, when set, is a path to bind onto itself *after* every tmpfs line
	// (that ordering is the whole point — see BaseFor).
	HostTmp string
	// Tail is what closes the base's own lines.
	Tail []string
}

// BaseFor builds the base a new definition starts from.
//
// The root is the load-bearing choice: `--dev-bind / /` hands the program the
// whole host including its devices, which is what an identity wrapper needs
// (twin is not a security boundary). It goes into `bwrap.root` rather than a
// pair of tokens in `extras` — one word, in the key that says what it is, since
// a definition may only name its root in one place (see inst.Check).
//
// privateTmp says which branch the /tmp decision takes, and twin only has to
// write one of them: a private /tmp is a tmpfs line, and the caller's own list
// already carries it (that is what makes this true) — so what is owed here is
// the *other* branch, the host's /tmp bound onto itself, as one more option
// line. Both branches leave /tmp *writable*: the mount is a no-op under the
// writable root of today's base, and it is the only thing keeping /tmp writable
// if a definition tightens that root to `/:/:ro`. Writable is the point — a
// read-only /tmp is what a read-only root quietly produces, and it makes
// programs fail somewhere unrelated (Chromium: "Failed to create socket
// directory"). The caller chooses from whether the program can fall back to
// X11, because a tmpfs /tmp takes /tmp/.X11-unix/X0 with it.
//
// Being a line in `extras` is what makes the swap against a tmpfs work: "把 tmpfs
// 里那条 /tmp 换成宿主那个" is now a matter of which line comes last, in one
// list, in one file — the same file where the tmpfs line would be written by
// hand. It also stops the line from being *reported* as one of the program's own
// data directories: it is an option that happens to name a path, not a mapping.
func BaseFor(privateTmp bool) Base {
	b := Base{
		Root: "dev",
		Head: []string{"--proc /proc"},
		// An empty private shared memory: /dev/shm on the host is shared with
		// every other process, which is not what an instance's own name means.
		Tmpfs: []string{"/dev/shm"},
		Tail:  []string{"--die-with-parent"},
	}
	if !privateTmp {
		b.HostTmp = "/tmp"
	}
	return b
}

// bindGroup renders one mapping as the bubblewrap option that carries it.
//
// The mode is the whole reason the flag is not a constant. Read-only is for
// handing a program somebody else's directory, `:dev` for a directory of device
// nodes (which only `--dev-bind` lets through — the root mount is one of them),
// and plain writable is what the private-copy isolation is for.
//
// `:try` picks the `-try` spelling of that same option, which is bwrap's own
// "ignores non-existent SRC". It is a suffix on the flag rather than a fourth
// mode because it says nothing about *how* the mapping is made — only about
// what to do when its source is not there (see inst.Bind.Try).
func bindGroup(p inst.Paths, b inst.Bind) []string {
	flag := "--bind"
	switch {
	case b.ReadOnly:
		flag = "--ro-bind"
	case b.Dev:
		flag = "--dev-bind"
	}
	if b.Try {
		flag += "-try"
	}
	return []string{flag, p.Expand(b.Source), p.Expand(b.Target)}
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

	// `source.args` — the program's own arguments. They are collected here,
	// before the level=alias branch below, because they are not part of the
	// sandbox: level=none execs the program directly and must carry them just
	// as an alias-level instance does, or turning the sandbox off would
	// silently drop the switch that made the program usable.
	//
	// Each line is split on whitespace and nothing else — twin does not parse
	// arguments (the same understanding `extras` has). They are deliberately
	// *not* run through Expand the way a mapping or an option line is: these
	// tokens go into the launcher without quotes (see progArgs), so the shell
	// that starts the program is what expands `~` and `$HOME`. Expanding them
	// here would freeze the home directory of whoever ran `gen` into a
	// definition that is meant to be replayable on another machine.
	for _, line := range i.Args {
		plan.ProgArgs = append(plan.ProgArgs, strings.Fields(line)...)
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

	// The root, before everything — including the file's own option list. It is
	// the one mount whose place in the command is not the place it has in the
	// file, and that is not a preference: bubblewrap applies mounts in order and
	// the later one wins, so a root bound *after* the alias overlay puts the
	// host's directory back over the copy the program is supposed to run from —
	// hiding the alias path, and with it the program.
	//
	// Two spellings reach here, and only one of them can be in a file at a time
	// (inst.Check refuses both): `bwrap.root`, which every definition `gen`
	// writes, and the `/:/…` entry older ones carry. Both come out through
	// bindGroup, so the two never render differently.
	if rb, ok := i.RootBind(); ok {
		plan.Groups = append(plan.Groups, bindGroup(p, rb))
	} else {
		for _, b := range i.Binds {
			if b.IsRootMount() {
				plan.Groups = append(plan.Groups, bindGroup(p, b))
			}
		}
	}

	// The alias overlay, right after the root and *before* the rest of `bind`.
	// Both halves of that position were measured: the overlay's lower layer does
	// not follow submounts, so a mapping onto a path inside the target directory
	// that went down first is simply not visible through the overlay — and the
	// `--ro-bind` that creates the alias has to follow the overlay, because it is
	// a mount *on* the writable copy (before it, the copy would hide the alias).
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
		// The root went down before everything (above); the rest is emitted here,
		// in the order the file lists it.
		if b.IsRootMount() {
			continue
		}
		plan.Groups = append(plan.Groups, bindGroup(p, b))
	}

	// Everything else bwrap is called with, as the option lines the definition
	// writes them. One group per line, split on whitespace: the line *is* the
	// argument group, and twin does not know any bwrap option's arity, so
	// splitting on spaces is the whole of its understanding (设计 §2.1) — which
	// is also why a value containing a space cannot be written here.
	//
	// After every mount, and that is the position `--remount-ro` needs: it only
	// bites on a path that is itself a mount (a `bind` target, an overlay, the
	// root), and anywhere earlier the mount it was meant to tighten would be laid
	// down on top of it, writable again. Anywhere else bwrap refuses to start
	// (`Can't remount readonly on …: Unable to find … in mount table` — measured).
	// It is also the position the base's host-/tmp line needs (see BaseFor), and
	// the two are the same statement: in one list, in one file, the line written
	// last wins.
	//
	// Each token then goes through Expand, like every other path in the file: a
	// rule's `--tmpfs ~/.cache/x` has to mean *this* machine's home, and it can
	// only know that here — at generation time the path would have been frozen
	// into whoever ran `gen`. Expanding a leading `~/` is not parsing the option:
	// it is the same one-line rule the mappings get, applied to whatever the
	// token turns out to be.
	for _, line := range i.Extras {
		toks := strings.Fields(line)
		if len(toks) == 0 {
			continue
		}
		for n, t := range toks {
			toks[n] = p.Expand(t)
		}
		plan.Groups = append(plan.Groups, toks)
	}

	// The variables, in the order the file lists them (later `--setenv` wins on
	// a duplicate, exactly as bwrap does it), and then twin's own marker.
	//
	// TWIN_INSTANCE is emitted here rather than read from the definition: it
	// says which sandbox this is, the launcher uses it to notice that it was
	// started from inside its own namespace (see RenderLauncher), and a key
	// that could set it could also switch that check off. It goes last so a
	// definition that still carries the old line — every file `gen` wrote
	// before the key moved — cannot overrule it; inst.Load drops those lines
	// when they agree with the id and refuses the ones that do not.
	for _, e := range i.Env {
		plan.Groups = append(plan.Groups, []string{"--setenv", e.Key, e.Value})
	}
	plan.Groups = append(plan.Groups, []string{"--setenv", "TWIN_INSTANCE", i.ID})
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
	fmt.Fprintf(&b, "readonly STATE=%s\n\n", shHome(p.Home, p.StateDir(i.ID)))
	b.WriteString(failHelpers)

	// Pure-identity wrap: no sandbox, the program runs untouched. Saying so
	// explicitly beats emitting a bubblewrap call that does nothing.
	if plan.Bwrap == "" {
		writeExec(&b,
			fmt.Sprintf("%s%s \"$@\"", shHome(p.Home, plan.Prog), progArgs(plan.ProgArgs)),
			Sh("无法启动程序"), shHome(p.Home, plan.Prog))
		return []byte(b.String()), nil
	}

	// Re-entry guard, before any mount-point work.
	//
	// A wrapped program can start itself again: an integrated terminal running
	// its own CLI, a shell alias pointing at this very launcher. That second
	// run is already inside the namespace this script built, so every mount it
	// is about to make is there — and the overlay is not merely redundant, it
	// is refused: bwrap asks the kernel to overlay the same directory a second
	// time, and with the alias mount inside it the answer is EINVAL (measured;
	// the same nesting without the alias succeeds). Skipping to the alias is
	// therefore the right semantics, not a workaround: this process already
	// has this instance's root, mappings and variables, and a second sandbox
	// would only lay the same ones down again.
	//
	// Two facts, both read at launch. TWIN_INSTANCE is what Build sets with
	// --setenv (it is not a key of the definition any more — see inst.Env).
	// The alias path is the second half: it exists only inside the namespace
	// (install never writes it to the host), which is what keeps an
	// `export TWIN_INSTANCE=<id>` in somebody's shell profile from quietly
	// running the program with no sandbox at all.
	fmt.Fprintf(&b, "# 已经在自己的沙箱里（TWIN_INSTANCE 与别名都在）：不再套一层 bwrap，直接跑别名。\n")
	fmt.Fprintf(&b, "if [ \"${TWIN_INSTANCE:-}\" = %s ] && [ -e %s ]; then\n", Sh(i.ID), shHome(p.Home, plan.Prog))
	writeExec(&b,
		fmt.Sprintf("%s%s \"$@\"", shHome(p.Home, plan.Prog), progArgs(plan.ProgArgs)),
		Sh("无法启动程序"), shHome(p.Home, plan.Prog))
	b.WriteString("fi\n\n")

	// The other instance's sandbox.
	//
	// TWIN_INSTANCE carries a value that is not this instance: the launch was
	// started inside **someone else's** namespace (a wrapped program exec'ing
	// another instance's launcher, or a shell inside that sandbox running it).
	// Starting bwrap there is not "one layer more", it is the *wrong* layer —
	// the new sandbox's root is the parent's root, so the parent's overlay, its
	// private /tmp and its private data directories are all inside it, while
	// this instance's own alias is not (it exists only in its own namespace).
	// The program would then start from its real path, the window's app_id would
	// fall back to the original, and the identity would be lost silently.
	//
	// Getting out can only be done from **outside**: a process that has entered
	// a mount namespace has no permission to setns back into another one (that
	// needs CAP_SYS_ADMIN in the host userns), so every "jump out by ourselves"
	// spelling is an illusion. The host's systemd user manager is always
	// outside, so the launch is handed to it: it execs this launcher ($0) inside
	// the host's namespaces, and the script comes back in through the normal
	// branch above. TWIN_INSTANCE is cleared explicitly — the manager's
	// environment does not carry it today, but "not today" is not a fact to rely
	// on (`systemctl --user import-environment` can move anything there, and
	// arriving with it set is an endless hand-back).
	//
	// The test is "has a value", not "is really inside a twin sandbox": an
	// `export TWIN_INSTANCE=…` in somebody's shell profile lands here too, and it
	// still launches once handed back (paying for --pipe with a pipe instead of
	// this terminal) — which beats quietly dropping one layer of sandbox.
	//
	// level=none is not part of this: it builds no sandbox, so there is no second
	// layer to stack and no alias to lose (see the branch above).
	b.WriteString("# 在别人的沙箱里（TWIN_INSTANCE 有值、却不是这个实例）：交给宿主的 systemd 重跑这一份。\n")
	b.WriteString("if [ -n \"${TWIN_INSTANCE:-}\" ]; then\n")
	// Both preconditions are facts of this moment (a binary on PATH, a socket on
	// disk), so they are decided in the script: when either is missing, "cannot
	// hand back" has to be said right here — falling back to nesting a
	// wrong-layered sandbox is the one thing this branch must not do.
	b.WriteString("  if ! command -v systemd-run >/dev/null 2>&1; then\n")
	b.WriteString("    printf 'twin %s：启动失败 —— 这次启动来自实例 %s 的沙箱，要交回宿主重跑，但这台机器上没有 systemd-run。\\n' \"$ID\" \"$TWIN_INSTANCE\" >&2\n")
	b.WriteString("    twin_fail 3 \"在实例 $TWIN_INSTANCE 的沙箱里，找不到 systemd-run：请在宿主上直接启动 $ID\"\n")
	b.WriteString("  fi\n")
	// At least one of systemd-run --user's three doors has to be there:
	// $XDG_RUNTIME_DIR/systemd/private, $DBUS_SESSION_BUS_ADDRESS, or
	// $XDG_RUNTIME_DIR/bus. Left to itself it prints one English line about the
	// user scope bus — and a desktop launch drops that into the report file,
	// where nobody reads it.
	b.WriteString("  if [ -z \"${DBUS_SESSION_BUS_ADDRESS:-}\" ] &&\n")
	b.WriteString("     [ ! -S \"${XDG_RUNTIME_DIR:-/nonexistent}/systemd/private\" ] &&\n")
	b.WriteString("     [ ! -S \"${XDG_RUNTIME_DIR:-/nonexistent}/bus\" ]; then\n")
	b.WriteString("    printf 'twin %s：启动失败 —— 这次启动来自实例 %s 的沙箱，要交回宿主重跑，但这里够不着宿主的用户管理器。\\n' \"$ID\" \"$TWIN_INSTANCE\" >&2\n")
	b.WriteString("    twin_fail 3 \"在实例 $TWIN_INSTANCE 的沙箱里够不着宿主（/run/user 被遮掉，或会话总线不在）：请在宿主上直接启动 $ID\"\n")
	b.WriteString("  fi\n")
	// $0 is *this* copy — somebody may have copied the launcher elsewhere, and
	// what they want re-run is the one in their hand — while the far side
	// resolves a relative path against the host's cwd. So make it absolute.
	b.WriteString("  self=$(readlink -f -- \"$0\" 2>/dev/null) || self=$0\n")
	// The exit code is passed through: what runs over there is the program itself
	// (bwrap included), so a non-zero status is the **program's**, not "could not
	// hand back". The two real failures are the checks above, and those are the
	// ones that can name the step a human has to take.
	b.WriteString("  code=0\n")
	b.WriteString("  systemd-run --user --quiet --collect --wait --pipe \\\n")
	b.WriteString("    --description=\"twin $ID\" --setenv=TWIN_INSTANCE= \\\n")
	b.WriteString("    -- \"$self\" \"$@\" || code=$?\n")
	b.WriteString("  exit \"$code\"\n")
	b.WriteString("fi\n\n")

	// Every mount point, both ends of every mapping, has to exist on the host
	// before bwrap runs, and in the shape the mapping has: bwrap refuses to put a
	// file on a directory (`Can't create file at <目录>: Is a directory`) and
	// makes what it is missing itself as a directory — not what a mapping whose
	// end is a config file asked for. They are created even under a writable
	// root, which is today's base: the shape is the reason, not the permission.
	//
	// The shape is a fact about the disk at *launch* time, so it is decided in
	// the script rather than while rendering: `mkdir -p` on a file exits non-zero
	// ("Already exists"), which under `set -e` kills the launcher before exec.
	// install's half of the same rule is mountKind/EnsureMountPoints below; the
	// two must change together.
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
		// The root mount has nothing to create: both of its ends are `/`, which
		// is already there. A `mount_pair / /` line in every launcher would only
		// be a line that cannot fail.
		if bind.IsRootMount() {
			continue
		}
		// `:ro` travels as the third argument: the **source** of a read-only
		// mapping is somebody else's path, and manufacturing one would turn
		// "you gave the wrong path" into an empty mount that looks like success.
		ro := ""
		if bind.ReadOnly {
			ro = " " + mountRO
		}
		// A `:try` mapping goes through mount_try, which does nothing at all
		// when the source is missing — bwrap will skip that mount itself, so
		// there is nothing for twin to prepare. Creating it here would undo the
		// suffix: the source's absence is the whole thing it is about.
		helper := "mount_pair"
		if bind.Try {
			helper = "mount_try"
		}
		fmt.Fprintf(&b, "%s %s %s%s || twin_fail \"$?\" %s\n", helper,
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
	fmt.Fprintf(&cmd, "  %s%s \"$@\"", shHome(p.Home, plan.Prog), progArgs(plan.ProgArgs))

	// Which one of them exec fails on is the first word of the command, and
	// that is the one worth naming — the rest of the line is already printed.
	if plan.Trampoline != "" {
		writeExec(&b, cmd.String(), Sh("无法启动跳板程序"), shHome(p.Home, plan.Trampoline))
	} else {
		writeExec(&b, cmd.String(), Sh("无法启动 bwrap"), shHome(p.Home, plan.Bwrap))
	}
	return []byte(b.String()), nil
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
	b.WriteString(`# exec 这一条没有"下一条命令"能接住：exec 成功时这个 shell 已经不在，失败时非交互 shell
# 会直接退出（exec 这个内建命令自己的规矩，与 set -e 无关）。所以要显式关掉这两件事 ——
# 否则"程序根本没启起来"在图形会话里就是那句"点了没反应"。走到 twin_fail 那一行，
# 就意味着上面这条命令没能执行。
shopt -s execfail
set +e
`)
	fmt.Fprintf(b, "exec %s 2>&3\n", cmd)
	fmt.Fprintf(b, "twin_fail \"$?\" %s %s\n", what, path)
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
//
// A `:try` mapping creates nothing at all, either — the same rule the launcher
// follows through mount_try: an absent source is what that suffix is for, and
// manufacturing one here would make the mount unconditional from then on.
func EnsureMountPoints(i *inst.Instance, p inst.Paths) error {
	for _, b := range i.Binds {
		// The root's two ends are `/` — nothing to create, and `EnsureDir("/")`
		// here would be a call whose only possible outcome is "already there"
		// (the launcher skips it too, see RenderLauncher).
		if b.IsRootMount() {
			continue
		}
		if b.ReadOnly {
			continue
		}
		if b.Try {
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

# 带 :try 的那条映射：源不在就整条跳过 —— 两边都不建。bwrap 自己会用 --bind-try 跳过
# 这一条挂载，所以"什么都没挂"是这一趟的正常结果，不是失败。在这里顺手把源造出来的话
# 这个后缀就白写了：源不存在正是它的全部内容。
mount_try() {  # <源> <目标> [ro]
  [ -e "$1" ] || return 0
  mount_pair "$@"
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
    printf 'twin %s：启动失败（退出码 %s）\n\n哪一步没成：%s\n' "$ID" "$code" "$step"
    if [ -n "$captured" ]; then
      printf '\n──── 这次启动的输出 ────\n%s\n' "$captured"
    fi
    printf '\n启动器：%s\n报告文件：%s\n下一步：twin doctor 看这台机器，twin verify %s 看这个实例。\n' \
      "$0" "$LOG" "$ID"
  )
  printf '%s\n' "$report" > "$LOG"
  printf '%s' "$report" | twin_dialog "twin $ID：启动失败"
  printf 'twin %s：启动失败，报告在 %s\n' "$ID" "$LOG" >&2
  exit "$code"
}

`

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
