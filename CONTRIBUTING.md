# 贡献指南

> 给 AI 助手的约定在 [`AGENTS.md`](AGENTS.md)；设计依据在 [`docs/architecture.md`](docs/architecture.md)。
> 本文件讲的是"人怎么参与"。

---

## 1. 这个项目想要什么样的贡献

`twin` 的立场是**宁可不做，不做近似**。所以最有价值的贡献不是"多一个选项"，而是：

- **一条新的实测事实** —— 某个程序在某个桌面环境下的真实行为（附取证方式）；
- **一条内置规则** —— 只在你**亲自跑通**过之后提交（静默猜错一个路径比开口要用户写一条 `bind` 更糟）；
- **一个能抓住静默失败的检查** —— 这个工具存在的理由就是消灭静默失败；
- **一处文档/代码不一致的修正**。

**不被接受的方向**：引入第三方依赖、写 `/etc`、实现没被实测过的身份级别
（`argument` / `env`）、给身份工具加"安全沙箱"承诺、把中文文档翻成英文。

## 2. 环境准备

要求：

- Linux ＋ bubblewrap（`sudo apt install bubblewrap`）
- Go **1.27+**（构建与测试）
- 可选：`python3-gi`（最强的一道自检）、`rsvg-convert` / ImageMagick（SVG 源图标）
- 桌面：GNOME ＋ `ubuntu-appindicators`（只在实测托盘行为时需要）

```bash
git clone <this repo> && cd twin
go build -o twin ./cmd/twin
./twin doctor            # 先跑这个，它会告诉你这台机器缺什么
```

## 3. 质量门

改动完成前，下面三件事**必须全绿**，缺一不可：

```bash
gofmt -l .          # 必须输出为空
go vet ./...
go test ./...
```

一次跑完：

```bash
gofmt -l . && go vet ./... && go test ./...
```

⚠️ 若 `/tmp` 是小容量 tmpfs，`go test` 会报 `no space left on device`（Go 链接每个测试二进制
都要在临时目录落一个几 MB 的文件）。换个临时目录即可：

```bash
GOTMPDIR=$HOME/.cache/gotmp TMPDIR=$HOME/.cache/gotmp go test ./...
```

⚠️ 依赖真实机器状态的诊断测试默认跳过，**不要**把它们加进默认测试集：

```bash
TWIN_REAL_PROBE=1 go test ./internal/userns/ -run RealProbe -v
```

## 4. 手工验收（端到端）

项目**没有**自动化集成测试，理由见 [`docs/development.md`](docs/development.md) §7：真正的端到端
要写进用户的 `~/.local/share/applications`、`~/.local/state`，还要连会话总线 —— 一个会动真实
`$HOME` 的测试套件比它防住的问题更危险。

替代做法是**scratch HOME**：

```bash
mkdir -p /tmp/scratch-home/.config/twin
HOME=/tmp/scratch-home ./twin new wechat --dry-run
HOME=/tmp/scratch-home ./twin new wechat
HOME=/tmp/scratch-home ./twin verify wechat2
```

`twin` 的一切路径都从 `$HOME` 派生，所以这样可以完整验证落盘逻辑而不碰真实环境。

## 5. 代码约定

完整版在 [`docs/development.md`](docs/development.md) §4，摘要：

1. **零第三方依赖**：`go.mod` 不许出现 `require`，不许有 `go.sum`。
2. **注释写"为什么"，不写"是什么"**：记录"不这么做会发生什么"，以及它对应哪次实测。
3. **不制造静默失败**：失败时必须能被使用者看见（硬报错 / 退出码 3，而不是假装成功）。
4. **回读是判据**：凡是写下去的东西都要读回来确认，不信内存里的期望。
5. **`--dry-run` 用与真实落盘相同的代码渲染**，否则预览就是谎话。
6. 产物路径从 `$HOME` 派生；写进快照的一律用 `~/` 相对形式。

## 6. 提交与合并请求

- 一个**自洽的小改动**一个提交，不要攒大提交。
- 提交信息首行用祈使句，说明**为什么**；正文写取舍与实测证据。
- 合并请求请写清：
  - **改了什么 + 为什么**（不是罗列 diff）；
  - **怎么验证的**（`gofmt` / `go test` 输出、scratch HOME 的实际命令与结果）；
  - **有没有碰文档**：改了 CLI 选项 → 更新 `docs/cli.md`；改了配置键 → 更新
    `docs/configuration.md`；撞了新坑 → 记进 `docs/troubleshooting.md`。
- 新增/变更面对用户的选项，请**同步更新 [`CHANGELOG.md`](CHANGELOG.md)** 的 `Unreleased` 段。

## 7. 文档在哪

| 文件 | 内容 |
|---|---|
| [`README.md`](README.md) | 面向使用者：它做什么 / 不做什么、30 秒上手、命令一览 |
| [`AGENTS.md`](AGENTS.md) | 面向 AI 协作者：质量门、包结构、硬性约定、常见任务 |
| [`docs/architecture.md`](docs/architecture.md) | 身份模型、三级阶梯、管线、产物模板、权限与边界 |
| [`docs/cli.md`](docs/cli.md) | 全部子命令与选项、退出码、文件表、拒绝清单 |
| [`docs/configuration.md`](docs/configuration.md) | 三层配置逐键参考、覆盖规则 |
| [`docs/apparmor-userns.md`](docs/apparmor-userns.md) | 嵌套 user namespace：实测事实、退化路线、跳板机制 |
| [`docs/troubleshooting.md`](docs/troubleshooting.md) | 症状 → 根因 → 修法 |
| [`docs/development.md`](docs/development.md) | 包结构、构建与测试、开发环境的坑、扩展点 |

## 8. 许可

提交即表示你同意以 MIT 许可（见 [`LICENSE`](LICENSE)）发布你的贡献。
