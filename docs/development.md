# 开发

---

## 1. 构建与质量门

```bash
gofmt -l .          # 必须输出为空
go vet ./...
go test ./...

# 一次跑完
gofmt -l . && go vet ./... && go test ./...
```

这套命令由 [`Taskfile.yml`](../Taskfile.yml) 收口，**本地与 CI 用的是同一条**（两处各写一份的话，
迟早一处绿一处红）：

```bash
task --list         # 全部任务
task check          # = 上面那一条：gofmt ＋ vet ＋ test，按顺序遇错即停
task build          # 构建 ./twin，版本号取 git describe 注入 main.version
task fmt            # gofmt -w .
task clean          # 删掉 ./twin
task test:real      # 依赖真实机器状态的诊断测试（默认不跑）
```

`task test` 自己把 `GOTMPDIR` / `TMPDIR` 指向 `$HOME/.cache/gotmp`（就是下面那个坑），所以本机不必再
手写那串环境变量。Go 不在 PATH 上时：`task build GO=$HOME/.g/go/bin/go`。

`.github/workflows/ci.yml` 只做三件事：装 Go（版本**读 `go.mod`**，不另写一个）、装 task、跑
`task check` 与 `task build`（后者顺带冒烟一次 `./twin version`）。质量门本身定义在 `Taskfile.yml` 里，
CI 不再抄第二份。

⚠️ **`/tmp` 是小容量 tmpfs 的机器上，`go test` 会报 `no space left on device`** ——
Go 链接每个测试二进制都要在临时目录里落一个几 MB 的文件。改到别的目录：

```bash
GOTMPDIR=$HOME/.cache/gotmp TMPDIR=$HOME/.cache/gotmp go test ./...
```

⚠️ Go 不一定在 PATH 上（例如装在 `~/.g/go/bin/go`）。用绝对路径，或自己加到 PATH。

### 版本注入

```bash
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" \
  -o twin ./cmd/twin
```

`main.version` 的默认值是 `0.2.0-dev`。

### 诊断性测试

依赖**真实机器状态**的测试默认跳过，需要显式开启：

```bash
TWIN_REAL_PROBE=1 go test ./internal/userns/ -run RealProbe -v
```

它真的去扫 `/etc/apparmor.d` 并打印发现结果。**不要**把这些检查放进默认测试集 ——
它们的结果随机器而变，会让 CI 变得不可复现。

---

## 2. 包结构

```
cmd/twin/            CLI 入口：动词分派、flag 解析、全部终端输出
internal/
  tomlz/                TOML 子集的编解码（自写，见 §3；config.toml 与 rules.d 用它）
  yamlz/                YAML 子集的编解码（自写，见 §3；profile.yml 用它）
  inst/                 实例 schema（真源）＋ Paths（一切产物路径）＋ 语义校验
  desk/                 .desktop 读写（含 Name[xx] / Comment[xx] / Keywords[xx]）
  config/               环境层（config.toml）＋ 应用知识层（rules.d）
  generate/             inspect / plan / materialize / unwrap / refresh
    traits.go           从二进制字符串扫出工具箱特征（Wayland/Qt/多合一/嵌套 ns）
    runtime.go          bwrap 副本同步 ＋ 跳板脚本
  sandbox/              bwrap 调用构建、透传 lint、Runtime.Resolve（降级链）
  icon/                 纯 Go 图标合成（解码、缩放、角标蒙版、5×7 点阵字体）
  userns/               userns 限制判定、AppArmor profile 解析、跳板发现
  tray/                 托盘覆盖表的 read-modify-write
  verify/               回读自证
  doctor/               环境自检
  style/                终端着色判定（TTY / NO_COLOR）与状态字形
  sysx/                 系统调用封装（Run / Which / FirstInstalled / EnsureDir / WriteFileAtomic …）
```

### 依赖方向

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

规则：**下层不知道上层存在**。`inst` 不知道 bwrap，`sandbox` 不知道桌面项。
唯一"反向"的知识是 `inst.CheckBwrapToken` —— 它校验 bwrap token，因为那是**实例 schema
的合法性**（写进 `profile.yml` 的东西必须合法），而不是 bwrap 的实现细节。

---

## 3. 为什么零第三方依赖

`go.mod` 只有一行 `module` ＋ `go 1.27.1`。没有 `require`，没有 `go.sum`。

代价与收益都实在：

| 收益 | 代价 |
|---|---|
| `go build` 永远不需要网络 | 要自己写一个 TOML 子集（`internal/tomlz`）与一个 YAML 子集（`internal/yamlz`） |
| 没有供应链风险 —— 这个工具会跑在用户的**桌面会话**里 | 图标合成要自己写缩放与蒙版 |
| 二进制自包含，拷到别的机器就能用 | 不能用现成的 TOML/YAML/图像库 |
| 不会因为上游 breaking change 半夜坏掉 | — |

`tomlz` 只实现了实际用到的子集：表、内联表、字符串数组、数组表（`[[x]]`）、bool、int、
字符串、`#` 注释。**它不需要成为一个通用 TOML 库。**

`yamlz` 同样只管 `profile.yml` 会写出的那些形状：缩进映射、块序列、`[]` / `{}`、单行 flow
序列、引号与纯量、bool / int。**它拒绝** Tab 缩进、文档标记、锚点别名、块标量、多行 flow、
重复键，以及 YAML 里含义有歧义的布尔拼法（`yes` / `no` / `on` / `off`）—— 歧义处一律硬报错，
因为一个被悄悄读错的身份文件比一个被拒绝的更糟。它也不需要一个通用 YAML 库。

图标同理：PNG/JPEG 用标准库解码，缩放与角标是自绘位图蒙版（`image.RGBA` ＋ 覆盖率表）。
只有 SVG 这类"长尾格式"交给外部工具（`python3-gi` → `rsvg-convert` → ImageMagick，
按序尝试）。

---

## 4. 代码约定

### 注释写"为什么"，不写"是什么"

这个项目的注释密度很高，但**没有一句在描述代码在做什么**。它们记录的是：

```go
// parseFL parses a FlagSet whose positional arguments may appear anywhere among
// the flags.
//
// Go's flag package stops at the first non-flag token, so the documented form
// `twin gen <source> --as <id>` would otherwise ignore --as — and every
// flag after the source — without a word. That is precisely the silent failure
// this tool exists to prevent, so we take the stray token as a positional and
// re-parse the remainder.
```

这段注释的价值不在"它做了什么"，而在**"不这么做会发生什么"**。这个项目里几乎每个非平凡的
决定都能追溯到一次实测到的故障。

### 静默失败是这个项目的头号敌人

工具的存在理由是"防止静默失败"，所以它自己也必须不制造静默失败：

- `config.toml` / `profile.yml` 语法坏了 ⇒ **硬报错**，不静默回退到内置默认、也不猜测歧义写法。
- 找不到源图标 ⇒ **拒绝生成**，不静默出一个齿轮占位图。
- 托盘写不进去 ⇒ **打印具体命令 ＋ 退出码 3**，不假装成功。
- 没会话总线 ⇒ 报事实（跳过），不报一条跑不了的修复命令。

写新代码时问一句：**失败的时候，使用者能看见吗？**

### 回读是判据，不是礼貌

任何"我写了"的地方都必须**读回来确认**。`gsettings set` 会假成功（`exit 0` 但什么都没写），
所以 `tray.Ensure` 是 读 → 合并 → 写 → **回读**。同理 `verify` 不信内存里的期望，只看硬盘。

**判据只能用"回读"，不能用"事先猜"。** 例如"这个上下文能不能写 dconf"取决于会话属性，
不取决于代码跑在哪里 —— 所以代码里不能有 `if 在沙箱里 { 跳过 }` 这种判断。

---

## 5. 定位故障的实用技巧

### 别用 `rg` / `grep` 搜二进制

会读到 NUL 就停，结果毫无意义。先转成文本：

```bash
strings -a -n 6 /path/to/binary > /tmp/strings.txt
# 然后用 LC_ALL=C ＋ 显式字符类；前导一律用 {0,N}，{3,} 这类会假阴性
LC_ALL=C grep -n 'chrom[^ ]*sandbox' /tmp/strings.txt
```

### 判断 AppArmor 标签

```bash
cat /proc/self/attr/current                      # 当前进程的标签
sudo aa-status                                   # 已加载的 profile
ls /etc/apparmor.d/                              # profile 文件
```

### 判断 dconf 写是否真的生效

```bash
gsettings set org.gnome.shell.extensions.appindicator custom-icons "[('x','/a.png','/a.png')]"
echo "exit=$?"                                                    # 0 不代表成功
gsettings get org.gnome.shell.extensions.appindicator custom-icons # 这才是判据
```

`dconf` 的**读**走 mmap 直读 `~/.config/dconf/user`（不经总线，永远可用）；
**写**走会话总线（无总线时会假成功）。

### 抓窗口的 app_id（零特权）

```bash
WAYLAND_DEBUG=1 bash ~/.local/share/twin/<id>/launch 2>&1 | grep set_app_id
```

---

## 6. 扩展点

### 加一条内置规则

`internal/config/config.go` 的 `BuiltinRules()`。它是**同一种形状**的用户文件，所以
`twin rules <app>` 能直接打印出来。

⚠️ 内置表刻意很短。**静默猜错一个路径，比开口要用户自己写一条 `bind` 更糟** —— 加之前先确认这条
路径是真的实测过。

### 加一个 `wrap` 选项

三处：

1. `cmd/twin/main.go` 的 `cmdNew`：加 flag，放进 `generate.Options`；
2. `internal/generate/generate.go` 的 `Options` 与 `Plan`：决定它的优先级与默认值来源；
3. 若是会进快照的字段，还要动 `internal/inst` 的 `toTree` / `Load` 并写往返测试。

⚠️ 记住 `parseFL`：位置参数可以出现在选项之间。`fs.Parse` 直接用会让文档里的
`twin gen <source> --as <id>` 静默忽略 `--as`。

### 加/改一段终端输出

全部着色判定在 `internal/style`，**不要在别处写转义序列，也不要自己判断 `isatty`**：

1. 判定按**流**做：`style.For(f)` 只看 `f` 一个文件，`cmd/twin` 为此各留一个（stdout / stderr）；
2. 状态行用 `st.Mark(style.LevelOK|Warn|Fail|Skip)` —— 字形与颜色一起给，纯文本里也不丢信息；
3. 段落标题用 `cmd/twin` 的 `title()`（粗体），emoji 留在调用点，别塞进 helper 里。

⚠️ 三条容易踩的：

- **别跨流复用结论**：`twin gen wechat > log` 时 stdout 是文件、stderr 还是终端，两个流的答案不同，
  这也正是 `fail` / `warn` 用 `errStyle` 而不是 `outStyle` 的原因；
- **别按字符设备判断终端**：`/dev/null` 也是字符设备。`internal/style` 里走的是 `TCGETS`，
  换实现时别退回 `os.ModeCharDevice`；
- **别用按字节补齐的列装字形**：emoji 是双宽，`%-6s` 那一套会把后面的列顶歪，改用
  `"%s %-28s %s"` 这种"字形 ＋ 名字 ＋ 详情"的形状。

### 加一个自检项

`internal/verify/verify.go` 的 `Run()` 里 `add(name, status, detail, fix)`。
若这一项**只能由人在图形会话里完成**，把 `Finding.Manual` 置真 —— 它会被 `printManual`
收集，并驱动退出码 3。

⚠️ 别把"这台机器没有某能力"写成 FAIL。那属于 `ActionNoHost` 那类事实性跳过，应该是 OK。

### 加一个新 Trait

`internal/generate/traits.go` 的 `Traits` 结构体 ＋ 一个 needle。注意 trait 扫描是**按二进制
内字符串**做的（`strings` 语义），所以 needle 要选足够独特、不会误命中的字符串。

---

## 7. 测试策略

| 层次 | 在哪里 | 说明 |
|---|---|---|
| 单元测试 | 每个包的 `*_test.go` | 纯逻辑：解析、推导、合并、校验、渲染 |
| 诊断性真机测试 | `internal/userns/zz_real_test.go` | `TWIN_REAL_PROBE=1` 才跑；打印真实发现结果 |
| 端到端冒烟 | 手工 | `twin gen <source> --dry-run` ＋ `twin install --dry-run`，然后目视检查 |

**为什么没有自动化集成测试**：真正的端到端要写进用户的 `~/.local/share/applications`、
`~/.local/state`，还要连会话总线。一个会动真实 HOME 的测试套件比它防住的问题更危险。

替代做法是用**scratch HOME** 手工验收：

```bash
mkdir -p /tmp/scratch-home/.config/twin /tmp/scratch-work
cd /tmp/scratch-work                       # gen 把 config.yml 写在当前目录，所以先挑一个空的
HOME=/tmp/scratch-home twin gen wechat --dry-run
HOME=/tmp/scratch-home twin gen wechat
HOME=/tmp/scratch-home twin install
HOME=/tmp/scratch-home twin verify wechat2
```

（`twin` 的一切路径都从 `$HOME` 派生，所以这样可以完整验证落盘逻辑而不碰真实环境。）

### `--dry-run` 必须是忠实的

它由**和真实落盘完全相同的代码**渲染出来 —— 一个用另一套代码画的预览就是一句谎话。
`DesktopEntry` 与 `it.Render()` 被 dry-run 和 `Materialize` 共用，就是为了这一点。

---

## 8. 已知的边界

| 边界 | 说明 |
|---|---|
| 跳板是外部依赖 | 那个 Electron 应用被卸载/改名 ⇒ 实例起不来。`verify` 有对应 FAIL |
| 路线 B 未做真启动验证 | 三条事实由独立探针取证；`--dry-run` 只证明"选择"对 |
| 托盘表是全局单键 | 写入必须 read-modify-write；并发写入没有加锁（桌面会话里不会有并发 `wrap`） |
| 只在 Ubuntu / GNOME 上实测过 | 其他发行版/桌面：`doctor` 会如实报告哪些能力缺失 |
| 依赖 `python3-gi` 做最强的那道自检 | 没有它时 `doctor` 与 `verify` 会报 WARN 并说明损失 |
