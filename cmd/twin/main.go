// Command twin gives an existing program its own identity: a name of its own
// for the window and the tray, its own icon, and optionally its own private
// data directories.
//
// The design rationale lives in 应用包裹生成器_twin设计.md; the measurements
// it is built on are in 微信身份识别_定案.md.
package main

import (
	"flag"
	"fmt"
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

// Colour is decided per stream, and decided here rather than at each print
// site: `twin new wechat > log` must leave the log plain while the warnings
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
  twin new <source> [选项]                写出一份实例配置（profile.yml）
  twin new <source> --publish [选项]      按配置落盘：桌面项 / 图标 / 启动器 / 托盘
  twin inspect <source> [--name 显示名]   只看：源程序是什么、身份该怎么做
  twin verify <id>                        自检一个实例的身份链路
  twin regen <id> [--refresh]             改了 profile.yml 后重新落盘
  twin remove <id> [--yes] [--purge]      删除产物
  twin list                               列出全部实例
  twin doctor                             环境自检（先跑这个）
  twin rules [<app>]                      列出内置规则 / 查看某程序生效的规则
  twin config                             打印 config.toml 模板（不存在时）
  twin version                            版本

第一个 token 永远是动词，第二个是它的宾语 —— 位置即含义，没有例外，也没有保留字表。
所以程序真名就算叫 list / new / doctor，照常写：twin new list 就是包一个叫 list 的程序。

两段式（默认）—— 一次 wrap 里唯一随程序而变、又猜错了不出声的东西是路径映射，
所以先把它落成一份可以读、可以改的文件，再安装：
  twin new wechat                    只写 ~/.local/share/twin/wechat2/profile.yml（id、显示名、
                                     路径映射全部推导出来写进去），并告诉你改哪个文件。
  $EDITOR …/wechat2/profile.yml      改 bind / name / keywords / identity.level；
                                     不改也行，推导出来的默认值通常够用。
  twin new wechat --publish          按那份配置落盘，再自检。

一步到位：没有待落盘的配置时，--publish 就是一次完整的生成，选项与 -- 透传区照常生效：
  twin new wechat --publish -- --unshare-pid
待落盘的配置不止一份时（同源多实例），加 --as <id> 指名要做哪一个。

配置三层（越具体越优先；数组类低位在前拼接，后者遮蔽前者）:
  CLI 参数 > profile.yml（实例快照）> rules.d/<app>.toml（应用知识）> config.toml（环境）
  ~/.config/twin/config.toml        这台机器的默认（bwrap 路径、图标尺寸、全局透传）
  ~/.config/twin/rules.d/<app>.toml 某程序的知识（状态路径、keywords），支持 {id} 占位
  ~/.local/share/twin/<id>/profile.yml   这一个实例的意图（真源）
  ⚠️ regen 只读 CLI 与快照（要确定性）；要重读上面两层，加 --refresh，且只补快照里为空的字段。

退出码:
  0 成功    1 失败/自检有 ❌    2 用法错误
  3 身份已成立，但还差一步必须由真实图形会话执行。两个来源：① 托盘图标覆盖表写不进去
    （那张表只能经会话总线写入）；② 本机限制非特权 userns 而找不到 Electron 跳板 ——
    那时会打印一份 AppArmor profile 全文，给你 sudo apparmor_parser -r 装上。

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

new 选项:
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
  --force                覆盖已存在但不是 twin 生成的桌面项；也允许落盘遮蔽同名的系统桌面项
  --publish              落盘：写桌面项 / 图标 / 启动器 / 宿主托盘，然后自检。
                        已经有一份待落盘的配置时，它按那份配置走 —— 此时再给
                        --name / --level 这类描述实例的选项会被拒绝（要改就改那个文件）
  --explain              连推导出的默认值、规则模板、手工验证步骤一起打印
                         （默认只打印快照路径与下一步）
  --dry-run              只打印将要写什么；与 --publish 一起用就是完整落盘的预演

  -- <bwrap 选项…>       第一个 -- 之后的 token **原样**透传给 bwrap，拼在基座之后、程序之前：
                         twin new wechat -- --unshare-pid --setenv TWIN_FOO bar
                         它排在基座之后，所以同目标挂载由它胜出（bwrap 后挂载遮蔽前者）。
                         ⚠️ 不接受 --（会截断 bwrap 解析）、--argv0（会破坏身份不变量）、
                            以及需要文件描述符的选项。

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
	case "new":
		os.Exit(cmdNew(p, args))
	case "inspect":
		os.Exit(cmdInspect(p, args))
	case "verify":
		os.Exit(cmdVerify(p, args))
	case "regen":
		os.Exit(cmdRegen(p, args))
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
		// a usage error, never a guess. `twin new list` is how you wrap a program
		// that happens to be named after a subcommand.
		fail(fmt.Sprintf("未知子命令 %q", cmd))
		fmt.Fprint(os.Stderr, usage)
		os.Exit(exitUsage)
	}
}

// ---------------------------------------------------------------- flag parsing

// parseFL parses a FlagSet whose positional arguments may appear anywhere among
// the flags.
//
// Go's flag package stops at the first non-flag token, so the documented form
// `twin new <source> --as <id>` would otherwise ignore --as — and every
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
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	initDraft := fs.Bool("init", false, "为这个程序探测并写出一份规则草稿（不覆盖已有文件）")
	pos, err := parseFL(fs, args)
	if err != nil {
		return exitUsage
	}
	if *initDraft {
		if len(pos) != 1 {
			fail("用法：twin rules --init <源桌面项 | 程序名 | id>")
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
	fmt.Printf("  应用  twin new <source> --as <id> --force\n")
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
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	name := fs.String("name", "", "预览：把该名字当作你显式指定的显示名")
	pos, err := parseFL(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 {
		fail("用法：twin inspect <source>")
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

func cmdNew(p inst.Paths, args []string) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	var (
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
		force      = fs.Bool("force", false, "覆盖非 twin 生成的产物")
		dryRun     = fs.Bool("dry-run", false, "只打印，不落盘")
		explain    = fs.Bool("explain", false, "打印完整说明（推导出的默认值、规则模板、手工验证步骤）")
		publish    = fs.Bool("publish", false, "把配置落盘：桌面项 / 图标 / 启动器 / 托盘 ＋ 自检")
		noTmpfs    = fs.Bool("no-tmpfs", false, "不要 tmpfs")
		noTray     = fs.Bool("no-tray", false, "不要把托盘图标覆盖表指向本实例")
	)
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
		return exitUsage
	}
	if len(pos) < 1 {
		fail("用法：twin new <source> [--as <id>] [--publish]")
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
		Bwrap:      bwrapTokens,
		NoTmpfs:    *noTmpfs,
		NoTray:     *noTray,
		Force:      *force,
	}
	// Both halves of the wrap answer "which instance is this?" the same way, and
	// never by re-deriving the id: the first half has already taken it (freeID
	// counts profile.yml as taken, precisely so two instances cannot share an
	// id), so a re-derivation would quietly move on to wechat3.
	snaps, err := generate.SnapshotsFor(p, insp.SourceDesktop)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	run := newRun{
		p: p, insp: insp, opts: o, cfg: cfg, snaps: snaps,
		as: *as, source: pos[0], bwrap: bwrapTokens,
		given: derivationInputs(fs, bwrapTokens),
		force: *force, dry: *dryRun, explain: *explain,
	}
	if !*publish {
		return run.configure()
	}
	target, code := run.target()
	if code != exitOK {
		return code
	}
	it, der, origin := target.it, target.der, target.origin

	rt := sandbox.Resolve(p, cfg, it.NestedNS)
	printRuntime(rt)
	plan, err := sandbox.Build(it, p, rt)
	if err != nil {
		fail(err.Error())
		return exitFail
	}

	report := installReport{
		lead:    outStyle.Green(style.Glyph(style.LevelOK)+" 已生成") + " " + outStyle.Bold(it.ID),
		der:     der,
		origin:  origin,
		verbose: run.verbose(),
	}
	if *dryRun {
		printRehearsal(p, it, insp, plan, report)
		return exitOK
	}
	return installAndCheck(p, it, insp, rt, plan, o.IconSizeFor(cfg), report)
}

// newRun is one `twin new` invocation after the flags are parsed: the source
// program, the choices, and the instances already on disk for that source.
type newRun struct {
	p      inst.Paths
	insp   *generate.Inspection
	opts   generate.Options
	cfg    *config.Config
	snaps  []generate.Snapshot
	as     string
	source string
	// bwrap is the `--` passthrough region. given lists the flags the user
	// actually typed: the install half refuses to act on a snapshot when the
	// invocation also carries decisions about what that snapshot should say,
	// and a refusal has to name what it would have ignored.
	bwrap   []string
	given   []string
	force   bool
	dry     bool
	explain bool
}

// verbose is the long form. --explain asks for it; --dry-run has always been
// the "show me everything" mode.
func (r newRun) verbose() bool { return r.dry || r.explain }

// configure is the first half of a wrap: profile.yml, and nothing else.
//
// This is the default on purpose. Which of a program's own directories to
// isolate, and what to call the instance, is knowledge about the *program* — the
// one part of a wrap that can be wrong without saying so. The place to review it
// is a file, before any of it is installed; a wall of terminal output printed
// after the install already happened is not.
func (r newRun) configure() int {
	// The passthrough region is linted, never rewritten (设计 §2.6): a typo in a
	// bwrap option is cheap to flag here and expensive to hit at launch time.
	for _, w := range sandbox.LintBwrap(r.bwrap) {
		warn(w)
	}

	id, prepared, err := configureTarget(r.snaps, r.as)
	if err != nil {
		fail(err.Error())
		return exitUsage
	}
	if prepared {
		// Do not stack a second configuration behind the user's back: report the
		// one that exists. This is also what makes `twin new wechat` idempotent
		// between the two halves — and it never rewrites the file, because the
		// edits in it are the whole reason the file exists.
		s, _ := snapshotByID(r.snaps, id)
		r.printConfig(s.Instance, s.Path, outStyle.Yellow("ℹ️  已有一份写好、还没落盘的配置"),
			"实例快照 "+s.Path, nil)
		return exitOK
	}

	it, der, err := planInstance(r.p, r.opts, r.insp, r.force)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	lead := outStyle.Green("📝 已写出实例配置")
	if !r.dry {
		if _, err := generate.WriteSnapshot(r.p, it); err != nil {
			fail(err.Error())
			return exitFail
		}
	} else {
		lead = title("🔍 将要写出实例配置（--dry-run，未写盘）")
	}
	r.printConfig(it, r.p.Profile(it.ID), lead, mapsOrigin(r.insp.Rule), der)
	return exitOK
}

// printConfig says where the configuration is and how to install it. The heading
// differs between "just written", "would be written" and "already there"; the
// body does not, because it is the same file in all three cases.
//
// The default body is two lines plus the next step, because every explanation
// this used to print now lives in the file itself as a comment on the key it is
// about. Repeating it here is how the two halves drift apart, and a comment the
// user reads *while editing* beats a paragraph they scrolled past. The one thing
// that stays is the shared-state warning: it is the only failure this command
// can leave behind that nothing else will surface.
func (r newRun) printConfig(it *inst.Instance, path, lead, origin string, der []generate.Derivation) {
	fmt.Printf("%s %s\n", lead, outStyle.Bold(it.ID))
	fmt.Printf("  快照   %s\n", path)
	if r.explain {
		printDerived(der)
	}
	if r.verbose() {
		printPathMaps(r.p, it, r.insp, origin, true)
		fmt.Printf("\n%s\n", title("✏️  要改就改这一个文件"))
		fmt.Printf("  %-18s %s\n", "bind", "隔离哪个数据目录（见上面的路径映射）")
		fmt.Printf("  %-18s %s\n", "name / comment", "显示名与说明（现在："+it.Name+" / "+orDash(it.Comment)+"）")
		fmt.Printf("  %-18s %s\n", "keywords", "搜索关键词（现在："+orDash(it.Keywords)+"）")
		fmt.Printf("  %-18s %s\n", "identity.level", "身份级别（现在："+string(it.Level)+"）")
	} else if len(it.Binds) == 0 {
		fmt.Printf("  %s\n", outStyle.Yellow("⚠️ 没有隔离任何数据目录：这个程序若有单实例锁"+
			"（Electron / Qt / GTK 系常见），两个实例会互相干扰 —— 见快照里的 bind"))
	}

	if r.dry {
		body, err := it.Render()
		if err != nil {
			fail(err.Error())
			return
		}
		fmt.Printf("\n%s\n%s", title("📄 实例定义（profile.yml）"), indent(body, "  "))
	}
	fmt.Printf("\n%s\n", title("👉 下一步"))
	if r.dry {
		fmt.Printf("  落盘（写桌面项 / 图标 / 启动器 / 宿主托盘，并自检）：\n")
		fmt.Printf("    twin new %s --as %s --publish\n", r.source, it.ID)
		return
	}
	fmt.Printf("  1. 改上面那个文件 —— 每个键的说明都写在文件里的注释里（不改也行，推导出来的默认值通常够用）\n")
	fmt.Printf("  2. 落盘：twin new %s --as %s --publish\n", r.source, it.ID)
	fmt.Printf("     想先看它会写什么、或看完整说明，就在后面再加 --dry-run / --explain。\n")
}

// installTarget is the instance the install half will act on, plus where its
// content came from — the snapshot the user edited, or this invocation's flags.
type installTarget struct {
	it     *inst.Instance
	der    []generate.Derivation
	origin string
}

// target resolves the instance `--publish` installs.
//
// Three answers are possible and all three are ordinary: --as named it, exactly
// one prepared snapshot of this source is waiting to be installed, or nothing
// was prepared at all and this is a one-step wrap.
func (r newRun) target() (installTarget, int) {
	id, err := publishTarget(r.snaps, r.as)
	if err != nil {
		fail(err.Error())
		return installTarget{}, exitUsage
	}

	if s, ok := snapshotByID(r.snaps, id); ok {
		// The snapshot is the truth the user has been editing, so flags that
		// describe the instance may not compete with it. Refusing beats both
		// alternatives: silently re-deriving would discard their edits, and
		// silently ignoring the flags would discard their words.
		if len(r.given) > 0 {
			fail(fmt.Sprintf("实例 %s 的配置已经写好了（%s）：--publish 按它落盘，%s 不会起作用。"+
				"要改就编辑那个文件（或 twin remove %s 之后重跑），要一步到位就换一个 --as",
				id, s.Path, strings.Join(r.given, "、"), id))
			return installTarget{}, exitUsage
		}
		if err := s.Instance.Check(); err != nil {
			fail(err.Error())
			return installTarget{}, exitFail
		}
		if err := generate.CheckInstall(r.p, id, r.force); err != nil {
			fail(err.Error())
			return installTarget{}, exitFail
		}
		// Re-read the source for the two values the desktop entry inherits
		// (Categories, Keywords), with the snapshot supplying everything else.
		return installTarget{
			it:     s.Instance,
			origin: "实例快照 " + s.Path,
		}, exitOK
	}

	// Nothing prepared: `--publish` is an ordinary one-step wrap of a new
	// instance, id and all.
	it, der, err := planInstance(r.p, r.opts, r.insp, r.force)
	if err != nil {
		fail(err.Error())
		return installTarget{}, exitFail
	}
	return installTarget{it: it, der: der, origin: mapsOrigin(r.insp.Rule)}, exitOK
}

// planInstance derives the instance and refuses the collisions that would make
// it a shadow of something else. Both halves call it, and the second half only
// when there is nothing on disk yet.
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

// configureTarget decides which instance `twin new` is about to write a
// configuration for. An explicit --as always wins.
//
// Otherwise the answer comes from what is already on disk: a single prepared
// snapshot for this source means the user is standing between the two halves,
// and the honest response is to point at that file rather than stack a second
// configuration next to it. `prepared` reports exactly that, so the caller can
// print the file instead of rewriting it.
func configureTarget(snaps []generate.Snapshot, as string) (id string, prepared bool, err error) {
	if as != "" {
		return as, false, nil
	}
	switch pending := pendingSnapshots(snaps); len(pending) {
	case 0:
		return "", false, nil
	case 1:
		return pending[0].Instance.ID, true, nil
	default:
		return "", false, fmt.Errorf("这个源程序有 %d 份写好、还没落盘的配置（%s）—— 指名一个：twin new <source> --as <id>",
			len(pending), snapshotIDs(pending))
	}
}

// publishTarget decides which instance `--publish` installs. An explicit --as
// wins; a single prepared snapshot is the instance the user is in the middle of;
// none prepared means this is a one-step wrap, which "" reports.
func publishTarget(snaps []generate.Snapshot, as string) (string, error) {
	if as != "" {
		return as, nil
	}
	switch pending := pendingSnapshots(snaps); len(pending) {
	case 0:
		return "", nil
	case 1:
		return pending[0].Instance.ID, nil
	default:
		return "", fmt.Errorf("这个源程序有 %d 份写好、还没落盘的配置（%s）—— 指名一个：twin new <source> --as <id> --publish",
			len(pending), snapshotIDs(pending))
	}
}

// pendingSnapshots are the snapshots with no desktop entry: configured, not yet
// installed. One line of filtering, but it is the entire vocabulary of a
// two-phase wrap, so it gets a name.
func pendingSnapshots(snaps []generate.Snapshot) []generate.Snapshot {
	var out []generate.Snapshot
	for _, s := range snaps {
		if !s.Published {
			out = append(out, s)
		}
	}
	return out
}

func snapshotIDs(snaps []generate.Snapshot) string {
	ids := make([]string, 0, len(snaps))
	for _, s := range snaps {
		ids = append(ids, s.Instance.ID)
	}
	return strings.Join(ids, " ")
}

func snapshotByID(snaps []generate.Snapshot, id string) (generate.Snapshot, bool) {
	for _, s := range snaps {
		if s.Instance.ID == id {
			return s, true
		}
	}
	return generate.Snapshot{}, false
}

// derivationFlags are the options that describe the *instance* rather than the
// act of installing it. --as is not one of them: it selects which instance,
// which is exactly what the install half needs to be told.
var derivationFlags = map[string]bool{
	"name": true, "name-en": true, "comment": true, "comment-en": true,
	"keywords": true, "categories": true, "icon": true, "badge": true,
	"badge-style": true, "level": true, "tmpfs": true,
	"no-tmpfs": true, "no-tray": true,
}

// derivationInputs lists what the user typed that would go into the snapshot.
//
// flag.Visit only reports flags that were actually given, which is what this
// needs: a default is not a decision, and counting one would refuse a
// perfectly ordinary `--publish`.
func derivationInputs(fs *flag.FlagSet, bwrap []string) []string {
	var out []string
	fs.Visit(func(f *flag.Flag) {
		if derivationFlags[f.Name] {
			out = append(out, "--"+f.Name)
		}
	})
	if len(bwrap) > 0 {
		out = append(out, "`--` 之后的 bwrap 透传区")
	}
	return out
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
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	pos, err := parseFL(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 {
		fail("用法：twin verify <id>")
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
	// A configured-but-unpublished instance is one state, not five missing
	// files. Saying so once — with the command that finishes the wrap — is more
	// use than a table of cascading failures, and it is honest about the fact
	// that nothing is broken.
	if !sysx.Exists(p.Desktop(id)) {
		fmt.Printf("%s %s 还没落盘：只有配置 %s，桌面项 / 图标 / 启动器都还没写\n",
			outStyle.Mark(style.LevelSkip), it.ID, p.Profile(id))
		fmt.Printf("  落盘：twin new <source> --as %s --publish    （等价的直接做法：twin regen %s）\n", id, id)
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
		fmt.Printf("   做第一个：twin new wechat\n")
		return exitOK
	}
	fmt.Printf("%s\n", title("📋 实例"))
	fmt.Printf("%-16s %-10s %-8s %-24s %s\n", "id", "level", "别名", "程序", "桌面项")
	unpublished := 0
	for _, it := range items {
		desktop := filepath.Base(p.Desktop(it.ID))
		mark := ""
		// A row without a desktop entry is not a broken instance any more: it is
		// the middle of a two-phase wrap, which is a state twin itself creates.
		if !sysx.Exists(p.Desktop(it.ID)) {
			mark = "  " + outStyle.Yellow("← 未落盘")
			unpublished++
		}
		fmt.Printf("%-16s %-10s %-8s %-24s %s%s\n", it.ID, it.Level, it.EffectiveAlias(),
			filepath.Base(it.Target), desktop, mark)
	}
	if unpublished > 0 {
		fmt.Printf("\n%s 有 %d 个只写了配置、还没落盘：twin new <source> --publish（或 twin regen <id>）\n",
			outStyle.Mark(style.LevelWarn), unpublished)
	}
	// The one thing you come to this table to do next is get rid of a row, and
	// `remove` is the only verb that does it — so name it here rather than
	// leaving it to be discovered in --help.
	fmt.Printf("\n删除某一个：twin remove <id>（不加 --yes 只打印要删什么）\n")
	return 0
}

// ---------------------------------------------------------------- regen

func cmdRegen(p inst.Paths, args []string) int {
	fs := flag.NewFlagSet("regen", flag.ContinueOnError)
	refresh := fs.Bool("refresh", false, "重读 config.toml / rules.d，只补快照里为空的字段")
	pos, err := parseFL(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 {
		fail("用法：twin regen <id> [--refresh]")
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

	// Replaying a snapshot is deterministic by design: it reads the CLI and
	// profile.yml only. Touching config.toml must not silently rewrite a
	// shelf of existing instances — that is what --refresh is for, and even
	// then it only fills fields the snapshot left empty (设计 §3.5).
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
	plan, err := sandbox.Build(it, p, rt)
	if err != nil {
		fail(err.Error())
		return exitFail
	}
	// Same path as `twin new --publish`: replaying a snapshot and installing one
	// are the same operation with two entry points, and giving them separate
	// code would be two chances to drift.
	return installAndCheck(p, it, insp, rt, plan, iconSizeFor(p), installReport{
		lead:    outStyle.Green("✅ 已重新落盘") + " " + outStyle.Bold(it.ID),
		origin:  "实例快照 " + p.Profile(it.ID),
		verbose: false,
	})
}

// installAndCheck writes an instance's artefacts and reports what it found when
// reading them back. It is the shared second half of `twin new --publish` and
// `twin regen`.
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
	printPathMaps(p, it, insp, report.origin, report.verbose)

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
	verbose bool
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
	fmt.Printf("     完整落盘预演（含规则模板与推导值）：twin new <source> --as %s --publish --dry-run --explain\n", it.ID)
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
	printPathMaps(p, it, insp, report.origin, report.verbose)
	if plan.Bwrap != "" {
		fmt.Printf("\n%s\n%s\n", title("🧱 bwrap 调用"), plan.String())
	}
	body, err := it.Render()
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

// runtimeFor and iconSizeFor resolve the environment layer for `regen`, where a
// missing or malformed config.toml must not stop a replay: regen's contract is
// that it reads the CLI and the snapshot, and the config is optional garnish.
// `--refresh` is the path where a malformed file is reported instead.
func runtimeFor(p inst.Paths, needNestedNS bool) sandbox.Runtime {
	cfg, err := config.Load(p)
	if err != nil {
		return sandbox.Runtime{}
	}
	return sandbox.Resolve(p, cfg, needNestedNS)
}

// printProfileHint is the fallback for a machine with no Electron binary to
// borrow an AppArmor label from: instead of a trampoline, one command run as
// root gives the private bubblewrap copy the userns permission directly.
//
// twin never installs this itself. Writing to /etc is exactly the kind of
// side effect a wrap must not have, and the user is one copy-paste away from
// the same result — which is also why this path exists only as a fallback.
func printProfileHint(copyPath string) {
	fmt.Printf("\n%s\n", title("✋ 还差一步：这台机器上没有可用的跳板"))
	fmt.Printf("  这个程序运行时要自己建一个嵌套 user namespace，而内核只放行\n")
	fmt.Printf("  「profile 里写了 userns 许可」的程序（%s）。\n", userns.SysctlSource)
	fmt.Printf("  私有 bwrap 副本故意不带 profile，所以它自己建不了。\n\n")
	fmt.Printf("  两条路，任选其一：\n")
	fmt.Printf("    1) 装一个 Electron 应用（VS Code 就够了），再跑一次 `twin regen <id>`；\n")
	fmt.Printf("       它会自动发现新跳板，什么都不用配置。\n")
	fmt.Printf("    2) 把下面这份 profile 存成 %s 并加载：\n\n", userns.ProfileFile)
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
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "确认删除")
	purge := fs.Bool("purge", false, "连私有状态目录一起删")
	pos, err := parseFL(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 {
		fail("用法：twin remove <id> [--yes] [--purge]")
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
func printDerived(der []generate.Derivation) {
	if len(der) == 0 {
		return
	}
	fmt.Printf("\n%s\n", title("🧩 推导出的默认值（都可用选项覆盖）"))
	for _, d := range der {
		fmt.Printf("  %-9s %s   ← %s\n", d.Key, d.Value, d.Source)
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
// this mapping come from" has an answer the two halves of `twin new` disagree
// about: the configuring half knows the rule and the command line, while the
// installing half is replaying a snapshot the user may have edited by hand.
func printPathMaps(p inst.Paths, it *inst.Instance, insp *generate.Inspection, origin string, verbose bool) {
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
		printMapFix(p, it, key, rulePath)
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
	printMapFix(p, it, key, rulePath)
	if verbose {
		printRuleTemplate(p, it, key, it.Binds)
	}
}

// printMapFix names the two files that can hold this program's knowledge. It is
// what turns "that mapping is wrong" into an action, so it survives into the
// short form.
//
// The two routes are the same in both halves of a wrap, which is why this does
// not have to know which half it is printing for: a rules.d file is for the
// instances the user has not created yet, the snapshot is for this one, and
// `twin regen` installs whatever the snapshot says.
func printMapFix(p inst.Paths, it *inst.Instance, key, rulePath string) {
	fmt.Printf("  改：%s —— 影响这个程序以后新建的实例（twin rules --init %s 生成草稿）\n",
		rulePath, key)
	fmt.Printf("      只改这一个：编辑 %s 里的 bind，再 twin regen %s\n",
		p.Profile(it.ID), it.ID)
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
