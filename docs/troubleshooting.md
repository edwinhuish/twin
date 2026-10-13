# 排障

排障的固定顺序，**不要跳步**：

```bash
twin doctor            # 这台机器能不能干活？输出末尾就是下一步
twin verify <id>       # 这个实例的每一环对不对？
bash -x ~/.local/share/twin/<id>/launch   # 真跑一次，看它到底怎么起
```

绝大多数"怪问题"在这三步里就能定位。下面是按症状排的。

---

## A. 生成成功了，但桌面上看不到 / 点不开

### A1. 窗口出现，但任务栏图标是**齿轮**（`application-x-executable`）

**根因**：`Exec=` 指向的启动器**没有执行位**。GLib 在这一点上不是降级，而是
**静默丢弃整个桌面项**。

**为什么难查**：`desktop-file-validate` **查不出来**。文件语法完全合法。

```bash
ls -l ~/.local/share/twin/<id>/launch      # 必须是 -rwxr-xr-x
twin verify <id>                           # 会直接报"启动器可执行"
chmod +x ~/.local/share/twin/<id>/launch   # 临时修；根治用 twin reinstall <id> --refresh
```

> 这是本项目最贵的一个坑，所以 `doctor` 里专门用一对"好/坏"探针桌面项来**验证检查器本身
> 是否可信**：若坏的也被 GLib 接受，说明判据与实测不符，会明确警告。

### A2. 桌面项改了，但名字/图标还是旧的

**根因**：GNOME **不会给一个已经存在的窗口重算归属**。桌面项是后写的。

**修法**：**完全退出**这个程序（不是关窗口 —— 很多程序会最小化到托盘），再从这个新桌面项
启动一次。

### A3. 生成完桌面项就"不见了"

检查基名是否**正好等于 `id`** —— GNOME 按桌面项基名匹配。`twin verify` 有一项
`桌面项基名 == id`。

```bash
# 刷新桌面数据库（有时是缓存问题）
update-desktop-database ~/.local/share/applications
```

---

## B. 托盘图标不对

### B1. 托盘还是原程序的图标

**根因**：托盘覆盖表（`custom-icons`）按 **SNI `Id` 严格相等**匹配。

| 情况 | 结论 |
|---|---|
| `level=alias` / `redirect` | `Id` 可预测（= 别名）⇒ 应该能命中。检查覆盖表里那一行是否真的写进去了 |
| `level=none` | **没有改名** ⇒ `Id` 由程序自己决定，那一行是**猜测**。工具会明确提醒"未必匹配得上" |

```bash
gsettings get org.gnome.shell.extensions.appindicator custom-icons
twin verify <id>
```

### B2. `gsettings set` 退出码 0，但什么都没变

**根因**：**无会话总线时 `gsettings set` 会"假成功"** —— 它退 0，只在 stderr 留一行
`dconf-WARNING: failed to commit changes to dconf`。

**修法**：在**真实图形会话的终端**里执行。`twin doctor` 有一项专门探会话总线可达性。

⚠️ 所以 `twin` 写完**必须回读** —— 回读才是唯一判据。

### B3. 读覆盖表看到 `@a(sss) []`

正常，别误判。`gsettings reset` 的语义 = **删除该条目**，`get` 回到空列表就是这个结果。

### B4. "本机没有托盘宿主"

`twin` 报的是 **OK 并跳过，退出码 0**。这不是待办：别的发行版、没装
`ubuntu-appindicators`、没有 `gsettings`，都属于"这台机器就是这样"。工具绝不打印一条这台
机器永远跑不了的修复命令。

---

## C. 程序起不来

### C1. Electron / Chromium 类程序 FATAL 退出

唯一一类"**产物全对、`verify` 全绿、就是起不来**"的故障。

```bash
twin doctor | grep -A2 userns
```

**修法**：见 [`apparmor-userns.md`](apparmor-userns.md)。速查：

| `doctor` 那一行 | 含义 | 做法 |
|---|---|---|
| `内核未限制…可以直接包裹` | 没这个问题 | — |
| `内核在限制，但发现 N 个 Electron 跳板可用` | 正常，自动走路线 B | 不用管 |
| `内核在限制，N 个候选里没有一个像 Electron` | 有带 `userns` 许可的程序，但没有能静默驱动的 | 装个 Electron 应用，或按打印的 profile 装一份 |
| `内核在限制，且没有找到任何…` | 一个都没有 | 同上 |

另外：**跳板被卸载/改名**会让本来正常的实例突然起不来。`twin reinstall <id> --refresh` 会重挑一个。

### C2. X11 程序报 `cannot open display` / `No protocol specified`

**根因**：`--tmpfs /tmp` 把 `/tmp/.X11-unix/X0` 一起砍掉了。

```bash
twin gen <source> --no-tmpfs && twin install
```

`twin inspect` 的 `/tmp 策略` 一节会告诉你这个程序是 Wayland 还是 X11。同时支持两套的
程序会有一条警告：它今天走 Wayland，但哪天真退到 X11，`--tmpfs /tmp` 就会踩这个坑。

### C3. 程序起不来，报 `Can't create file: Read-only file system`

**根因**：一条路径映射的**目标**（程序看到那一侧）的**父目录**在沙箱里是只读的（因为基座是
`--ro-bind / /`）。

`twin` 会自动 `mkdir -p` 两端，所以正常情况不会遇到。若你在**透传区**手工写了 bind，
那就踩了透传区"不享受 mkdir 补齐"这条：

```bash
# ❌ 透传区不会替你建目录
twin myapp -- --bind /some/new/dir /some/new/dir

# ✅ 写进 bind（享受 mkdir ＋ ~/ 展开）
#    ~/.config/twin/rules.d/myapp.toml
#    bind = [ "~/.local/state/twin/myapp2/data:/some/new/dir" ]
twin rules myapp
```

### C4. 网络断了，或程序报"已在运行"

**根因**：`isolation.unshare` 里放了 `net`。

```bash
twin verify <id>    # 会说明这一条被拒绝
```

改 `profile.yml` 去掉它，或接受"这个程序无法被这样隔离"。`net` 切断网络，也会打破基于
abstract socket 的单实例锁，而 bwrap 本来也隔离不了网络。

### C5. `错误：HOME is not set`

`twin` 拒绝在没有 HOME 时猜一个家目录。手写包裹曾用 `HOME=${HOME:-/root}` —— 在无 HOME
的上下文里会往 `/root` 建目录，那是**数据丢失**级别的默认值。

以真实用户身份运行（图形会话里的终端）。

---

## D. `wrap` 被拒绝

`twin` 的立场是"宁可不做，不做近似"。下面每一条拒绝都对应一次实测到的静默失败。

| 报错 | 含义 | 修法 |
|---|---|---|
| `id 已存在` | 显式 `--as` 撞到了已存在的东西 | 换个 `--as`，或确认后加 `--force` |
| `多合一二进制` | BusyBox / coreutils 之类靠 `argv[0]` 分派行为的程序，改名后会报 `unknown program` | 这类程序**不能**用 `alias`；用 `--level none` |
| `找不到可用的源图标` | 源桌面项没有 `Icon=`，或图标文件不存在 | `--icon <png\|svg>` |
| `不是 Electron 程序（没有 resources/app*）` | 你显式指定的跳板不是 Electron，驱动不了 Node 模式 | 换一个，或删掉 `userns.trampoline` 用 `auto` |
| `--argv0 会破坏身份不变量` | 窗口 `app_id` 跟真实路径、托盘 `Id` 跟 `argv[0]`；分叉必然一方找不到桌面项 | 别用 |
| 报错原文「`--` 之后应以 bwrap 选项开头」 | 你想写 POSIX 的"选项到此为止"，而 twin 的 `--` 意思是"以下是 bwrap 参数" | 去掉 `--`，或学习 `--` 的真实含义 |
| `透传区不能再出现 --` | bwrap 会把它读成"选项结束"，后面全被当成程序名 | 要传字面的 `--` 用 `--flag=--` 形式 |
| `--level argument/env 还没实现` | 这两个级别没过实测 | 用 `none` / `alias` / `redirect` |
| `config.toml … 解析失败` | 文件存在但语法坏了 | **这是刻意的**：静默回退会让你的修改"什么都不发生"且毫无线索 |

---

## E. 中文 / 编码

| 症状 | 原因 | 修法 |
|---|---|---|
| 名字或关键词里有乱码 | 终端 locale 与桌面项编码不一致（桌面项本身是 UTF-8） | `locale` 检查；`twin inspect` 直接读源桌面项，对比是否一致 |
| 角标里的中文变成"豆腐块" | 内置点阵字体只覆盖 ASCII | 用数字/几何风格（`corner`＋数字，或 `dot`） |
| 搜索找不到这个实例 | 关键词没推导出来（源桌面项既无 `Keywords`，也无可用 `Name`/程序名） | `--keywords 'k1;k2'`；`wrap` 遇到这种情况会警告 |

---

## F. 开发 / 构建相关的坑

见 [`development.md`](development.md)。最常见的两个：

| 症状 | 原因 |
|---|---|
| `go test` 报 `no space left on device` | `/tmp` 是小容量 tmpfs，Go 链接测试二进制时爆掉 ⇒ 设 `GOTMPDIR=/ TMPDIR=` |
| `command -v go` 找不到 go | 装在非标准前缀（如 `~/.g/go/bin/go`） |
