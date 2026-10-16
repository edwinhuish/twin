package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
)

// The launcher has to follow the user, not the machine the instance was
// generated on: everything under HOME must come out as "$HOME/...". The
// boundary matters — a sibling directory like /home/edwin2 is a different home.
func TestShHomeCollapsesHome(t *testing.T) {
	const home = "/home/edwin"
	cases := []struct{ in, want string }{
		{"/home/edwin", `"$HOME"`},
		{"/home/edwin/.xwechat", `"$HOME/.xwechat"`},
		{"/home/edwin/.local/state/twin/wechat2/xwechat", `"$HOME/.local/state/twin/wechat2/xwechat"`},
		{"/home/edwin2/x", `/home/edwin2/x`},
		{"/opt/wechat/wechat2", `/opt/wechat/wechat2`},
		{"/opt/wechat", `/opt/wechat`},
	}
	for _, c := range cases {
		if got := shHome(home, c.in); got != c.want {
			t.Errorf("shHome(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestShHomeWithoutHomeLeavesTheTokenAlone(t *testing.T) {
	if got := shHome("", "/x/y"); got != `/x/y` {
		t.Errorf("shHome = %s", got)
	}
}

// A launcher nobody can read is a launcher nobody fixes: the quotes are for the
// tokens a shell would read as something else, and every other token is written
// the way it will be handed to bwrap.
func TestShQuotesOnlyWhatTheShellWouldReadDifferently(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/usr/bin/bwrap", "/usr/bin/bwrap"},
		{"--dev-bind", "--dev-bind"},
		{"/", "/"},
		{"微信2", "微信2"},
		{"PATH=/a:/b", "PATH=/a:/b"},
		// The ones the quotes exist for: a space would split one argument into
		// two, and everything else here would be expanded or re-read.
		{"", `''`},
		{"/home/a b/x", `'/home/a b/x'`},
		{"it's", `'it'\''s'`},
		{"$HOME/x", `'$HOME/x'`},
		{"a b", `'a b'`},
		{"{a,b}", `'{a,b}'`},
		{"*.png", `'*.png'`},
		{"x#y", `'x#y'`},
	}
	for _, c := range cases {
		if got := Sh(c.in); got != c.want {
			t.Errorf("Sh(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// A quote in a path must not break out of the surrounding double quotes.
func TestShHomeEscapesQuotes(t *testing.T) {
	if got := shHome("/h", `/h/.x"y`); got != `"$HOME/.x\"y"` {
		t.Errorf("shHome = %s", got)
	}
}

// ------------------------------------------------------------- mount points

func mountInstance() *inst.Instance {
	i := passthroughInstance()
	i.Binds = []inst.Bind{
		{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"},
		{Source: "~/Documents/xwechat_files", Target: "~/Documents/xwechat_files"},
		{Source: "/usr/share/wechat/theme.conf", Target: "~/.config/wechat/theme.conf", ReadOnly: true},
		{Source: "/etc/wechat/optional.conf", Target: "~/.config/wechat/optional.conf", ReadOnly: true, Try: true},
	}
	return i
}

// One line per mapping, both ends on it: the launcher has to be readable as the
// mapping list it is, and the in-place upgrade must render both ends equal
// rather than being special-cased away.
func TestLauncherRendersOneMountPairPerMapping(t *testing.T) {
	i := mountInstance()
	p := inst.Paths{Home: "/home/edwin"}
	plan, err := Build(i, p, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderLauncher(i, p, plan)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	for _, want := range []string{
		`mount_pair "$HOME/.local/state/twin/wechat2/xwechat" "$HOME/.xwechat" || twin_fail "$?" '建立挂载点失败：~/.local/state/twin/wechat2/xwechat → ~/.xwechat'` + "\n",
		`mount_pair "$HOME/Documents/xwechat_files" "$HOME/Documents/xwechat_files" || twin_fail "$?" '建立挂载点失败：~/Documents/xwechat_files → ~/Documents/xwechat_files'` + "\n",
		`mount_pair /usr/share/wechat/theme.conf "$HOME/.config/wechat/theme.conf" ro || twin_fail "$?"`,
		// The state root is a mount point like any other, and a file end needs
		// the helper that can create one. Its message keeps the quotes — it
		// says `~/…`, and a bare `~` at the start of a word expands — while the
		// mapping above lost them: quote a token only when it changes meaning
		// without them.
		`mount_point dir "$HOME/.local/state/twin/wechat2" || twin_fail "$?" '建立状态目录失败：~/.local/state/twin/wechat2'` + "\n",
		`touch -- "$2"`,
		// `:try` goes through the helper that does nothing when the source is
		// missing — bwrap skips that mount itself, so there is nothing to build
		// and nothing to complain about.
		`mount_try /etc/wechat/optional.conf "$HOME/.config/wechat/optional.conf" ro || twin_fail "$?"`,
		`[ -e "$1" ] || return 0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("launcher is missing %q:\n%s", want, got)
		}
	}
	// The old shape: a bare `mkdir -p` on a mapping path. On a *file* that exits
	// non-zero, and `set -e` then kills the launcher before exec.
	if strings.Contains(got, "mkdir -p \"$HOME/.xwechat\"") {
		t.Errorf("launcher still mkdirs mapping ends directly:\n%s", got)
	}
	// `set -e` is suspended inside a function called as the left side of `||`,
	// so without this line the first mount_point's failure would be swallowed
	// by the second one's success — and bwrap would be the one to complain.
	if !strings.Contains(got, `mount_point "$kind" "$1" || return 1`) {
		t.Errorf("mount_pair does not pass on its first failure:\n%s", got)
	}
}

// The shape comes from the disk, and from whichever end is on it. Nothing there
// means directory: an empty file would claim the configuration is empty.
func TestMountKindFollowsTheEndOnDisk(t *testing.T) {
	home := t.TempDir()
	p := inst.Paths{Home: home}

	file := filepath.Join(home, "cfg")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "d")
	if err := os.MkdirAll(filepath.Join(dir, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "nowhere")

	cases := []struct {
		name string
		bind inst.Bind
		want string
	}{
		{"都不在 ⇒ 目录", inst.Bind{Source: missing + "/a", Target: missing + "/b"}, mountDir},
		{"目标端是文件 ⇒ 文件", inst.Bind{Source: missing + "/a", Target: file}, mountFile},
		{"源端是文件 ⇒ 文件", inst.Bind{Source: file, Target: missing + "/b"}, mountFile},
		{"两端都是目录 ⇒ 目录", inst.Bind{Source: dir, Target: dir}, mountDir},
	}
	for _, c := range cases {
		if got := mountKind(p, c.bind); got != c.want {
			t.Errorf("%s：mountKind = %q, want %q", c.name, got, c.want)
		}
	}
}

// install must not create a directory where the mapping is a file: the launcher
// never touches a path that already exists, so a wrong-shaped private copy made
// here would survive every later launch.
func TestEnsureMountPointsCreatesFilesForFileMappings(t *testing.T) {
	home := t.TempDir()
	p := inst.Paths{Home: home}
	real := filepath.Join(home, "vlcrc") // the program's own config file
	if err := os.WriteFile(real, []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	i := &inst.Instance{ID: "vlc2", Binds: []inst.Bind{
		{Source: "~/.local/state/twin/vlc2/vlcrc", Target: "~/" + filepath.Base(real)},
		{Source: "~/.local/state/twin/vlc2/data", Target: "~/data"},
		{Source: "~/elsewhere", Target: "~/elsewhere2", ReadOnly: true},
		{Source: "~/.local/state/twin/vlc2/optional", Target: "~/optional", Try: true},
	}}
	if err := EnsureMountPoints(i, p); err != nil {
		t.Fatal(err)
	}

	private := filepath.Join(p.StateDir("vlc2"), "vlcrc")
	st, err := os.Stat(private)
	if err != nil {
		t.Fatalf("私有侧没建出来：%v", err)
	}
	if st.IsDir() {
		t.Errorf("文件型映射的私有侧被建成了目录：%s", private)
	}
	if !sysx.IsRegularFile(private) {
		t.Errorf("文件型映射的私有侧不是普通文件：%s", private)
	}
	// The program's own file is left exactly as it was.
	if body, err := os.ReadFile(real); err != nil || string(body) != "[core]\n" {
		t.Errorf("碰了程序自己的配置文件：%q %v", body, err)
	}

	if st, err := os.Stat(filepath.Join(p.StateDir("vlc2"), "data")); err != nil || !st.IsDir() {
		t.Errorf("目录型映射的私有侧应当是目录：%v %v", st, err)
	}
	// A `:ro` source is somebody else's path: twin must not manufacture it.
	if sysx.Exists(filepath.Join(home, "elsewhere")) {
		t.Error("只读映射的源不该被创建")
	}
	// `:try` 的私有侧同样不建 —— 源不在正是这个后缀的全部内容，在这里替它补齐
	// 会把"这一次没有这条映射"变成一个从此每次都有的空挂载。
	if sysx.Exists(filepath.Join(p.StateDir("vlc2"), "optional")) {
		t.Error(":try 映射的源不该被创建")
	}
}

// ---------------------------------------------------------------- option list

func passthroughInstance(extra ...string) *inst.Instance {
	// A real instance carries the base `gen` wrote into it — the root in
	// `bwrap.root` and the rest as the option lines of bwrap.extras, in the
	// order they reach the command. twin adds none of it at launch, so an
	// instance without it is one bwrap would start with nothing mounted.
	lines := []string{"--proc /proc", "--tmpfs /dev/shm", "--bind /tmp /tmp", "--die-with-parent"}
	return &inst.Instance{
		ID:              "wechat2",
		Level:           inst.LevelAlias,
		BadgeStyle:      "corner",
		Target:          "/opt/wechat/wechat",
		DesktopOverride: "./wechat2.desktop",
		Root:            "dev",
		Extras:          append(lines, extra...),
		Env:             []inst.EnvVar{{Key: "TWIN_INSTANCE", Value: "wechat2"}},
	}
}

// The base twin writes into the definition: the root as one word in
// `bwrap.root` (`dev` — which *is* `--dev-bind / /`), and everything else as
// the option lines `Plan` interleaves into bwrap.extras.
//
// Both branches of the /tmp decision have to leave /tmp *writable*, because a
// read-only /tmp is what a read-only root quietly produced — Chromium reports it
// as "[process_singleton_posix.cc] Failed to create socket directory", a message
// about a socket from a program the user only just wrapped. Which branch it is
// has to be visible in the file, so both spellings are asserted here rather than
// left to a launch-time decision.
func TestBaseIsWrittenByNode(t *testing.T) {
	for _, c := range []struct {
		name        string
		privateTmp  bool
		wantHostTmp string
	}{
		{"私有 /tmp", true, ""},
		{"宿主 /tmp", false, "/tmp"},
	} {
		b := BaseFor("wechat2", c.privateTmp)
		if b.Root != "dev" {
			t.Errorf("%s：root = %q，want dev（--dev-bind / /）", c.name, b.Root)
		}
		if got := strings.Join(b.Head, " "); got != "--proc /proc" {
			t.Errorf("%s：head = %q", c.name, got)
		}
		if len(b.Tmpfs) != 1 || b.Tmpfs[0] != "/dev/shm" {
			t.Errorf("%s：tmpfs = %v，want [/dev/shm]（私有 /tmp 那条是调用方自己列表里的一条）", c.name, b.Tmpfs)
		}
		if got := strings.Join(b.Tail, " "); got != "--die-with-parent" {
			t.Errorf("%s：tail = %q", c.name, got)
		}
		// The host's /tmp is the branch that has to be *written*: under a tmpfs
		// the caller's own list already carries /tmp, and what is owed is the
		// line that swaps it back — which is why it is a path here and a line in
		// extras after that list (see BaseFor).
		if b.HostTmp != c.wantHostTmp {
			t.Errorf("%s：hostTmp = %q，want %q", c.name, b.HostTmp, c.wantHostTmp)
		}
		if len(b.Env) != 1 || b.Env[0].Key != "TWIN_INSTANCE" || b.Env[0].Value != "wechat2" {
			t.Errorf("%s：env = %+v", c.name, b.Env)
		}
	}
}

// The root goes down before everything, and the definition's option lines come
// after every mount.
//
// That order is the whole reason Build emits the root on its own instead of
// taking it from wherever a mount happens to sit: bubblewrap applies mounts in
// order and the later one wins, so a root bound *after* the alias overlay puts
// the host's directory back over the copy the program is supposed to run from —
// hiding the alias path, and with it the program. The key exists so that this
// position is a fact about the mount rather than a rule about the file's order.
func TestBuildPutsTheRootBeforeEverything(t *testing.T) {
	i := passthroughInstance("--unshare-pid", "--setenv", "TWIN_FOO", "bar")
	i.Binds = []inst.Bind{{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"}}
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Groups[0], " "); got != "--dev-bind / /" {
		t.Fatalf("第一组应当是根挂载，得到 %q", got)
	}
	// The definition's option lines reach the command after every mount, in the
	// order the file writes them — one group per line, all of them.
	var rest []string
	for _, g := range plan.Groups[1:] {
		rest = append(rest, strings.Join(g, " "))
	}
	if joined, want := strings.Join(rest, "\n"), strings.Join(i.Extras, "\n"); !strings.Contains(joined, want) {
		t.Fatalf("定义里那份选项列表没有按原序出现在根挂载之后：\n%s\nwant %s", joined, want)
	}
	// Everything twin derives comes after the root, so a mapping can overrule a
	// base mount the way bwrap promises (the later mount wins) — and the root is
	// not repeated down there.
	for _, g := range plan.Groups[1:] {
		if len(g) == 0 {
			continue
		}
		if g[0] == "--dev-bind" {
			t.Fatalf("根挂载又被发了一遍，或者派生参数排在了它前面：%q", g)
		}
	}
	// The argv must carry the separator twin owns.
	argv := plan.Argv()
	if argv[len(argv)-2] != "--" {
		t.Fatalf("argv is missing the program separator: %q", argv)
	}
}

// 根挂载的三种写法都要发成对应的那条 bwrap 选项 —— `root` 的三个词就是 bind 的
// 三个模式，走的是同一份渲染（bindGroup），所以这里断言的是"一个词等于一条挂载"。
func TestBuildRendersEveryRootMode(t *testing.T) {
	for _, c := range []struct{ root, want string }{
		{"ro", "--ro-bind / /"},
		{"rw", "--bind / /"},
		{"dev", "--dev-bind / /"},
	} {
		i := passthroughInstance()
		i.Root = c.root
		plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
		if err != nil {
			t.Fatalf("root=%s: %v", c.root, err)
		}
		if got := strings.Join(plan.Groups[0], " "); got != c.want {
			t.Errorf("root=%s：第一组 = %q，want %q", c.root, got, c.want)
		}
	}
	// 空值不是"默认那个"：没有根挂载的定义由 Check 拦下（bwrap 会在一个什么都没挂
	// 的命名空间里启动程序），这里只确认它不会自己长出一条来。
	i := passthroughInstance()
	i.Root = ""
	i.Extras = nil
	if _, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"}); err == nil {
		t.Error("没有根挂载的定义必须被 Check 拦下，而不是由 Build 补一个")
	}
}

// 老定义把根挂载写在 bind 里（`/:/:dev`），Build 照旧认它 —— 而且在文件里排第几
// 都不影响它落在命令的第一位（新写法用 root 键，这一条只为不打断已装好的实例）。
func TestBuildStillReadsTheOldRootSpelling(t *testing.T) {
	i := passthroughInstance()
	i.Root = ""
	i.Binds = []inst.Bind{
		{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"},
		{Source: "/", Target: "/", Dev: true},
	}
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Groups[0], " "); got != "--dev-bind / /" {
		t.Fatalf("第一组应当是 bind 里那条根挂载，得到 %q", got)
	}
	seen := 0
	for _, g := range plan.Groups {
		if len(g) > 0 && g[0] == "--dev-bind" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("根挂载发了 %d 次，want 1 次", seen)
	}
}

// Without a sandbox there is no bwrap to separate from, so no `--` either.
func TestBuildNoSeparatorWithoutBwrap(t *testing.T) {
	i := &inst.Instance{ID: "app2", Level: inst.LevelNone, Target: "/opt/app/app",
		DesktopOverride: "./app2.desktop"}
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Bwrap != "" {
		t.Fatalf("level=none must not resolve bwrap: %q", plan.Bwrap)
	}
}

// Extra tmpfs lines are the definition's, and Build passes them through
// verbatim. /tmp is deliberately not among them: which /tmp this instance gets
// is a decision about the root the base binds, so it is written in the file
// where that decision is — twin adding one here would be exactly the invisible
// argument this design removed.
func TestBuildEmitsTheDefinitionsTmpfsButNeverInventsTmp(t *testing.T) {
	i := passthroughInstance()
	i.Extras = append(i.Extras, "--tmpfs /var/cache/wechat")
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(plan.Args(), " ")
	if !strings.Contains(args, "--tmpfs /var/cache/wechat") {
		t.Errorf("argv 里没有定义里那条 tmpfs：%s", args)
	}
	if strings.Contains(args, "--tmpfs /tmp") {
		t.Errorf("Build 自己补了 /tmp 的 tmpfs —— 那一条只该来自定义：%s", args)
	}
}

// The mode is the reason a mapping is not just two paths: `:ro` hands over
// somebody else's data, `:dev` lets a directory of device nodes through, and
// the bare form is the private copy everything else is built from. `:try` is a
// suffix on whichever of them was picked, and it is the only spelling that
// survives a source which is not there yet.
func TestBuildMapsEachModeToItsFlag(t *testing.T) {
	i := passthroughInstance()
	i.Binds = []inst.Bind{
		{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"},
		{Source: "/usr/share/wechat/theme.conf", Target: "~/.config/wechat/theme.conf", ReadOnly: true},
		{Source: "/dev/dri", Target: "/dev/dri", Dev: true},
		{Source: "~/.config/wechat/pending.conf", Target: "~/pending.conf", ReadOnly: true, Try: true},
		{Source: "~/.local/state/twin/wechat2/cache", Target: "~/.cache/wechat", Try: true},
	}
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(plan.Args(), " ")
	for _, want := range []string{
		"--bind /home/edwin/.local/state/twin/wechat2/xwechat /home/edwin/.xwechat",
		"--ro-bind /usr/share/wechat/theme.conf /home/edwin/.config/wechat/theme.conf",
		"--dev-bind /dev/dri /dev/dri",
		"--ro-bind-try /home/edwin/.config/wechat/pending.conf /home/edwin/pending.conf",
		"--bind-try /home/edwin/.local/state/twin/wechat2/cache /home/edwin/.cache/wechat",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("argv 里没有 %q：%s", want, args)
		}
	}
}

// 顺序：根挂载（root 键，永远第一位）→ 别名 overlay → 其余映射按文件顺序 →
// bwrap.extras 那几行（行序即命令序）→ setenv 收在最后。--remount-ro 之所以能排在
// 最后，是因为它只作用在已经存在的挂载上；基座那条"把宿主 /tmp 换回来"的 --bind 也是
// 靠这个位置压过前面那条 --tmpfs /tmp。
func TestBuildEmitsTheOptionLinesInFileOrder(t *testing.T) {
	i := passthroughInstance()
	i.Extras = []string{"--proc /proc", "--tmpfs /var/cache/wechat", "--die-with-parent"}
	i.Env = []inst.EnvVar{{Key: "TWIN_FOO", Value: "bar"}}
	i.Binds = []inst.Bind{
		{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"},
	}
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(plan.Args(), " ")
	// The paths are folded to absolute form on the way out; the relative order
	// is what this test is about.
	order := []string{
		"--dev-bind / /",
		"--overlay-src /opt/wechat",
		"--bind /home/edwin/.local/state/twin/wechat2/xwechat /home/edwin/.xwechat",
		"--proc /proc",
		"--tmpfs /var/cache/wechat",
		"--die-with-parent",
		"--setenv TWIN_FOO bar",
	}
	at := -1
	for _, want := range order {
		idx := strings.Index(argv, want)
		if idx < 0 {
			t.Fatalf("argv 里没有 %q：%s", want, argv)
		}
		if idx < at {
			t.Errorf("%q 排在它该在的位置之前：%s", want, argv)
		}
		at = idx
	}
}

// 留空就是"没要这个"，不能变成"要默认的那个"：默认的那几条写在定义里（root / bind /
// env / extras），是文件里看得见的一段文本，twin 不再自己补 —— /tmp 那条尤其不能再被凭空加回来。
func TestBuildAddsNoOptionOfItsOwn(t *testing.T) {
	i := passthroughInstance()
	i.Extras = nil
	i.Env = nil
	i.Binds = nil
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(plan.Args(), " ")
	// The root is the definition's, so it is the whole command; twin's own
	// additions are the alias overlay (identity, not an option) and nothing else.
	for _, unwanted := range []string{"--proc", "--tmpfs", "--remount-ro", "--setenv", "--bind /tmp /tmp"} {
		if strings.Contains(argv, unwanted) {
			t.Errorf("定义里没有 %s，Build 不该自己补上：%s", unwanted, argv)
		}
	}
}

// redirect is alias plus a rule file, so the namespace part must be identical:
// the rename is what leaves the alias path in mountinfo and argv[0], and that
// is the evidence the rule matches on.
func TestBuildRedirectRenamesLikeAlias(t *testing.T) {
	alias := passthroughInstance()
	redirected := passthroughInstance()
	redirected.Level = inst.LevelRedirect

	pa, err := Build(alias, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := Build(redirected, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	if pa.Prog != pr.Prog {
		t.Errorf("redirect 的入口应与 alias 相同：%q vs %q", pa.Prog, pr.Prog)
	}
	if strings.Join(pa.Args(), " ") != strings.Join(pr.Args(), " ") {
		t.Error("redirect 与 alias 的 bwrap 参数应当逐字相同")
	}
}

func TestRenderLauncherCarriesTheOptionListAndSeparator(t *testing.T) {
	i := passthroughInstance("--unshare-pid")
	i.Binds = append(i.Binds, inst.Bind{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"})
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderLauncher(i, inst.Paths{Home: "/home/edwin"}, plan)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{"  --dev-bind / / \\\n", "--unshare-pid"} {
		if !strings.Contains(got, want) {
			t.Errorf("launcher dropped %s from the option list:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "  -- \\\n") {
		t.Errorf("launcher is missing twin's own `--`:\n%s", got)
	}
	// A path under HOME in the passthrough still has to follow $HOME.
	if !strings.Contains(got, `"$HOME/.xwechat"`) {
		t.Errorf("launcher baked in the generation-time home:\n%s", got)
	}
}

func TestLintBwrapAcceptsKnownTokens(t *testing.T) {
	if w := LintBwrap([]string{"--unshare-pid", "--setenv", "TWIN_FOO", "bar"}); len(w) != 0 {
		t.Errorf("unexpected lint warnings: %q", w)
	}
}

// A typo must be called out with the intended name, not silently forwarded —
// bwrap would fail at launch, which is the worst place to find out.
func TestLintBwrapSuggestsNearestForTypo(t *testing.T) {
	warns := LintBwrap([]string{"--unshere-pid"})
	if len(warns) != 1 {
		t.Fatalf("warnings = %q, want exactly one", warns)
	}
	if !strings.Contains(warns[0], "--unshare-pid") {
		t.Errorf("no nearest-match hint in %q", warns[0])
	}
}

func TestLintBwrapWarnsOnBindInPassthrough(t *testing.T) {
	warns := LintBwrap([]string{"--ro-bind", "/a", "/b"})
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "--ro-bind") {
		t.Errorf("the offending option is not named: %q", warns)
	}
	if !strings.Contains(joined, "bind") {
		t.Errorf("the warning does not point at the supported route: %q", warns)
	}
}

// ------------------------------------------------------------- 启动失败报告

// launchSandbox prepares the three things a launcher needs in a test: a scratch
// HOME, a PATH with a fake dialog on it, and the file that fake records into.
//
// The dialog is faked rather than mocked away because the thing under test *is*
// a dialog: what matters is the text someone could select and copy out of it,
// not that a window opened.
func launchSandbox(t *testing.T) (home, bin, out string) {
	t.Helper()
	home, bin = t.TempDir(), t.TempDir()
	out = filepath.Join(bin, "zenity.out")
	fake := "#!/bin/bash\n{ printf 'ARGS: %s\\n' \"$*\"; cat; } > \"$TWIN_TEST_DIALOG_OUT\"\n"
	if err := os.WriteFile(filepath.Join(bin, "zenity"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, bin, out
}

// runLauncher renders a launcher and runs the file `install` would have
// written. Every stream is a pipe, which is the whole point: a desktop launch
// has no terminal either, so the dialog is the only channel left. The script
// path comes back because the report signs itself with it ($0).
func runLauncher(t *testing.T, i *inst.Instance, p inst.Paths, rt Runtime,
	home, bin, out string, display bool) (sysx.Result, string) {
	t.Helper()
	if sysx.Which("bash") == "" {
		t.Skip("没有 bash：这份启动器本来就以 bash 为宿主")
	}
	plan, err := Build(i, p, rt)
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderLauncher(i, p, plan)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "launch")
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"TWIN_TEST_DIALOG_OUT=" + out,
	}
	if display {
		// A graphical session, and nothing else: no terminal anywhere near it.
		env = append(env, "DISPLAY=:0.0")
	}
	res, err := sysx.RunEnv(env, script)
	if err != nil {
		t.Fatalf("启动器跑不起来：%v", err)
	}
	return res, script
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// blockerInstance is a mapping whose source cannot be manufactured: its parent
// is a regular file, so mount_point's mkdir fails before exec — the failure
// mode that used to look like a click that did nothing.
func blockerInstance(t *testing.T, home string) *inst.Instance {
	t.Helper()
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	i := passthroughInstance()
	i.Binds = []inst.Bind{{Source: blocker + "/sub", Target: "~/data"}}
	return i
}

// A graphical session has no terminal, so the failure has to open a window —
// and the text in it has to be selectable, because the only thing anyone does
// with it is paste it into a bug report.
func TestLauncherShowsTheFailureInAGuiSession(t *testing.T) {
	home, bin, out := launchSandbox(t)
	i := blockerInstance(t, home)
	p := inst.Paths{Home: home}
	res, script := runLauncher(t, i, p, Runtime{Bwrap: "/usr/bin/bwrap"}, home, bin, out, true)

	if res.Code != 1 {
		t.Fatalf("mount_point 失败应当以 mkdir 的退出码收场，得到 %d（stderr=%q）", res.Code, res.Stderr)
	}
	report := filepath.Join(home, ".local/state/twin/wechat2/launch-error.log")
	shown := readText(t, out)
	for _, want := range []string{
		"twin wechat2：启动失败（退出码 1）",
		"哪一步没成：建立挂载点失败：" + filepath.Join(home, "blocker", "sub") + " → ~/data",
		// The captured stderr is the "why": this is the line a user pastes.
		"mkdir:",
		"启动器：" + script,
		"报告文件：" + report,
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("弹窗里缺 %q：\n%s", want, shown)
		}
	}
	// One composition, two readers: the window got byte-for-byte what was
	// written down. The fake prefixes its own argv, hence dropping one line.
	_, body, _ := strings.Cut(shown, "\n")
	if body != strings.TrimSuffix(readText(t, report), "\n") {
		t.Errorf("弹窗正文与报告文件不一致：\n%q\n%q", body, readText(t, report))
	}
}

// exec failing is "the program never started", and it is the one step a failing
// command cannot report: a non-interactive shell exits on the spot when exec
// cannot run the command, whatever set -e says.
func TestLauncherReportsAProgramThatCannotRun(t *testing.T) {
	home, bin, out := launchSandbox(t)
	gone := filepath.Join(home, "gone", "wechat")
	i := &inst.Instance{ID: "wechat2", Level: inst.LevelNone, Target: gone,
		DesktopOverride: "./wechat2.desktop"}
	p := inst.Paths{Home: home}
	res, _ := runLauncher(t, i, p, Runtime{}, home, bin, out, true)

	if res.Code != 127 {
		t.Fatalf("exec 失败应当保持 127，得到 %d（stderr=%q）", res.Code, res.Stderr)
	}
	shown := readText(t, out)
	for _, want := range []string{
		"启动失败（退出码 127）",
		// twin_why reads the disk instead of guessing, which also proves the
		// path went through $HOME rather than being baked at generation time.
		"哪一步没成：无法启动程序：" + gone + "（这个文件不在了）",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("弹窗里缺 %q：\n%s", want, shown)
		}
	}
	// bash's own wording still lands on the real stderr — the journal keeps the
	//原文 for whoever goes looking afterwards.
	if !strings.Contains(res.Stderr, gone) {
		t.Errorf("真 stderr 上应当留着 bash 的说法：%q", res.Stderr)
	}
}

// A terminal launch must stay exactly as it was: the same error is already on
// screen, and a window on top of it would only cover it up.
func TestLauncherStaysQuietWithoutAGraphicalSession(t *testing.T) {
	home, bin, out := launchSandbox(t)
	i := blockerInstance(t, home)
	p := inst.Paths{Home: home}
	res, _ := runLauncher(t, i, p, Runtime{Bwrap: "/usr/bin/bwrap"}, home, bin, out, false)

	if res.Code != 1 {
		t.Fatalf("退出码应当仍是 mkdir 的，得到 %d", res.Code)
	}
	if sysx.Exists(out) {
		t.Error("没有图形会话时不该弹窗")
	}
	if sysx.Exists(filepath.Join(home, ".local/state/twin/wechat2/launch-error.log")) {
		t.Error("终端启动不该写报告文件")
	}
	if !strings.Contains(res.Stderr, "mkdir:") {
		t.Errorf("错误应当原样落在 stderr 上：%q", res.Stderr)
	}
}

// The dialog chain is picked for selectable text: xmessage's body is a Label
// nothing can be copied out of, and a notification is no better. A launcher
// that grew one of them would pass every other test here and still show the
// user a window they cannot copy from.
func TestLauncherOnlyOffersCopyableDialogs(t *testing.T) {
	i := passthroughInstance()
	p := inst.Paths{Home: "/home/edwin"}
	plan, err := Build(i, p, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderLauncher(i, p, plan)
	if err != nil {
		t.Fatal(err)
	}
	// Comments name the rejected tools on purpose, so the assertions look at
	// the lines that actually run something.
	var code strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code.WriteString(line + "\n")
		}
	}
	got := code.String()
	for _, want := range []string{
		`[ ! -t 0 ] && [ ! -t 2 ] && [ -n "${WAYLAND_DISPLAY:-}${DISPLAY:-}" ]`,
		"zenity --text-info",
		"--textbox /dev/stdin",
		"yad --text-info",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("启动器缺 %q：\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"xmessage", "notify-send"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("弹窗链里混进了复制不出来的 %s：\n%s", unwanted, got)
		}
	}
}

// twin never runs the launcher, so a syntax error in it would first surface at
// the one moment nobody is watching: a click.
func TestLauncherIsValidBash(t *testing.T) {
	if sysx.Which("bash") == "" {
		t.Skip("没有 bash：这份启动器本来就以 bash 为宿主")
	}
	home := t.TempDir()
	p := inst.Paths{Home: home}
	plain := &inst.Instance{ID: "app2", Level: inst.LevelNone, Target: "/opt/app/app",
		DesktopOverride: "./app2.desktop"}
	sandboxed := mountInstance()
	trampolined := mountInstance()
	trampolined.Binds = nil

	cases := []struct {
		name string
		i    *inst.Instance
		rt   Runtime
	}{
		{"纯身份", plain, Runtime{}},
		{"沙箱", sandboxed, Runtime{Bwrap: "/usr/bin/bwrap"}},
		{"跳板", trampolined, Runtime{
			Bwrap: "/usr/bin/bwrap", Copy: p.BwrapCopy(),
			Trampoline: "/opt/electron/electron", TrampolineEnv: []string{"ELECTRON_RUN_AS_NODE=1"},
			Bootstrap: p.Bootstrap(),
		}},
	}
	for _, c := range cases {
		plan, err := Build(c.i, p, c.rt)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		body, err := RenderLauncher(c.i, p, plan)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		script := filepath.Join(t.TempDir(), "launch")
		if err := os.WriteFile(script, body, 0o755); err != nil {
			t.Fatal(err)
		}
		if res, err := sysx.Run("bash", "-n", script); err != nil || res.Code != 0 {
			t.Errorf("%s：bash -n 不认这份启动器：%v %s", c.name, err, res.Stderr)
		}
	}
}
