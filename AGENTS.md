# AGENTS.md — 给 AI 协作者的项目约定

> 本文件是 **AI 编码助手**（Claude Code / CodeBuddy / Cursor / Copilot …）在本仓库动手前
> 的第一入口。人类贡献者请看 [`CONTRIBUTING.md`](CONTRIBUTING.md)；设计依据看
> [`docs/architecture.md`](docs/architecture.md)。
>
> 若本文件与其它文档冲突：**以本文件为准**，并顺手把冲突改掉。

---

## 0. 一句话

`twin` 是一个**零第三方依赖**的 Go CLI，用 bubblewrap 的零拷贝 overlay 给 Linux 上**已有**的
程序换一个身份 —— 自己的窗口 `app_id`、自己的托盘 `SNI Id`、自己的显示名、自己的私有数据目录。
不碰原程序一个字节，不写 `/etc`，不需要 root，不提供 GUI。

## 1. 动手前必读

| 你要改什么 | 先读 |
|---|---|
| 任何事 | 本文件 |
| 身份模型 / 为什么这么设计 | [`docs/architecture.md`](docs/architecture.md) |
| CLI 选项、退出码、拒绝清单 | [`docs/cli.md`](docs/cli.md) —— 但**选项表不在那里**：它在 `twin <命令> -h`（flag 说明即文档，见 §4.13） |
| 配置文件逐键语义 | [`docs/configuration.md`](docs/configuration.md) |
| bwrap / nested userns / AppArmor | [`docs/apparmor-userns.md`](docs/apparmor-userns.md) |
| 症状 → 根因 → 修法 | [`docs/troubleshooting.md`](docs/troubleshooting.md) |
| 包结构 / 测试策略 / 扩展点 | [`docs/development.md`](docs/development.md) |

## 2. 质量门（改动完成前必须全绿）

```bash
gofmt -l .          # 必须输出为空
go vet ./...
go test ./...

# 一次跑完
gofmt -l . && go vet ./... && go test ./...
```

同一个门也在 `Taskfile.yml` 里，`.github/workflows/ci.yml` 调的就是它 —— **本地绿就等于 CI 绿**，
所以别在 CI 里另抄一份命令（`GOTMPDIR` 那个坑也在里面处理掉了）：

```bash
task check          # = gofmt -l ＋ go vet ＋ go test
task build          # = go build -ldflags "-X main.version=$(git describe …)" -o bin/twin ./cmd/twin
task install        # 构建并装进 $HOME/.local/bin/twin
```

已知的环境坑（**不是**代码问题，别去改代码修它们）：

- ⚠️ `/tmp` 是小容量 tmpfs 时，Go 链接测试二进制会 `no space left on device`：
  ```bash
  GOTMPDIR=$HOME/.cache/gotmp TMPDIR=$HOME/.cache/gotmp go test ./...
  ```
- ⚠️ Go 不一定在 `PATH` 上（本机在 `~/.g/go/bin/go`）—— 用绝对路径。
- ⚠️ 依赖真实机器状态的诊断测试**默认跳过**，只有显式开启才跑。**绝不要**把它们塞进默认
  测试集，那会让结果随机器而变、不可复现：
  ```bash
  TWIN_REAL_PROBE=1 go test ./internal/userns/ -run RealProbe -v
  ```

构建（版本号可注入，默认 `main.version = "0.2.0-dev"`）：

```bash
go build -o twin ./cmd/twin
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o twin ./cmd/twin
```

手工端到端验收**必须用 scratch HOME**，不要动真实 `$HOME`：

```bash
mkdir -p /tmp/scratch-home/.config/twin /tmp/scratch-work
cd /tmp/scratch-work                      # gen 默认写到 $HOME/.local/share/twin/<id>/profile.yml
HOME=/tmp/scratch-home /path/to/twin gen wechat --dry-run
HOME=/tmp/scratch-home /path/to/twin gen wechat
HOME=/tmp/scratch-home /path/to/twin install --dry-run
HOME=/tmp/scratch-home /path/to/twin install
HOME=/tmp/scratch-home /path/to/twin verify wechat2
```
⚠️ `install` 需要 AppArmor profile 时会调 `sudo`。验收时**看到密码提示就 Ctrl-C**（它会退回打印
profile 并以退出码 3 结束，那正是要验的兜底行为）；别在别人的机器上真装那份 profile。

## 3. 目录结构

```
cmd/twin/            CLI 入口：动词分派、flag 解析、全部终端输出
internal/
  tomlz/               TOML 子集的编解码（自写，非通用库；config.toml 与 rules.d 用它）
  yamlz/               YAML 子集的编解码（自写，非通用库；实例定义与存档记录两份 profile.yml 用它）
  inst/                实例 schema（真源）＋ Paths（全部产物路径）＋ 语义校验
  desk/                .desktop 读写（含 Name[xx] / Comment[xx] / Keywords[xx]）
  config/              环境层（config.toml）＋ 应用知识层（rules.d）＋ 内置规则
  generate/            inspect / plan / materialize / unwrap / refresh
    traits.go           从二进制内字符串扫出特征（Wayland/Qt/多合一/嵌套 ns）
    suggest.go          状态目录探测（目录存在 ＋ 二进制内 ~/ 字面量）＋ rules.d 草稿
    runtime.go          bwrap 私有副本同步 ＋ 跳板脚本
  sandbox/             bwrap 调用构建、透传 lint、Runtime.Resolve（降级链）、挂载点形态
                       （mountHelpers 写进启动器 ＋ EnsureMountPoints 供 install 调用）
  icon/                纯 Go 图标合成（解码、缩放、角标蒙版、5×7 点阵字体）
  userns/              userns 限制判定、AppArmor profile 解析、跳板发现
  tray/                托盘覆盖两条路：宿主表 read-modify-write（tray.go）＋ 中立文件表
                       （table.go，扩展读它画图标）＋ 扩展的装入/状态与内嵌的 ext/（ext.go）
  verify/              回读自证
  doctor/              环境自检
  style/               终端着色判定（TTY / NO_COLOR）与状态字形
  sysx/                系统调用封装（Run / Which / FirstInstalled / EnsureDir / WriteFileAtomic …）
```

依赖方向（**下层不知道上层存在**）：

```
cmd → {doctor, verify, generate, sandbox, inst, config, tray, userns, sysx, desk, style}
doctor → {inst, sandbox, verify, tray, userns, sysx, style}
verify → {inst, desk, tray, userns, sysx}
generate → {config, desk, icon, inst, sandbox, sysx}
sandbox → {config, inst, userns, sysx}
config → {icon, inst, tomlz}
inst → {yamlz}
style → {}    ← 只有标准库
```

`inst` 不知道 bwrap，`sandbox` 不知道桌面项。唯一"反向"的知识是 `inst.CheckBwrapToken` ——
它校验 bwrap token，因为那是**实例 schema 的合法性**，不是 bwrap 的实现细节。

## 4. 硬性约定（改代码前先内化）

1. **零第三方依赖。** `go.mod` 允许出现的只有 `module` 行与 `go` 行；**不许**有 `require`，
   **不许**有 `go.sum`。要新能力就自己写一个最小实现（`tomlz` / `icon` 就是这么来的），
   或者走外部命令（SVG 光栅化的三级降级链）。
2. **注释写"为什么"，不写"是什么"。** 本项目注释密度很高，但**没有一句在描述代码在做什么**。
   它们记录的是"不这么做会发生什么"、以及这个决定对应哪次实测故障。改代码时如果删掉一段
   注释，先确认它不是在解释某个坑。
3. **静默失败是头号敌人。** 写新代码前先问：**失败的时候，使用者能看见吗？** 具体地：
   - `config.toml` 语法坏了 ⇒ 硬报错，不静默回退到内置默认；
   - 找不到源图标 ⇒ 拒绝生成，不出齿轮占位图；
   - 托盘写不进去 ⇒ 打印具体命令 ＋ 退出码 3，不假装成功；
   - "这台机器没有某能力" ⇒ 报事实性跳过（OK），**不是** FAIL。
4. **回读是判据，不是礼貌。** 任何"我写了"的地方都必须读回来确认（`gsettings set` 会假成功：
   `exit 0` 但什么都没写）。判据只能用**回读**，不能用"事先猜"—— 代码里不许出现
   `if 在沙箱里 { 跳过 }` 这种基于位置的判断。
5. **`--dry-run` 必须是忠实的。** 预览与真实落盘必须由**同一份代码**渲染
   （`generate.EntryBytes` 与 `it.RenderAs(inst.RoleSnapshot)` 被 dry-run 和 `Materialize` 共用；
   `gen` 用 `RoleConfig`，预览的也就是它写的那份）。`gen` 多写的第二件产物（桌面项正文）同理：
   预览印的那份文本就是它写进文件的那一份，`install` 复制的还是它。用另一套代码画的预览就是
   一句谎话。
6. **CLI 用 `parseFL`，不要直接 `fs.Parse`；`FlagSet` 一律用 `newFlags` 建。** Go 的 `flag` 在
   第一个非 flag token 处停止，而文档里的写法是 `twin gen <source> --as <id>` —— 直接用会让
   `--as` 及其后的所有选项被**静默忽略**。位置参数可以出现在选项之间，这是文档承诺的行为。
   另一条：`FlagSet` 的输出被 `newFlags` 丢掉，错误经 `usageError` 出手 —— 出错的约定是
   **只打印那一行错误**，不复述帮助（`twin --help` 才是帮助的出口，`-h` 才给该命令的选项表）。
   把整份 "Usage of gen:" 或 `usage` 常量倒在笔误下面，会把真正有用的那一行顶出屏幕。
7. **产物路径一律从 `$HOME` 派生**，且写进实例定义的一律用 `~/` 相对形式
   （`RelStateDir`）—— 那份文件要能搬到另一台机器重放。启动器里则统一折叠成 `$HOME/...`，
   绝不把生成时刻的 HOME 烤进去。`gen` 的默认落点（`Paths.Profile(id)`）同样如此，而且
   **一个 id 一份**：这份配置要在下一个 shell、明天、别人的 bug 报告里都还找得到，而"当前目录"
   只是这次站的地方，两份 wrap 挤同一个文件名还会互相覆盖 —— 想写到别处就显式 `-o`。
   定义里的路径还可以**写成相对的**，基准是那份文件自己所在的目录（`inst.Load` 解析，见 §5"两段式"）
   —— 一个 bundle 因此能整体搬走。唯一不解析的是 `bwrap.extras`（那是一份 bwrap 选项列表，一行按
   空格拆开照搬，猜不出哪个 token 是路径；`~` 是唯一例外，它取自 `Paths.Expand`），而 `rules.d`
   里的相对路径被指名拒绝：规则服务这个程序的所有实例，它没有"自己所在目录"这个基准。
8. **输出走 `internal/style`，判定按流。** 不在别处写转义序列、不自己判 `isatty`：stdout 与
   stderr 各判一次（`> log` 时两者结论不同），非终端一律纯文本。状态行必须同时有字形和颜色
   （`st.Mark(...)`），因为 `| grep` / 无颜色终端 / bug 报告里只剩字形。
9. **默认输出是给人读的，不是给人查的。** `twin gen` 只打印两个落点与下一步；逐键说明用
   `yamlz.SetComment` 写在那份配置里，人读到它的那一刻正在编辑它 —— 同一件事在终端和
   文件里各说一遍，就是两处将来会各表一套。第二件产物（桌面项正文）的路径留下来是因为
   "想让菜单里显示别的字"要编辑的正是它，只给 `profile.yml` 会把人指到一个改不动的文件上。
   唯一的例外是"一条映射都没有"的警告：它描述的是别处不会再报的后果，所以留在终端上。`twin install` 打印产物、路径映射、下一步；自检**只在
   有非 OK 项时**才出现（`formatProblems`，而 `twin verify` 用的是 `formatFindings`，把每一条
   都列出来正是它的职责），而且通过项不带修复命令 —— 那会给读者一条他没有的问题的命令。
   `--explain` 是唯一例外：那时整份报告就是用户要的答案。其余细节（推导值、规则模板、候选目录、
   改哪个文件、手工验证命令）也走 `--explain`。加新输出前先问：这条不进精简版会不会让人做错事？
   会，才是默认；不会，就写进注释或 `--explain`。
10. **一条命令只做一件事，安装是显式的。** `twin gen` **只写一份配置**，`twin install` 才落盘。
    理由见 §5"加一个 `gen` 选项"：一次 wrap 里唯一随程序而变、又猜错了不出声的东西是
    路径映射，它该先落成一份能读能改的文件。
11. **实例定义只有一份，`install` 不重写它；正文只有一份，`gen` 也只写一次。** 前者的默认落点是
    `~/.local/share/twin/<id>/profile.yml`，`install <id>` 与 `reinstall <id>` 读的是同一份，
    `Materialize` 见到"定义已在此处"就跳过写入（`definitionIsHere`）—— 写回一次就是一次解析往返，
    用户手写的注释与排版会没。**桌面项正文同理**：`gen` 在它不存在时才按源桌面项生成，之后每次
    `install` 只是把 `override.desktop` 指的那份**逐字复制**成 `~/.local/share/applications/<id>.desktop`
    —— 它是这一份包裹里唯一"改写"而不是"阅读"的东西（菜单里那几个字），所以重写一次就等于用推导
    覆盖用户的编辑。想改显示名就改那份文件，别指望 `override.icon.*` 那一类键再回头影响它。
    只有 `-p` 指到别处时，实例目录里才另写一份存档记录
    （`RoleSnapshot`）供 `reinstall` 重放。两条路都不从 CLI 重新推导 —— 所以"带着描述实例的选项
    去装一个已存在的实例"这件事**不存在**了：`install` 根本没有那些选项（见 §5"加一个 `gen` 选项"）。
12. **`gofmt` 是硬门**，`-l` 必须为空。
13. **选项表只住在 `twin <命令> -h` 里。** flag 的说明字符串就是这张表的正文，所以**默认值、
    可接受取值、被拒绝的取值**都要写在那一行里 —— 文档里再抄一份，得到的是两处会各表一套，
    而默认值属于"猜错了一个字都不会有人告诉你"的那类信息。PrintDefaults 打不出来的用法
    （`--` 透传区、`install <id> | -p` 的二选一）进 `cmd/twin/main.go` 的 `helpFooter`，也不进文档。
    文档只写**为什么**：这个命令是干什么的、这条边界对应哪次实测。
14. **挂载点两端同形，形态看磁盘。** 一条映射的两端必须是同一种东西（文件挂到目录上 bwrap 直接
    拒绝），而 `mkdir -p` 打在文件上还会报 `Already exists` 并让 `set -e` 的启动器在 exec 之前死掉。
    所以两端都建，形态由**磁盘上已有的那一端**决定，两端都没在时按目录 —— 可写根下也照建：省掉它，
    bwrap 会把挂载点建到**宿主的家目录**里，而且一律建成目录。唯一不建的是**根挂载**（两端都是
    `/`，本来就在那儿）。这条规则有**两份实现**，各管一段时间：生成的启动器里是
    `sandbox.mountHelpers` 的 `mount_point` / `mount_pair`（每次启动判一次），install 里是
    `sandbox.EnsureMountPoints`（落盘时判一次，否则一个被造成目录的私有副本会一直留着 —— 启动器
    不碰已经存在的路径）。**改一处必须改两处**。
15. **沙箱基座是文件里的一段文本，不是 Go 里的常量；而这一次调用的参数只有四个键。**
  `sandbox.BaseFor` 只负责在 `gen` 时把它**写进** `profile.yml`：根是 `bwrap.root` 里的**一个词**
  （`dev` = `--dev-bind / /`）；其余每一条都是 `bwrap.extras` 里的一行 —— `--proc /proc`、
  `--tmpfs /dev/shm`、（程序走 Wayland 时的）`--tmpfs /tmp` 或把宿主那个换回来的 `--bind /tmp /tmp`、
  `--die-with-parent`；`--setenv TWIN_INSTANCE <id>` → `bwrap.env`；数据目录 → `bwrap.bind`。
  `sandbox.Build` 一字不改地照发；twin 不再自己补任何 bwrap 选项。之所以这么费劲："这个程序拿到
  哪个根"是定义里唯一随程序而变、又曾经完全表达不了的决定 —— 基座原先是硬编码的 `--ro-bind / /`，
  要碰设备的程序只能从透传区开口，而透传区排在所有映射**之后**，在那里重新绑一次根会把前面的
  挂载全盖掉。
  **`proc` / `tmpfs` / `remountRO` 曾经各有自己的键，现在都是 `extras` 里的一行**（连同 `bwrap.args`
  与 `bwrap.extra`，五个旧键全部指名报错并给出新写法）。改回去的理由只有一个：它们和 `extras` 本来
  就是同一次调用的两半，分成两份列表之后"哪一条压住哪一条"没有任何地方写着 —— 宿主 `/tmp` 那一支
  就是被这个逼进 `bind` 的（它得排在 tmpfs 之后，而**两个列表落地在命令里的位置**是 `extras` 在前、
  `bind` 在后）。一行一条的列表里行序就是命令序，那件事于是退化成"写在后面"。代价与边界照实说：
  `extras` 一行按空格拆，**值里不能有空格**；`--remount-ro` 现在没有自己的键，收紧 `bind` 的目标时
  写在 `extras` 的哪一行都成立（它由 `Build` 统发在映射之后），只有目标本身也是 `extras` 里的一行时
  才要守行序 —— 这是唯一一处靠人守的规则；它也只能作用在自己就是一条挂载的路径上（宿主那条根
  挂载底下的普通路径点错了，bwrap 直接拒绝启动，实测）。
  **根挂载反过来，从 `bind` 里收回到一个键**（`bwrap.root: ro|rw|dev`，`inst.Instance.RootBind`
  把它渲染成普通映射，所以两种写法的渲染只有一处）。理由不是"一个挂载该有一个键"那种对称性，
  而是**它的位置不由文件决定**：`Build` 把它发在所有东西之前（bwrap 按顺序挂载、后写的胜出，
  根排在 overlay 别名之后会把宿主的目录盖回程序的运行路径上，别名和程序一起消失），于是写在
  `bind` 里等于教读者这份文件的顺序对它有含义 —— 而它恰恰是唯一没有那种含义的一条。三段的
  `/:/:dev` 也不该是"换这个程序的根"的入口。**老定义照旧**：`bind` 里那条 `/:/:dev`（或 `/:/:ro`）
  与 `extras` 里的 `--dev-bind / /` 都还算数，`Build` 认三处；**说过两遍会被拒**（`root` 与 `bind`
  里那条同时存在），因为两条挂载都会发出去，谁赢就成了发射顺序的事。`/:/ro` 这种两段写法照旧拒
  （它读成"把根挂到 `/ro`"）。`extras` 整体发在所有映射**之后**、`env` **之前**，所以能压住任何一条
  映射（`--remount-ro /`、`--dev-bind / /` 都能收紧/换掉根），反过来不行。
  同一份 `bind` 清单在终端那边要**滤掉根挂载**（只剩老定义里那种）：`install` 的「路径映射」数的
  是这个程序的数据目录，而 `uncoverHints` 拿映射的**目标端**做前缀比较 —— 根的目标端是 `/`，
  留下它那一段建议就永久闭嘴了。

  **`/tmp` 两个方向都必须是可写的**：建不出临时目录的程序会在跟 twin 无关的地方炸（Chromium 的单
  实例 socket → `process_singleton_posix.cc: Failed to create socket directory`，Electron 实测）。
  走 tmpfs 的程序拿空的私有 `/tmp`（`extras` 里那行 `--tmpfs /tmp`），X11 那一路拿宿主那个 ——
  tmpfs 会把 `/tmp/.X11-unix/X0` 一起带走，所以它是**同一份列表里、排在它后面**的一行
  `--bind /tmp /tmp`：两行都留在文件里，读者不必记得去别处删一行（点不同了：顺序不再由两个列表的
  落地位置决定，而由同一份列表里的行序决定）。映射本身写在 `bwrap.bind`（`rules.d` 里那条仍在顶层，
  那是应用知识、不是一个 bwrap 调用），顶层 `bind` **指名报错** —— 读成"没有映射"会让程序看不见
  自己的文件，而那时离这次编辑已经很远。会改名的 level 之下**根挂载三处都没有**（`root` 空着、
  `bind` 里没有 `/:/…`、`extras` 也空着）就**指名报错** —— 少了根挂载，程序会在一个什么都没挂的
  命名空间里"找不到自己"，而那条失败信息里不会有一个字提到 twin。空值发出零个参数，不会变成
  "要默认的那个"；往 `bwrap.extras` 里加新的一行之前先问：去掉它之后这里还写得进去吗。
16. **启动器里每一步失败都要自己说一句话。** 桌面项启动的进程没有终端，脚本死在 `exec` 之前
   在用户眼里就是"点了没反应"，而这是 `verify`/`doctor` 补不回来的（等到能跑它们的时候，用户
   早忘了刚才点过什么）。所以 `mount_point` / `mount_pair` / `exec` 后面都跟着
   `|| twin_fail "$?" …`（`sandbox.failHelpers`），**加新的前置步骤就得加它的那一句**：生成物里
   没有兜底的 ERR trap —— bash 要 `set -E` 才让 trap 进函数，而那时报出来的是函数内部的一行，
   不是"哪条映射"。三条容易踩空的地方：
   - 弹窗链只收**正文能选中复制**的工具（`xmessage` / `notify-send` 不算）：复制不出来，这个
     窗口就白弹了；
   - 调用方写的是 `mount_pair … || twin_fail …`，而 `set -e` 在**被当作 `||` 左侧调用的函数**
     体内是关掉的（bash 的规矩）⇒ `mount_pair` 里第一个 `mount_point` 后面必须跟 `|| return 1`，
     否则源端建不出来的失败会被目标端的成功盖掉，最后由 bwrap 报一句跟真因无关的话；
   - 只在**这一刻确实是图形会话**时才弹（没有控制终端 ＋ 有显示），终端里跑的时候一句都不多说。
   报告落在 `$STATE/launch-error.log`，弹窗正文与它逐字相同（一次拼好、两处使用）。


## 5. 常见任务怎么做

### 加一条内置规则
`internal/config/config.go` 的 `BuiltinRules()`。内置表刻意很短：**静默猜错一个路径，比开口要
用户自己写一条 `bind` 更糟** —— 加之前必须确认这条路径真的实测过。

想让用户不必自己猜路径时，不要往内置表里加：那正是 `internal/generate/suggest.go` 的活儿。
`SuggestStateDirs` 只报"这台机器上目录真的存在"（事实，`twin gen` 每次都打印），
`SuggestStateDirsDeep` 再叠加二进制里的 `~/…` 字面量（猜测，只有 `twin rules --init` 会读），
`RuleDraft` 把前者写成生效的 `bind` 条目、后者全部注释掉。**任何探测结果都不得自动生效** ——
`--init` 是唯一的出口，因为它是一条显式命令。

### 加一个 `gen` 选项
四处联动：
1. `cmd/twin/main.go` 的 `cmdGen`：加 flag，放进 `generate.Options`；
   **flag 那一行的说明就是这份选项表的正文**（默认值、可接受取值都写在里面，见 §4.13）——
   如果它的用法 PrintDefaults 表达不了（例如它给 `--` 透传区加了一条规则），进 `helpFooter`；
2. **决定它属于哪一类**：描述**实例**（会进 profile.yml）还是描述**这次动作**
   （`-o` / `--force` / `--dry-run` / `--explain`）。只有 `gen` 拥有前者 ——
   `install` 的 flag 表里一个实例选项都没有，所以"带着描述实例的选项去装一个已存在的实例"
   这件事**无法表达**：那不是一条运行时判断，而是动词拆分本身（见 §4.11）；
3. `internal/generate/generate.go` 的 `Options` 与 `Plan`：决定优先级与默认值来源；
4. 会进配置的字段还要动 `internal/inst` 的 `toTree` / `Load`，补一个往返测试，并给它加一条
   `SetComment` 说明 —— 终端不再解释任何键，注释是它唯一的说明书（`TestEverySnapshotKeyIsExplained`
   会拦下漏写的）。

### 两段式（`twin gen` / `twin install`）
- 前半段（`twin gen`）写**两份**东西，都是"用户自己的文档"：**实例定义**（`it.Render()` ＋
  `writeConfigFile`，`inst.RoleConfig`，默认落在 `~/.local/share/twin/<id>/profile.yml`，
  `Paths.Profile(id)`，跟 `$HOME` 派生、一个 id 一份，见 §4.7；`-o` 可以写到任何地方）与
  **桌面项正文**（`generate.EntryBytes`，落在定义旁边 —— 定义里的 `override.desktop` 就是相对
  它写的相对路径，所以 `-o` 挪到哪正文就跟到哪）。id 是 `Plan` 推出来的，所以默认落点只能在
  `planInstance` 之后算。定义那份拒绝覆盖已存在的文件，除非 `--force`：撞上默认落点时说的是
  "这份已经是那个实例的定义 —— 直接 `twin install <id>`"，而不是泛泛的"换个 -o"。正文那份
  反过来：**已经存在就不碰**（它是用户改过的正文，覆盖等于把菜单里那几个字回退掉）。
- 后半段（`twin install <id>`）读**同一份文件**落盘，`Materialize` 不重写它，正文也只复制不重写
  （§4.11）；缺正文时才按 `source.desktop` 重新生成一份。`-p` 指到别处时，实例目录里才另写一份
  `RoleSnapshot` 的存档记录，供 `reinstall` 重放；`Role` 这层就是为这个例外存在的，两个 header
  各自指向读它的那条命令。
- 配置里的注释由 `inst.toTree` 用 `yamlz.SetComment` 挂上，渲染只依赖 `Instance`。`yamlz.Parse`
  **丢掉**注释 —— 这也是 install 不能重写定义的原因：写回一次，手写的注释就没了。
- **定义里的路径可以写成相对的**，基准是那份文件自己所在的目录：`inst.Load` 解析一次
  （  `resolvePath`），下游每个读者拿到的是同一个绝对路径。读者自己解析不行 —— 那等于按"用户当时站
  在哪个目录"解析。`~` 仍然留给 `Paths.Expand` 在用的时候折叠成本机 HOME（§4.7），而
  `override.desktop` 也走同一条路（定义与正文因此能一起搬走）。`bwrap.extras` 照旧不解析
  （§4.3 的规矩，只有 `~` 例外），`rules.d` 里的相对路径被 `config.checkRulePaths` 指名拒绝。
- 前后两半不靠重新推导 id 相认，而靠 `<id>`（或 `-p` 显式指的那份文件）：一次 `gen` 只写一份
  定义，那个 id 就被那份文件占住了，重新推导只会悄悄挪到 `wechat3`。
- `install` 接 `<id>` 或 `-p <文件>`，`len(pos) > 1`、两者都给、以及一个都不给都直接报错：
  同时给会让其中一个被静默忽略，都不给就只能猜要装哪个实例。要包哪个程序永远是 `gen` 那一步
  说过的。留一条重新推导的路，就是给"同一个问题两处答案"留机会。
- 安装半段只有一条路（`installAndCheck`），`twin install` 与 `twin reinstall` 共用；给它们写
  第二份实现就是给"install 会做、reinstall 不做"留机会。

### 加一个自检项
`internal/verify/verify.go` 的 `Run()` 里 `add(name, status, detail, fix)`。若这一项**只能由人
在图形会话里完成**（写 dconf 表那种），把 `Finding.Manual` 置真 —— 它驱动退出码 3；打印上和别的
非 OK 项没有区别，`install` 就地列在 `🔍 自检` 那一段、`verify` 列在整份报告里。**可选项不要标
Manual**：那会让它和非做不可的那一步共用一个退出码（托盘尺寸助手扩展就是这么被摘出去的）。

### 加一个新 Trait
`internal/generate/traits.go` 的 `Traits` 结构体 ＋ 一个 needle。扫描是**按二进制内字符串**做的
（`strings` 语义），needle 要足够独特、不会误命中。

### 发新版本
`cmd/twin/main.go` 的 `version` 常量是默认值（构建时可用 `-ldflags` 覆盖）；同时更新
[`CHANGELOG.md`](CHANGELOG.md)。

## 6. 不要做的事

- ❌ 不要引入第三方依赖（包括"顺手"加个工具库）。
- ❌ 不要写 `/etc`、不要装 AppArmor profile、不要创建 systemd 单元。走投无路时打印 profile
  文本并以退出码 3 结束。
- ❌ 不要实现 `--level argument` / `env`（没过实测）；不要放开 `--argv0`（会破坏"窗口
  `app_id` 与托盘 `SNI Id` 同名"的不变量）。
- ❌ 不要让 `isolation.unshare` 接受 `net`。
- ❌ 不要把根挂载写回 `bwrap.bind` 里那条 `/:/:dev`：那正是 `bwrap.root` 替掉的东西 —— 它在文件里
  的位置没有含义，而三段式不该是"换这个程序的根"的入口。老定义照旧读（`Build` 认三处），但新写的
  只走 `root` 那个键，两处同时出现会被 Check 拒掉。
- ❌ 不要把桌面项正文拆回 `profile.yml` 里的几个键（`desktop.name` / `keywords` / `categories` …）：
  正文是一份**文件**，那条拆分是它的前身（那次改动就是为了让"改菜单里显示什么"变成一次文本编辑，
  而不是"改配置里一行、再 install 一遍"）。同理不要让 `install` 去"补全"那份正文 —— 生成一次之后
  它每个字节都属于编辑它的人。
- ❌ 不要写会动真实 `$HOME` 的自动化测试。
- ❌ 不要让托盘覆盖**同时**走两条路（扩展读的文件表 ＋ 宿主 `custom-icons` 表）。一次只走一条，
  判据只有 `tray.ExtState.Usable()` 一个，而它被四处读（`applyTray` 的选路、预演、`verify`、
  `doctor`）—— 改判据要四处一起改，改出的措辞由 `ExtState.ShortWhy()` 统一供。
- ❌ 不要为了让托盘那条路"更结实"顺手把扩展改成写 `custom-icons`（那是把两条路合回一条 dconf 键，
  选路就白做了）；也不要让扩展 import 宿主的模块 —— 补丁只碰被补的那个函数本来就在用的三个名字。
- ❌ 不要"顺手"重排无关代码、改注释风格、或把中文文档翻译成英文 —— diff 要能一眼看明白。

## 7. Git 约定

- 提交信息：首行祈使句、说明**为什么**（而非罗列改了什么）；必要时正文写清取舍。
- 一个自洽的改动一个提交，不要攒大提交。
- **不要**在没有明确要求时提交、改动 git config、或 force push。
