package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/generate"
	"github.com/edwinhuish/twin/internal/inst"
)

// The documented form is `twin gen <source> --as <id>`: the source comes
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
		fs := flag.NewFlagSet("gen", flag.ContinueOnError)
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

// ---------------------------------------------------------------- two commands

// A missing configuration is the first thing a new user runs into, so the error
// has to name the command that writes one. "config.yml: no such file" is true
// and useless.
func TestMissingConfigNamesTheCommandThatWritesOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), DefaultConfigName)
	_, err := loadConfigFile(missing)
	if err == nil {
		t.Fatal("读一份不存在的配置必须报错")
	}
	if !strings.Contains(err.Error(), "twin gen") {
		t.Errorf("报错没有告诉用户下一步敲什么：%v", err)
	}
}

// The configuration is the user's working copy, so `twin gen` refuses to write
// over one that is already there: replacing it silently would throw away the
// edits that are the only reason the file exists.
func TestGenRefusesToClobberAWorkingCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultConfigName)
	if err := writeConfigFile(path, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(path, []byte("second"), false); err == nil {
		t.Fatal("已存在的配置必须拒绝覆盖")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "first" {
		t.Errorf("拒绝之后文件却被改了：%q %v", got, err)
	}
	if err := writeConfigFile(path, []byte("second"), true); err != nil {
		t.Fatalf("--force 应当允许覆盖：%v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "second" {
		t.Errorf("--force 之后内容没更新：%q %v", got, err)
	}
}

// `-o` and `--output` are two names for one setting, and the default has to
// lose to either. The rule lives in one function so it can be tested in one
// place — a flag package with no aliases makes this the kind of thing that
// otherwise gets decided independently at each use.
func TestOneSettingBeatsTheDefault(t *testing.T) {
	if got := firstNonEmpty("out.yml", "", DefaultConfigName); got != "out.yml" {
		t.Errorf("长选项应当胜过默认值：%q", got)
	}
	if got := firstNonEmpty("", "out.yml", DefaultConfigName); got != "out.yml" {
		t.Errorf("短选项应当胜过默认值：%q", got)
	}
	if got := firstNonEmpty("", "", DefaultConfigName); got != DefaultConfigName {
		t.Errorf("都没给时才落到默认值：%q", got)
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
	fix := mapFix{file: DefaultConfigName, replay: installCommandFor(DefaultConfigName)}

	short := captureStdout(t, func() { printPathMaps(p, it, insp, "", fix, false) })
	if strings.Contains(short, "照抄下面这份") {
		t.Errorf("默认输出不该带规则模板：\n%s", short)
	}
	if !strings.Contains(short, "twin rules --init fakeapp") {
		t.Errorf("默认输出必须点出生成草稿的路子：\n%s", short)
	}

	long := captureStdout(t, func() { printPathMaps(p, it, insp, "", fix, true) })
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
