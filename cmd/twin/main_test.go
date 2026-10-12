package main

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/generate"
	"github.com/edwinhuish/twin/internal/inst"
)

// The documented form is `twin new <source> --as <id>`: the source comes
// first. Go's flag package stops at the first non-flag token, which used to
// swallow every flag placed after the source — --as, --name, --badge, and even
// --dry-run all vanished without a word. parseFL must see through that.
func TestParseFLKeepsFlagsAfterPositional(t *testing.T) {
	cases := [][]string{
		{"wechat", "--as", "wechat2", "--badge", "2"},
		{"--as", "wechat2", "wechat", "--badge", "2"},
		{"--as", "wechat2", "--badge", "2", "wechat"},
		{"--as=wechat2", "wechat", "--badge=2"},
	}
	for _, args := range cases {
		fs := flag.NewFlagSet("new", flag.ContinueOnError)
		as := fs.String("as", "", "")
		badge := fs.String("badge", "", "")
		pos, err := parseFL(fs, args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(pos) != 1 || pos[0] != "wechat" {
			t.Errorf("%v: positional = %q, want [wechat]", args, pos)
		}
		if *as != "wechat2" {
			t.Errorf("%v: --as = %q, want wechat2", args, *as)
		}
		if *badge != "2" {
			t.Errorf("%v: --badge = %q, want 2", args, *badge)
		}
	}
}

func TestParseFLKeepsBoolAfterPositional(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "")
	pos, err := parseFL(fs, []string{"wechat", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 1 || pos[0] != "wechat" {
		t.Fatalf("positional = %q", pos)
	}
	if !*dry {
		t.Fatal("--dry-run placed after the positional was dropped")
	}
}

func TestParseFLRejectsUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	if _, err := parseFL(fs, []string{"wechat", "--nope"}); err == nil {
		t.Fatal("an unknown flag must be an error, not silently ignored")
	}
}

// ---------------------------------------------------------------- `--`

func TestSplitPassthroughWithoutSeparator(t *testing.T) {
	head, tail, err := splitPassthrough([]string{"wechat", "--as", "wechat2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(head, " ") != "wechat --as wechat2" {
		t.Errorf("head = %q", head)
	}
	if len(tail) != 0 {
		t.Errorf("tail = %q, want empty", tail)
	}
}

func TestSplitPassthroughKeepsTokensVerbatim(t *testing.T) {
	head, tail, err := splitPassthrough([]string{
		"wechat", "--as", "wechat2", "--", "--unshare-pid", "--setenv", "TWIN_FOO", "bar",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 3 || head[0] != "wechat" {
		t.Errorf("head = %q", head)
	}
	want := "--unshare-pid --setenv TWIN_FOO bar"
	if strings.Join(tail, " ") != want {
		t.Errorf("tail = %q, want %q", tail, want)
	}
}

func TestSplitPassthroughEmptyTail(t *testing.T) {
	head, tail, err := splitPassthrough([]string{"wechat", "--"})
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 1 || head[0] != "wechat" || len(tail) != 0 {
		t.Fatalf("head=%q tail=%q", head, tail)
	}
}

// `twin -- wechat` is the POSIX "end of options" reading, which is not what
// twin means by `--`. Catching it early beats forwarding a stray source.
func TestSplitPassthroughRefusesNonDashHead(t *testing.T) {
	if _, _, err := splitPassthrough([]string{"--", "wechat"}); err == nil {
		t.Fatal("a passthrough region that starts with a non-option must be refused")
	}
}

// A `--` inside the passthrough would end bwrap's option parsing and swallow
// every following token as the program name.
func TestSplitPassthroughRefusesSecondSeparator(t *testing.T) {
	if _, _, err := splitPassthrough([]string{"wechat", "--", "--unshare-pid", "--", "--foo"}); err == nil {
		t.Fatal("a `--` inside the passthrough region must be refused")
	}
}

// ---------------------------------------------------------------- two-phase wrap

func snap(id string, published bool) generate.Snapshot {
	return generate.Snapshot{
		Instance:  &inst.Instance{ID: id},
		Path:      "/home/someone/.local/share/twin/" + id + "/instance.toml",
		Published: published,
	}
}

// `twin new wechat --publish` has to find the configuration `twin new wechat`
// just wrote. Re-deriving the id cannot do it — the snapshot itself makes that
// id taken — so the answer comes from what is on disk, and only an unambiguous
// answer is acted on.
func TestPublishTarget(t *testing.T) {
	cases := []struct {
		name    string
		snaps   []generate.Snapshot
		as      string
		want    string
		wantErr bool
	}{
		{"--as 指名就是那个", []generate.Snapshot{snap("wechat2", true), snap("wechat3", false)}, "wechat3", "wechat3", false},
		{"唯一待落盘的那一份", []generate.Snapshot{snap("wechat2", true), snap("wechat3", false)}, "", "wechat3", false},
		{"一份待落盘的都没有 ⇒ 一步到位", []generate.Snapshot{snap("wechat2", true)}, "", "", false},
		{"一个实例都没有 ⇒ 一步到位", nil, "", "", false},
		{"待落盘不止一份 ⇒ 拒绝猜", []generate.Snapshot{snap("a", false), snap("b", false)}, "", "", true},
	}
	for _, c := range cases {
		got, err := publishTarget(c.snaps, c.as)
		if (err != nil) != c.wantErr {
			t.Errorf("%s：err = %v，期望出错=%v", c.name, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("%s：id = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// The configuring half must not stack a second configuration behind the user's
// back, and must never rewrite the file the user is editing — that file's edits
// are the entire reason it exists.
func TestConfigureTarget(t *testing.T) {
	cases := []struct {
		name         string
		snaps        []generate.Snapshot
		as           string
		wantID       string
		wantPrepared bool
		wantErr      bool
	}{
		{"--as 说出口了就照办", []generate.Snapshot{snap("wechat2", false)}, "wechat9", "wechat9", false, false},
		{"已有一份待落盘的 ⇒ 报告它", []generate.Snapshot{snap("wechat2", false), snap("wechat1", true)}, "", "wechat2", true, false},
		{"没有待落盘的 ⇒ 写一份新的", []generate.Snapshot{snap("wechat2", true)}, "", "", false, false},
		{"待落盘不止一份 ⇒ 拒绝猜", []generate.Snapshot{snap("a", false), snap("b", false)}, "", "", false, true},
	}
	for _, c := range cases {
		id, prepared, err := configureTarget(c.snaps, c.as)
		if (err != nil) != c.wantErr {
			t.Errorf("%s：err = %v，期望出错=%v", c.name, err, c.wantErr)
			continue
		}
		if id != c.wantID || prepared != c.wantPrepared {
			t.Errorf("%s：id = %q prepared = %v，期望 %q / %v", c.name, id, prepared, c.wantID, c.wantPrepared)
		}
	}
}

// Only flags that describe the *instance* block a publish from an existing
// snapshot. --as selects which instance, and --force/--dry-run/--explain/
// --publish describe the act — refusing those would refuse ordinary commands.
func TestDerivationInputsLeavesTheInstallFlagsAlone(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("name", "", "")
	fs.String("map", "", "")
	fs.Bool("force", false, "")
	fs.Bool("dry-run", false, "")
	fs.Bool("explain", false, "")
	fs.Bool("publish", false, "")
	fs.String("as", "", "")
	if err := fs.Parse([]string{"--as", "wechat2", "--force", "--dry-run", "--name", "微信2"}); err != nil {
		t.Fatal(err)
	}
	got := derivationInputs(fs, nil)
	if len(got) != 1 || got[0] != "--name" {
		t.Errorf("derivationInputs = %v，期望只有 --name", got)
	}
	// The `--` region is a decision about the instance too, even though it is
	// not a flag and flag.Visit can never see it.
	if got := derivationInputs(fs, []string{"--unshare-pid"}); len(got) != 2 {
		t.Errorf("`--` 透传区必须算作描述实例的输入：%v", got)
	}
}

// ---------------------------------------------------------------- path maps

// The rules.d template is meant to be pasted, and a rules.d file serves every
// instance of a program. An id baked into the printed paths would make the
// pasted copy serve exactly one instance — which is what the {id} placeholder
// exists to prevent.
func TestRuleTemplateUnbakesInstanceID(t *testing.T) {
	p := inst.Paths{Home: "/home/someone"}
	it := &inst.Instance{ID: "wechat2"}
	if got := dedupStateDir(p, it, "~/.local/state/twin/wechat2/xwechat"); got != "~/.local/state/twin/{id}/xwechat" {
		t.Errorf("dedupStateDir = %q, want ~/.local/state/twin/{id}/xwechat", got)
	}
	// A shared directory must survive untouched: rewriting an unrelated path
	// would turn "shared" into "private" in the template the user copies.
	if got := dedupStateDir(p, it, "~/Documents/xwechat_files"); got != "~/Documents/xwechat_files" {
		t.Errorf("dedupStateDir rewrote an unrelated path: %q", got)
	}
}

// A wrap report has to be readable in one screen: the paths, the maps, the
// self-check, and the one thing left to do. The paste-ready rules.d template is
// the single longest block in it and the least urgent, so it belongs behind
// --explain — but the pointer to `twin rules --init` has to stay, otherwise the
// short form stops saying what to type next.
func TestPathMapsShortFormDropsTheTemplate(t *testing.T) {
	p := inst.Paths{Home: "/nonexistent-twin-home"}
	it := &inst.Instance{ID: "demo"}
	insp := &generate.Inspection{SourceDesktop: "/usr/share/applications/fakeapp.desktop"}

	short := captureStdout(t, func() { printPathMaps(p, it, insp, "", false) })
	if strings.Contains(short, "照抄下面这份") {
		t.Errorf("默认输出不该带规则模板：\n%s", short)
	}
	if !strings.Contains(short, "twin rules --init fakeapp") {
		t.Errorf("默认输出必须点出生成草稿的路子：\n%s", short)
	}

	long := captureStdout(t, func() { printPathMaps(p, it, insp, "", true) })
	if !strings.Contains(long, "照抄下面这份") {
		t.Errorf("--explain 输出应当带规则模板：\n%s", long)
	}
}

// captureStdout runs fn with stdout redirected into a pipe. Colour is decided
// once, for the process's real stdout, so a captured buffer is always plain
// text — which is what the assertions above rely on.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	return string(out)
}

// The file name offered to the user is what twin will read back next time, so
// it has to be the key the rule was found under — the desktop basename is only
// the fallback for a program that has no rule yet.
func TestRuleKeyPrefersTheMatchedKey(t *testing.T) {
	insp := &generate.Inspection{
		RuleKey:       "xwechat",
		SourceDesktop: "/usr/share/applications/wechat.desktop",
	}
	if got := ruleKeyFor(insp); got != "xwechat" {
		t.Errorf("ruleKeyFor = %q, want xwechat", got)
	}
	bare := &generate.Inspection{SourceDesktop: "/usr/share/applications/wechat.desktop"}
	if got := ruleKeyFor(bare); got != "wechat" {
		t.Errorf("ruleKeyFor fallback = %q, want wechat", got)
	}
}
