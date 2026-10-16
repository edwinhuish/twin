// Command twin gives an existing program its own identity: a name of its own
// for the window and the tray, its own icon, and optionally its own private
// data directories.
//
// The design rationale lives in 应用包裹生成器_twin设计.md; the measurements
// it is built on are in 微信身份识别_定案.md.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edwinhuish/twin/internal/config"
	"github.com/edwinhuish/twin/internal/desk"
	"github.com/edwinhuish/twin/internal/doctor"
	"github.com/edwinhuish/twin/internal/generate"
	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sandbox"
	"github.com/edwinhuish/twin/internal/style"
	"github.com/edwinhuish/twin/internal/sysx"
	"github.com/edwinhuish/twin/internal/tray"
	"github.com/edwinhuish/twin/internal/userns"
	"github.com/edwinhuish/twin/internal/verify"
)

// Exit codes (设计 §5.3). 3 exists so a script can tell "the identity works but
// one step needs a human" apart from "this is broken". Two things land there:
// the tray icon table, which can only be written through the session bus that
// an installer or a headless run does not have, and an AppArmor profile that
// nobody could install for the user — no sudo or no terminal to answer a
// password prompt, and no trampoline to borrow a label from either.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
	exitHuman = 3
)

// defaultProfileLabel is how the default instance definition is spelled
// wherever twin has to name it in text: the help line of -o/-p, the usage
// block, and the refusal that a missing configuration produces.
//
// It is the ~ form, not the absolute one, for the same reason profile.yml
// itself stores ~ paths: the machine's home is one machine's answer, while
// this string has to read the same in a help screen, a bug report and on
// someone else's box. The real path comes from inst.Paths.Profile(id).
const defaultProfileLabel = "~/.local/share/twin/<id>/profile.yml"

// Colour is decided per stream, and decided here rather than at each print
// site: `twin gen wechat > log` must leave the log plain while the warnings
// still land in colour on the terminal (see internal/style).
var (
	outStyle = style.For(os.Stdout)
	errStyle = style.For(os.Stderr)
)

// title renders a section title: bold, with the emoji kept at the call site so
// the shape of the output stays readable from the source. Titles and verdicts
// are the only things that get colour, so a scan down the left edge is enough
// to get through a long wrap report.
func title(t string) string { return outStyle.Bold(t) }

// loadConfig reads the environment layer, turning a malformed file into a
// usage error. Silently falling back to the built-in defaults would make a
// typo in config.toml invisible — the exact class of failure this tool exists
// to prevent.
func loadConfig(p inst.Paths) (*config.Config, int) {
	cfg, err := config.Load(p)
	if err != nil {
		fail(err.Error())
		return nil, exitUsage
	}
	return cfg, exitOK
}

// exitFor maps a verification report onto the exit code.
func exitFor(rep *verify.Report) int {
	if !rep.OK() {
		return exitFail
	}
	if rep.NeedsHuman() {
		return exitHuman
	}
	return exitOK
}

const usage = `twin —— 程序双开

用法:
  twin gen <source> [选项]                生成一份配置（默认 ` + defaultProfileLabel + `）
  twin install <id> | -p <文件> [选项]    读那份定义并落盘：桌面项 / 图标 / 启动器 / 托盘
  twin install --ext                      装入 shell 侧的托盘扩展（机器级，一次即可）
  twin inspect <source> [--name 显示名]   只看：源程序是什么、身份该怎么做
  twin verify <id>                        自检一个实例的身份链路
  twin reinstall <id> [--refresh]         按实例定义重新落盘（同 twin install <id>）
  twin reinstall --ext                    同 twin install --ext（两个入口，同一件事）
  twin remove <id> [--yes] [--purge]      删除产物
  twin list                               列出全部实例
  twin doctor                             环境自检（先跑这个）
  twin rules [<app>]                      列出内置规则 / 查看某程序生效的规则
  twin config                             打印 config.toml 模板（不存在时）
  twin version                            版本

每个子命令的选项与默认值：twin <子命令> -h（例如 twin gen -h）
`

// version is what `twin version` prints; override at build time with
// -ldflags "-X main.version=…".
var version = "0.2.0-dev"

func main() {
	if len(os.Args) < 2 {
		// 空命令行就是 `twin --help`：没有动作可做，能回答的只有"这个工具是什么"，
		// 所以走和 --help 相同的出口 —— 包括退出码 0。2 说的是"你的命令行写错了"，
		// 而裸调用一个字都没写错：按用法错误收尾，会让 `twin || echo 坏了` 这种写法
		// 把一次提问报成故障。
		fmt.Print(usage)
		return
	}
	cmd := os.Args[1]

	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	case "-v", "--version", "version":
		fmt.Println("twin " + version)
		return
	}

	home, err := inst.Home()
	if err != nil {
		fail(err.Error())
		fmt.Fprintln(os.Stderr, "twin 拒绝在 HOME 缺失的情况下猜一个家目录（旧的手写脚本会退回 /root，那是数据风险）。")
		os.Exit(exitFail)
	}
	p := inst.Paths{Home: home}

	args := os.Args[2:]
	switch cmd {
	case "gen":
		os.Exit(cmdGen(p, args))
	case "install":
		os.Exit(cmdInstall(p, args))
	case "inspect":
		os.Exit(cmdInspect(p, args))
	case "verify":
		os.Exit(cmdVerify(p, args))
	case "reinstall":
		os.Exit(cmdReinstall(p, args))
	case "remove":
		os.Exit(cmdRemove(p, args))
	case "list":
		os.Exit(cmdList(p))
	case "doctor":
		os.Exit(cmdDoctor(p))
	case "rules":
		os.Exit(cmdRules(p, args))
	case "config":
		os.Exit(cmdConfig(p))
	default:
		// No implicit verb and no reserved-word table: an unknown first token is
		// a usage error, never a guess. `twin gen list` is how you wrap a program
		// that happens to be named after a subcommand.
		//
		// The usage block is not repeated here: it is a screenful, and it would
		// push the one line naming the typo off the top of the terminal. Bare
		// `twin` still prints it — there the user asked for nothing else.
		fail(fmt.Sprintf("未知子命令 %q（帮助：twin --help）", cmd))
		os.Exit(exitUsage)
	}
}

// ---------------------------------------------------------------- flag parsing

// parseFL parses a FlagSet whose positional arguments may appear anywhere among
// the flags.
//
// Go's flag package stops at the first non-flag token, so the documented form
// `twin gen <source> --as <id>` would otherwise ignore --as — and every
// flag after the source — without a word. That is precisely the silent failure
// this tool exists to prevent, so we take the stray token as a positional and
// re-parse the remainder; values already set accumulate on the same FlagSet.
func parseFL(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// newFlags builds the FlagSet every verb parses its options with.
//
// The difference from a bare flag.NewFlagSet is that this one says nothing on
// its own. Go's flag package answers a mistake with the error *and* the whole
// "Usage of gen:" flag list, printed straight to stderr — which is two things
// twin does not want: the list belongs to `twin --help`, and printing it under
// a one-line typo buries the line that matters. It also bypasses internal/style,
// so a typo would come out plain while every other diagnostic respects NO_COLOR
// and the per-stream decision. usageError does the talking instead.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// usageError reports a command line the flag package rejected.
//
// Errors get one line — what was wrong, nothing else. `-h` gets the flag list,
// because that is a question rather than a mistake, and it is the only answer
// to it. Both forms go out through twin's own printers so the output stays one
// stream, one style.
func usageError(fs *flag.FlagSet, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		fmt.Printf("%s 的选项：\n", fs.Name())
		fs.SetOutput(os.Stdout)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
		if footer := helpFooter[fs.Name()]; footer != "" {
			fmt.Print(footer)
		}
		return exitOK
	}
	// The flag package's own wording is English and aimed at Go programmers
	// ("flag provided but not defined: -nope"). Two phrases carry almost every
	// real typo; the rest is left verbatim rather than guessed at.
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined: "):
		msg = "没有这个选项：" + strings.TrimPrefix(msg, "flag provided but not defined: ")
	case strings.HasPrefix(msg, "flag needs an argument: "):
		msg = "选项缺一个值：" + strings.TrimPrefix(msg, "flag needs an argument: ")
	}
	fail(msg)
	return exitUsage
}

// helpFooter 收下 PrintDefaults 打不出来的那几行：`--` 不是 flag，而"这个动词不接受
// 位置参数"也不是任何一个 flag 的属性。它们留在 `-h` 里、而不是只留在文档里，理由是
// 文档会烂：选项表若在别处再抄一份，`-h` 与它迟早各表一套；这里与实现同一次编译。
var helpFooter = map[string]string{
	"gen": "\n  twin gen <source> [选项] -- <bwrap 选项…>\n" +
		"    `--` 之后的 token 原样进配置的 bwrap.extras（合并后那份列表的尾部 ⇒ 同目标的挂载\n" +
		"    由它胜出；有形状的那几个键排在它之后，压不住）。它是**写进文件**的一份，不是每次启动的覆盖。\n" +
		"    ⚠️ 不享受 ~/ 展开与 mkdir 补齐；再写一个 `--`、写 --argv0、或写需要 fd 的选项都会被拒绝，\n" +
		"       其余 token 只做 lint（警告），不改写。\n",
	"install": "\n  twin install <id> | -p <文件> [选项]   （二选一，同时给是用法错误）\n" +
		"    <id>：读那个实例自己的定义 " + defaultProfileLabel + "；\n" +
		"    -p  ：读你指定的那份（配置放在别处时用它）。\n" +
		"    两种写法都是「读一份配置落盘」—— 包哪个程序、隔离哪些目录是 twin gen 那一步\n" +
		"    决定的（都在那份文件里）。托盘图标走两条路中的一条：助手扩展被 shell\n" +
		"    加载了 ⇒ 写它读的那份文件表（~/.local/share/twin/tray.d/<id>.json）；\n" +
		"    没加载（未装／未启用／被禁用列表挡住／读不到列表）⇒ 写宿主覆盖表兜底。\n" +
		"    这次走了哪条写在输出里，两种都不需要会话总线以外的东西。\n" +
		"    带 --ext：只装入 shell 侧的托盘扩展（机器级、与实例无关，\n" +
		"    只与 --dry-run 同用；装完之后要注销重登一次才启用得上）。\n",
	"reinstall": "\n  twin reinstall <id> [--refresh] | --ext\n" +
		"    给 <id>：读那个实例的定义（" + defaultProfileLabel + "）重新落盘，\n" +
		"    托盘那半按上面那条规则重新选路；`twin install <id>` 是同一件事的另一个入口。\n" +
		"    --refresh 才回读 config.toml / rules.d（补上的值只用于这次落盘，不改那份文件）。\n" +
		"    带 --ext：只装入 shell 侧的托盘扩展 —— 与 `twin install --ext` 是同一个\n" +
		"    动作（因而也不带 <id>：机器级的东西不属于任何实例），同样只与 --dry-run 同用。\n",
}

// splitPassthrough cuts args at the first `--`. Everything left of it belongs
// to twin; everything right of it is forwarded to bubblewrap verbatim and
// never parsed again (设计 §2.2).
//
// The tail is checked for the two miswrites that are cheap to catch here and
// expensive to diagnose later:
//   - the first tail token must start with `-`, otherwise the user meant the
//     POSIX "end of options" reading of `--`, which is not twin's meaning;
//   - a second `--` in the tail is refused because bubblewrap reads it as "end
//     of options", truncating its own parsing and swallowing the rest as the
//     program name.
//
// To pass a literal `--` as a flag *value*, use the `--flag=--` spelling.
func splitPassthrough(args []string) (head, tail []string, err error) {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return args, nil, nil
	}
	head, tail = args[:sep], args[sep+1:]
	if len(tail) == 0 {
		return head, nil, nil
	}
	if !strings.HasPrefix(tail[0], "-") {
		return nil, nil, fmt.Errorf("`--` 之后应以 bwrap 选项开头（收到 %q）——"+
			"twin 的 `--` 意思是「以下是 bwrap 参数」，不是 POSIX 的「选项到此为止」", tail[0])
	}
	for _, t := range tail {
		if t == "--" {
			return nil, nil, fmt.Errorf("透传区不能再出现 `--`：bwrap 会把它读成「选项结束」，" +
				"后面的选项全被当成程序名（要传字面的 `--` 请用 --flag=-- 形式）")
		}
	}
	return head, tail, nil
}

// ---------------------------------------------------------------- doctor

func cmdDoctor(p inst.Paths) int {
	rep := doctor.Run(p)
	fmt.Print(rep.Format(outStyle))
	if steps := rep.NextSteps(); len(steps) > 0 {
		fmt.Printf("\n%s\n", title("👉 下一步"))
		for i, s := range steps {
			fmt.Printf("  %d. %s\n", i+1, s)
		}
	}
	cfgPath := p.GlobalConfig()
	fmt.Printf("\n%s\n", title("⚙️ 配置层"))
	if cfg, err := config.Load(p); err != nil {
		fmt.Printf("  %s：读不了 —— %v\n", cfgPath, err)
	} else if cfg.Exists {
		fmt.Printf("  %s（已启用；bwrap=%s）\n", cfgPath, orDash(cfg.Tools.Bwrap))
	} else {
		fmt.Printf("  %s 不存在（用内置默认）。要改默认就把它写出来：\n", cfgPath)
		fmt.Printf("     twin config            # 打印模板，重定向到上面那个路径即可\n")
	}
	fmt.Printf("  应用规则目录 %s（<app>.toml；内置规则见 `twin rules`）\n", p.RulesDir())
	if rep.Blocking() {
		return exitFail
	}
	return exitOK
}

// ---------------------------------------------------------------- config / rules

func cmdConfig(p inst.Paths) int {
	path := p.GlobalConfig()
	if sysx.Exists(path) {
		data, err := os.ReadFile(path)
		if err != nil {
			fail(err.Error())
			return exitFail
		}
		fmt.Print(string(data))
		return exitOK
	}
	fmt.Print(config.Sample())
	return exitOK
}

func cmdRules(p inst.Paths, args []string) int {
	fs := newFlags("rules")
	initDraft := fs.Bool("init", false, "为这个程序探测状态目录并写出一份 rules.d 草稿（已存在则拒绝，不覆盖）")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if *initDraft {
		if len(pos) != 1 {
			fail("twin rules --init 要正好一个参数：源桌面项、程序名或 id")
			return exitUsage
		}
		return cmdRulesInit(p, pos[0])
	}
	if len(pos) == 1 {
		key := strings.TrimSuffix(filepath.Base(pos[0]), ".desktop")
		rule, matched, err := config.FindRule(p, key)
		if err != nil {
			fail(err.Error())
			return exitUsage
		}
		if rule == nil {
			fmt.Printf("%s 没有 %q 的规则（%s 下也没有同名文件）\n",
				outStyle.Mark(style.LevelSkip), key, p.RulesDir())
			return exitOK
		}
		fmt.Printf("规则 %s\n  来源   %s\n", matched, orBuiltin(rule))
		if rule.Keywords != "" {
			fmt.Printf("  keywords   %s\n", rule.Keywords)
		}
		if rule.Categories != "" {
			fmt.Printf("  categories %s\n", rule.Categories)
		}
		if len(rule.Tmpfs) > 0 {
			fmt.Printf("  tmpfs      %s\n", strings.Join(rule.Tmpfs, " "))
		}
		for _, b := range rule.Binds {
			fmt.Printf("  bind       %s\n", b)
		}
		if len(rule.Args) > 0 {
			fmt.Printf("  bwrap.args %s\n", strings.Join(rule.Args, " "))
		}
		return exitOK
	}
	builtin := config.BuiltinRules()
	fmt.Printf("内置规则：")
	for _, k := range sortedRuleKeys(builtin) {
		fmt.Printf(" %s", k)
	}
	fmt.Printf("\n用户规则：%s/<app>.toml\n", p.RulesDir())
	fmt.Printf("\n写一条用户规则（会与内置同名字段逐项合并，文件里的值优先）：\n")
	fmt.Print(`  app = "<程序名>"
  keywords = "k1;k2;"
  tmpfs = ["/tmp"]

  bind = [
    "~/.local/state/twin/{id}/config:~/.config/<app>",
  ]
`)
	fmt.Printf("\n不想凭空猜路径，就让 twin 先探测一遍再写草稿：\n  twin rules --init <源桌面项|程序名>\n")
	return exitOK
}

// cmdRulesInit writes a rules.d draft built from what twin could discover about
// the program.
//
// It is the one place where discovery turns into something with effect, and the
// thing that makes that acceptable is that the user asked for it by name. twin
// still never writes a path map on its own initiative: the file lands on disk
// as a draft, with the guessed entries commented out (设计 §7).
func cmdRulesInit(p inst.Paths, source string) int {
	insp, err := generate.Inspect(p, source)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	key := ruleKeyFor(insp)
	path := filepath.Join(p.RulesDir(), key+".toml")
	if sysx.Exists(path) {
		// Overwriting somebody's hand-tuned rule would be the most destructive
		// thing this command could do, and it is exactly the file they would
		// have to restore from memory.
		fail(fmt.Sprintf("%s 已存在 —— 不覆盖。现状：twin rules %s", path, key))
		return exitFail
	}

	hints := generate.SuggestStateDirsDeep(p, insp)
	if err := sysx.WriteFileAtomic(path, []byte(generate.RuleDraft(key, insp.SourceDesktop, hints)), 0o644); err != nil {
		fail(err.Error())
		return exitFail
	}

	live := 0
	for _, h := range hints {
		if h.Strong {
			live++
		}
	}
	fmt.Printf("%s %s\n", outStyle.Green("✅ 已写出规则草稿"), path)
	switch {
	case live > 0:
		fmt.Printf("  %d 条已生效（这台机器上目录真的存在），%d 条只是二进制里的字符串，被注释掉了。\n",
			live, len(hints)-live)
		fmt.Printf("  请逐条确认该私有还是共用 —— 草稿里的映射默认按「私有副本」写（源=私有目录，目标=程序看到的路径）。\n")
	case len(hints) > 0:
		fmt.Printf("  没有目录存在的证据，%d 条都只是二进制里的字符串 —— 全部被注释掉了。\n", len(hints))
		fmt.Printf("  请挑出真正的状态目录，按「源:目标」补上映射再取消注释。\n")
	default:
		fmt.Printf("  什么都没探测到：这台机器上没有 ~/.config/<程序名> 这类目录，二进制里也没有相关的\n")
		fmt.Printf("  ~/ 路径字面量。草稿里只有一份形状示例 —— 路径要你自己判断。\n")
	}
	fmt.Printf("  验证  twin rules %s\n", key)
	fmt.Printf("  应用  twin install --force    （规则只影响以后新建的实例，已装的那份要 twin reinstall <id>）\n")
	return exitOK
}

func sortedRuleKeys(m map[string]*config.Rule) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- inspect

func cmdInspect(p inst.Paths, args []string) int {
	fs := newFlags("inspect")
	name := fs.String("name", "", "预览：把该名字当作你显式指定的显示名（它会真的参与 keywords 推导）")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if len(pos) < 1 {
		fail("缺少 <source>：源桌面项、程序名或 id 都可以")
		return exitUsage
	}
	insp, err := generate.Inspect(p, pos[0])
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	cfg, code := loadConfig(p)
	if code != exitOK {
		return code
	}
	fmt.Printf("源桌面项   %s\n", insp.SourceDesktop)
	// Show the localised name, not the generic one: Plan derives the display
	// name from Name[<locale>] first, so printing the ASCII Name here would
	// make the preview disagree with what wrap actually writes — the exact
	// class of silent mismatch this tool exists to prevent.
	if loc, tag := insp.Entry.LocalizedName(desk.LocaleTags()...); loc != "" {
		fmt.Printf("显示名     %s   ← 源 Name[%s]（实例会再追加序号）\n", loc, tag)
	} else {
		fmt.Printf("显示名     %s   ← 源 Name（实例会再追加序号）\n", insp.Entry.Name)
	}
	if loc, tag := insp.Entry.LocalizedComment(desk.LocaleTags()...); loc != "" {
		fmt.Printf("说明       %s   ← 源 Comment[%s]\n", loc, tag)
	} else if insp.Entry.Comment != "" {
		fmt.Printf("说明       %s   ← 源 Comment\n", insp.Entry.Comment)
	}
	fmt.Printf("Exec       %s\n", insp.Entry.Exec)
	if len(insp.Argv) > 1 {
		fmt.Printf("静态参数   %s\n", strings.Join(insp.Argv[1:], " "))
	}
	fmt.Printf("真实程序   %s\n", insp.Target)
	fmt.Printf("所在目录   %s\n", filepath.Dir(insp.Target))
	fmt.Printf("源图标     %s\n", orDash(insp.IconSource))
	fmt.Printf("Categories %s\n", orDash(insp.Entry.Categories))
	// The environment layer is not part of the program, but it does change the
	// wrap (template bwrap path, global passthrough, whether /tmp is
	// sandboxed), so say where it is and whether it is actually there.
	if cfg.Exists {
		fmt.Printf("配置       %s\n", cfg.Path)
	} else {
		fmt.Printf("配置       %s（不存在，用内置默认；`twin doctor` 可打印模板）\n", cfg.Path)
	}
	if cfg.Tools.Bwrap != "" {
		fmt.Printf("bwrap      %s  ← config.toml [tools] bwrap\n", cfg.Tools.Bwrap)
	}
	// Show the value that would actually be written, and where it came from:
	// "inherited from the source" and "synthesised from its names" are very
	// different claims, and the user is about to trust one of them.
	//
	// --name is accepted here purely so the interaction between naming an
	// instance and its keywords can be previewed without writing anything.
	// A rules.d keyword list counts as explicit, exactly as Plan treats it.
	kwIn := ""
	if insp.Rule != nil {
		kwIn = insp.Rule.Keywords
	}
	kw, kwWhy := generate.Keywords(insp, kwIn, *name, *name != "")
	if kwIn != "" {
		kwWhy = "应用规则里的 keywords（" + orBuiltin(insp.Rule) + "）"
	}
	fmt.Printf("Keywords   %s\n", orDash(kw))
	if kwWhy != "" {
		fmt.Printf("           ← %s\n", kwWhy)
	}

	t := insp.Traits
	fmt.Printf("\n%s\n", title("🔧 工具箱探测（按二进制内字符串）"))
	fmt.Printf("  Wayland 支持  %s\n", yn(t.Wayland))
	fmt.Printf("  X11 支持      %s\n", yn(t.X11))
	fmt.Printf("  Qt            %s\n", yn(t.Qt))
	fmt.Printf("  GTK           %s\n", yn(t.GTK))
	fmt.Printf("  多合一二进制  %s\n", yn(t.MultiCall))
	switch {
	case insp.Rule != nil && len(insp.Rule.Tmpfs) > 0:
		fmt.Printf("\n📂 /tmp 策略：用应用规则 %s 里的 tmpfs（%s）—— 在 bwrap.extras 里各写成一行 --tmpfs\n",
			orBuiltin(insp.Rule), strings.Join(insp.Rule.Tmpfs, " "))
	case t.Wayland && t.X11:
		fmt.Printf("\n📂 /tmp 策略：bwrap.extras 里写一行 --tmpfs /tmp（空的私有 /tmp）—— 本会话有 WAYLAND_DISPLAY，该程序会走 Wayland。\n")
		fmt.Printf("  %s 但它两套平台插件都带着：若哪天真退到 X11，--tmpfs /tmp 会砍掉 /tmp/.X11-unix/X0 而连不上 X；\n"+
			"     那时把那一行往下挪一行、改成 --bind /tmp /tmp 即可（同一份列表里，写在后面的压得住它；生成时用 --no-tmpfs 写的就是它）。\n",
			outStyle.Yellow("⚠️"))
	case t.Wayland && cfg.Defaults.TmpfsUnlessX11:
		fmt.Printf("\n📂 /tmp 策略：bwrap.extras 里写一行 --tmpfs /tmp（该程序走 Wayland）\n")
	case t.Wayland:
		fmt.Printf("\n📂 /tmp 策略：bwrap.extras 里写一行 --bind /tmp /tmp（宿主那个，原地可写；config.toml 关掉了 tmpfs_unless_x11）\n")
	default:
		fmt.Printf("\n📂 /tmp 策略：bwrap.extras 里写一行 --bind /tmp /tmp（宿主那个，原地可写），%s\n",
			outStyle.Red("**不要** --tmpfs /tmp（会砍掉 /tmp/.X11-unix/X0）"))
	}

	if len(insp.Notes) > 0 {
		fmt.Printf("\n%s\n", title("📝 备注"))
		for _, n := range insp.Notes {
			fmt.Printf("  · %s\n", n)
		}
	}
	return exitOK
}

// ---------------------------------------------------------------- wrap

type listFlag []string

func (l *listFlag) String() string { return "" }
func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func cmdGen(p inst.Paths, args []string) int {
	fs := newFlags("gen")
	// These strings are the option table's only home: the docs no longer restate
	// them (they point at `-h`), so a default that a user cannot see here cannot
	// be seen anywhere — and a wrong default is exactly the kind of thing that
	// says nothing until two instances fight over one lock file.
	var (
		output    = fs.String("output", "", "配置写到哪（默认 "+defaultProfileLabel+"；与 -o 是同一个设置）")
		as        = fs.String("as", "", "实例 id，同时是窗口 app_id 与托盘 SNI Id（默认 源基名 ＋ 最小空闲序号，从 2 起）")
		name      = fs.String("name", "", "显示名，写进生成的桌面项正文（默认 源 Name[当前语言] ＋ 序号；那份正文一般由 gen 写一次，之后改它本身）")
		nameEn    = fs.String("name-en", "", "英文显示名，写 Name[en]（默认 源 Name ＋ 序号；与 --name 相同时不写）")
		comment   = fs.String("comment", "", "说明，写进桌面项正文（默认 源 Comment[当前语言] → Comment）")
		commentEn = fs.String("comment-en", "", "英文说明，写 Comment[en]")
		// No backquotes in these strings: flag.UnquoteUsage takes the first
		// backquoted run as the flag's argument name, so a quoted `;` would be
		// printed as `-keywords ;` instead of `-keywords string`.
		keywords   = fs.String("keywords", "", "搜索关键词，分号分隔，写进桌面项正文（默认 rules.d → 源桌面项）")
		categories = fs.String("categories", "", "分类，分号分隔，写进桌面项正文（默认 rules.d → 源桌面项）")
		iconPath   = fs.String("icon", "", "源图标 png|svg（默认 源桌面项的 Icon=；找不到就拒绝生成，不出齿轮占位图）")
		badge      = fs.String("badge", "", "角标文字（默认 id 尾部数字；可用字符 0-9 A-Z 与 -+.!?/ 和空格）")
		badgeStyle = fs.String("badge-style", "", "角标风格 corner|number|dot（默认 config.toml → corner）")
		level      = fs.String("level", "", "身份级别 none|alias|redirect（默认 config.toml → rules.d → alias；argument/env 未实现，会被拒绝）")
		force      = fs.Bool("force", false, "覆盖已存在的输出文件（默认拒绝：那份文件正是你要改的东西）")
		dryRun     = fs.Bool("dry-run", false, "只打印，不落盘（连配置正文一起打印）")
		explain    = fs.Bool("explain", false, "打印完整说明（推导出的默认值、路径映射的修改粒度、规则模板）")
		noTmpfs    = fs.Bool("no-tmpfs", false, "不把 /tmp 盖成空 tmpfs，改成在 bwrap.extras 里写一条 --bind /tmp /tmp（宿主那个，原地可写）—— X11 程序必须如此：--tmpfs /tmp 会砍掉 /tmp/.X11-unix/X0")
		noTray     = fs.Bool("no-tray", false, "不要把这个实例的托盘图标指给 twin（写文件表还是宿主覆盖表见 tray_override 的说明；整机开关在 config.toml 的 defaults.tray_override）")
	)
	// Go's flag package has no aliases, so the documented `-o` is a second
	// variable for the same setting; the two are reconciled before use rather
	// than letting "whichever was parsed last" decide.
	var shortOutput string
	fs.StringVar(&shortOutput, "o", "", "配置写到哪（--output 的简写；两个拼写同时给是用法错误）")
	var tmpfs listFlag
	fs.Var(&tmpfs, "tmpfs", "沙箱内屏蔽的目录（空 tmpfs），可重复，在 bwrap.extras 里写成 --tmpfs 一行（路径里的 ~ 在落盘时展开）")
	// Cut the bwrap passthrough off before twin's own parser runs: tokens
	// after `--` are not twin's to interpret (设计 §2.2).
	head, bwrapTokens, err := splitPassthrough(args)
	if err != nil {
		fail(err.Error())
		return exitUsage
	}
	pos, err := parseFL(fs, head)
	if err != nil {
		return usageError(fs, err)
	}
	if len(pos) < 1 {
		fail("缺少 <source>：源桌面项、程序名或 id 都可以（写到别处用 -o <文件>）")
		return exitUsage
	}
	if *output != "" && shortOutput != "" {
		fail("同时给了 -o 和 --output：它们是同一个设置，请只留一个")
		return exitUsage
	}

	// An empty --level means "let the config layers decide", which is not the
	// same as passing --level alias: the rules.d entry for the program may know
	// better. Plan resolves the order and reports which layer won.
	var lvl inst.Level
	if *level != "" {
		lvl, err = inst.ParseLevel(*level)
		if err != nil {
			fail(err.Error())
			return exitUsage
		}
		if !lvl.Implemented() {
			fail(fmt.Sprintf("--level %s 还没实现：阶梯上只有 none / alias / redirect 通过了实测，"+
				"把没验证过的级别做成选项等于把坑留给使用者", lvl))
			return exitUsage
		}
	}

	insp, err := generate.Inspect(p, pos[0])
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	cfg, code := loadConfig(p)
	if code != exitOK {
		return code
	}
	o := generate.Options{
		Source:     pos[0],
		ID:         *as,
		Name:       *name,
		NameEn:     *nameEn,
		Comment:    *comment,
		CommentEn:  *commentEn,
		Keywords:   *keywords,
		Categories: *categories,
		Icon:       *iconPath,
		Badge:      *badge,
		BadgeStyle: *badgeStyle,
		Level:      lvl,
		Tmpfs:      tmpfs,
		Cfg:        cfg,
		Args:       bwrapTokens,
		NoTmpfs:    *noTmpfs,
		NoTray:     *noTray,
	}
	// force is left out on purpose: it means "overwrite the file I asked for",
	// and letting it also mean "yes, shadow that other program's desktop entry"
	// would make one flag do two unrelated destructive things by accident.
	it, body, der, err := planInstance(p, o, insp, false)
	if err != nil {
		fail(err.Error())
		return exitFail
	}

	// The passthrough region is linted, never rewritten (设计 §2.6): a typo in a
	// bwrap option is cheap to flag here and expensive to hit at launch time.
	for _, w := range sandbox.LintBwrap(bwrapTokens) {
		warn(w)
	}

	// The default landing place is the instance's own directory, so it can only
	// be worked out after Plan has said which instance this is. One file per id
	// is also why two wraps of two programs do not collide any more.
	outPath := firstNonEmpty(*output, shortOutput, p.Profile(it.ID))
	// The entry's text lands beside the definition, under the name the
	// definition points at (override.desktop) — resolved with the very function
	// Load uses, so `./<id>.desktop` means one file to both of them (AGENTS §5).
	txtPath := inst.ResolveAgainst(filepath.Dir(outPath), it.DesktopOverride)
	txtNew := !sysx.Exists(txtPath)

	if !*dryRun {
		// A definition that is already at the default place is not "another
		// output file": it *is* this instance, and the command that consumes it
		// is named here rather than a generic "change -o" (which would send the
		// reader off to make a second copy of an instance they already have).
		if outPath == p.Profile(it.ID) && sysx.Exists(outPath) && !*force {
			fail(fmt.Sprintf("%s 已经是这个实例的定义 —— 直接落盘：twin install %s（要重来先 twin remove %s，"+
				"或 -o 写到别处、--force 覆盖它）", outPath, it.ID, it.ID))
			return exitFail
		}
		config, err := renderDefinition(p, it, outPath)
		if err != nil {
			fail(err.Error())
			return exitFail
		}
		if err := writeConfigFile(outPath, config, *force); err != nil {
			fail(err.Error())
			return exitFail
		}
		// Only when it is not there yet, and never overwritten: this file is
		// the desktop entry itself, and `gen` exists to hand the user something
		// to read and edit. Rewriting it would throw those edits away — which is
		// the same rule the definition gets, for the same reason.
		if txtNew {
			if err := sysx.WriteFileAtomic(txtPath, generate.EntryBytes(p, it, body), 0o644); err != nil {
				fail(err.Error())
				return exitFail
			}
		}
	}
	printGenResult(p, it, insp, outPath, txtPath, *dryRun, *explain, txtNew, der, body)
	return exitOK
}

// renderDefinition is the single place that decides which header the definition
// carries, and both the writer and the --dry-run preview go through it — a
// preview rendered by another rule is a preview of a different file (AGENTS §4.5).
//
// The default place names `twin install <id>`; a file anywhere else (gen -o)
// names the -p line that reads *it*, because under `twin install <id>` that
// reader would land on the record in the instance directory instead.
func renderDefinition(p inst.Paths, it *inst.Instance, path string) ([]byte, error) {
	if path == p.Profile(it.ID) {
		return it.Render()
	}
	return it.RenderAt(path)
}

// writeConfigFile writes the configuration, refusing to clobber a file that is
// already there unless --force says otherwise.
//
// The refusal is the important half. This file is the user's instance
// definition, and the whole point of splitting the wrap in two is that somebody
// reads and edits it before anything is installed; silently replacing it would
// throw those edits away without a word.
func writeConfigFile(path string, body []byte, force bool) error {
	if sysx.Exists(path) && !force {
		return fmt.Errorf("%s 已存在 —— 不覆盖。它是你要改的那份配置："+
			"换一个 -o、或先删掉它、或确认要重来再加 --force", path)
	}
	return sysx.WriteFileAtomic(path, body, 0o644)
}

// printGenResult says where the configuration landed and what the next command
// is. That is the whole of the short form: the body of the file is printed only
// for --dry-run and the path maps only for --explain, because the file itself
// carries a note above every key — saying the same thing here would be two
// places that can disagree about what a key means.
//
// The one fact that does not wait for --explain is "no mapping at all". Nothing
// in this or any later step reports that consequence again, and the programs it
// happens to are exactly the ones that fight each other over a lock file.
func printGenResult(p inst.Paths, it *inst.Instance, insp *generate.Inspection,
	outPath, txtPath string, dry, explain, txtNew bool, der []generate.Derivation, body generate.EntryBody) {
	lead := outStyle.Green(style.Glyph(style.LevelOK) + " 已写出配置")
	if dry {
		lead = title("🔍 将要写出配置（--dry-run，未写盘）")
	}
	fmt.Printf("%s %s\n", lead, outStyle.Bold(outPath))
	// The second file is named on the spot: it is where a user renames the
	// program, and walking them into an editor for the wrong file is the one
	// mistake this line prevents. When it already exists it is *not* rewritten,
	// and saying so here is cheaper than an FAQ.
	switch {
	case txtNew && dry:
		fmt.Printf("  桌面项正文（将写出） %s\n", txtPath)
	case txtNew:
		fmt.Printf("  桌面项正文（已写出） %s\n", txtPath)
	default:
		fmt.Printf("  桌面项正文 %s（已存在，未改动 —— 改它、install 就照它装）\n", txtPath)
	}
	if explain {
		printDerived(der)
		// "This instance" is the definition the user is holding, and gen has
		// not installed anything yet: the one file to edit is the one just
		// written, and the map section says so with its real path.
		printPathMaps(p, it, insp, mapsOrigin(insp.Rule),
			mapFix{file: outPath, replay: installCommandFor(p, outPath, it.ID)}, true)
	}
	// Under --explain the map section carries this sentence itself — but only
	// for a program with no rule at all, so a rule that maps nothing still
	// needs the line here (saying it twice is how a reader learns to skip it).
	// Counted without the root mount: every definition has that one, and it
	// isolates nothing.
	if len(dataMaps(it)) == 0 && (!explain || insp.Rule != nil) {
		fmt.Printf("  %s\n", outStyle.Yellow("⚠️ 没有隔离任何数据目录：这个程序若有单实例锁"+
			"（Electron / Qt / GTK 系常见），两个实例会互相干扰 —— 见配置里的 bwrap.bind"))
	}
	if dry {
		config, err := renderDefinition(p, it, outPath)
		if err != nil {
			fail(err.Error())
			return
		}
		fmt.Printf("\n%s\n%s", title("📄 实例定义（"+filepath.Base(outPath)+"）"), indent(config, "  "))
		// The entry's own text, shown the way install will see it: an existing
		// file is copied as it stands (so *it* is what is previewed), and a
		// missing one is what gen just rendered above.
		text := generate.EntryBytes(p, it, body)
		if !txtNew {
			if onDisk, err := os.ReadFile(txtPath); err == nil {
				text = onDisk
			}
		}
		fmt.Printf("\n%s\n%s", title("📄 桌面项正文（"+filepath.Base(txtPath)+"，install 逐字复制它）"),
			indent(text, "  "))
	}
	fmt.Printf("\n%s\n", title("👉 下一步"))
	fmt.Printf("  1. 改 %s —— 每个键的说明都写在文件里的注释里（不改也能装）\n", outPath)
	fmt.Printf("  2. 安装：%s\n", installCommandFor(p, outPath, it.ID))
}

// installRun is one `twin install` invocation: the choices that are about the
// act of installing rather than about the instance being installed.
type installRun struct {
	p       inst.Paths
	force   bool
	dry     bool
	explain bool
}

// verbose is the long form. --explain asks for it; --dry-run has always been
// the "show me everything" mode.
func (r installRun) verbose() bool { return r.dry || r.explain }

// cmdInstall is the second step: read the instance definition, turn it into an
// instance on disk, and install that.
//
// It reads the file the user has been editing rather than re-deriving anything
// from flags — which is why it takes no derivation options at all. A flag and a
// file can disagree; only one of them is what the user just spent time on.
func cmdInstall(p inst.Paths, args []string) int {
	fs := newFlags("install")
	var (
		profilePath = fs.String("profile", "", "读哪一份配置（默认 按 <id> 推导："+defaultProfileLabel+"；与 -p 是同一个设置）")
		force       = fs.Bool("force", false, "覆盖已存在但不是 twin 生成的桌面项；也允许落盘时遮蔽同名的系统桌面项")
		dryRun      = fs.Bool("dry-run", false, "只打印，不落盘（文件清单、桌面项与实例定义正文、bwrap 调用、托盘覆盖这次会走哪条路、托盘扩展的当前状态）")
		explain     = fs.Bool("explain", false, "打印完整说明（推导值、路径映射的修改粒度、规则模板、候选状态目录、整份自检、手工验证命令）")
		// The machine-level half of install is a switch, not an object word: a
		// bare word is also a legal instance id, and the object position is the
		// one place where a reserved word would collide with a name a user can
		// pick — `twin install ext` would write the shell helper while `twin
		// remove ext` deletes an instance named ext.
		ext = fs.Bool("ext", false, "装入 shell 侧的托盘扩展（机器级，与实例无关；它被加载之后各实例的托盘图标改由它画；只与 --dry-run 同用）")
	)
	var shortProfile string
	fs.StringVar(&shortProfile, "p", "", "读哪一份配置（--profile 的简写；两个拼写同时给是用法错误）")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if *profilePath != "" && shortProfile != "" {
		fail("同时给了 -p 和 --profile：它们是同一个设置，请只留一个")
		return exitUsage
	}
	if len(pos) > 1 {
		fail(fmt.Sprintf("twin install 只接一个 <id>（收到 %q 与 %q）", pos[0], pos[1]))
		return exitUsage
	}
	profileGiven := *profilePath != "" || shortProfile != ""
	if len(pos) > 0 && profileGiven {
		// Two names for the same thing is the one shape twin refuses to guess
		// at: the id says where its definition *should* be, -p says where this
		// particular file *is*, and picking one would silently ignore the other.
		fail(fmt.Sprintf("同时给了 <id>（%q）和 -p —— 二选一：按 id 装就省略 -p，"+
			"配置在别处就用 -p 指出它的路径", pos[0]))
		return exitUsage
	}
	if len(pos) == 0 && !profileGiven && !*ext {
		// Not "the only instance that exists": guessing would install an
		// instance the user did not name, and the id is already printed by the
		// `twin gen` that created it. --ext is exempt because it names an
		// object of its own (the machine-level helper), not an instance.
		fail("缺少要装的实例：twin install <id>（twin list 看有哪些实例），" +
			"配置不在实例目录里时用 -p <文件>")
		return exitUsage
	}
	// --ext is the machine-level half of install: the helper serves every
	// instance and outlives all of them, so it is not a property of the file -p
	// names, and an <id> here would be an argument that gets silently dropped.
	// The instance switches would each be ignored the same way — the failure
	// mode this tool exists to prevent — so they are refused by name rather
	// than quietly dropped.
	if *ext {
		if profileGiven {
			fail("twin install --ext 不读配置：-p/--profile 说的是实例落盘那一步")
			return exitUsage
		}
		if len(pos) > 0 {
			fail(fmt.Sprintf("twin install --ext 不接受 <id>（收到 %q）——"+
				"助手扩展是机器级的：装一次就服务所有实例，与哪一个实例无关", pos[0]))
			return exitUsage
		}
		if *force {
			fail("twin install --ext 没有 --force：它只写扩展目录里的两个文件，覆盖不覆盖由内容比对决定")
			return exitUsage
		}
		if *explain {
			fail("twin install --ext 没有 --explain：它要写什么已经全在输出里了")
			return exitUsage
		}
		return installExt(p, *dryRun)
	}
	// The id names the instance and with it the definition's place; -p names a
	// file. Both end up as one path, which is the only thing the rest of this
	// function needs to know.
	path := firstNonEmpty(*profilePath, shortProfile)
	if path == "" {
		path = p.Profile(pos[0])
	}

	it, err := loadConfigFile(path)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	cfg, code := loadConfig(p)
	if code != exitOK {
		return code
	}
	run := installRun{p: p, force: *force, dry: *dryRun, explain: *explain}
	return run.apply(it, path, cfg)
}

// loadConfigFile reads the user-facing configuration and refuses a file that
// says something impossible, before anything on the machine changes.
//
// A missing file is turned into the command that writes one: "profile.yml: no
// such file" tells a first-time user nothing about `twin gen`.
func loadConfigFile(path string) (*inst.Instance, error) {
	it, err := inst.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("读不到 %s（%s）—— 先用 twin gen <source> 生成一份，"+
				"或用 -p 指定它的位置", path, err)
		}
		return nil, err
	}
	if err := it.Check(); err != nil {
		return nil, err
	}
	return it, nil
}

// apply installs the instance the configuration describes.
//
// `path` is the file that was read, and it travels down to Materialize as the
// definition: when it already is the instance's own profile.yml (the ordinary
// case — `twin gen` wrote it there), nothing writes it back, so the comments
// and the ordering the user is looking at survive the install. The archive role
// only appears when `path` is somewhere else, which is what `twin reinstall`
// replays.
func (r installRun) apply(it *inst.Instance, path string, cfg *config.Config) int {
	if err := generate.CheckInstall(r.p, it.ID, r.force); err != nil {
		fail(err.Error())
		return exitFail
	}
	// The source desktop entry supplies the two values the desktop item
	// inherits (Categories, Keywords). A configuration whose source has gone
	// missing is not fatal: both keys have a valid empty form and everything
	// else is in the file.
	insp := snapshotInspection(r.p, it)

	// Two resolves around one step, and the order between them is the point:
	// the first asks what this machine would *like* to do — install a profile
	// of twin's own — and the second asks what can actually go into the
	// launcher now that that step is behind us. Where it did not happen (no
	// password could be asked for, or the attempt failed), the second one lands
	// on a trampoline; and because the first route is what a real run aims at,
	// `--dry-run` deliberately stops at the first answer.
	rt := sandbox.Resolve(r.p, cfg, it.NestedNS, true)
	if code := ensureProfile(r.p, rt, r.dry); code != exitOK {
		return code
	}
	if !r.dry {
		rt = sandbox.Resolve(r.p, cfg, it.NestedNS, false)
	}
	printRuntime(rt)

	plan, err := sandbox.Build(it, r.p, rt)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	report := installReport{
		lead:    outStyle.Green(style.Glyph(style.LevelOK)+" 已安装") + " " + outStyle.Bold(it.ID),
		origin:  "配置 " + path,
		fix:     mapFix{file: path, replay: installCommandFor(r.p, path, it.ID)},
		verbose: r.verbose(),
	}
	if r.dry {
		printRehearsal(r.p, it, insp, plan, report)
		return exitOK
	}
	// Icon size is environment (this machine's DPI), so it comes from
	// config.toml every time instead of being frozen into the configuration.
	// `path` travels along as the definition: when it is the file in the
	// instance directory, install leaves it exactly as the user wrote it.
	return installAndCheck(r.p, it, insp, rt, plan, cfg.Defaults.IconSize, report, path)
}

// ensureProfile is the one step of an install that has to run as root.
//
// It only fires on a machine that restricts unprivileged user namespaces: the
// private bubblewrap copy cannot build one on its own there, so twin installs
// the profile that grants it, instead of depending on some Electron app being
// installed today. A password is the entire cost, and it can only be asked for
// where there is something to ask with — sudo on PATH and a terminal on stdin.
// Where either is missing (a script, a pipe, a hook) this step says so and
// installs nothing; the caller then resolves again, and that is where the
// trampoline becomes the route.
//
// Both entry points call it, not just `twin install`: a machine that needs this
// needs it equally when a snapshot is replayed, and leaving `reinstall` to print
// a hint pointing at a configuration file the user may not have any more would
// be a dead end dressed up as advice.
func ensureProfile(p inst.Paths, rt sandbox.Runtime, dry bool) int {
	if !rt.NeedsProfile {
		return exitOK
	}
	if dry {
		fmt.Printf("  %s\n", outStyle.Yellow("本机需要 AppArmor profile（要 root）：--dry-run 只报告，"+
			"真正安装时会用 sudo 要求你输入密码"))
		return exitOK
	}
	// The two reasons a prompt cannot happen are named, not lumped together:
	// they send the reader to different places (put sudo on PATH, versus come
	// back from a terminal).
	var whyNot string
	switch {
	case sysx.Which("sudo") == "":
		whyNot = "PATH 上没有 sudo"
	case !style.IsTerminal(os.Stdin):
		whyNot = "标准输入不是终端，没法让你输密码"
	}
	if whyNot != "" {
		fmt.Printf("  %s\n", outStyle.Yellow("本机需要 AppArmor profile（要 root），但"+
			whyNot+"：跳过自动安装"))
		return exitOK
	}
	// What is about to be written to /etc is named before sudo asks for a
	// password, not after: handing root to a line the user cannot see is
	// the one thing this prompt must never do.
	fmt.Printf("  %s\n", title("🔐 需要 root 装一份 AppArmor profile"))
	fmt.Printf("     将写 %s（%s 挂到 bwrap 副本上）。sudo 会要求输入密码 —— "+
		"不想给就 Ctrl-C，这一步跟着跳过，下面那一段会说明这次改走哪条路。\n\n",
		userns.ProfileFile, userns.ProfileName)
	note, err := userns.InstallProfile(rt.Bwrap, p.BinDir())
	if err != nil {
		warn(err.Error())
		fmt.Printf("  %s\n", outStyle.Yellow("自动安装没成功：下面按「这台机器上还没有 profile」重新选路"))
		return exitOK
	}
	fmt.Printf("  %s %s\n", outStyle.Green(style.Glyph(style.LevelOK)), note)
	return exitOK
}

// planInstance derives the instance and refuses the collisions that would make
// it a shadow of something else.
//
// Only `twin gen` calls it now. `twin install` and `twin reinstall` do not
// re-derive anything: one reads a configuration, the other replays a snapshot,
// and both of those already say which instance they are.
func planInstance(p inst.Paths, o generate.Options, insp *generate.Inspection, force bool) (*inst.Instance, generate.EntryBody, []generate.Derivation, error) {
	it, body, der, err := generate.Plan(p, o, insp)
	if err != nil {
		return nil, generate.EntryBody{}, nil, err
	}
	// Keywords are derived rather than demanded now, so this should never fire —
	// which is exactly why it is worth stating: if it ever does, the search
	// index would silently lose this instance. What is checked is the *entry
	// text*, because that is where keywords end up (override.desktop); the
	// definition no longer carries them.
	if body.Keywords == "" {
		warn("推不出搜索关键词（源桌面项既无 Keywords，也没有可用的 Name/程序名）——" +
			"搜索结果里可能找不到这个实例，建议补 --keywords 'k1;k2'")
	}
	// This only guards an explicit --as: a derived id is free by construction.
	if err := generate.CheckCollision(p, it.ID, force); err != nil {
		return nil, generate.EntryBody{}, nil, err
	}
	return it, body, der, nil
}

// mapsOrigin names the layer a path map came from, or "" when no layer claims
// it — a mapping no rule recognises can only have come from the snapshot the
// user edited, and saying so is how "why is this mapped?" gets an answer.
//
// Only a rule can claim one. The command line used to be a second source, and
// that was the wrong way round: a mapping is precisely the thing worth reading
// in a file before anything is installed (设计 §3.3).
func mapsOrigin(rule *config.Rule) string {
	if rule == nil {
		return ""
	}
	return orBuiltin(rule)
}

// ---------------------------------------------------------------- verify

func cmdVerify(p inst.Paths, args []string) int {
	fs := newFlags("verify")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if len(pos) < 1 {
		fail("缺少 <id>：twin list 看有哪些实例")
		return exitUsage
	}
	id := pos[0]
	it, err := inst.LoadProfile(p, id)
	if err != nil {
		// A legacy instance.toml already carries its own full explanation;
		// prefixing it with "读不到 <profile.yml>" would name the wrong file.
		if os.IsNotExist(err) {
			fail(fmt.Sprintf("读不到 %s：%v", p.Profile(id), err))
		} else {
			fail(err.Error())
		}
		return exitFail
	}
	// An instance directory can still lack its desktop entry: an install that
	// died between writing the snapshot and writing the identity anchor leaves
	// exactly that. It is one state, not five missing files, so it is reported
	// as one thing with the command that repeats the install.
	if !sysx.Exists(p.Desktop(id)) {
		fmt.Printf("%s %s 的桌面项不在：这个实例还没落盘（定义 %s 在，桌面项 / 图标 / 启动器不全），"+
			"或者上一次安装没走完\n",
			outStyle.Mark(style.LevelFail), it.ID, p.Profile(id))
		fmt.Printf("  落盘：twin install %s（等价的重放入口：twin reinstall %s）\n", id, id)
		return exitFail
	}
	rep := verify.Run(it, p)
	fmt.Printf("%s %s（level=%s）\n\n", outStyle.Bold("🪪 实例"), it.ID, it.Level)
	fmt.Print(formatFindings(rep))
	return exitFor(rep)
}

// formatFindings renders every check — that is what `twin verify` is for: the
// answer to "is this instance healthy?" on one screen, including the checks
// that are skipped because this machine cannot do them.
func formatFindings(rep *verify.Report) string {
	var b strings.Builder
	for _, f := range rep.Findings {
		writeFinding(&b, f)
	}
	return b.String()
}

// formatProblems renders only the checks that did not pass, for the runs that
// just wrote the files the other checks confirm. Everything else passed a
// moment ago by construction, and listing it as ✅ is a page of text between the
// paths and the only thing still to do; a check that is not OK is the one line
// in the run that needs the user.
//
// Manual findings are included: there is no separate "还差一步" list any more.
// They used to be collected into one block at the end, which is exactly what
// made a step nobody is required to take (the tray helper) look like the same
// kind of thing as one they are. Anything not OK is printed here, with its
// command, and the exit code is what distinguishes "come back from the desktop"
// from "you are broken".
func formatProblems(rep *verify.Report) string {
	var b strings.Builder
	for _, f := range rep.Findings {
		if f.Status == verify.StatusOK {
			continue
		}
		writeFinding(&b, f)
	}
	return b.String()
}

// writeFinding prints one check. The fix command appears only for a check that
// did not pass: under a ✅ it would hand the reader a command for a problem they
// do not have — and the tray check always has one ready, so every healthy
// instance would end on a gsettings line nobody asked for.
func writeFinding(b *strings.Builder, f verify.Finding) {
	fmt.Fprintf(b, "%s %-22s %s\n", outStyle.Mark(levelOf(f.Status)), f.Name, f.Detail)
	if f.Fix != "" && f.Status != verify.StatusOK {
		fmt.Fprintf(b, "   %-22s → %s\n", "", f.Fix)
	}
}

// levelOf maps a check result onto the shared verdict vocabulary.
func levelOf(s verify.Status) style.Level {
	switch s {
	case verify.StatusOK:
		return style.LevelOK
	case verify.StatusWarn:
		return style.LevelWarn
	default:
		return style.LevelFail
	}
}

// ---------------------------------------------------------------- list

func cmdList(p inst.Paths) int {
	items, err := inst.List(p)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	if len(items) == 0 {
		// "下没有实例目录"而不是"下为空"：那个目录里本来就常驻着共享的 bin/
		// 与扩展的产物，把"空"说出口就是把已经在那儿的东西说成不存在。
		fmt.Printf("%s 还没有任何实例（%s 下没有实例目录）\n",
			outStyle.Mark(style.LevelSkip), filepath.Join(p.Home, ".local/share/twin"))
		fmt.Printf("   做第一个：twin gen wechat && twin install\n")
		return exitOK
	}
	fmt.Printf("%s\n", title("📋 实例"))
	// One id column, not two: the alias *is* the id now (窗口 app_id 与托盘
	// SNI Id 都等于它), and a column that always repeats its neighbour teaches
	// the reader to skip both.
	fmt.Printf("%-16s %-10s %-24s %s\n", "id", "level", "程序", "桌面项")
	broken := 0
	for _, it := range items {
		desktop := filepath.Base(p.Desktop(it.ID))
		mark := ""
		// A row without a desktop entry means this instance has not been put on
		// disk yet — `twin gen` wrote the definition, or an install died half
		// way. Either way nothing in this row would start, which is the whole
		// point of the column.
		if !sysx.Exists(p.Desktop(it.ID)) {
			mark = "  " + outStyle.Yellow("← 还没落盘")
			broken++
		}
		fmt.Printf("%-16s %-10s %-24s %s%s\n", it.ID, it.Level,
			filepath.Base(it.Target), desktop, mark)
	}
	if broken > 0 {
		fmt.Printf("\n%s 有 %d 个没有桌面项（刚 gen 出来还没装，或上次安装没走完）：twin install <id>\n",
			outStyle.Mark(style.LevelWarn), broken)
	}
	// The one thing you come to this table to do next is get rid of a row, and
	// `remove` is the only verb that does it — so name it here rather than
	// leaving it to be discovered in --help.
	fmt.Printf("\n删除某一个：twin remove <id>（不带 --yes 会先列出来问你一次）\n")
	return 0
}

// ---------------------------------------------------------------- reinstall

// cmdReinstall puts an instance back on disk from its definition under
// ~/.local/share/twin/<id>/.
//
// It and `twin install <id>` do the same thing: one file, two entry points, and
// both read it where it lives. The verb stays because that is what the split
// wrap was named after and because "repeat this instance" is a different intent
// from "install the configuration I just generated" — an intent that decides
// what the verb is allowed to touch: no derivation flags, and no reading of the
// instance through some other path.
func cmdReinstall(p inst.Paths, args []string) int {
	fs := newFlags("reinstall")
	refresh := fs.Bool("refresh", false, "重读 config.toml / rules.d：只补定义里为空的字段、只追加缺的 bwrap token，绝不改写已有值（默认只读定义）；补上的值只用于这次落盘，那份文件不会被动")
	dryRun := fs.Bool("dry-run", false, "只打印，不落盘（只与 --ext 同用：重放定义是确定性的，没有可预演的东西）")
	// The same switch with the same meaning as in `install`: installing a
	// configuration and replaying the snapshot install wrote are one operation
	// through two doors (they share installAndCheck), so the machine-level half
	// has to be reachable from both — otherwise which commands exist would
	// depend on which door you happened to come in by.
	ext := fs.Bool("ext", false, "装入 shell 侧的托盘扩展（机器级，与实例无关；它被加载之后各实例的托盘图标改由它画；只与 --dry-run 同用）")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if *ext {
		// The helper serves every instance and outlives all of them, so an
		// <id> here would be an argument that gets silently dropped — the
		// failure mode this tool exists to prevent. --refresh reads the
		// snapshot, which is the instance half, so it goes for the same
		// reason. What is left is exactly `twin install --ext`.
		if len(pos) > 0 {
			fail(fmt.Sprintf("twin reinstall --ext 不接受 <id>（收到 %q）——"+
				"助手扩展是机器级的：装一次就服务所有实例，与哪一个实例无关", pos[0]))
			return exitUsage
		}
		if *refresh {
			fail("twin reinstall --ext 不读实例定义：--refresh 说的是实例重放那一步")
			return exitUsage
		}
		return installExt(p, *dryRun)
	}
	if *dryRun {
		// A replay is deterministic by design: it writes the bytes the install
		// that read this definition wrote, so a rehearsal has nothing to add
		// over `twin inspect <id>` — and silently ignoring the flag would leave
		// the reader thinking a dry run had happened.
		fail("twin reinstall 没有 --dry-run：重放是确定性的（产物与上次逐字节相同），" +
			"要看它会写什么用 `twin inspect <id>`；预演只对 --ext 有意义")
		return exitUsage
	}
	if len(pos) < 1 {
		fail("缺少 <id>：twin list 看有哪些实例")
		return exitUsage
	}
	id := pos[0]
	it, err := inst.LoadProfile(p, id)
	if err != nil {
		// Same as verify: a legacy snapshot's error already says everything.
		if os.IsNotExist(err) {
			fail(fmt.Sprintf("读不到 %s：%v", p.Profile(id), err))
		} else {
			fail(err.Error())
		}
		return exitFail
	}
	if err := it.Check(); err != nil {
		fail(err.Error())
		return exitFail
	}
	insp := snapshotInspection(p, it)

	// Replaying a definition is deterministic by design: it reads profile.yml
	// (and the CLI, which has nothing left to say here). Touching config.toml
	// must not silently rewrite a shelf of existing instances — that is what
	// --refresh is for, and even then it only fills fields the definition left
	// empty (设计 §3.5).
	if *refresh {
		cfg, code := loadConfig(p)
		if code != exitOK {
			return code
		}
		notes, err := generate.Refresh(it, insp, cfg)
		if err != nil {
			fail(err.Error())
			return exitFail
		}
		if len(notes) == 0 {
			fmt.Printf("--refresh：config.toml 与 rules.d 里没有可补的字段（定义已经写满）\n")
		}
		for _, n := range notes {
			fmt.Printf("--refresh 补上 %s\n", n)
		}
		if len(notes) > 0 {
			// The file is the user's document now, and Materialize leaves it
			// alone — so these supplements are this run's and nothing else.
			// Saying so beats letting the next replay quietly lose them.
			fmt.Printf("  这些值只用于这次落盘：那份定义不会被改写，想留住它们就照上面改文件\n")
		}
	} else if w := generate.SessionWarning(it); w != "" {
		// The definition records the decision as intent; if today's session
		// contradicts it, say so rather than replaying it in silence.
		warn(w + "（这是定义里的决定，不是这次的问题）")
	}

	// Same two resolves as `twin install` (see installRun.apply): aim at our
	// own profile first, then write the launcher for whatever route is left.
	rt := runtimeFor(p, it.NestedNS, true)
	if code := ensureProfile(p, rt, false); code != exitOK {
		return code
	}
	rt = runtimeFor(p, it.NestedNS, false)
	printRuntime(rt)

	plan, err := sandbox.Build(it, p, rt)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	// Same path as `twin install`: installing a configuration and replaying the
	// archive one wrote are the same operation with two entry points, and giving
	// them separate code would be two chances to drift.
	return installAndCheck(p, it, insp, rt, plan, iconSizeFor(p), installReport{
		lead:   outStyle.Green("✅ 已重新安装") + " " + outStyle.Bold(it.ID),
		origin: "实例定义 " + p.Profile(it.ID),
		// The file reinstall just read is the one to edit: whether it is the
		// definition the user keeps in the instance directory (the ordinary
		// case) or the record install wrote there, it is what this command
		// replays — and the check inside printPathMaps leaves it untouched.
		fix:     mapFix{file: p.Profile(it.ID), replay: "twin reinstall " + it.ID},
		verbose: false,
	}, p.Profile(it.ID))
}

// installAndCheck writes an instance's artefacts and reports what it found when
// reading them back. It is the shared back end of `twin install` and
// `twin reinstall`, which differ only in where the instance came from.
func installAndCheck(p inst.Paths, it *inst.Instance, insp *generate.Inspection, rt sandbox.Runtime,
	plan *sandbox.Plan, iconSize int, report installReport, definition string) int {
	res, err := generate.Materialize(p, it, insp, plan, iconSize, definition)
	if err != nil {
		fail(err.Error())
		return exitFail
	}

	// The tray override is written here, before the self-check, so the check
	// reads back what this run just did rather than a state from last time.
	// The shell-side helper is only *read*: it is machine-wide, so it has its
	// own switch (`twin install --ext`) and this run must not quietly change
	// something that outlives the instance it was called for. Its state is
	// reported by the self-check below, in the same line `verify` prints —
	// one fact, one place.
	trayLine := applyTray(p, it)
	rep := verify.Run(it, p)

	fmt.Printf("%s\n", report.lead)
	fmt.Printf("  桌面项   %s\n", res.Desktop)
	fmt.Printf("  图标     %s\n", res.Icon)
	fmt.Printf("  启动器   %s\n", res.Launch)
	fmt.Printf("  实例定义 %s\n", res.Profile)
	if res.Rule != "" {
		fmt.Printf("  L5 规则  %s\n", res.Rule)
	}
	fmt.Printf("  %s\n", trayLine)
	if report.verbose {
		// The derived values are all already in profile.yml, and their whole
		// point is to be visible — visible is profile.yml, not a wall of
		// output on every wrap.
		printDerived(report.der)
	}
	for _, n := range res.Notes {
		fmt.Printf("  · %s\n", n)
	}
	printPathMaps(p, it, insp, report.origin, report.fix, report.verbose)

	// Silence is the normal case: this run just wrote every file the passing
	// checks would be confirming. Only a check that is not OK has something to
	// say, and then the section appears with the command that fixes it.
	//
	// --explain is the exception, and the only one: there the whole report is
	// the answer the user asked for, so it prints all of it.
	if report.verbose {
		fmt.Printf("\n%s\n%s", title("🔍 自检"), formatFindings(rep))
	} else if problems := formatProblems(rep); problems != "" {
		fmt.Printf("\n%s\n%s", title("🔍 自检"), problems)
	}
	printNextSteps(p, it, report.verbose)

	if rt.NeedsProfile {
		// The identity is complete and every file is on disk; what is missing
		// is a machine-level permission twin refuses to grant itself out of
		// the user's sight. Exit 3 says exactly that, and the identity is not
		// broken — a `verify` would still pass.
		printProfileHint(p.BwrapCopy())
		if code := exitFor(rep); code != exitOK {
			return code
		}
		return exitHuman
	}
	return exitFor(rep)
}

// installReport is what the caller knows about the instance that the shared
// install path cannot work out for itself: how to introduce it, where its
// knowledge came from, and how much of it to print.
type installReport struct {
	lead    string
	der     []generate.Derivation
	origin  string
	fix     mapFix
	verbose bool
}

// mapFix names the file that holds *this* instance's own bind list, and the
// command that replays it.
//
// There is more than one right answer, which is why it is passed in rather than
// worked out from position: `twin gen` hands the user a configuration and
// `twin install` reads that same file, while `twin reinstall` replays the
// snapshot in the instance directory. Naming a file that is not there yet sends
// the reader looking for it.
type mapFix struct {
	file   string
	replay string
}

// installCommandFor spells the command that installs a given configuration.
//
// The id form wins whenever the file is where that id's definition belongs:
// `twin install wechat2` is the same instruction, shorter, and it keeps working
// after the file is moved by a later `twin gen` — while a `-p` line, which is
// what the alternative would print, points at one path and says nothing about
// the instance behind it.
func installCommandFor(p inst.Paths, path, id string) string {
	if id != "" && path == p.Profile(id) {
		return "twin install " + id
	}
	return "twin install -p " + path
}

// printNextSteps says the one thing that still has to happen by hand, and where
// to read the rest. The desktop entry is written after the program is already
// running, and GNOME will not recompute the identity of a window that already
// exists — so "restart it" is not advice, it is the last step of the wrap.
func printNextSteps(p inst.Paths, it *inst.Instance, verbose bool) {
	fmt.Printf("\n%s\n", title("👉 下一步"))
	if verbose {
		fmt.Printf("  1. 桌面项是后写的，GNOME 不会给已经存在的窗口重算归属 —— 请**重新启动**这个实例。\n")
		fmt.Printf("  2. 托盘图标已就位：上面「托盘覆盖」那条说了这次走的是哪条路（助手扩展，或宿主表兜底）。\n")
		fmt.Printf("     两条都是热生效，不用重登；🔍 自检 那一段里若有 ⚠️，按它给的那条命令跑一次。\n")
		fmt.Printf("  3. 想确认身份，看这两个值是否都等于 %q：\n", it.ID)
		fmt.Printf("     窗口 app_id ：WAYLAND_DEBUG=1 bash %s 2>&1 | grep set_app_id\n", p.Launch(it.ID))
		fmt.Printf("     托盘 SNI Id ：busctl --user list | grep -i statusnotifier  （名字里带 pid）\n")
		return
	}
	fmt.Printf("  1. 桌面项是后写的 —— 请**重新启动**这个实例，否则窗口还是旧的名字和图标。\n")
	fmt.Printf("  2. 确认身份与托盘：twin verify %s\n", it.ID)
	fmt.Printf("     改了实例定义要重放：twin reinstall %s\n", it.ID)
}

// printRehearsal is --dry-run's output: every file and every host-side change
// the next step would make. It is rendered by the same code the real run uses
// (it.Render, generate.EntryBytes, plan.String are the very calls Materialize
// makes) —
// a preview drawn by a second implementation would be a lie.
func printRehearsal(p inst.Paths, it *inst.Instance, insp *generate.Inspection, plan *sandbox.Plan, report installReport) {
	fmt.Printf("%s\n\n%s\n", title("🔍 预演（--dry-run，未写盘）"), title("📦 产物"))
	fmt.Printf("  桌面项   %s\n", p.Desktop(it.ID))
	fmt.Printf("  图标     %s\n", p.Icon(it.ID))
	fmt.Printf("  启动器   %s\n", p.Launch(it.ID))
	// The definition is listed with the artefacts because it is one of the paths
	// this instance lives at — but when it is the file we read, nothing writes
	// it, and the line has to say so: "产物" means "这一趟会写的东西".
	if report.fix.file == p.Profile(it.ID) {
		fmt.Printf("  实例定义 %s（已在原处，不重写）\n", p.Profile(it.ID))
	} else {
		fmt.Printf("  实例定义 %s（存档记录）\n", p.Profile(it.ID))
	}
	fmt.Printf("  私有状态 %s\n", p.StateDir(it.ID))
	if it.Level == inst.LevelRedirect {
		fmt.Printf("  L5 规则  %s\n", p.RuleFile(it.ID))
	}
	if plan.Copy != "" {
		fmt.Printf("  bwrap 副本 %s\n", plan.Copy)
	}
	if plan.Bootstrap != "" {
		fmt.Printf("  跳板脚本  %s\n", plan.Bootstrap)
	}
	fmt.Printf("\n%s\n  level=%s  app_id / 托盘 Id = %q\n",
		title("🪪 身份"), it.Level, it.ID)
	// The tray override is host state (or the helper's file), not one of the
	// artefacts above, but it is still part of what a real run would change —
	// so the rehearsal has to show it, and it has to show *which mechanism*
	// the real run would use. That is planTray, the very decision applyTray
	// takes: previewing a different branch than the one that runs would be a
	// lie about the only part of a wrap that depends on the machine.
	if !it.TrayOverride {
		fmt.Printf("\n%s\n  跳过（override.tray = false）：宿主表与助手扩展读的那份表都不写\n",
			title("🔔 托盘覆盖"))
	} else {
		plan := planTray(p, it)
		fmt.Printf("\n%s\n", title("🔔 托盘覆盖"))
		if plan.File != "" {
			fmt.Printf("  将写入 %s（助手扩展读它画图标，不写宿主表）\n", plan.File)
			fmt.Printf("  图标     %s\n", p.Icon(it.ID))
			fmt.Printf("  并删掉宿主表 %s 里 %q 的那一行（若还在）：同一个图标不留两个来源\n",
				tray.Key, it.ID)
		} else {
			fmt.Printf("  将写入宿主表 %s / %s [('%s', '%s', '%s')]（合并，不覆盖别人的条目）\n",
				tray.Schema, tray.Key, it.ID, p.Icon(it.ID), p.Icon(it.ID))
		}
		// The helper itself is machine-wide, so this run only *looks* at it —
		// and says what it found in the very words the real run's 自检 section
		// uses (verify.TrayExtFinding). The rehearsal prints no other part of
		// that report, so this is not one fact twice; a sentence drawn up here
		// instead would be a second answer to one question.
		var sb strings.Builder
		writeFinding(&sb, verify.TrayExtFinding(p, it))
		for _, line := range strings.Split(strings.TrimRight(sb.String(), "\n"), "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
	printDerived(report.der)
	printPathMaps(p, it, insp, report.origin, report.fix, report.verbose)
	if plan.Bwrap != "" {
		fmt.Printf("\n%s\n%s\n", title("🧱 bwrap 调用"), plan.String())
	}
	// The definition, and what this run does with it. When the file that was
	// read is already the one in the instance directory, Materialize leaves it
	// untouched (it is the user's document, and a round trip through the parser
	// would drop their comments) — showing a body under "will be written" would
	// be a lie, so that case says so in words instead.
	if report.fix.file == p.Profile(it.ID) {
		fmt.Printf("\n%s\n  这一份原样保留，不重写（它就是你手上那份实例定义）\n",
			title("📄 实例定义（"+p.Profile(it.ID)+"）"))
	} else {
		// Written in the archive role because that is what install puts in the
		// instance directory: a preview drawn from a different renderer is a lie.
		body, err := it.RenderAs(inst.RoleSnapshot)
		if err != nil {
			fail(err.Error())
			return
		}
		fmt.Printf("\n%s\n%s", title("📄 存档记录（"+p.Profile(it.ID)+"）"), indent(body, "  "))
	}
	// The desktop entry is a copy of a file this time, not something rendered
	// from the instance: so the rehearsal shows the bytes that would be copied —
	// the file on disk when it is there, the text install would generate when it
	// is not. Rendering it from the instance instead would preview a file that
	// install never writes.
	entry, _ := generate.DeriveBody(insp, it.ID, generate.Options{})
	text := generate.EntryBytes(p, it, entry)
	if it.DesktopOverride != "" {
		if onDisk, err := os.ReadFile(it.DesktopOverride); err == nil {
			text = onDisk
		}
	}
	fmt.Printf("\n%s\n%s", title("📄 桌面项（"+filepath.Base(p.Desktop(it.ID))+"，逐字复制 "+it.DesktopOverride+"）"),
		indent(text, "  "))
	if it.Level == inst.LevelRedirect {
		rule, err := generate.RuleJSON(it, p)
		if err != nil {
			fail(err.Error())
			return
		}
		fmt.Printf("\n%s\n%s", title("📄 L5 规则（"+filepath.Base(p.RuleFile(it.ID))+"）"), indent(rule, "  "))
	}
}

// snapshotInspection re-reads the source desktop entry for the two values the
// desktop entry inherits from it (Categories, Keywords). A snapshot whose
// source has gone missing is not fatal: replaying the snapshot is the point,
// and both keys have a valid empty form.
func snapshotInspection(p inst.Paths, it *inst.Instance) *generate.Inspection {
	if it.Source != "" && sysx.Exists(it.Source) {
		if got, err := generate.Inspect(p, it.Source); err == nil {
			return got
		} else {
			warn("重新解析源桌面项失败（用空值继续）：" + err.Error())
		}
	} else {
		warn("profile.yml 里的 source 不存在，Categories/Keywords 将为空")
	}
	return &generate.Inspection{Entry: &desk.Entry{}}
}

// runtimeFor and iconSizeFor resolve the environment layer for `reinstall`,
// where a missing or malformed config.toml must not stop a replay: replaying a
// snapshot reads the snapshot, and the config is optional garnish. `--refresh`
// is the path where a malformed file is reported instead.
//
// tryProfile travels straight into sandbox.Resolve: it says whether installing
// our own AppArmor profile is still on the table for this resolution.
func runtimeFor(p inst.Paths, needNestedNS, tryProfile bool) sandbox.Runtime {
	cfg, err := config.Load(p)
	if err != nil {
		return sandbox.Runtime{}
	}
	return sandbox.Resolve(p, cfg, needNestedNS, tryProfile)
}

// printProfileHint is what is left when neither route is available: no password
// could be asked for (no sudo on PATH, or stdin is not a terminal), or the
// attempt itself failed — and no Electron binary is installed to borrow an
// AppArmor label from either.
//
// `twin install` runs the two commands below itself through sudo whenever it
// can, and falls back to a trampoline when it cannot. Printing them is the last
// resort, not the plan: the commands are exactly what was attempted, so the
// user can run them and get the same result.
func printProfileHint(copyPath string) {
	fmt.Printf("\n%s\n", title("✋ 还差一步：证书这关要走一次 root"))
	fmt.Printf("  这个程序运行时要自己建一个嵌套 user namespace，而内核只放行\n")
	fmt.Printf("  「profile 里写了 userns 许可」的程序（%s）。\n", userns.SysctlSource)
	fmt.Printf("  私有 bwrap 副本故意不带 profile，所以它自己建不了。\n\n")
	fmt.Printf("  两条路，任选其一：\n")
	fmt.Printf("    1) 在有终端的环境里再跑一次 twin install（或 twin reinstall <id>）——\n")
	fmt.Printf("       它会用 sudo 替你装好下面这份 profile；也可以手工把它存成 %s 并加载：\n\n", userns.ProfileFile)
	for _, line := range strings.Split(strings.TrimRight(userns.ProfileHint(copyPath), "\n"), "\n") {
		fmt.Printf("       %s\n", line)
	}
	fmt.Printf("\n    2) 装一个 Electron 应用（VS Code 就够了）当跳板，再跑一次 twin reinstall <id>；\n")
	fmt.Printf("       下一次安装会自己挑中它 —— 借标签不需要任何权限。\n\n")
}

// printRuntime says how this machine will launch the sandbox, but only when
// that is not the boring answer. Silence means "the system bubblewrap, no
// trampoline" — the case with nothing worth reading.
func printRuntime(rt sandbox.Runtime) {
	if len(rt.Notes) == 0 {
		return
	}
	fmt.Printf("%s\n", title("🧱 沙箱方案"))
	for _, n := range rt.Notes {
		fmt.Printf("  · %s\n", n)
	}
	fmt.Println()
}

func iconSizeFor(p inst.Paths) int {
	cfg, err := config.Load(p)
	if err != nil {
		return 0
	}
	return cfg.Defaults.IconSize
}

// ---------------------------------------------------------------- unwrap

func cmdRemove(p inst.Paths, args []string) int {
	fs := newFlags("remove")
	yes := fs.Bool("yes", false, "不再问，直接删（脚本 / CI 用）；不加它会在终端里先列清单再问一次，标准输入不是终端时只预览")
	purge := fs.Bool("purge", false, "连私有状态目录一起删 —— 唯一不可逆的部分（默认保留，下次同名重建接着用）")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if len(pos) < 1 {
		fail("缺少 <id>：twin list 看有哪些实例（不带 --yes 会先列出来问你一次）")
		return exitUsage
	}
	id := pos[0]
	if err := inst.ValidateID(id); err != nil {
		fail(err.Error())
		return exitUsage
	}
	if !*yes {
		// The preview is the whole safety mechanism: it names exactly what the
		// next answer would delete, including the one thing that is not a file
		// (the `custom-icons` row) and the one thing that is irreversible (--purge).
		printRemovePreview(p, id, *purge)
		if !style.IsTerminal(os.Stdin) {
			// A question nobody can answer is a hang, not a confirmation — the
			// same rule the sudo step follows. A script or a pipe gets the
			// preview plus the explicit command, so nothing is deleted behind
			// its back and nothing ever waits on a prompt.
			fmt.Printf("\n%s\n", outStyle.Dim("标准输入不是终端（脚本 / 管道）：只预览，什么都没删"))
			fmt.Printf("确认无误：twin remove %s --yes\n", id)
			return exitOK
		}
		fmt.Printf("\n删除以上内容？[y/N] ")
		ok, err := readConfirmation(os.Stdin)
		if err != nil {
			fail("读确认时出错：" + err.Error())
			return exitFail
		}
		if !ok {
			// Anything but an explicit yes — "n", a typo, an empty line, Ctrl-D —
			// means no. For the one irreversible verb the safe reading of an
			// ambiguous answer is the only defensible one.
			fmt.Printf("%s 已取消，什么都没删\n", outStyle.Mark(style.LevelSkip))
			return exitOK
		}
	}
	removed, err := generate.Unwrap(p, id, *purge)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	if len(removed.Paths) == 0 {
		fmt.Printf("%s 没有可删的东西（%s 从未生成过）\n", outStyle.Mark(style.LevelSkip), id)
	}
	for _, f := range removed.Paths {
		fmt.Printf("%s 已删除 %s\n", outStyle.Red("🗑️"), f)
	}
	for _, k := range removed.Kept {
		fmt.Printf("%s 保留 %s\n", outStyle.Dim("💾"), k)
	}
	if line := dropTray(id, false); line != "" {
		fmt.Println(line)
	}
	return exitOK
}

// printRemovePreview lists what a confirmed `twin remove` would delete. It is the
// context the question is asked against, so it stays a separate step from asking:
// a preview that only appears when someone already typed --yes would be a preview
// nobody reads.
func printRemovePreview(p inst.Paths, id string, purge bool) {
	fmt.Printf("%s\n", title("🗑️ 将要删除"))
	for _, f := range []string{p.Desktop(id), p.Icon(id), p.InstanceDir(id), p.RuleFile(id)} {
		if !sysx.Exists(f) {
			fmt.Printf("  %s %s\n", outStyle.Dim("➖"), outStyle.Dim(f+"（不存在）"))
			continue
		}
		fmt.Printf("  %s %s\n", outStyle.Green("✅"), f)
	}
	state := p.StateDir(id)
	if purge {
		fmt.Printf("  %s %s\n", outStyle.Red("💣"), state+"（私有数据，--purge：不可逆地删掉）")
	} else {
		fmt.Printf("  %s %s\n", outStyle.Dim("💾"), state+"（私有数据，保留 —— 要一起删加 --purge）")
	}
	fmt.Printf("  %s %s\n", outStyle.Dim("🔔"), dropTray(id, true))
}

// readConfirmation reads the one answer `twin remove` takes: y / yes, any case.
// Everything else is a no, including an empty line and EOF (Ctrl-D) — this is the
// last question before irreversible deletion, so an answer it cannot read has to
// fall on the side that deletes nothing.
func readConfirmation(in io.Reader) (bool, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return false, nil
}

// ---------------------------------------------------------------- output

// applyTray points the tray at this instance's icon and returns the one line
// describing what happened.
//
// Two mechanisms can put a twin icon in the tray, and this function is where
// the choice between them is made — once, from a fact about the machine
// (is the shell-side helper loaded? see tray.ExtState.Usable) rather than from
// anything the user has to remember to pass:
//
//   - the helper is loaded ⇒ it reads the neutral file table (tray/table.go)
//     and applies the icon itself. twin writes that file, and deletes the row
//     an earlier install may have left in the host's table: two mechanisms
//     over one tray item is one too many, and the row is pointless once the
//     helper draws the icon. Nothing here needs the session bus.
//   - the helper is missing, not enabled, blocked or unreadable ⇒ twin writes
//     the host's table, as it always has. That write needs the session bus,
//     so this is the branch that can honestly end in "not done yet".
//
// The switch is deliberately conservative: the fallback is the path that works
// on every machine, so anything short of "loaded, and I could confirm it"
// takes it. What the run actually did is printed either way; which path is in
// force never has to be inferred from silence.
//
// The tray override is the only state twin touches outside the user's home,
// and twin touches it itself, at wrap time. The alternative — printing a
// gsettings command for the user to run once, before wrapping — would make a
// wrapper depend on preparation done by hand on each machine, which is exactly
// what this tool exists to remove: a copied-over instance is regenerated by
// running twin again, and that run has to leave the machine in the finished
// state.
//
// Failure here is not fatal. Without a session bus (an installer, a headless
// run, a script) the host table cannot be written, and the honest answer is
// the command plus exit code 3, which `verify` supplies.
func applyTray(p inst.Paths, it *inst.Instance) string {
	if !it.TrayOverride {
		return "托盘覆盖 跳过（override.tray = false）"
	}
	// level=none does not rename anything, so the program's StatusNotifierItem
	// id is whatever the program chose — the row is a guess. It is still worth
	// writing (the desktop basename often coincides) but not worth promising.
	// Both mechanisms key on that same id, so the caveat is the same either way.
	vague := ""
	if !it.Level.Renames() {
		vague = "（level=none 没有改名：托盘的 SNI Id 由程序自己决定，这一条未必匹配得上）"
	}

	plan := planTray(p, it)
	if plan.File == "" {
		return applyTrayHost(it, p.Icon(it.ID), "兜底路径："+plan.Why, vague)
	}
	return applyTrayFile(p, it, plan.File, vague)
}

// trayPlan is the decision applyTray takes, taken in one place so that the
// rehearsal and the real run cannot disagree about it.
//
// A preview drawn from a second reading of the machine would be a second
// implementation (see the note on printRehearsal), and the case it would get
// wrong is exactly the interesting one: a machine whose helper is installed
// but not loaded.
type trayPlan struct {
	// File is the helper's table file, empty when the host's table is the one
	// in force.
	File string
	// Why is the extension state that put this instance on the host's table,
	// empty on the helper's path.
	Why string
}

func planTray(p inst.Paths, it *inst.Instance) trayPlan {
	st := tray.CheckExt(p)
	if st.Usable() {
		return trayPlan{File: p.TrayTable(it.ID)}
	}
	return trayPlan{Why: st.ShortWhy()}
}

// applyTrayHost is the fallback: the host's own override table, read-modified
// and read back by tray.Ensure (see internal/tray/tray.go for why every one of
// those three steps is mandatory).
func applyTrayHost(it *inst.Instance, icon, why, vague string) string {
	oc, err := tray.Ensure(it.ID, icon)
	switch {
	case err != nil:
		return "托盘覆盖 未写入宿主表（" + err.Error() + "）—— 自检里会给出要跑的那条命令" + vague
	case oc.Action == tray.ActionNoHost:
		return "托盘覆盖 跳过（" + oc.Detail + "）" + vague
	case oc.Action == tray.ActionWritten:
		return "托盘覆盖 已写入宿主表 " + tray.Key + "（" + why + "；该表热重载，立刻生效）" + vague
	default:
		return "托盘覆盖 已是最新（宿主表 " + tray.Key + "；" + why + "）" + vague
	}
}

// applyTrayFile is the helper's path: the instance's row in the neutral table,
// and the removal of whatever the host's table still claims about it.
func applyTrayFile(p inst.Paths, it *inst.Instance, table, vague string) string {
	out, err := tray.EnsureOverride(p, tray.NewOverride(it.ID, p.Icon(it.ID)))
	if err != nil {
		return "托盘覆盖 未写入 " + table + "（" + err.Error() + "）" + vague
	}
	line := "托盘覆盖 已写入 " + table + "（助手扩展读它画图标，不再写宿主表）"
	if !out.Written {
		line = "托盘覆盖 已是最新（" + table + "；助手扩展读它画图标）"
	}
	// The host table must not also carry a row for this instance. Both would
	// set the same image, so this is not a visible conflict — it is the "two
	// sources for one fact" the switch exists to remove, and a row left behind
	// is the row that keeps pointing at an icon after the instance is gone.
	switch oc, err := tray.Remove(it.ID); {
	case err != nil:
		return line + "；宿主表里那一行没能删掉（" + err.Error() + "）—— 自检里会给出要跑的那条命令"
	case oc.Action == tray.ActionRemoved:
		return line + "；并删掉了宿主表里的那一行"
	default:
		return line
	}
}

// installExt is `twin install --ext`: the one place that puts the shell-side
// half of the tray override on this machine.
//
// It is separate from the instance install on purpose. The helper is
// machine-wide — one copy serves every wrapped program, and it outlives each of
// them — so writing it from `twin install` made an optional fix a side effect of
// an unrelated action, repeated on every replay and indistinguishable in the
// output from the artefacts that really were this instance's. The install path
// only *checks* it now and names the missing piece; this command is what changes
// it.
//
// The one outcome that is not "done" is "the files are there but the shell has
// not scanned them yet": that is a login, not a retry, and the run spells the
// three steps out instead of calling them a failure. It stays exit code 0: the
// helper is an enhancement on top of a fallback that already draws the icon, so
// installing it does not leave an outstanding task the way writing the host's
// table without a session bus does.
func installExt(p inst.Paths, dry bool) int {
	dir := tray.ExtDir(p)
	if dry {
		fmt.Printf("%s\n", title("🔍 预演（--dry-run，未写盘）"))
		fmt.Printf("  扩展目录 %s（内容相同则整个跳过，不碰 mtime —— shell 只在启动时读它）\n", dir)
		for _, name := range tray.ExtFiles() {
			fmt.Printf("  %s %s\n", outStyle.Dim("·"), filepath.Join(dir, name))
		}
		fmt.Printf("  作用     托盘图标由它自己画（读 twin 写的那份文件表，不写宿主扩展的 dconf 键）\n")
		fmt.Printf("  然后请 shell 启用它：gnome-extensions enable %s\n", tray.ExtUUID)
		fmt.Printf("  它是机器级的：装一次就服务所有实例，与 -p 那份配置无关。\n")
		return exitOK
	}

	oc, err := tray.EnsureExt(p)
	if err != nil {
		fail("装入托盘扩展失败：" + err.Error())
		return exitFail
	}

	lead := outStyle.Green(style.Glyph(style.LevelOK)+" 托盘扩展") + " " + outStyle.Bold(tray.ExtUUID)
	fmt.Printf("%s\n", lead)
	// Written or already right is the one thing the reader cannot tell from the
	// exit code, and it is the whole reason ExtOutcome carries a flag for it.
	if oc.Installed {
		fmt.Printf("  文件     %s（本次写入／更新）\n", dir)
	} else {
		fmt.Printf("  文件     %s（与内嵌副本逐字节相同，没碰）\n", dir)
	}
	fmt.Printf("  作用     托盘图标由它自己画（读 twin 写的那份表，不写宿主扩展的 dconf 键）\n")
	switch {
	case oc.Enabled:
		fmt.Printf("  启用     已启用 —— 之后 install / reinstall 都走这条路\n")
	case oc.NeedsRelogin:
		if oc.Detail != "" {
			fmt.Printf("  启用     还没有 —— 在装的当下做不到（%s）\n", oc.Detail)
		} else {
			fmt.Printf("  启用     还没有 —— 在装的当下做不到\n")
		}
		fmt.Printf("\n%s\n", title("🔔 让它生效还要三步（可选增强：不做也不影响任何实例的托盘图标）"))
		fmt.Printf("  1. 注销重登一次：shell 只在启动时扫一遍扩展目录，它现在还不认识这个 uuid。\n")
		fmt.Printf("  2. gnome-extensions enable %s\n", tray.ExtUUID)
		fmt.Printf("  在此之前，各实例仍走宿主覆盖表兜底 —— 图标是对的，只是比面板图标小一圈。\n")
		fmt.Printf("  3. 重登之后，让已装的实例改走这条路：twin reinstall <id>（一条命令一个实例）。\n")
		return exitOK
	default:
		fmt.Printf("  启用     状态未知\n")
	}
	// The instances move over on their own install, not here: rewriting every
	// instance's table is an action per instance, and this command serves the
	// machine (see the note above installExt). Saying so is the difference
	// between "the tray icon is still small" being a bug and being a step the
	// user has not run yet.
	fmt.Printf("  已装的实例不会自己搬家：下一次 twin install / reinstall 时它们才改走这条路。\n")
	return exitOK
}

// dropTray removes this instance's row from the host table. The table is global
// and the row is a claim about a program that is about to stop existing, so
// leaving it behind would keep pointing the tray at an icon we are deleting.
// A missing row, or a machine with no tray host, is not worth failing an unwrap
// over: the files are what the user asked to be rid of.
func dropTray(id string, dry bool) string {
	if dry {
		return "宿主托盘覆盖表里的 " + id + " 条目（若有）"
	}
	oc, err := tray.Remove(id)
	switch {
	case err != nil:
		warn("托盘覆盖表里的条目没删掉（" + err.Error() + "）—— 它会继续指向已删除的图标，请在图形会话里手工清理")
		return ""
	case oc.Action == tray.ActionRemoved:
		return "已从宿主托盘覆盖表移除 " + id
	}
	return ""
}

// printDerived shows every value that was filled in from the source program,
// with the reason. A default nobody can see is a default nobody can correct.
//
// The key column is sized to the batch rather than fixed: these are dotted
// config paths (`override.desktop Keywords`, `isolation.nested_ns`) whose width
// follows the schema, and a fixed column turns "one key got longer" into a table
// where the values no longer line up.
func printDerived(der []generate.Derivation) {
	if len(der) == 0 {
		return
	}
	width := 0
	for _, d := range der {
		if n := len([]rune(d.Key)); n > width {
			width = n
		}
	}
	fmt.Printf("\n%s\n", title("🧩 推导出的默认值（都可用选项覆盖）"))
	for _, d := range der {
		fmt.Printf("  %-*s %s   ← %s\n", width, d.Key, d.Value, d.Source)
	}
}

// printPathMaps says the one part of a wrap that is knowledge about the
// *program* rather than about its identity: which of the program's own data
// directories twin redirected, and where they went.
//
// It is spelled out because that knowledge is the part most likely to be wrong
// for a program twin has never seen — and a wrong map is silent until two
// instances fight over a lock file, or one of them quietly reads the other's
// profile.
//
// verbose adds the part that only matters once the user has decided one of
// those maps *is* wrong: which directories twin did not map but could have, the
// two files that hold this knowledge, and the paste-ready template. Advice
// belongs there and not in the short form: the run just wrote everything in the
// list above, so on the normal path — nothing wrong — a page of "here is how you
// would fix it" is text between the paths and the one thing left to do.
//
// origin is named by the caller rather than derived here, because "where did
// this mapping come from" has an answer `gen`, `install` and `reinstall`
// disagree about: gen knows the rule and the command line, install is reading a
// file the user may have edited by hand, and reinstall is replaying the archive
// an earlier install wrote.
func printPathMaps(p inst.Paths, it *inst.Instance, insp *generate.Inspection,
	origin string, fix mapFix, verbose bool) {
	key := ruleKeyFor(insp)
	rulePath := filepath.Join(p.RulesDir(), key+".toml")
	maps := dataMaps(it)

	fmt.Printf("\n%s\n", title("📁 路径映射"))
	if len(maps) == 0 {
		// "twin redirected nothing" is a fact worth stating: it is the normal
		// case for an unknown program, and the reason two instances of it may
		// start interfering with each other. It is also the one consequence the
		// rest of the file does not spell out for this program.
		fmt.Printf("  0 条：数据目录与源程序共用（本实例只做身份包裹）。\n")
		if insp.Rule == nil {
			fmt.Printf("  %s\n", outStyle.Yellow("⚠️ 这个程序没有规则：它若有单实例锁或共享配置"+
				"（Electron / Qt / GTK 系很常见），两个实例会互相干扰。"))
		}
	} else {
		if origin == "" {
			fmt.Printf("  %d 条\n", len(maps))
		} else {
			fmt.Printf("  %d 条，来源：%s\n", len(maps), origin)
		}
		for _, b := range maps {
			// Printed in the file's own spelling, so the line can be pasted into
			// profile.yml or rules.d without a translation step in between.
			// `:try` rides along as a trailing note rather than a tag of its
			// own: it says nothing about *what* is mapped, only about what
			// happens when the source is not there.
			try := ""
			if b.Try {
				try = "   （:try —— 源不在就整条跳过，不报错）"
			}
			switch {
			case b.ReadOnly:
				fmt.Printf("  %s  %s%s\n", outStyle.Cyan("只读"), b, try)
			case b.Dev:
				fmt.Printf("  %s  %s   （带设备访问：--dev-bind）%s\n", outStyle.Cyan("设备"), b, try)
			case b.Source == b.Target:
				fmt.Printf("  %s  %s   （原地升回可写，数据不搬家）%s\n", outStyle.Cyan("共用"), b, try)
			default:
				fmt.Printf("  %s  %s%s\n", outStyle.Green("私有"), b, try)
			}
		}
	}
	if verbose {
		printStateHints(p, insp, it, key)
		printMapFix(it, key, rulePath, fix)
		printRuleTemplate(p, it, key, maps)
	}
}

// dataMaps is the instance's list of data mappings: `bind` without the root
// mount.
//
// The root is written into that node because that is where mounts are written,
// but it is not one of the directories this program's data lives in — and this
// report counts them, prints them, and hands them over for pasting into
// rules.d. Left in, `/:/:dev` would be twin's own base line wearing the user's
// data layout as a disguise. It also has to go before uncoverHints, which
// compares candidate paths against a mapping's *target*: the root's target is
// `/`, every path on the machine sits under that, and the "state directories you
// may want to isolate" section would go quiet for good.
func dataMaps(it *inst.Instance) []inst.Bind {
	out := make([]inst.Bind, 0, len(it.Binds))
	for _, b := range it.Binds {
		if b.IsRootMount() {
			continue
		}
		out = append(out, b)
	}
	return out
}

// printMapFix names the two files that can hold this program's knowledge. It is
// what turns "that mapping is wrong" into an action, so it survives into the
// short form.
//
// The rules.d route is the same in every entry point, because it is about the
// instances the user has not created yet. The route for *this* instance is the
// one that differs — see mapFix — so the caller supplies it.
func printMapFix(it *inst.Instance, key, rulePath string, fix mapFix) {
	fmt.Printf("  改：%s —— 影响这个程序以后新建的实例（twin rules --init %s 生成草稿）\n",
		rulePath, key)
	fmt.Printf("      只改这一个：编辑 %s 里的 bwrap.bind，再 %s\n", fix.file, fix.replay)
}

// printStateHints lists directories the program is likely to keep state in that
// twin is *not* redirecting.
//
// This is the answer to the question a user asks when a program has no rule:
// "nothing was mapped — so what should have been?" It is deliberately the
// weakest evidence in the whole pipeline (a directory that exists, and nothing
// else), so it is labelled as such and never applied; acting on it is what
// `twin rules --init` is for.
func printStateHints(p inst.Paths, insp *generate.Inspection, it *inst.Instance, key string) {
	const maxHints = 6
	hints := uncoverHints(generate.SuggestStateDirs(p, insp), dataMaps(it))
	if len(hints) == 0 {
		return
	}
	fmt.Printf("\n  %s\n", title("💡 这个程序可能的状态目录（未应用，仅供参考）"))
	for i, h := range hints {
		if i == maxHints {
			fmt.Printf("    …还有 %d 个（twin rules --init %s 会全部写上）\n", len(hints)-i, key)
			break
		}
		fmt.Printf("    %-44s %s\n", h.Path, h.Evidence)
	}
	fmt.Printf("    要隔离就先生成一份规则草稿：twin rules --init %s\n", key)
}

// uncoverHints drops what the instance already maps. Proposing "maybe isolate
// ~/.xwechat" directly under a line saying ~/.xwechat is already isolated would
// teach the user to stop reading the list.
func uncoverHints(hints []generate.StateHint, binds []inst.Bind) []generate.StateHint {
	var out []generate.StateHint
	for _, h := range hints {
		covered := false
		for _, b := range binds {
			// Against the target, not the source: a hint is a path the program
			// looks for, and that is exactly what a target is.
			if h.Path == b.Target || strings.HasPrefix(h.Path, strings.TrimSuffix(b.Target, "/")+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, h)
		}
	}
	return out
}

// printRuleTemplate prints a paste-ready rules.d file built from the maps that
// are actually in effect. "Copy this and change one line" is the whole edit;
// an empty template would make the user retype paths twin already knows, and
// retyping is where the typos come from.
func printRuleTemplate(p inst.Paths, it *inst.Instance, key string, binds []inst.Bind) {
	fmt.Printf("\n  %s\n", title("📋 照抄下面这份（就是规则文件的全部内容）："))
	fmt.Printf("\n    app = %q\n", key)
	fmt.Printf("\n    bind = [\n")
	if len(binds) == 0 {
		fmt.Printf("      %q,   # ← 换成这个程序真正的状态目录\n",
			"~/.local/state/twin/{id}/config:~/.config/"+key)
	} else {
		for _, b := range binds {
			b.Source = dedupStateDir(p, it, b.Source)
			fmt.Printf("      %q,\n", b.String())
		}
	}
	fmt.Printf("    ]\n")
	fmt.Printf("\n  源:目标 ⇒ 私有副本，两个实例互不干扰；单写一条路径 ⇒ 与该目录共用，只把只读升回可写；\n")
	fmt.Printf("  源:目标:ro ⇒ 只读挂载；源:目标:dev ⇒ 带设备访问（--dev-bind，给设备节点目录用）；\n")
	fmt.Printf("  末尾再加 :try ⇒ 源不在时整条跳过、不报错（要写全模式：源:目标:ro:try）。\n")
}

// dedupStateDir rewrites this instance's state directory back to the {id}
// placeholder. A template that froze "wechat2" into the path would make the
// pasted rule serve exactly one instance — the opposite of what a rules.d file
// is for (设计 §3.3), and the same reason profile.yml stores `~` paths.
func dedupStateDir(p inst.Paths, it *inst.Instance, s string) string {
	return strings.ReplaceAll(s, p.RelStateDir(it.ID), "~/.local/state/twin/{id}")
}

// ruleKeyFor names the rules.d file the user would write to correct this
// program's knowledge. It prefers the key a rule was actually found under, and
// otherwise falls back to the desktop entry's basename — the name a person
// calls the program, and the first name RuleKeys tries.
func ruleKeyFor(insp *generate.Inspection) string {
	if insp.RuleKey != "" {
		return insp.RuleKey
	}
	return strings.TrimSuffix(filepath.Base(insp.SourceDesktop), ".desktop")
}

// indent prefixes every line of a file body, for printing it under a heading.
func indent(b []byte, prefix string) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// fail and warn colour from the stderr style, not the stdout one: the two
// streams are redirected independently, and a diagnostic that lands on a
// terminal should be readable even when stdout went into a file.
func fail(msg string) {
	fmt.Fprintln(os.Stderr, errStyle.Red(style.Glyph(style.LevelFail)+" 错误：")+msg)
}
func warn(msg string) {
	fmt.Fprintln(os.Stderr, errStyle.Yellow(style.Glyph(style.LevelWarn)+" 提醒：")+msg)
}
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// firstNonEmpty is the "last one wins only if the earlier ones said nothing"
// rule. Both `gen` and `install` need it for the same reason: a documented
// short flag and its long form are two variables for one setting, so the two
// have to be reconciled somewhere, and a default has to lose to either.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// orBuiltin names where an application rule came from: the user's file, or the
// table compiled into the binary.
func orBuiltin(r *config.Rule) string {
	if r == nil {
		return "（无规则）"
	}
	if r.Path != "" {
		return r.Path
	}
	return "内置规则 " + r.App
}
func yn(b bool) string {
	if b {
		return "是"
	}
	return "否"
}
