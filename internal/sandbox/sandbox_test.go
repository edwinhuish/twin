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
		{"/home/edwin2/x", `'/home/edwin2/x'`},
		{"/opt/wechat/wechat2", `'/opt/wechat/wechat2'`},
		{"/opt/wechat", `'/opt/wechat'`},
	}
	for _, c := range cases {
		if got := shHome(home, c.in); got != c.want {
			t.Errorf("shHome(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestShHomeWithoutHomeQuotesNormally(t *testing.T) {
	if got := shHome("", "/x/y"); got != `'/x/y'` {
		t.Errorf("shHome = %s", got)
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
		`mount_pair "$HOME/.local/state/twin/wechat2/xwechat" "$HOME/.xwechat"` + "\n",
		`mount_pair "$HOME/Documents/xwechat_files" "$HOME/Documents/xwechat_files"` + "\n",
		`mount_pair '/usr/share/wechat/theme.conf' "$HOME/.config/wechat/theme.conf" ro` + "\n",
		// The state root is a mount point like any other, and a file end needs
		// the helper that can create one.
		`mount_point dir "$HOME/.local/state/twin/wechat2"` + "\n",
		`touch -- "$2"`,
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
}

// ---------------------------------------------------------------- passthrough

func passthroughInstance(extra ...string) *inst.Instance {
	return &inst.Instance{
		ID:         "wechat2",
		Name:       "微信2",
		Level:      inst.LevelAlias,
		BadgeStyle: "corner",
		Target:     "/opt/wechat/wechat",
		Alias:      "wechat2",
		Bwrap:      extra,
	}
}

// The passthrough must land after the base and before the program — that
// placement is exactly what gives it the final word on a shared mount target.
func TestBuildAppendsPassthroughLast(t *testing.T) {
	i := passthroughInstance("--unshare-pid", "--setenv", "TWIN_FOO", "bar")
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	last := plan.Groups[len(plan.Groups)-1]
	want := []string{"--unshare-pid", "--setenv", "TWIN_FOO", "bar"}
	if strings.Join(last, " ") != strings.Join(want, " ") {
		t.Fatalf("passthrough is not the last group: %q", last)
	}
	// ...immediately after --die-with-parent, i.e. after the whole base.
	prev := plan.Groups[len(plan.Groups)-2]
	if len(prev) != 1 || prev[0] != "--die-with-parent" {
		t.Fatalf("passthrough is not directly after the base: %q", prev)
	}
	// The argv must carry the separator twin owns.
	argv := plan.Argv()
	if argv[len(argv)-2] != "--" {
		t.Fatalf("argv is missing the program separator: %q", argv)
	}
}

// Without a sandbox there is no bwrap to separate from, so no `--` either.
func TestBuildNoSeparatorWithoutBwrap(t *testing.T) {
	i := &inst.Instance{ID: "app2", Level: inst.LevelNone, Target: "/opt/app/app"}
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Bwrap != "" {
		t.Fatalf("level=none must not resolve bwrap: %q", plan.Bwrap)
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

func TestRenderLauncherCarriesPassthroughAndSeparator(t *testing.T) {
	i := passthroughInstance("--unshare-pid")
	i.Binds = []inst.Bind{{Source: "~/.local/state/twin/wechat2/xwechat", Target: "~/.xwechat"}}
	plan, err := Build(i, inst.Paths{Home: "/home/edwin"}, Runtime{Bwrap: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderLauncher(i, inst.Paths{Home: "/home/edwin"}, plan)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "'--unshare-pid' \\\n") {
		t.Errorf("launcher dropped the passthrough token:\n%s", got)
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
