package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/generate"
	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/style"
	"github.com/edwinhuish/twin/internal/verify"
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

// ---------------------------------------------------------------- usage errors

// A mistake gets one line. Go's flag package answers with the error *and* the
// whole flag list, which pushes the line that matters off the top of the
// terminal — and the flag list is what `twin --help` is for.
func TestUsageErrorPrintsTheMistakeWithoutTheFlagList(t *testing.T) {
	fs := newFlags("gen")
	fs.String("badge", "", "角标文字")
	_, err := parseFL(fs, []string{"wechat", "--nope"})
	if err == nil {
		t.Fatal("未知选项必须是错误，不能静默忽略")
	}
	got := captureStderr(t, func() { usageError(fs, err) })
	if strings.Contains(got, "角标文字") {
		t.Errorf("错误输出里不该出现选项表：\n%s", got)
	}
	if !strings.Contains(got, "没有这个选项") {
		t.Errorf("错误输出应当说人话：\n%s", got)
	}
	if lines := strings.Count(strings.TrimSpace(got), "\n"); lines != 0 {
		t.Errorf("错误应当只有一行，实际 %d 行：\n%s", lines+1, got)
	}
}

// `-h` is a question, not a mistake: it is the one case that gets the flag
// list, and it must not come back as a usage error — the caller returns 0, so
// the exit code has to agree.
func TestDashHAnswersWithTheFlagList(t *testing.T) {
	fs := newFlags("gen")
	fs.String("badge", "", "角标文字")
	_, err := parseFL(fs, []string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFL(-h) = %v, want flag.ErrHelp", err)
	}
	var code int
	got := captureStdout(t, func() { code = usageError(fs, err) })
	if code != exitOK {
		t.Errorf("-h 不是用法错误，退出码应当是 0：%d", code)
	}
	if !strings.Contains(got, "角标文字") {
		t.Errorf("-h 应当打印选项表：\n%s", got)
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
// has to name the command that writes one. "profile.yml: no such file" is true
// and useless.
func TestMissingConfigNamesTheCommandThatWritesOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), DefaultProfileName)
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
	path := filepath.Join(t.TempDir(), DefaultProfileName)
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
	if got := firstNonEmpty("out.yml", "", DefaultProfileName); got != "out.yml" {
		t.Errorf("长选项应当胜过默认值：%q", got)
	}
	if got := firstNonEmpty("", "out.yml", DefaultProfileName); got != "out.yml" {
		t.Errorf("短选项应当胜过默认值：%q", got)
	}
	if got := firstNonEmpty("", "", DefaultProfileName); got != DefaultProfileName {
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

// A wrap report has to be readable in one screen: the paths, the maps, and the
// one thing left to do. Everything that answers "what if that map is wrong" —
// the directories twin did not map, the two files that hold the knowledge, the
// paste-ready rules.d template — is advice for a decision the user has not made
// yet, so it belongs behind --explain. On the normal path nothing is wrong, and
// a page of instructions for a problem nobody has is exactly the noise the
// short form exists to remove.
func TestPathMapsShortFormIsJustTheMap(t *testing.T) {
	p := inst.Paths{Home: "/nonexistent-twin-home"}
	it := &inst.Instance{ID: "demo"}
	insp := &generate.Inspection{SourceDesktop: "/usr/share/applications/fakeapp.desktop"}
	fix := mapFix{file: DefaultProfileName, replay: installCommandFor(DefaultProfileName)}

	short := captureStdout(t, func() { printPathMaps(p, it, insp, "", fix, false) })
	for _, noise := range []string{"照抄下面这份", "twin rules --init", "改：", "💡"} {
		if strings.Contains(short, noise) {
			t.Errorf("默认输出不该带 %q：\n%s", noise, short)
		}
	}
	if !strings.Contains(short, "路径映射") {
		t.Errorf("默认输出要保留映射本身：\n%s", short)
	}

	long := captureStdout(t, func() { printPathMaps(p, it, insp, "", fix, true) })
	for _, want := range []string{"照抄下面这份", "twin rules --init"} {
		if !strings.Contains(long, want) {
			t.Errorf("--explain 输出应当带 %q：\n%s", want, long)
		}
	}
}

// The self-check is silent when everything passed. The run just wrote every file
// those ✅ lines would confirm, so printing them is a wall of text between the
// paths and the next step; a check that is not OK is the only one with anything
// to say. `twin verify` still lists them all — enumerating them is its job.
func TestProblemsAreQuietWhenNothingIsWrong(t *testing.T) {
	ok := &verify.Report{Findings: []verify.Finding{
		{Name: "exec.target", Status: verify.StatusOK, Detail: "/opt/wechat/wechat"},
	}}

	if got := formatProblems(ok); got != "" {
		t.Errorf("全绿时不该有输出：\n%s", got)
	}
	if got := formatFindings(ok); !strings.Contains(got, "exec.target") {
		t.Errorf("verify 应当照旧列全部：\n%s", got)
	}

	bad := &verify.Report{Findings: []verify.Finding{
		{
			Name:   "托盘图标覆盖表",
			Status: verify.StatusWarn,
			Detail: "custom-icons 里没有 wechat2 的条目",
			Fix:    "gsettings set …",
		},
	}}
	got := formatProblems(bad)
	if !strings.Contains(got, "托盘图标覆盖表") || !strings.Contains(got, "gsettings set") {
		t.Errorf("有 WARN 时应当连修复命令一起打印：\n%s", got)
	}
	// A fix under a ✅ offers a command for a problem the reader does not have;
	// the tray check always has one ready, so this is the difference between a
	// quiet run and every healthy instance ending on a gsettings line.
	if got := formatFindings(ok); strings.Contains(got, "→") {
		t.Errorf("通过的自检项不该带修复命令：\n%s", got)
	}
}

// `twin gen` says where the configuration landed and what to type next —
// nothing else. The path maps are the longest block in the report and the file
// being written explains every key of them; saying it here too would be a
// second place that can disagree with the file (see AGENTS.md §4.9).
func TestGenShortFormIsPathAndNextStep(t *testing.T) {
	p := inst.Paths{Home: "/nonexistent-twin-home"}
	it := &inst.Instance{ID: "demo", Binds: []inst.Bind{{Source: "~/a", Target: "~/b"}}}
	insp := &generate.Inspection{SourceDesktop: "/usr/share/applications/fakeapp.desktop"}

	short := captureStdout(t, func() {
		printGenResult(p, it, insp, DefaultProfileName, false, false, nil)
	})
	if strings.Contains(short, "路径映射") {
		t.Errorf("gen 的默认输出不该带路径映射：\n%s", short)
	}
	if !strings.Contains(short, "已写出配置") || !strings.Contains(short, "下一步") {
		t.Errorf("gen 的默认输出应当只有「已写出配置」与「下一步」：\n%s", short)
	}

	long := captureStdout(t, func() {
		printGenResult(p, it, insp, DefaultProfileName, false, true, nil)
	})
	if !strings.Contains(long, "路径映射") {
		t.Errorf("--explain 应当展开路径映射：\n%s", long)
	}
}

// ------------------------------------------------------- `twin remove` 的确认

// 这道问句是不可逆删除之前的最后一道，所以它接受的回答刻意窄：y / yes，大小写都行，
// 其余一律当作"不删"。
func TestReadConfirmationAcceptsOnlyYes(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"  Yes  \n", true},
		{"y", true}, // 管道里最后一行常常没有换行
		{"n\n", false},
		{"no\n", false},
		{"\n", false},    // 直接回车 ⇒ 提示里的默认 [y/N]
		{"yse\n", false}, // 拼错 ⇒ 不猜
		{"", false},      // EOF（Ctrl-D）⇒ 不删
	}
	for _, c := range cases {
		t.Run(strings.TrimSpace(c.in), func(t *testing.T) {
			got, err := readConfirmation(strings.NewReader(c.in))
			if err != nil {
				t.Fatalf("readConfirmation(%q) 报错：%v", c.in, err)
			}
			if got != c.want {
				t.Errorf("readConfirmation(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// 标准输入不是终端（脚本 / 管道 / CI）时不提问，只预览并给出那条显式命令：提问没人能回答
// 就是卡住，而卡在一个删除命令上比少删一次更糟。
func TestRemoveWithoutYesPreviewsWhenStdinIsNotATerminal(t *testing.T) {
	if style.IsTerminal(os.Stdin) {
		t.Skip("标准输入是终端 —— 这条测的是脚本场景")
	}
	p := inst.Paths{Home: t.TempDir()}
	var code int
	got := captureStdout(t, func() { code = cmdRemove(p, []string{"demo2"}) })
	if code != exitOK {
		t.Errorf("预览不是错误，退出码应当是 0：%d", code)
	}
	if !strings.Contains(got, "只预览") {
		t.Errorf("非终端下要说清「只预览」：\n%s", got)
	}
	if !strings.Contains(got, "twin remove demo2 --yes") {
		t.Errorf("非终端下要给出那条显式命令：\n%s", got)
	}
}

// captureStdout runs fn with stdout redirected into a pipe. Colour is decided
// once, for the process's real stdout, so a captured buffer is always plain
// text — which is what the assertions above rely on.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stdout, fn)
}

// captureStderr is the same for the other stream: fail() colours from stderr,
// so the two have to be swapped independently.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stderr, fn)
}

func capture(t *testing.T, dst **os.File, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := *dst
	*dst = w
	fn()
	w.Close()
	*dst = old
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
