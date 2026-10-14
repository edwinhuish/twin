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
task build          # = go build -ldflags "-X main.version=$(git describe …)" -o twin ./cmd/twin
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
cd /tmp/scratch-work                      # gen 默认往**当前目录**写 profile.yml
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
  yamlz/               YAML 子集的编解码（自写，非通用库；工作副本与存档两份 profile.yml 用它）
  inst/                实例 schema（真源）＋ Paths（全部产物路径）＋ 语义校验
  desk/                .desktop 读写（含 Name[xx] / Comment[xx] / Keywords[xx]）
  config/              环境层（config.toml）＋ 应用知识层（rules.d）＋ 内置规则
  generate/            inspect / plan / materialize / unwrap / refresh
    traits.go           从二进制内字符串扫出特征（Wayland/Qt/多合一/嵌套 ns）
    suggest.go          状态目录探测（目录存在 ＋ 二进制内 ~/ 字面量）＋ rules.d 草稿
    runtime.go          bwrap 私有副本同步 ＋ 跳板脚本
  sandbox/             bwrap 调用构建、透传 lint、Runtime.Resolve（降级链）
  icon/                纯 Go 图标合成（解码、缩放、角标蒙版、5×7 点阵字体）
  userns/              userns 限制判定、AppArmor profile 解析、跳板发现
  tray/                托盘覆盖表的 read-modify-write ＋ 宿主侧尺寸助手（ext.go ＋ 内嵌的 ext/）
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
   （`DesktopEntry` 与 `it.RenderAs(inst.RoleSnapshot)` 被 dry-run 和 `Materialize` 共用；
   `gen` 用 `RoleConfig`，预览的也就是它写的那份）。用另一套代码画的预览就是一句谎话。
6. **CLI 用 `parseFL`，不要直接 `fs.Parse`；`FlagSet` 一律用 `newFlags` 建。** Go 的 `flag` 在
   第一个非 flag token 处停止，而文档里的写法是 `twin gen <source> --as <id>` —— 直接用会让
   `--as` 及其后的所有选项被**静默忽略**。位置参数可以出现在选项之间，这是文档承诺的行为。
   另一条：`FlagSet` 的输出被 `newFlags` 丢掉，错误经 `usageError` 出手 —— 出错的约定是
   **只打印那一行错误**，不复述帮助（`twin --help` 才是帮助的出口，`-h` 才给该命令的选项表）。
   把整份 "Usage of gen:" 或 `usage` 常量倒在笔误下面，会把真正有用的那一行顶出屏幕。
7. **产物路径一律从 `$HOME` 派生**，且写进实例定义的一律用 `~/` 相对形式
   （`RelStateDir`）—— 那份文件要能搬到另一台机器重放。启动器里则统一折叠成 `$HOME/...`，
   绝不把生成时刻的 HOME 烤进去。唯一的例外是 `gen` 的输出文件：它在**当前目录**，那是用户的
   选择，不是机器的属性。
8. **输出走 `internal/style`，判定按流。** 不在别处写转义序列、不自己判 `isatty`：stdout 与
   stderr 各判一次（`> log` 时两者结论不同），非终端一律纯文本。状态行必须同时有字形和颜色
   （`st.Mark(...)`），因为 `| grep` / 无颜色终端 / bug 报告里只剩字形。
9. **默认输出是给人读的，不是给人查的。** `twin gen` 只打印文件路径与下一步；逐键说明用
   `yamlz.SetComment` 写在那份配置里，人读到它的那一刻正在编辑它 —— 同一件事在终端和
   文件里各说一遍，就是两处将来会各表一套。唯一的例外是"一条映射都没有"的警告：它描述的是
   别处不会再报的后果，所以留在终端上。`twin install` 打印产物、路径映射、下一步；自检**只在
   有非 OK 项时**才出现（`formatProblems`，而 `twin verify` 用的是 `formatFindings`，把每一条
   都列出来正是它的职责），而且通过项不带修复命令 —— 那会给读者一条他没有的问题的命令。
   `--explain` 是唯一例外：那时整份报告就是用户要的答案。其余细节（推导值、规则模板、候选目录、
   改哪个文件、手工验证命令）也走 `--explain`。加新输出前先问：这条不进精简版会不会让人做错事？
   会，才是默认；不会，就写进注释或 `--explain`。
10. **一条命令只做一件事，安装是显式的。** `twin gen` **只写一份配置**，`twin install` 才落盘。
    理由见 §5"加一个 `gen` 选项"：一次 wrap 里唯一随程序而变、又猜错了不出声的东西是
    路径映射，它该先落成一份能读能改的文件。
11. **存档快照只被 `reinstall` 重放。** `install` 按工作副本重新渲染它，`reinstall` 按它重放。
    两条路都不从 CLI 重新推导 —— 所以"带着描述实例的选项去装一个已存在的实例"这件事**不存在**了：
    `install` 根本没有那些选项（见 §5"加一个 `gen` 选项"）。
12. **`gofmt` 是硬门**，`-l` 必须为空。
13. **选项表只住在 `twin <命令> -h` 里。** flag 的说明字符串就是这张表的正文，所以**默认值、
    可接受取值、被拒绝的取值**都要写在那一行里 —— 文档里再抄一份，得到的是两处会各表一套，
    而默认值属于"猜错了一个字都不会有人告诉你"的那类信息。PrintDefaults 打不出来的用法
    （`--` 透传区、"这个动词不接受位置参数"）进 `cmd/twin/main.go` 的 `helpFooter`，也不进文档。
    文档只写**为什么**：这个命令是干什么的、这条边界对应哪次实测。

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
2. **决定它属于哪一类**：描述**实例**（会进 profile.yml，进而进快照）还是描述**这次动作**
   （`-o` / `--force` / `--dry-run` / `--explain`）。只有 `gen` 拥有前者 ——
   `install` 的 flag 表里一个实例选项都没有，所以"带着描述实例的选项去装一个已存在的实例"
   这件事**无法表达**：那不是一条运行时判断，而是动词拆分本身（见 §4.11）；
3. `internal/generate/generate.go` 的 `Options` 与 `Plan`：决定优先级与默认值来源；
4. 会进配置的字段还要动 `internal/inst` 的 `toTree` / `Load`，补一个往返测试，并给它加一条
   `SetComment` 说明 —— 终端不再解释任何键，注释是它唯一的说明书（`TestEverySnapshotKeyIsExplained`
   会拦下漏写的）。

### 两段式（`twin gen` / `twin install`）
- 前半段（`twin gen`）只有一个写点：`it.Render()` ＋ `writeConfigFile`，写的是**工作副本**
  （`inst.RoleConfig`），默认落在**当前目录**的 `profile.yml`（`DefaultProfileName`）——
  那是用户的选择，不是机器的属性
  （§4.7）。它拒绝覆盖已存在的文件，除非 `--force`：这份文件正是接下来要被人读、被人改的那一份。
- 后半段（`twin install`）读那份配置、按 `inst.RoleSnapshot` 重新渲染一份写进
  `~/.local/share/twin/<id>/profile.yml`，再由 `Materialize` 落盘。两半的字节**本来就该不同**：
  header 要说明这份文件是"被 `reinstall` 重放的存档"还是"被 `install` 读的工作副本"，
  这就是 `Role` 这层存在的全部理由。
- 配置里的注释由 `inst.toTree` 用 `yamlz.SetComment` 挂上，渲染只依赖 `Instance`。`yamlz.Parse`
  **丢掉**注释 —— 手改进来的说明不会被下一次 `gen` 带偏，也所以往返必须是逐字节稳定的。
- 前后两半不靠重新推导 id 相认，而靠**显式指名的那份文件**（`install -p <文件>`，默认
  `./profile.yml`）：一次 `gen` 只写一份配置，那个 id 就被那份文件占住了，重新推导只会悄悄挪到
  `wechat3`。
- `install` 不接受位置参数（`len(pos) > 0` 直接报错）：要装哪份配置用 `-p` 说，要包哪个程序是
  `gen` 那一步说过的。留一条重新推导的路，就是给"同一个问题两处答案"留机会。
- 安装半段只有一条路（`installAndCheck`），`twin install` 与 `twin reinstall` 共用；给它们写
  第二份实现就是给"install 会做、reinstall 不做"留机会。

### 加一个自检项
`internal/verify/verify.go` 的 `Run()` 里 `add(name, status, detail, fix)`。若这一项**只能由人
在图形会话里完成**，把 `Finding.Manual` 置真 —— 它会被 `printManual` 收集，并驱动退出码 3。

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
- ❌ 不要写会动真实 `$HOME` 的自动化测试。
- ❌ 不要"顺手"重排无关代码、改注释风格、或把中文文档翻译成英文 —— diff 要能一眼看明白。

## 7. Git 约定

- 提交信息：首行祈使句、说明**为什么**（而非罗列改了什么）；必要时正文写清取舍。
- 一个自洽的改动一个提交，不要攒大提交。
- **不要**在没有明确要求时提交、改动 git config、或 force push。
