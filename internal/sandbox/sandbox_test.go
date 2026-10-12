package sandbox

import (
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/inst"
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

// An in-place rw upgrade has the same path on both ends, so it must not be
// mkdir'd twice.
func TestUniqueDirsKeepsFirstOccurrence(t *testing.T) {
	got := uniqueDirs([]string{"/a", "/b"}, []string{"/b", "/c", "/a"})
	want := []string{"/a", "/b", "/c"}
	if len(got) != len(want) {
		t.Fatalf("uniqueDirs = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uniqueDirs = %q, want %q", got, want)
		}
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
	i.Binds = []inst.Bind{{Canonical: "~/.xwechat", Private: "~/.local/state/twin/wechat2/xwechat"}}
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
	if !strings.Contains(joined, "--map") {
		t.Errorf("the warning does not point at the supported route: %q", warns)
	}
}
