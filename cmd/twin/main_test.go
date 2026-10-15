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
	"github.com/edwinhuish/twin/internal/sysx"
	"github.com/edwinhuish/twin/internal/tray"
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

// ---------------------------------------------------------------- 托盘尺寸扩展

// fakeGSettings puts a stand-in `gsettings` on PATH and *only* that, so
// `gnome-extensions` is genuinely absent. That is the interesting first-install
// state — the files go down, the shell has never heard of the uuid, and no
// command on this machine can change that until the next login — and it is also
// what keeps the test from ever running the real `gnome-extensions` against the
// shell of whoever is running `go test`.
func fakeGSettings(t *testing.T, list string) {
	t.Helper()
	dir := t.TempDir()
	// 按 key 分答，像真的那样：给进来的这份是**启用**列表，别的 key（shell 的禁用列表、
	// 宿主那张图标表）都是空的。一个答案敷衍所有 key 会让"已启用"与"没被拒"变成同一件事，
	// 而这两条路径的切换恰恰靠它们区分。
	script := "#!/bin/sh\ncase \"$3\" in\nenabled-extensions) echo \"" + list + "\" ;;\n*) echo \"@as []\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gsettings"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// The one thing `twin install --ext` must not do is claim it finished: on a
// first install the shell cannot load the helper yet, and that is a logout, not
// a retry. So the files land, and the run ends with the manual step and exit 3.
func TestInstallExtWritesTheFilesAndAsksForOneRelogin(t *testing.T) {
	fakeGSettings(t, "[]")
	p := inst.Paths{Home: t.TempDir()}

	var code int
	got := captureStdout(t, func() { code = cmdInstall(p, []string{"--ext"}) })
	if code != exitHuman {
		t.Errorf("文件装好了、只差一次注销重登 ⇒ 退出码应为 %d，得到 %d:\n%s", exitHuman, code, got)
	}
	for _, name := range tray.ExtFiles() {
		if !sysx.Exists(filepath.Join(tray.ExtDir(p), name)) {
			t.Errorf("%s 没落盘", name)
		}
	}
	if !strings.Contains(got, "注销重登") {
		t.Errorf("没说清那一步要人做：\n%s", got)
	}
	if !strings.Contains(got, "gnome-extensions enable "+tray.ExtUUID) {
		t.Errorf("没给出要跑的那条命令：\n%s", got)
	}
}

// --dry-run is the only switch that combines with --ext, and it must stay a
// read: a rehearsal that wrote the extension directory would be the one thing a
// rehearsal cannot be.
func TestInstallExtDryRunWritesNothing(t *testing.T) {
	fakeGSettings(t, "[]")
	p := inst.Paths{Home: t.TempDir()}

	var code int
	got := captureStdout(t, func() { code = cmdInstall(p, []string{"--ext", "--dry-run"}) })
	if code != exitOK {
		t.Errorf("预演不是错误：%d\n%s", code, got)
	}
	if sysx.Exists(tray.ExtDir(p)) {
		t.Error("--dry-run 写了盘")
	}
	if !strings.Contains(got, tray.ExtDir(p)) {
		t.Errorf("预演要说清会写到哪：\n%s", got)
	}
}

// The three instance switches are refused by name rather than ignored: silently
// dropping -p would make `twin install --ext -p other.yml` look like it had read
// that file. The last case is the old spelling of the machine-level half added
// on top of the new one — the positional check has to win there too, or the two
// spellings would each be half honoured.
func TestInstallExtRefusesTheInstanceFlags(t *testing.T) {
	cases := [][]string{
		{"--ext", "-p", "other.yml"},
		{"--ext", "--profile=other.yml"},
		{"--ext", "--force"},
		{"--ext", "--explain"},
		{"extension", "--ext"},
	}
	for _, args := range cases {
		fakeGSettings(t, "[]")
		p := inst.Paths{Home: t.TempDir()}
		var code int
		var msg string
		captureStdout(t, func() { msg = captureStderr(t, func() { code = cmdInstall(p, args) }) })
		if code != exitUsage {
			t.Errorf("%v: 退出码应为 %d，得到 %d（%s）", args, exitUsage, code, msg)
		}
		if msg == "" {
			t.Errorf("%v: 拒绝了却什么都没说", args)
		}
	}
}

// A positional argument is refused whatever it says — `install` has none, and
// that is what keeps the object position free of reserved words. Both mistakes
// arrive here: an unknown name (`twin install wechat` reads like "install the
// instance called wechat") and the word it used to take, so the message has to
// name both the switch and -p.
func TestInstallRefusesAPositionalAndNamesBothWays(t *testing.T) {
	for _, word := range []string{"wechat", "extension"} {
		p := inst.Paths{Home: t.TempDir()}
		var code int
		var msg string
		captureStdout(t, func() { msg = captureStderr(t, func() { code = cmdInstall(p, []string{word}) }) })
		if code != exitUsage {
			t.Fatalf("%q: 退出码应为 %d，得到 %d（%s）", word, exitUsage, code, msg)
		}
		if !strings.Contains(msg, "--ext") {
			t.Errorf("%q: 报错要给出现在那个开关：%s", word, msg)
		}
		if !strings.Contains(msg, "-p") {
			t.Errorf("%q: 报错也要说明实例走 -p：%s", word, msg)
		}
	}
}

// install / reinstall only look. If this ever writes again, the helper is back
// to being a side effect of wrapping one instance — and the switch that exists
// to install it becomes a second way to do the same thing.
func TestCheckTrayExtOnlyChecks(t *testing.T) {
	fakeGSettings(t, "[]")
	p := inst.Paths{Home: t.TempDir()}

	got := checkTrayExt(p, &inst.Instance{ID: "demo2", TrayOverride: true})
	if !strings.Contains(got, "twin install --ext") {
		t.Errorf("缺扩展时要给出那个开关：%q", got)
	}
	if sysx.Exists(tray.ExtDir(p)) {
		t.Error("install / reinstall 上的这一句碰了扩展目录：它只许看")
	}

	if got := checkTrayExt(p, &inst.Instance{ID: "demo2"}); !strings.Contains(got, "跳过") {
		t.Errorf("tray.override = false 时应当跳过：%q", got)
	}

	// Both halves in place: the files are there and the shell's list names the
	// uuid, which is the only state that may come out green.
	fakeGSettings(t, "['"+tray.ExtUUID+"']")
	if _, err := tray.EnsureExt(p); err != nil {
		t.Fatal(err)
	}
	if got := checkTrayExt(p, &inst.Instance{ID: "demo2", TrayOverride: true}); !strings.Contains(got, "已启用") {
		t.Errorf("文件在、启用列表里也有它时应当报已启用：%q", got)
	}
}

// fakeTrayShell answers both halves of the tray session at once: the host's
// override table (read-modify-write over a state file, so a `set` is really
// visible to the read-back) and the two shell extension lists. One script for
// both is what makes the *switch* testable — it depends on both answers, and a
// stub that answered only one of them could not tell the two paths apart.
func fakeTrayShell(t *testing.T, hostTable, enabled, blocked string) string {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "host")
	if err := os.WriteFile(state, []byte(hostTable), 0o644); err != nil {
		t.Fatal(err)
	}
	// PATH is this directory and nothing else (like fakeGSettings above, and for
	// the same reason), so the one external command it uses has to be spelled
	// out: `cat` is not on this PATH.
	script := `#!/bin/sh
case "$1" in
get)
  case "$3" in
  custom-icons) /bin/cat "` + state + `" ;;
  enabled-extensions) echo "` + enabled + `" ;;
  disabled-extensions) echo "` + blocked + `" ;;
  *) echo "@as []" ;;
  esac ;;
set)
  printf '%s' "$4" > "` + state + `" ;;
*) echo "unsupported: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gsettings"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return state
}

// 助手扩展被 shell 加载时，托盘覆盖走它自己那条路：写 twin 的文件表，并把宿主表里
// 那一行删掉。删掉那一行是"两条路径只走一条"的可观测形式 —— 它正是 twin remove
// 之后还会指着图标的那一行。
func TestApplyTrayPrefersTheHelperWhenTheShellLoadedIt(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	state := fakeTrayShell(t, "[('demo2', '/old/icon.png', '/old/icon.png')]",
		"['"+tray.ExtUUID+"']", "[]")
	if _, err := tray.EnsureExt(p); err != nil {
		t.Fatal(err)
	}

	got := applyTray(p, &inst.Instance{ID: "demo2", TrayOverride: true, Level: inst.LevelAlias})

	row, found, err := tray.ReadOverride(p, "demo2")
	if err != nil || !found {
		t.Fatalf("文件表里应当有这一行：%+v %v %v", row, found, err)
	}
	if row.Icon != p.Icon("demo2") {
		t.Errorf("表里指的应当是本次生成的图标：%q", row.Icon)
	}
	if !strings.Contains(got, p.TrayTable("demo2")) {
		t.Errorf("要说清写到了哪个文件：%q", got)
	}
	if body, err := os.ReadFile(state); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(body), "demo2") {
		t.Errorf("宿主表里那一行没删掉：%s", body)
	}
}

// 扩展没被加载（这里是"装了但没启用"）时走宿主表兜底，而且**不写**文件表：
// 两条路同时开着，就是这次改动要消掉的那件事。
func TestApplyTrayFallsBackWhenTheHelperIsNotLoaded(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	state := fakeTrayShell(t, "[]", "[]", "[]")
	if _, err := tray.EnsureExt(p); err != nil {
		t.Fatal(err)
	}

	got := applyTray(p, &inst.Instance{ID: "demo2", TrayOverride: true, Level: inst.LevelAlias})

	body, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), p.Icon("demo2")) {
		t.Errorf("兜底这条路要把图标写进宿主表，得到：%s", body)
	}
	if _, found, err := tray.ReadOverride(p, "demo2"); err != nil || found {
		t.Errorf("兜底时不该同时写文件表：%v %v", found, err)
	}
	if !strings.Contains(got, "兜底路径") {
		t.Errorf("要说清这次为什么走兜底：%q", got)
	}
}

// The help for `install` is the only place the switch is explained, and the
// usage block is the only place a user looks before typing it.
func TestInstallHelpNamesTheMachineLevelSwitch(t *testing.T) {
	if h := helpFooter["install"]; !strings.Contains(h, "--ext") {
		t.Errorf("twin install -h 没提这个开关：%q", h)
	}
	if !strings.Contains(usage, "twin install --ext") {
		t.Error("用法块里没有 `twin install --ext` —— 那是它唯一被看见的地方")
	}
}

// `reinstall --ext` and `install --ext` are one action through two doors:
// installing a configuration and replaying the snapshot it wrote differ only in
// where the instance comes from, and the helper belongs to neither. So the
// switch has to mean here exactly what it means there — including the exit code
// that says the last step is a logout rather than a retry.
func TestReinstallExtIsTheSameActionAsInstallExt(t *testing.T) {
	fakeGSettings(t, "[]")
	p := inst.Paths{Home: t.TempDir()}

	var code int
	got := captureStdout(t, func() { code = cmdReinstall(p, []string{"--ext"}) })
	if code != exitHuman {
		t.Errorf("与 install --ext 是同一件事 ⇒ 退出码也应当是 %d，得到 %d:\n%s", exitHuman, code, got)
	}
	for _, name := range tray.ExtFiles() {
		if !sysx.Exists(filepath.Join(tray.ExtDir(p), name)) {
			t.Errorf("%s 没落盘", name)
		}
	}
	if !strings.Contains(got, "注销重登") {
		t.Errorf("没说清那一步要人做：\n%s", got)
	}
}

// ...and the arguments that mean nothing under --ext are refused by name rather
// than dropped: an <id> names an instance the helper does not belong to, and
// --refresh reads the snapshot, which is the instance half. --dry-run outside
// --ext is the same rule seen from the other side — a replay writes the bytes
// the install wrote, so there is nothing to rehearse, and a rehearsal that
// looked like it had happened would be worse than the error.
func TestReinstallRefusesWhatMeansNothing(t *testing.T) {
	cases := [][]string{
		{"--ext", "wechat2"},
		{"--ext", "--refresh"},
		{"wechat2", "--dry-run"},
	}
	for _, args := range cases {
		fakeGSettings(t, "[]")
		p := inst.Paths{Home: t.TempDir()}
		var code int
		var msg string
		captureStdout(t, func() { msg = captureStderr(t, func() { code = cmdReinstall(p, args) }) })
		if code != exitUsage {
			t.Errorf("%v: 退出码应为 %d，得到 %d（%s）", args, exitUsage, code, msg)
		}
		if msg == "" {
			t.Errorf("%v: 拒绝了却什么都没说", args)
		}
		if sysx.Exists(tray.ExtDir(p)) {
			t.Errorf("%v: 被拒的那次运行碰了扩展目录", args)
		}
	}
}

// The same two places `install` has to say it: -h is where the switch is
// explained, the usage block is what a user reads before typing anything.
func TestReinstallHelpNamesTheMachineLevelSwitch(t *testing.T) {
	if h := helpFooter["reinstall"]; !strings.Contains(h, "--ext") {
		t.Errorf("twin reinstall -h 没提这个开关：%q", h)
	}
	if !strings.Contains(usage, "twin reinstall --ext") {
		t.Error("用法块里没有 `twin reinstall --ext`")
	}
}
