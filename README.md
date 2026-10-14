# twin

> 给**已有**程序一个独立身份：它自己的窗口名、自己的托盘图标、自己的私有数据目录 ——
> 不碰原程序的一个字节，不需要 root，不需要守护进程。

`twin` 是一个 Go 写的命令行工具，用于在 Linux 桌面（GNOME / Wayland 优先）上把任意一个
已安装程序"再包一层"，让它以**另一个身份**运行。典型用途：同一个程序开两份互相隔离的实例
（两个账号、两套配置、两个任务栏图标）。

```console
$ twin gen wechat       # 在当前目录写出 profile.yml：独立窗口名、独立托盘图标、独立账号状态
$ twin install          # 读它，落盘，然后自检这条身份链路的每一环
```

最短可用命令就是上面这两行 —— id、显示名、说明、角标、关键词、状态路径全部从源程序的
`.desktop` 与二进制里推导出来，落盘前**逐条打印来源**，都可用选项覆盖。

---

## 它解决什么问题

一个程序在桌面上"看起来是谁"，由三条互不相干的通道决定，而这三条通道**没有任何一个**
听命令行参数的：

| 通道 | 由什么决定 | 谁在读 |
|---|---|---|
| 窗口身份 `app_id` | Wayland 下是 `/proc/self/exe` 的 **basename**（X11 下是 `WM_CLASS`） | GNOME Shell 的窗口→应用归属、任务栏图标合并 |
| 托盘身份 `SNI Id` | `argv[0]` 的 **basename** | 托盘宿主（`ubuntu-appindicators`）的图标查找表 |
| 搜索 / 显示名 | 桌面项文件的 `Name=` / `Keywords=` / 图标 | GNOME 应用网格、搜索 |

所以"改个名字"这件事，唯一的通用做法是**让程序真的从另一个路径名启动**，再补一个同名
的桌面项。`twin` 把这件事自动化了，用的是 bubblewrap 的零拷贝 overlay：原目录一个字节
都不改，只在命名空间里多出一个别名文件。

## 它不做什么

诚实地划边界，是这个工具设计的一半。以下事情 `twin` **不会**做，也**不假装**做了：

- ❌ 不修改、不复制原程序的目录（overlay 是零拷贝、只在命名空间内）
- ❌ 不创建 systemd 单元，不写 `/etc` 下除**一份 AppArmor profile** 以外的任何东西 —— 那份是
  "内核不放行非特权 userns、机器上又没有跳板"时的唯一退路，`twin install` 会问你密码去装它，
  **不给密码就用不了它**（那时打印 profile 全文，你自己决定）。
- ❌ 不隔离网络（`isolation.unshare` 里出现 `net` 会被直接拒绝）
- ❌ 不做"权限沙箱" —— 它给的是**身份**，不是安全边界
- ❌ 不提供 GUI（为什么不做，见 [`docs/architecture.md`](docs/architecture.md) §7）

## 30 秒上手

```bash
# 0. 先自检这台机器能不能干活，输出会告诉你下一步敲什么
twin doctor

# 1. 只看不写：这个程序会被包成什么样
twin inspect wechat

# 2. 写配置：当前目录多一个 profile.yml；每个键的说明都写在文件里的注释里
twin gen wechat

# 3. 改配置（可选）：`bind` 那一节最容易猜错，每个键的说明都写在文件里的注释里
$EDITOR profile.yml

# 4. 安装 ＋ 自检，然后从桌面项重新启动这个实例（⚠️ 见下面"为什么必须重启"）
twin install
twin verify wechat2
```

一份配置可以放别处、可以有几份（多实例、多台机器共用）：

```bash
twin gen wechat -o ~/wraps/wx-work.yml
twin install -p ~/wraps/wx-work.yml
```

**为什么是两步**：一次 wrap 里唯一随程序而变、又猜错了不出声的，是"隔离哪些数据目录"。所以先把它
落成一份能读能改的 `profile.yml`，再安装 —— 而不是先装好、再在一屏输出里让你事后发现映射是错的。
放在**当前目录**是同一个道理：它是这份 wrap 里唯一需要人读一遍的东西，放在你**已经选好的**那个
目录里，才能 diff、提交、拷到另一台机器。

安装后你会发现多了这些东西：

```
~/.local/share/applications/wechat2.desktop     ← 身份锚点
~/.local/share/appicons/wechat2.png             ← 带角标的图标
~/.local/share/twin/wechat2/launch           ← 薄启动器（bash，exec bwrap）
~/.local/share/twin/wechat2/profile.yml      ← 安装时的存档快照（install 按你手上那份 profile.yml 渲染它）
~/.local/state/twin/wechat2/                 ← 这个实例的私有数据目录
```

**为什么必须重启实例**：桌面项是**后写的**，而 GNOME 不会给一个**已经存在**的窗口重算
归属。生成完之后要**完全退出**再从这个新桌面项启动一次，才会看到新的图标与名字。

**数据目录也值得看一眼**：`twin install` 会打印一节 `路径映射`，说明这次重定向了哪些目录、这份
知识从哪来（内置规则 / `rules.d` / 这份配置）。不同程序的数据目录完全不同，而隔离错一个目录要等到
两个实例抢同一个锁文件才暴露。**改它该编辑哪个文件**（`rules.d` 管这个程序**以后新建**的实例，
手上这份配置管**这一个**）和"还有哪些目录值得映射"是 `--explain` 里的内容 —— 它们是"这条映射错了"
之后才需要的东西，而默认输出的前提恰恰是它没错。改完配置重装一次：`twin install`（改的若是实例目录
里那份快照，就 `twin reinstall <id>`，两者走同一条安装路径）。

**输出默认是精简的**：`twin gen` 只报文件路径与下一步，`twin install` 报产物清单 ＋ `路径映射` ＋
下一步，都是一屏能看完的。自检只在**有问题**时才开口（那几条 `✅` 确认的正是这一次刚写下的文件），
并且带上修它的命令；`twin verify` 才是把每一条都列出来的地方。其余细节都要 `--explain`（或
`--dry-run`）才展开。报错时只打印那一行错误 —— 整份帮助的出口是 `twin --help`，逐命令的选项表是
`twin gen -h`。颜色只在终端上出现 —— 重定向、管道、`TERM=dumb` 一律纯文本，`NO_COLOR` 强制关掉、
`CLICOLOR_FORCE=1` 强制打开。字形（`✅` `⚠️` `❌` `➖`）与颜色表达同一件事，所以日志里也读得出来
哪一行失败。

陌生程序不知道该映射什么？`twin gen --explain` 会把这台机器上**真的存在**的 `~/.config/<程序名>`
之类目录列出来（只列，不套用）；`twin rules --init <source>` 则把探测结果写成一份规则草稿，其中
"目录真的存在"的几条生效、"二进制里有这个字符串"的几条注释掉，等你逐条确认。

## 原理速览

```
twin gen wechat                  ← 前半段：只配置
      │
      ├─ 1. 解析源桌面项 → 找到真实程序 /opt/wechat/wechat、图标、语言化的 Name/Keywords
      ├─ 2. 推导默认值   → id=wechat2  name=微信2  badge=2  keywords=微信;wechat;
      ├─ 3. 选身份级别   → alias（bwrap overlay 改名）。程序若写死 app_id，则 redirect
      ├─ 4. 从内置规则 / rules.d 算出路径映射
      └─ 5. 写出 profile.yml（当前目录，唯一一个文件），并打印"下一步敲什么"

twin install                     ← 后半段：按配置安装（reinstall 也走这条路）
      │
      ├─ 6. 解析沙箱方案 → 本机要不要私有 bwrap 副本 / 要不要跳板。
      │                    两者都没有、而程序又要嵌套 ns 时：用 sudo 装一份 AppArmor profile
      ├─ 7. 落盘 5~7 件产物（幂等），包括按配置渲染的存档快照 profile.yml
      ├─ 8. 写宿主托盘覆盖表（把自己那行指到本实例图标；热重载，立刻生效）
      └─ 9. 自检身份链路（target/别名/桌面项/图标/启动器/规则/嵌套 ns）
```

`launch` 最终长这样（`level=alias`，无嵌套 ns 需求时）：

```bash
#!/bin/bash
set -euo pipefail
: "${HOME:?twin: HOME 未设置，拒绝启动}"
readonly ID='wechat2'
readonly STATE="$HOME/.local/state/twin/wechat2"

mkdir -p "$HOME/.local/state/twin/wechat2" \
         "$HOME/.local/state/twin/wechat2/xwechat" \
         "$HOME/.local/state/twin/wechat2/all_users" \
         "$HOME/.xwechat" \
         "$HOME/Documents/xwechat_files/all_users"
mkdir -p "$HOME/Documents/xwechat_files"

exec '/usr/bin/bwrap' \
  '--ro-bind' '/' '/' \
  '--dev' '/dev' \
  '--dev-bind-try' '/dev/dri' '/dev/dri' \
  '--proc' '/proc' \
  '--bind' '/run/user' '/run/user' \
  '--tmpfs' '/tmp' \
  '--overlay-src' '/opt/wechat' \
  '--tmp-overlay' '/opt/wechat' \
  '--ro-bind' '/opt/wechat/wechat' '/opt/wechat/wechat2' \
  '--bind' "$HOME/.local/state/twin/wechat2/xwechat" "$HOME/.xwechat" \
  '--bind' "$HOME/Documents/xwechat_files" "$HOME/Documents/xwechat_files" \
  '--bind' "$HOME/.local/state/twin/wechat2/all_users" "$HOME/Documents/xwechat_files/all_users" \
  '--setenv' 'TWIN_INSTANCE' 'wechat2' \
  '--die-with-parent' \
  -- \
  '/opt/wechat/wechat2' "$@"
```

（上面是删掉重复项、并把 `mkdir -p` 合成两行的等价简化版；生成器逐目录输出一行。）

几处关键点，每一条都对应一次实测：

1. `--overlay-src` 必须排在 `--ro-bind … 别名` **之前**，否则挂载点建不出来；
2. `--bind /run/user /run/user` 必须排在 `--ro-bind / /` **之后**，否则 Wayland socket、
   会话总线、PipeWire 全断；
3. 程序名写成 `/opt/wechat/wechat2`（别名，只在命名空间里存在）⇒ `/proc/self/exe` 的
   basename 就是 `wechat2`，`argv[0]` 默认也是它 ⇒ **窗口与托盘的 id 天然一致**；
4. 每个 bind 的**两端**都先 `mkdir -p`：bwrap 要从源端 bind，而目标端的父目录在
   `--ro-bind / /` 之后是只读的 —— 不先建出来，挂载点就创建不了。

## Electron / Chromium 的特殊情况

Chromium 系的程序（含所有 Electron 应用）启动时会**自己再建一个 user namespace** 来做
沙箱。这在 Ubuntu 上会撞上两个叠在一起的限制：

- 内核 `kernel.apparmor_restrict_unprivileged_userns=1`（Ubuntu 24.04+ 默认开，由
  `/usr/lib/sysctl.d/10-apparmor.conf` 钉住、每次开机回写）；
- `/usr/bin/bwrap` 的 AppArmor profile 会给它的**每个后代**叠上 `unpriv_bwrap`，而那份
  profile 带 `audit deny capability` ⇒ 后代 capability 全丢 ⇒ 嵌套 `unshare()` 恒 `EPERM`。

`twin` 有两条出路，都自动选，优先第二条：

1. **私有 bwrap 副本 ＋ 跳板**：复制一份 bwrap 到没有 profile 的路径上（逃开限制二），运行时先
   `exec` 一个自带 `flags=(unconfined) { userns, }` profile 的 Electron 程序（通常是 VS Code），
   让内核在 exec 时**替换**掉 AppArmor 标签，再由它 spawn 副本 —— 副本及其后代就都带着那个许可了。
   **机器上装了任何 Electron 应用就能用**，换一台机器重跑一次 `twin` 就重新发现一次，不绑死任何
   特定程序。
2. **一份挂在副本路径上的 AppArmor profile**：不依赖机器上恰好装了什么。`twin install` 自己装它
   —— 写 `/etc/apparmor.d/twin-bwrap`、`apparmor_parser -r`，两条命令都经 `sudo`，所以会**要求你
   输入密码**；不想给就按 Ctrl-C。没有 `sudo`、或标准输入不是终端（脚本里跑）时，它退回打印
   profile 全文并以**退出码 3** 结束（身份是成立的，只差这一条 profile）。

装好之后 `install` 会**回读**：`apparmor_parser` 返回 0 只说明内核收下了，还要在
`/sys/kernel/security/apparmor/profiles` 里真的看到 `twin-bwrap` 才算数 —— 看不到就说看不到，
不把查不到当成查过了。

完整机制、三条退化路线与取证记录：[`docs/apparmor-userns.md`](docs/apparmor-userns.md)。

## 命令一览

| 命令 | 作用 |
|---|---|
| `twin gen <source>` | **最常用**：在当前目录写出一份配置（`profile.yml`）；id / 名字 / 角标 / 状态路径全部推导 |
| `twin install` | **最常用**：读那份配置，落盘（桌面项 / 图标 / 启动器 / 托盘）＋ 自检。需要 AppArmor profile 时用 `sudo` 装 |
| `twin inspect <source>` | 只读：源程序是什么、身份该怎么做、/tmp 策略 |
| `twin verify <id>` | 自检一个实例的身份链路 |
| `twin reinstall <id>` | 按实例目录里那份存档快照重新落盘 |
| `twin remove <id>` | 删除产物：先列出要删什么、问过你才动手（脚本里用 `--yes`） |
| `twin list` | 列出全部实例 |
| `twin doctor` | 环境自检（先跑这个），输出末尾直接告诉你下一步敲什么 |
| `twin rules [<app>]` | 列出内置规则 / 查看某程序生效的规则 |
| `twin rules --init <source>` | 探测这个程序的状态目录，写出一份 `rules.d` 草稿（不覆盖） |
| `twin config` | 打印 `config.toml` 模板（文件不存在时） |
| `twin version` | 版本 |

> 第一个参数永远是动词，第二个永远是它的宾语 —— 位置即含义，没有保留字表。
> 所以程序真名就叫 `list` 也不特殊：`twin gen list` 就是包它。

每个命令的**选项与默认值**：`twin <命令> -h`（它和实现同一次编译，所以这是唯一一份）。
**退出码、文件表、拒绝清单**：[`docs/cli.md`](docs/cli.md)；逐键语义：
[`docs/configuration.md`](docs/configuration.md)。

## 配置一览

三层，越具体越优先：

```
./profile.yml（或 -p 指的那份）              你正在编辑的那一份
  > ~/.config/twin/rules.d/<app>.toml      应用知识（支持 {id} 占位）
    > ~/.config/twin/config.toml           这台机器的环境
      > 编译进二进制里的内置默认
```

`profile.yml` 是**这一个实例**的全部意图（`gen` 写它、`install` 读它）；`install` 会按它渲染出
`~/.local/share/twin/<id>/profile.yml` —— 同一个实例的**存档快照**，`reinstall` 重放的是它。
`config.toml` 不存在是正常的（用内置默认）。数组类字段（`tmpfs` / `bind` / `bwrap.extra`）
按**低位在前**拼接，同目标挂载由后写的那个胜出（bubblewrap 自己的规则）。

```bash
twin config > ~/.config/twin/config.toml   # 生成一份带注释的模板
```

谁写哪个文件、逐键说明与完整示例：[`docs/configuration.md`](docs/configuration.md)（文件表的查阅版在
[`docs/cli.md`](docs/cli.md)）。

## 退出码

**`3` 是给脚本用的信号**：身份已经成立，只差一步必须由真实图形会话或 root 执行（托盘覆盖表写不进去；
或内核限制 userns 而机器上既没有跳板、也装不了那份 profile）—— **"去图形会话里跑一下"和"这东西坏了"
是两件事**。其余几个码的含义，以及"事实性跳过（这台机器根本没有托盘宿主）算成功"这条边界，见
[`docs/cli.md`](docs/cli.md) 的退出码一节。

## 依赖与安装

要求：

- Linux ＋ bubblewrap（`sudo apt install bubblewrap`）
- Go **1.27+**（构建时）；运行时零第三方依赖，二进制自包含
- 可选：`python3-gi`（启用一道最能抓"静默失败"的自检）、`rsvg-convert` 或
  ImageMagick（SVG 源图标的光栅化；PNG/JPEG 走内置解码器）
- 桌面：GNOME ＋ `ubuntu-appindicators` 托盘宿主（托盘图标覆盖表是它提供的）

```bash
git clone <this repo> && cd twin
go build -o ~/.local/bin/twin ./cmd/twin     # 或 task build（见 Taskfile.yml）

# 或者装到系统目录
sudo install -m755 twin /usr/local/bin/twin

# 版本号可以在构建时注入
go build -ldflags "-X main.version=$(git describe --tags --always)" -o twin ./cmd/twin
```

## 常见问题

**Q：我只有一个程序，为什么生成的 id 是 `wechat2` 而不是 `wechat`？**
因为 `wechat` 这个桌面项 id 已经被系统的那份占了。同名会撞车，而 GNOME 对重复 id 的处理是
**静默遮蔽其中一个**。`freeID` 从数字 2 开始找第一个没被占用的名字。

**Q：点图标没反应 / 名字还是旧的？**
生成完之后必须**完全退出**这个程序再启动（见上文"为什么必须重启实例"）。若窗口出现但任务栏
图标还是齿轮，说明 `Exec=` 指向的启动器**没有执行位** —— GLib 会因此丢弃**整个**桌面项，
而 `desktop-file-validate` 查不出这一点。`twin verify <id>` 会直接报出来。

**Q：托盘图标没变？**
托盘覆盖表按 **SNI Id 严格相等**匹配。只有 `level=alias|redirect`（真的改名了）才能预测
这个 id；`level=none` 时 id 由程序自己决定，那一行只是猜测（工具会提醒你）。

**Q：`gsettings set` 明明成功了却什么都没变？**
无会话总线时 `gsettings set` 会**假成功**（`exit 0`，只在 stderr 吐
`dconf-WARNING: failed to commit`）。所以 `twin` 写完**必须回读**，回读才是唯一判据。
`twin doctor` 里有一项专门探会话总线可达性。

**Q：Electron / Chromium 包裹后起不来？**
先看 `twin doctor` 里"非特权 userns"那一行，再读
[`docs/apparmor-userns.md`](docs/apparmor-userns.md)。

更多症状 → 原因 → 修法见 [`docs/troubleshooting.md`](docs/troubleshooting.md)。

## 文档索引

| 文档 | 内容 |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | 身份模型、三级阶梯、管线、产物模板、权限与边界、GUI 之辨 |
| [`docs/cli.md`](docs/cli.md) | 全部子命令**做什么**、退出码、文件表、拒绝清单；选项本身在 `twin <命令> -h` 里 |
| [`docs/configuration.md`](docs/configuration.md) | 三层配置逐键参考、覆盖规则、快照 vs 环境 |
| [`docs/apparmor-userns.md`](docs/apparmor-userns.md) | 嵌套 user namespace：三个实测事实、三条路线、跳板机制 |
| [`docs/troubleshooting.md`](docs/troubleshooting.md) | 症状 → 根因 → 修法，含全部实测踩坑 |
| [`docs/development.md`](docs/development.md) | 包结构、构建与测试、开发环境的坑 |
| [`AGENTS.md`](AGENTS.md) | 给 AI 协作者的项目约定：质量门、包结构、硬性规则、常见任务 |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | 贡献指南：环境准备、质量门、scratch HOME 验收、提交规范 |
| [`CHANGELOG.md`](CHANGELOG.md) | 变更日志 |

## 开发

质量门是 `gofmt -l .`（必须为空）＋ `go vet ./...` ＋ `go test ./...`；环境坑（`/tmp` 是小容量
tmpfs 时要设 `GOTMPDIR`、依赖真实机器状态的诊断性测试默认跳过）与构建细节一起写在
[`docs/development.md`](docs/development.md) —— 那里还有代码组织、各包的职责边界，以及开发过程中撞
出来的坑（Go `flag` 的解析行为、多合一二进制、`/usr/share/code/bin/code` 是 sh 脚本……）。贡献者的
环境准备、手工验收与提交规范：[`CONTRIBUTING.md`](CONTRIBUTING.md)。构建与质量门有
[`Taskfile.yml`](Taskfile.yml) 收口：`task build` / `task check` —— GitHub Actions 跑的也是同一条。

## 许可

MIT，见 [`LICENSE`](LICENSE)。
