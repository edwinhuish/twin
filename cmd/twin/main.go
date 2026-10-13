// Command twin gives an existing program its own identity: a name of its own
// for the window and the tray, its own icon, and optionally its own private
// data directories.
//
// The design rationale lives in 应用包裹生成器_twin设计.md; the measurements
// it is built on are in 微信身份识别_定案.md.
package main

import (
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
// only root can install when no trampoline exists on the machine.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
	exitHuman = 3
)

// DefaultConfigName is the configuration `twin gen` writes and `twin install`
// reads when neither -o/--output nor -c/--config says otherwise.
//
// It is deliberately a path in the current directory rather than one under
// $HOME: the file is meant to be read, edited, diffed and committed, and the
// directory the user is standing in is the one place they already chose. An
// instance directory under ~/.local/share/twin holds the archive of what was
// installed, which is a different thing with a different lifetime.
const DefaultConfigName = "config.yml"

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

// printManual prints the steps that only a graphical session can finish, and
// says why the exit code is not 0.
func printManual(rep *verify.Report) {
	var lines []string
	for _, f := range rep.Findings {
		if f.Manual && f.Status != verify.StatusOK && f.Fix != "" {
			lines = append(lines, f.Fix)
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Printf("\n%s\n", title(fmt.Sprintf("✋ 还差一步（必须由真实图形会话执行，脚本里写不了 —— 这就是退出码 %d 的含义）", exitHuman)))
	for i, l := range lines {
		fmt.Printf("  %d. %s\n", i+1, l)
	}
}

const usage = `twin —— 给已有程序一个独立身份（窗口 / 托盘 / 图标 / 私有数据）

用法:
  twin gen <source> [选项]                在当前目录生成一份配置（默认 ./config.yml）
  twin install [选项]                     读那份配置并落盘：桌面项 / 图标 / 启动器 / 托盘
  twin inspect <source> [--name 显示名]   只看：源程序是什么、身份该怎么做
  twin verify <id>                        自检一个实例的身份链路
  twin reinstall <id> [--refresh]         按实例快照重新落盘
  twin remove <id> [--yes] [--purge]      删除产物
  twin list                               列出全部实例
  twin doctor                             环境自检（先跑这个）
  twin rules [<app>]                      列出内置规则 / 查看某程序生效的规则
  twin config                             打印 config.toml 模板（不存在时）
  twin version                            版本

第一个 token 永远是动词，没有保留字表 —— 程序真名就算叫 list / gen / doctor 也照常写：
twin gen list 就是包一个叫 list 的程序。

两步（推荐）—— 一次 wrap 里唯一随程序而变、又猜错了不出声的东西是路径映射，
所以先把它落成一份可以读、可以改、能进版本库的文件，再安装：
  twin gen wechat          在当前目录写 config.yml（id、显示名、路径映射全部推导好写进去），
                           只打印文件路径与下一步。想写到别处：twin gen wechat -o ~/w/wechat.yml
  $EDITOR config.yml       改 bind / name / keywords / identity.level；
                           不改也行，推导出来的默认值通常够用。
  twin install             读 ./config.yml：写成实例快照 → 落盘 → 自检。

配置在别处（多实例、多台机器共用一份）：twin install -c ~/wraps/wechat2.yml

配置从哪来（越具体越优先；数组类低位在前拼接，后者遮蔽前者）:
  config.yml / -c 指的那份      你正在编辑的那一份：gen 生成、install 读取
  ~/.config/twin/config.toml    这台机器的默认（bwrap 路径、图标尺寸、全局透传）
  ~/.config/twin/rules.d/<app>.toml  某程序的知识（状态路径、keywords），支持 {id} 占位
  ~/.local/share/twin/<id>/profile.yml  安装时按 config.yml 重新生成的存档快照
  ⚠️ install 按 config.yml 重放；reinstall 只读那份存档快照（要确定性）。
     要让它重读 config.toml 与 rules.d，加 --refresh，且只补快照里为空的字段。

退出码:
  0 成功    1 失败/自检有 ❌    2 用法错误
  3 身份已成立，但还差一步必须由真实图形会话执行。两个来源：① 托盘图标覆盖表写不进去
    （那张表只能经会话总线写入）；② 本机限制非特权 userns，而既没有跳板、install 也没能
    装上 profile（没有 sudo，或标准输入不是终端、问不了密码）—— 那时打印一份 AppArmor
    profile 全文，给你 sudo apparmor_parser -r 装上。

颜色:
  stdout 与 stderr 各自判断：只在终端上着色，重定向 / 管道 / TERM=dumb 一律纯文本。
  NO_COLOR 强制关掉；CLICOLOR_FORCE=1 强制打开（管道给会渲染转义的 pager 时用）。
  字形（✅ ⚠️ ❌ ➖）与颜色表达同一件事，所以纯文本里也不丢信息。

托盘图标:
  twin 生成时**自己**把宿主的托盘图标覆盖表（org.gnome.shell.extensions.appindicator
  的 custom-icons，按 SNI Id 匹配）指向本实例的图标，该表热重载、立刻生效、不用重登。
  所以不需要预先准备环境：换一台机器再跑一次 twin，状态就重新建好了。
  没有会话总线时（安装脚本 / 无图形会话）它写不进去 —— 那时会打印要跑的命令并以退出码 3
  结束；--no-tray 或 config.toml 的 defaults.tray_override = false 则完全不碰这张表。

gen 选项:
  -o, --output <文件>    写到哪（默认 ./config.yml）。文件已存在就拒绝，除非 --force ——
                         那个文件正是你要改的东西，盖掉它比报个错难收拾得多
  --force                覆盖已存在的输出文件
  --as <id>              实例 id（默认 <源桌面项基名>+最小空闲序号）；同时是窗口 app_id 与托盘 Id
  --name <显示名>        默认 源 Name[当前语言] + 序号（如 微信2）
                         显式给出时，若源 Keywords 不含中文，该名字会被放进关键词（见下）
  --name-en <英文名>     默认 源 Name + 序号
  --comment <说明>       默认 源 Comment[当前语言]
  --comment-en <英文说明>（写 Comment[en]=）
  --keywords <k1;k2>     搜索关键词（默认 rules.d 的值 → 源 Keywords[当前语言]/Keywords；
                         源没有则用源名与程序名推导，wechat → 微信;wechat;；
                         若源列表不含中文且你显式给了 --name，会把该名字放在最前）
  --categories <C;D;>    分类（默认 rules.d → 源桌面项）
  --icon <png|svg>       源图标（默认取源桌面项的 Icon=）
  --badge <文字>         角标文字，仅 0-9 A-Z 与 -+.!?/ 空格；默认取 id 尾部数字
  --badge-style <风格>   corner | number | dot（留空则 config.toml → corner）
  --level <级别>         alias（默认，真实路径改名）| none（只加桌面项）| redirect（改名＋写 L5 规则）
                         redirect 用于程序把 app_id 写死、改名不起作用的场合：除改名外再写一条
                         ~/.config/twin/rules.d/<id>.json，交给 shell 扩展改判窗口归属。
                         留空则由 rules.d → config.toml → alias 逐层决定
  --tmpfs <dir>          让该目录在沙箱内为空，可重复
  --no-tmpfs             完全不要 tmpfs（X11 程序必须如此）
  --no-tray              不写宿主托盘覆盖表（这个实例的托盘图标保持程序自己的）
  --explain              连推导出的默认值、规则模板一起打印
  --dry-run              只打印将要写什么，一个字节不落盘

  -- <bwrap 选项…>       第一个 -- 之后的 token **原样**透传给 bwrap，拼在基座之后、程序之前：
                         twin gen wechat -- --unshare-pid --setenv TWIN_FOO bar
                         它会写进配置里（bwrap.extra），install 时生效。它排在基座之后，
                         所以同目标挂载由它胜出（bwrap 后挂载遮蔽前者）。
                         ⚠️ 不接受 --（会截断 bwrap 解析）、--argv0（会破坏身份不变量）、
                            以及需要文件描述符的选项。

install 选项:
  -c, --config <文件>    读哪一份配置（默认 ./config.yml）。读不到就报错，不会去猜别的路径
  --force                覆盖已存在但不是 twin 生成的桌面项；也允许落盘遮蔽同名的系统桌面项
  --explain              打印完整说明：推导值、路径映射的修改粒度、规则模板全文、手工验证步骤
  --dry-run              只打印将要写什么，一个字节不落盘。
                         本机需要 AppArmor profile 时，它也只会说要装什么，不会碰 /etc

install 在本机限制非特权 userns、而私有 bwrap 副本又拿不到许可时，会自己用 sudo 装好
一份挂在副本上的 profile（写 /etc/apparmor.d/twin-bwrap 并加载）—— 那一步要 root。
装不上时怎么办，见下面的退出码 3。

source 可以是 .desktop 路径、桌面项 id（wechat），或程序名。
`

// version is what `twin version` prints; override at build time with
// -ldflags "-X main.version=…".
var version = "0.2.0-dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(exitUsage)
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
	initDraft := fs.Bool("init", false, "为这个程序探测并写出一份规则草稿（不覆盖已有文件）")
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
		if len(rule.Bwrap) > 0 {
			fmt.Printf("  bwrap      %s\n", strings.Join(rule.Bwrap, " "))
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
	name := fs.String("name", "", "预览：把该名字当作你显式指定的显示名")
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
		fmt.Printf("\n📂 /tmp 策略：%s（来自应用规则 %s）\n",
			strings.Join(insp.Rule.Tmpfs, " "), orBuiltin(insp.Rule))
	case t.Wayland && t.X11:
		fmt.Printf("\n📂 /tmp 策略：可用 --tmpfs /tmp —— 本会话有 WAYLAND_DISPLAY，该程序会走 Wayland。\n")
		fmt.Printf("  %s 但它两套平台插件都带着：若哪天真退到 X11，--tmpfs /tmp 会砍掉 /tmp/.X11-unix/X0 而连不上 X。\n",
			outStyle.Yellow("⚠️"))
	case t.Wayland && cfg.Defaults.TmpfsUnlessX11:
		fmt.Printf("\n📂 /tmp 策略：可用 --tmpfs /tmp（该程序走 Wayland）\n")
	case t.Wayland:
		fmt.Printf("\n📂 /tmp 策略：不加 --tmpfs /tmp（config.toml 关掉了 tmpfs_unless_x11）\n")
	default:
		fmt.Printf("\n📂 /tmp 策略：%s\n", outStyle.Red("**不要** --tmpfs /tmp（会砍掉 /tmp/.X11-unix/X0）"))
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
	var (
		output     = fs.String("output", "", "配置写到哪（默认 ./config.yml）")
		as         = fs.String("as", "", "实例 id")
		name       = fs.String("name", "", "显示名")
		nameEn     = fs.String("name-en", "", "英文显示名")
		comment    = fs.String("comment", "", "说明")
		commentEn  = fs.String("comment-en", "", "英文说明（写 Comment[en]=）")
		keywords   = fs.String("keywords", "", "搜索关键词，覆盖源桌面项")
		categories = fs.String("categories", "", "分类，覆盖源桌面项")
		iconPath   = fs.String("icon", "", "源图标")
		badge      = fs.String("badge", "", "角标文字")
		badgeStyle = fs.String("badge-style", "", "角标风格（留空则 config.toml → corner）")
		level      = fs.String("level", "", "身份级别（留空则 config.toml → rules.d → alias）")
		force      = fs.Bool("force", false, "覆盖已存在的输出文件")
		dryRun     = fs.Bool("dry-run", false, "只打印，不落盘")
		explain    = fs.Bool("explain", false, "打印完整说明（推导出的默认值、规则模板）")
		noTmpfs    = fs.Bool("no-tmpfs", false, "不要 tmpfs")
		noTray     = fs.Bool("no-tray", false, "不要把托盘图标覆盖表指向本实例")
	)
	// Go's flag package has no aliases, so the documented `-o` is a second
	// variable for the same setting; the two are reconciled before use rather
	// than letting "whichever was parsed last" decide.
	var shortOutput string
	fs.StringVar(&shortOutput, "o", "", "配置写到哪（--output 的简写）")
	var tmpfs listFlag
	fs.Var(&tmpfs, "tmpfs", "沙箱内屏蔽的目录")
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
	outPath := firstNonEmpty(*output, shortOutput, DefaultConfigName)

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
		Bwrap:      bwrapTokens,
		NoTmpfs:    *noTmpfs,
		NoTray:     *noTray,
	}
	// force is left out on purpose: it means "overwrite the file I asked for",
	// and letting it also mean "yes, shadow that other program's desktop entry"
	// would make one flag do two unrelated destructive things by accident.
	it, der, err := planInstance(p, o, insp, false)
	if err != nil {
		fail(err.Error())
		return exitFail
	}

	// The passthrough region is linted, never rewritten (设计 §2.6): a typo in a
	// bwrap option is cheap to flag here and expensive to hit at launch time.
	for _, w := range sandbox.LintBwrap(bwrapTokens) {
		warn(w)
	}

	if !*dryRun {
		body, err := it.Render()
		if err != nil {
			fail(err.Error())
			return exitFail
		}
		if err := writeConfigFile(outPath, body, *force); err != nil {
			fail(err.Error())
			return exitFail
		}
	}
	printGenResult(p, it, insp, outPath, *dryRun, *explain, der)
	return exitOK
}

// writeConfigFile writes the configuration, refusing to clobber a file that is
// already there unless --force says otherwise.
//
// The refusal is the important half. This file is the user's working copy, and
// the whole point of splitting the wrap in two is that somebody reads and edits
// it before anything is installed; silently replacing it would throw those
// edits away without a word.
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
	outPath string, dry, explain bool, der []generate.Derivation) {
	lead := outStyle.Green(style.Glyph(style.LevelOK) + " 已写出配置")
	if dry {
		lead = title("🔍 将要写出配置（--dry-run，未写盘）")
	}
	fmt.Printf("%s %s\n", lead, outStyle.Bold(outPath))
	if explain {
		printDerived(der)
		// "This instance" is the configuration the user is holding, not a
		// snapshot: gen has not installed anything, so that snapshot does not
		// exist yet.
		printPathMaps(p, it, insp, mapsOrigin(insp.Rule),
			mapFix{file: outPath, replay: installCommandFor(outPath)}, true)
	}
	// Under --explain the map section carries this sentence itself — but only
	// for a program with no rule at all, so a rule that maps nothing still
	// needs the line here (saying it twice is how a reader learns to skip it).
	if len(it.Binds) == 0 && (!explain || insp.Rule != nil) {
		fmt.Printf("  %s\n", outStyle.Yellow("⚠️ 没有隔离任何数据目录：这个程序若有单实例锁"+
			"（Electron / Qt / GTK 系常见），两个实例会互相干扰 —— 见配置里的 bind"))
	}
	if dry {
		body, err := it.Render()
		if err != nil {
			fail(err.Error())
			return
		}
		fmt.Printf("\n%s\n%s", title("📄 配置内容（config.yml）"), indent(body, "  "))
	}
	fmt.Printf("\n%s\n", title("👉 下一步"))
	fmt.Printf("  1. 改 %s —— 每个键的说明都写在文件里的注释里（不改也能装）\n", outPath)
	fmt.Printf("  2. 安装：%s\n", installCommandFor(outPath))
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

// cmdInstall is the second step: read the configuration, turn it into an
// instance, and install that.
//
// It reads the file the user has been editing rather than re-deriving anything
// from flags — which is why it takes no derivation options at all. A flag and a
// file can disagree; only one of them is what the user just spent time on.
func cmdInstall(p inst.Paths, args []string) int {
	fs := newFlags("install")
	var (
		configPath = fs.String("config", "", "读哪一份配置（默认 ./config.yml）")
		force      = fs.Bool("force", false, "覆盖非 twin 生成的产物")
		dryRun     = fs.Bool("dry-run", false, "只打印，不落盘")
		explain    = fs.Bool("explain", false, "打印完整说明（推导值、规则模板、手工验证步骤）")
	)
	var shortConfig string
	fs.StringVar(&shortConfig, "c", "", "读哪一份配置（--config 的简写）")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if len(pos) > 0 {
		fail(fmt.Sprintf("twin install 不接受位置参数（收到 %q）：装哪份配置用 -c 指定，"+
			"包哪个程序是在 `twin gen` 那一步说的", pos[0]))
		return exitUsage
	}
	if *configPath != "" && shortConfig != "" {
		fail("同时给了 -c 和 --config：它们是同一个设置，请只留一个")
		return exitUsage
	}
	path := firstNonEmpty(*configPath, shortConfig, DefaultConfigName)

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
// A missing file is turned into the command that writes one: "config.yml: no
// such file" tells a first-time user nothing about `twin gen`.
func loadConfigFile(path string) (*inst.Instance, error) {
	it, err := inst.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("读不到 %s（%s）—— 先用 twin gen <source> 生成一份，"+
				"或用 -c 指定它的位置", path, err)
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
// The snapshot under ~/.local/share/twin/<id>/ is not the file the user works
// in any more: it is the archive of what was installed, re-rendered from the
// parsed instance so that install and reinstall write the same bytes. That is
// what makes `reinstall` a faithful replay of the last install rather than a
// second opinion about it.
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

	rt := sandbox.Resolve(r.p, cfg, it.NestedNS)
	printRuntime(rt)
	if code := ensureProfile(r.p, rt, r.dry); code != exitOK {
		return code
	}
	rt = sandbox.Resolve(r.p, cfg, it.NestedNS)

	plan, err := sandbox.Build(it, r.p, rt)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	report := installReport{
		lead:    outStyle.Green(style.Glyph(style.LevelOK)+" 已安装") + " " + outStyle.Bold(it.ID),
		origin:  "配置 " + path,
		fix:     mapFix{file: path, replay: installCommandFor(path)},
		verbose: r.verbose(),
	}
	if r.dry {
		printRehearsal(r.p, it, insp, plan, report)
		return exitOK
	}
	// Icon size is environment (this machine's DPI), so it comes from
	// config.toml every time instead of being frozen into the configuration.
	return installAndCheck(r.p, it, insp, rt, plan, cfg.Defaults.IconSize, report)
}

// ensureProfile is the one step of an install that has to run as root.
//
// It only fires on a machine that restricts unprivileged user namespaces (so
// the private bubblewrap copy cannot build one on its own) *and* has nothing to
// borrow an AppArmor label from. twin then installs the profile itself instead
// of handing the user a copy-paste job — but only where a password can actually
// be asked for, which means a terminal on stdin. Anything else (a script, a
// pipe, a hook) gets the printed commands and exit code 3, which is what this
// step used to do in every case.
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
	sudo := sysx.Which("sudo")
	switch {
	case sudo == "":
		fmt.Printf("  %s\n", outStyle.Yellow("本机需要 AppArmor profile，但 PATH 上没有 sudo：跳过自动安装"))
	case !style.IsTerminal(os.Stdin):
		fmt.Printf("  %s\n", outStyle.Yellow("本机需要 AppArmor profile（要 root），"+
			"但标准输入不是终端、没法让你输密码：跳过自动安装"))
	default:
		// What is about to be written to /etc is named before sudo asks for a
		// password, not after: handing root to a line the user cannot see is
		// the one thing this prompt must never do.
		fmt.Printf("  %s\n", title("🔐 需要 root 装一份 AppArmor profile"))
		fmt.Printf("     将写 %s（%s 挂到 bwrap 副本上）。sudo 会要求输入密码 —— "+
			"不想给就 Ctrl-C，下面会打印同样的命令。\n\n", userns.ProfileFile, userns.ProfileName)
		note, err := userns.InstallProfile(rt.Bwrap, p.BinDir())
		if err != nil {
			warn(err.Error())
			fmt.Printf("  %s\n", outStyle.Yellow("自动安装没成功，退回手工那一步"))
			return exitOK
		}
		fmt.Printf("  %s %s\n", outStyle.Green(style.Glyph(style.LevelOK)), note)
	}
	return exitOK
}

// planInstance derives the instance and refuses the collisions that would make
// it a shadow of something else.
//
// Only `twin gen` calls it now. `twin install` and `twin reinstall` do not
// re-derive anything: one reads a configuration, the other replays a snapshot,
// and both of those already say which instance they are.
func planInstance(p inst.Paths, o generate.Options, insp *generate.Inspection, force bool) (*inst.Instance, []generate.Derivation, error) {
	it, der, err := generate.Plan(p, o, insp)
	if err != nil {
		return nil, nil, err
	}
	// Keywords are derived rather than demanded now, so this should never fire —
	// which is exactly why it is worth stating: if it ever does, the search
	// index would silently lose this instance.
	if it.Keywords == "" {
		warn("推不出搜索关键词（源桌面项既无 Keywords，也没有可用的 Name/程序名）——" +
			"搜索结果里可能找不到这个实例，建议补 --keywords 'k1;k2'")
	}
	// This only guards an explicit --as: a derived id is free by construction.
	if err := generate.CheckCollision(p, it.ID, force); err != nil {
		return nil, nil, err
	}
	return it, der, nil
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
		fmt.Printf("%s %s 的桌面项不在：上一次安装没走完（快照 %s 在，桌面项 / 图标 / 启动器不全）\n",
			outStyle.Mark(style.LevelFail), it.ID, p.Profile(id))
		fmt.Printf("  重来一次：twin reinstall %s\n", id)
		return exitFail
	}
	rep := verify.Run(it, p)
	fmt.Printf("%s %s（level=%s，别名=%s）\n\n", outStyle.Bold("🪪 实例"), it.ID, it.Level, it.EffectiveAlias())
	fmt.Print(formatFindings(rep))
	printManual(rep)
	return exitFor(rep)
}

func formatFindings(rep *verify.Report) string {
	var b strings.Builder
	for _, f := range rep.Findings {
		fmt.Fprintf(&b, "%s %-22s %s\n", outStyle.Mark(levelOf(f.Status)), f.Name, f.Detail)
		if f.Fix != "" {
			fmt.Fprintf(&b, "   %-22s → %s\n", "", f.Fix)
		}
	}
	return b.String()
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
		fmt.Printf("%s 还没有任何实例（%s 下为空）\n",
			outStyle.Mark(style.LevelSkip), filepath.Join(p.Home, ".local/share/twin"))
		fmt.Printf("   做第一个：twin gen wechat && twin install\n")
		return exitOK
	}
	fmt.Printf("%s\n", title("📋 实例"))
	fmt.Printf("%-16s %-10s %-8s %-24s %s\n", "id", "level", "别名", "程序", "桌面项")
	broken := 0
	for _, it := range items {
		desktop := filepath.Base(p.Desktop(it.ID))
		mark := ""
		// A row without a desktop entry means an install died half way: the
		// snapshot is on disk but the identity anchor is not, so nothing in
		// this row would actually start. Saying so is the whole point of the
		// column.
		if !sysx.Exists(p.Desktop(it.ID)) {
			mark = "  " + outStyle.Yellow("← 安装未完成")
			broken++
		}
		fmt.Printf("%-16s %-10s %-8s %-24s %s%s\n", it.ID, it.Level, it.EffectiveAlias(),
			filepath.Base(it.Target), desktop, mark)
	}
	if broken > 0 {
		fmt.Printf("\n%s 有 %d 个桌面项缺失（上一次安装没走完）：twin reinstall <id>\n",
			outStyle.Mark(style.LevelWarn), broken)
	}
	// The one thing you come to this table to do next is get rid of a row, and
	// `remove` is the only verb that does it — so name it here rather than
	// leaving it to be discovered in --help.
	fmt.Printf("\n删除某一个：twin remove <id>（不加 --yes 只打印要删什么）\n")
	return 0
}

// ---------------------------------------------------------------- reinstall

// cmdReinstall replays the snapshot under ~/.local/share/twin/<id>/.
//
// It is the verb for an instance that is already installed: the configuration
// in the current directory may have been moved, committed or thrown away since,
// and the archive of what was installed is the only faithful record left. That
// is also why it takes no derivation flags — `twin install` is where a
// configuration becomes an instance, and this is where an instance is repeated.
func cmdReinstall(p inst.Paths, args []string) int {
	fs := newFlags("reinstall")
	refresh := fs.Bool("refresh", false, "重读 config.toml / rules.d，只补快照里为空的字段")
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

	// Replaying a snapshot is deterministic by design: it reads profile.yml
	// (and the CLI, which has nothing left to say here). Touching config.toml
	// must not silently rewrite a shelf of existing instances — that is what
	// --refresh is for, and even then it only fills fields the snapshot left
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
			fmt.Printf("--refresh：config.toml 与 rules.d 里没有可补的字段（快照已经写满）\n")
		}
		for _, n := range notes {
			fmt.Printf("--refresh 补上 %s\n", n)
		}
	} else if w := generate.SessionWarning(it); w != "" {
		// The snapshot records the decision as intent; if today's session
		// contradicts it, say so rather than replaying it in silence.
		warn(w + "（这是快照里的决定，不是这次的问题）")
	}

	rt := runtimeFor(p, it.NestedNS)
	printRuntime(rt)
	if code := ensureProfile(p, rt, false); code != exitOK {
		return code
	}
	rt = runtimeFor(p, it.NestedNS)

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
		origin: "实例快照 " + p.Profile(it.ID),
		// reinstall never reads a configuration, so the file to edit really is
		// the snapshot: this is the one entry point where that advice is right
		// rather than merely available.
		fix:     mapFix{file: p.Profile(it.ID), replay: "twin reinstall " + it.ID},
		verbose: false,
	})
}

// installAndCheck writes an instance's artefacts and reports what it found when
// reading them back. It is the shared back end of `twin install` and
// `twin reinstall`, which differ only in where the instance came from.
func installAndCheck(p inst.Paths, it *inst.Instance, insp *generate.Inspection, rt sandbox.Runtime,
	plan *sandbox.Plan, iconSize int, report installReport) int {
	res, err := generate.Materialize(p, it, insp, plan, iconSize)
	if err != nil {
		fail(err.Error())
		return exitFail
	}

	// The tray override is written here, before the self-check, so the check
	// reads back what this run just did rather than a state from last time.
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

	fmt.Printf("\n%s\n%s", title("🔍 自检"), formatFindings(rep))
	printNextSteps(p, it, report.verbose)
	printManual(rep)

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

// installCommandFor spells the command that installs a given configuration,
// with -c only when it is not the default name: `twin install` reads
// ./config.yml, and saying `-c ./config.yml` would suggest otherwise.
func installCommandFor(path string) string {
	if path == DefaultConfigName {
		return "twin install"
	}
	return "twin install -c " + path
}

// printNextSteps says the one thing that still has to happen by hand, and where
// to read the rest. The desktop entry is written after the program is already
// running, and GNOME will not recompute the identity of a window that already
// exists — so "restart it" is not advice, it is the last step of the wrap.
func printNextSteps(p inst.Paths, it *inst.Instance, verbose bool) {
	fmt.Printf("\n%s\n", title("👉 下一步"))
	if verbose {
		fmt.Printf("  1. 桌面项是后写的，GNOME 不会给已经存在的窗口重算归属 —— 请**重新启动**这个实例。\n")
		fmt.Printf("  2. 托盘图标已经写进宿主覆盖表（该表热重载，立刻生效，不用重登）。\n")
		fmt.Printf("     若上面那条是 WARN，说明这次没写进去：把命令拿到图形会话的终端里跑一次即可。\n")
		fmt.Printf("  3. 想确认身份，看这两个值是否都等于 %q：\n", it.EffectiveAlias())
		fmt.Printf("     窗口 app_id ：WAYLAND_DEBUG=1 bash %s 2>&1 | grep set_app_id\n", p.Launch(it.ID))
		fmt.Printf("     托盘 SNI Id ：busctl --user list | grep -i statusnotifier  （名字里带 pid）\n")
		return
	}
	fmt.Printf("  1. 桌面项是后写的 —— 请**重新启动**这个实例，否则窗口还是旧的名字和图标。\n")
	fmt.Printf("  2. 确认身份与托盘：twin verify %s\n", it.ID)
	fmt.Printf("     改了实例快照要重放：twin reinstall %s\n", it.ID)
}

// printRehearsal is --dry-run's output: every file and every host-side change
// the next step would make. It is rendered by the same code the real run uses
// (it.Render, DesktopEntry, plan.String are the very calls Materialize makes) —
// a preview drawn by a second implementation would be a lie.
func printRehearsal(p inst.Paths, it *inst.Instance, insp *generate.Inspection, plan *sandbox.Plan, report installReport) {
	fmt.Printf("%s\n\n%s\n", title("🔍 预演（--dry-run，未写盘）"), title("📦 产物"))
	fmt.Printf("  桌面项   %s\n", p.Desktop(it.ID))
	fmt.Printf("  图标     %s\n", p.Icon(it.ID))
	fmt.Printf("  启动器   %s\n", p.Launch(it.ID))
	fmt.Printf("  实例定义 %s\n", p.Profile(it.ID))
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
		title("🪪 身份"), it.Level, it.EffectiveAlias())
	// The tray row is host state, not a file, but it is still part of what a
	// real run would change — so the rehearsal has to show it too.
	if !it.TrayOverride {
		fmt.Printf("\n%s\n  跳过（tray.override = false），不动宿主表\n", title("🔔 托盘覆盖"))
	} else {
		fmt.Printf("\n%s\n  %s\n  将写入 [('%s', '%s', '%s')]（合并，不覆盖别人的条目）\n",
			title("🔔 托盘覆盖"), tray.Schema+" / "+tray.Key, it.ID, p.Icon(it.ID), p.Icon(it.ID))
	}
	printDerived(report.der)
	printPathMaps(p, it, insp, report.origin, report.fix, report.verbose)
	if plan.Bwrap != "" {
		fmt.Printf("\n%s\n%s\n", title("🧱 bwrap 调用"), plan.String())
	}
	// Rendered as the archive role because that is what install writes to
	// profile.yml: a preview drawn from a different renderer is a lie.
	body, err := it.RenderAs(inst.RoleSnapshot)
	if err != nil {
		fail(err.Error())
		return
	}
	fmt.Printf("\n%s\n%s", title("📄 实例定义（profile.yml）"), indent(body, "  "))
	fmt.Printf("\n%s\n%s", title("📄 桌面项（"+filepath.Base(p.Desktop(it.ID))+"）"),
		indent(generate.DesktopEntry(p, it, insp).Bytes(), "  "))
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
func runtimeFor(p inst.Paths, needNestedNS bool) sandbox.Runtime {
	cfg, err := config.Load(p)
	if err != nil {
		return sandbox.Runtime{}
	}
	return sandbox.Resolve(p, cfg, needNestedNS)
}

// printProfileHint is what is left when `twin install` cannot do the one step
// that needs root: the machine restricts unprivileged user namespaces, no
// Electron binary is installed to borrow an AppArmor label from, and no
// password could be asked for (no sudo on PATH, or stdin is not a terminal).
//
// `twin install` runs these two commands itself through sudo whenever it can.
// Printing them is the honest fallback, not the plan: the commands are exactly
// what was attempted, so the user can run them and get the same result.
func printProfileHint(copyPath string) {
	fmt.Printf("\n%s\n", title("✋ 还差一步：证书这关要走一次 root"))
	fmt.Printf("  这个程序运行时要自己建一个嵌套 user namespace，而内核只放行\n")
	fmt.Printf("  「profile 里写了 userns 许可」的程序（%s）。\n", userns.SysctlSource)
	fmt.Printf("  私有 bwrap 副本故意不带 profile，所以它自己建不了。\n\n")
	fmt.Printf("  两条路，任选其一：\n")
	fmt.Printf("    1) 装一个 Electron 应用（VS Code 就够了），再跑一次 `twin reinstall <id>`；\n")
	fmt.Printf("       它会自动发现新跳板，什么都不用配置。\n")
	fmt.Printf("    2) 在有终端的环境里再跑一次 twin install（或 twin reinstall <id>）——\n")
	fmt.Printf("       它会用 sudo 替你装好这份 profile；或者手工把它存成 %s 并加载：\n\n", userns.ProfileFile)
	for _, line := range strings.Split(strings.TrimRight(userns.ProfileHint(copyPath), "\n"), "\n") {
		fmt.Printf("       %s\n", line)
	}
	fmt.Println()
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
	yes := fs.Bool("yes", false, "确认删除")
	purge := fs.Bool("purge", false, "连私有状态目录一起删")
	pos, err := parseFL(fs, args)
	if err != nil {
		return usageError(fs, err)
	}
	if len(pos) < 1 {
		fail("缺少 <id>：twin list 看有哪些实例（先不加 --yes 只预览要删什么）")
		return exitUsage
	}
	id := pos[0]
	if err := inst.ValidateID(id); err != nil {
		fail(err.Error())
		return exitUsage
	}
	if !*yes {
		// The dry form is the whole safety mechanism: it lists exactly what the
		// next command would delete, including the one thing that is not a file
		// (`custom-icons` row) and the one thing that is irreversible (--purge).
		fmt.Printf("%s\n", title("🗑️ 将要删除（加 --yes 才真的删）"))
		for _, f := range []string{p.Desktop(id), p.Icon(id), p.InstanceDir(id), p.RuleFile(id)} {
			if !sysx.Exists(f) {
				fmt.Printf("  %s %s\n", outStyle.Dim("➖"), outStyle.Dim(f+"（不存在）"))
				continue
			}
			fmt.Printf("  %s %s\n", outStyle.Green("✅"), f)
		}
		state := p.StateDir(id)
		if *purge {
			fmt.Printf("  %s %s\n", outStyle.Red("💣"), state+"（私有数据，--purge：不可逆地删掉）")
		} else {
			fmt.Printf("  %s %s\n", outStyle.Dim("💾"), state+"（私有数据，保留 —— 要一起删加 --purge）")
		}
		fmt.Printf("  %s %s\n", outStyle.Dim("🔔"), dropTray(id, true))
		fmt.Printf("\n确认无误：twin remove %s --yes\n", id)
		return exitOK
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

// ---------------------------------------------------------------- output

// applyTray points the host's tray override table at this instance, and returns
// the one line describing what happened.
//
// This is the only state twin touches outside the user's home, and twin
// touches it itself, at wrap time. The alternative — printing a gsettings
// command for the user to run once, before wrapping — would make a wrapper
// depend on preparation done by hand on each machine, which is exactly what
// this tool exists to remove: a copied-over instance is regenerated by running
// twin again, and that run has to leave the machine in the finished state.
//
// Failure here is not fatal. Without a session bus (an installer, a headless
// run, a script) the table cannot be written, and the honest answer is the
// command plus exit code 3, which `verify` supplies.
func applyTray(p inst.Paths, it *inst.Instance) string {
	if !it.TrayOverride {
		return "托盘覆盖 跳过（tray.override = false）"
	}
	oc, err := tray.Ensure(it.ID, p.Icon(it.ID))
	// level=none does not rename anything, so the program's StatusNotifierItem
	// id is whatever the program chose — the row is a guess. It is still worth
	// writing (the desktop basename often coincides) but not worth promising.
	vague := ""
	if !it.Level.Renames() {
		vague = "（level=none 没有改名：托盘的 SNI Id 由程序自己决定，这一行未必匹配得上）"
	}
	switch {
	case err != nil:
		return "托盘覆盖 未写入（" + err.Error() + "）—— 自检里会给出要跑的那条命令"
	case oc.Action == tray.ActionNoHost:
		return "托盘覆盖 跳过（" + oc.Detail + "）"
	case oc.Action == tray.ActionWritten:
		return "托盘覆盖 已写入 " + tray.Key + "（该表热重载，立刻生效）" + vague
	default:
		return "托盘覆盖 已是最新" + vague
	}
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
// config paths (`desktop.keywords`, `isolation.nested_ns`) whose width follows
// the schema, and a fixed column turns "one key got longer" into a table where
// the values no longer line up.
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

// printPathMaps explains the one part of a wrap that is knowledge about the
// *program* rather than about its identity: which of the program's own data
// directories twin redirected, and where they went.
//
// It is spelled out because that knowledge is the part most likely to be wrong
// for a program twin has never seen — and a wrong map is silent until two
// instances fight over a lock file, or one of them quietly reads the other's
// profile. The built-in table is deliberately short for the same reason, so the
// output has to hand the user the exact file to edit rather than trusting them
// to find it (设计 §7).
//
// verbose adds what only matters once the user has decided the map is wrong:
// the paste-ready template, and the three granularities at which it can be
// corrected. The short form keeps the facts that decide whether to act — how
// many maps, where the knowledge came from, which file holds it.
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

	fmt.Printf("\n%s\n", title("📁 路径映射"))
	if len(it.Binds) == 0 {
		// "twin redirected nothing" is a fact worth stating: it is the normal
		// case for an unknown program, and the reason two instances of it may
		// start interfering with each other.
		fmt.Printf("  0 条：数据目录与源程序共用（本实例只做身份包裹）。\n")
		if insp.Rule == nil {
			fmt.Printf("  %s\n", outStyle.Yellow("⚠️ 这个程序没有规则：它若有单实例锁或共享配置"+
				"（Electron / Qt / GTK 系很常见），两个实例会互相干扰。"))
		}
		printStateHints(p, insp, it, key)
		printMapFix(it, key, rulePath, fix)
		if verbose {
			printRuleTemplate(p, it, key, nil)
		}
		return
	}

	if origin == "" {
		fmt.Printf("  %d 条\n", len(it.Binds))
	} else {
		fmt.Printf("  %d 条，来源：%s\n", len(it.Binds), origin)
	}
	for _, b := range it.Binds {
		// Printed in the file's own spelling, so the line can be pasted into
		// profile.yml or rules.d without a translation step in between.
		switch {
		case b.ReadOnly:
			fmt.Printf("  %s  %s\n", outStyle.Cyan("只读"), b)
		case b.Source == b.Target:
			fmt.Printf("  %s  %s   （原地升回可写，数据不搬家）\n", outStyle.Cyan("共用"), b)
		default:
			fmt.Printf("  %s  %s\n", outStyle.Green("私有"), b)
		}
	}
	printStateHints(p, insp, it, key)
	printMapFix(it, key, rulePath, fix)
	if verbose {
		printRuleTemplate(p, it, key, it.Binds)
	}
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
	fmt.Printf("      只改这一个：编辑 %s 里的 bind，再 %s\n", fix.file, fix.replay)
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
	hints := uncoverHints(generate.SuggestStateDirs(p, insp), it.Binds)
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
	fmt.Printf("  源:目标:ro ⇒ 只读挂载。\n")
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
