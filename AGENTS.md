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
| CLI 选项、退出码、拒绝清单 | [`docs/cli.md`](docs/cli.md) |
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
mkdir -p /tmp/scratch-home/.config/twin
HOME=/tmp/scratch-home ./twin new wechat --dry-run
HOME=/tmp/scratch-home ./twin new wechat
HOME=/tmp/scratch-home ./twin verify wechat2
```

## 3. 目录结构

```
cmd/twin/            CLI 入口：动词分派、flag 解析、全部终端输出
internal/
  tomlz/               TOML 子集的编解码（自写，非通用库）
  inst/                实例 schema（真源）＋ Paths（全部产物路径）＋ 语义校验
  desk/                .desktop 读写（含 Name[xx] / Comment[xx] / Keywords[xx]）
  config/              环境层（config.toml）＋ 应用知识层（rules.d）＋ 内置规则
  generate/            inspect / plan / materialize / unwrap / refresh
    traits.go           从二进制内字符串扫出特征（Wayland/Qt/多合一/嵌套 ns）
    runtime.go          bwrap 私有副本同步 ＋ 跳板脚本
  sandbox/             bwrap 调用构建、透传 lint、Runtime.Resolve（降级链）
  icon/                纯 Go 图标合成（解码、缩放、角标蒙版、5×7 点阵字体）
  userns/              userns 限制判定、AppArmor profile 解析、跳板发现
  tray/                托盘覆盖表的 read-modify-write
  verify/              回读自证
  doctor/              环境自检
  sysx/                系统调用封装（Run / Which / FirstInstalled / EnsureDir / WriteFileAtomic …）
```

依赖方向（**下层不知道上层存在**）：

```
cmd → {doctor, verify, generate, sandbox, inst, config, tray, userns, sysx, desk}
doctor → {inst, sandbox, verify, tray, userns, sysx}
verify → {inst, desk, tray, userns, sysx}
generate → {config, desk, icon, inst, sandbox, sysx}
sandbox → {config, inst, userns, sysx}
config → {icon, inst, tomlz}
inst → {tomlz}
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
   （`DesktopEntry` 与 `it.Render()` 被 dry-run 和 `Materialize` 共用）。用另一套代码画的
   预览就是一句谎话。
6. **CLI 用 `parseFL`，不要直接 `fs.Parse`。** Go 的 `flag` 在第一个非 flag token 处停止，
   而文档里的写法是 `twin new <source> --as <id>` —— 直接用会让 `--as` 及其后的所有选项被
   **静默忽略**。位置参数可以出现在选项之间，这是文档承诺的行为。
7. **产物路径一律从 `$HOME` 派生**，且写进 `instance.toml` 的一律用 `~/` 相对形式
   （`RelStateDir`）—— 快照要能搬到另一台机器重放。启动器里则统一折叠成 `$HOME/...`，
   绝不把生成时刻的 HOME 烤进去。
8. **`gofmt` 是硬门**，`-l` 必须为空。

## 5. 常见任务怎么做

### 加一条内置规则
`internal/config/config.go` 的 `BuiltinRules()`。内置表刻意很短：**静默猜错一个路径，比开口要
一个 `--map` 更糟** —— 加之前必须确认这条路径真的实测过。

### 加一个 `new` 选项
三处联动：
1. `cmd/twin/main.go` 的 `cmdNew`：加 flag，放进 `generate.Options`；
2. `internal/generate/generate.go` 的 `Options` 与 `Plan`：决定优先级与默认值来源；
3. 会进快照的字段还要动 `internal/inst` 的 `toTree` / `Load`，并补一个往返测试。

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
