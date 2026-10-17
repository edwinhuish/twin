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

### A4. 点了图标没反应

**根因**：启动器在 `exec` 之前就死了，而桌面项启动的进程**没有终端** —— 它的 stderr 归 journal
或 `/dev/null`，于是"启动失败"看起来和"什么都没发生"一模一样。

从图形会话启动（没有控制终端 ＋ 有 `DISPLAY`/`WAYLAND_DISPLAY`）时，启动器会**自己弹一个窗口**
说明哪一步没成，并把这段文字同时写到：

```bash
~/.local/state/twin/<id>/launch-error.log
```

窗口里的正文是**可以选中复制**的（弹窗工具按这个能力挑：`zenity --text-info` →
`kdialog --textbox` → `yad --text-info`，`xmessage` 和 `notify-send` 不在链里，它们的正文选不中）——
就是为了让这段字能直接贴进 bug 报告。三个工具一个都没有时不会弹窗：报告仍在上面那个文件里，
journal 里留了一行它的路径。

在终端里直接跑启动器时**不弹窗**：同一份错误已经在眼前，再糊一个窗口上去只会挡住它。那时看
stderr 就行：

```bash
bash -x ~/.local/share/twin/<id>/launch     # 真跑一次，看它到底怎么起
```

⚠️ 这一层只管"**程序根本没启起来**"：启动器自己那几步（挂载点、状态目录）和 `exec` 本身失败了
才会弹。程序**跑起来之后**退出码非零不算启动失败 —— 那是程序自己的事，`bootstrap.js` 特意逐字
转发它的退出码，把"退出应用"报成"启动失败"才是错的。

### A5. 从**另一个**实例的沙箱里启动它：报告"交回宿主失败"

**根因**：这一趟启动发生在另一个实例的命名空间里（被包裹的程序 exec 了另一个实例的启动器，或者
有人在那份沙箱的 shell 里敲了它）。在那里起第二层 bwrap 会是**错的那一层**：新沙箱的根就是父沙箱
的根，父实例的 overlay、私有 `/tmp`、私有数据目录全在里面，而它自己的别名不在 ⇒ 程序从真实路径
启动，窗口 `app_id` 退回原值。所以启动器不嵌套，而是把这一趟交给宿主的 systemd 用户管理器重跑：

```bash
systemd-run --user --wait --pipe --setenv=TWIN_INSTANCE= -- <这一份启动器> …
```

进程进了挂载命名空间就没有 `setns` 回去的权限（要宿主 userns 的 `CAP_SYS_ADMIN`），所以这是唯一
的一条路，它要求会话总线在这个沙箱里够得着。

**为什么难查**：报告里那句"在实例 X 的沙箱里……"是**这一次启动**的位置说明，不是实例坏了 ——
`twin verify <id>` 会全绿。而 `systemd-run` 自己只会说一句英文的 `Failed to connect to user scope
bus`。

```bash
systemctl --user is-system-running          # 用户管理器在不在（degraded 也能用）
bash -x ~/.local/share/twin/<id>/launch     # 看它到底死在哪一步
```

**修法**：沙箱把 `/run/user` 遮掉了 —— 要么 `root: ro` 之后没补回 `--bind /run/user /run/user`，
要么自己往 `bwrap.extras` 里加了一条 `--tmpfs /run`。补回一条映射即可；补不了就从宿主启动这个
实例（它本来就不该由沙箱里的进程来起）。

---

## B. 托盘图标不对

### B1. 托盘还是原程序的图标

**根因**：两条路都按 **SNI `Id` 严格相等**匹配。

| 情况 | 结论 |
|---|---|
| `level=alias` / `redirect` | `Id` 可预测（= 别名）⇒ 应该能命中。查 `twin verify <id>` 的「托盘图标」那一行说的**这次走那条路**里的条目在不在 |
| `level=none` / `wrap` | **没有改名** ⇒ `Id` 由程序自己决定，那一行是**猜测**。工具会明确提醒"未必匹配得上" |

```bash
twin verify <id>                     # 「托盘图标」那一行说清走的是哪条路、条目在不在
cat ~/.local/share/twin/tray.d/<id>.json   # 走扩展那条路时，图标就是从这里读的
gsettings get org.gnome.shell.extensions.appindicator custom-icons   # 走兜底那条路时看这里
```

两条路**只走一条**（判据与理由见 [`cli.md`](cli.md) 的 `twin install` 一节）。所以看到哪边是空的，
先确认**这次该走哪边**：`twin install` 的输出里「托盘覆盖」那一行每次都写明了。

### B2. `gsettings set` 退出码 0，但什么都没变

**根因**：**无会话总线时 `gsettings set` 会"假成功"** —— 它退 0，只在 stderr 留一行
`dconf-WARNING: failed to commit changes to dconf`。

**修法**：在**真实图形会话的终端**里执行。`twin doctor` 有一项专门探会话总线可达性。

⚠️ 所以 `twin` 写完**必须回读** —— 回读才是唯一判据。

⚠️ 这条**只对兜底那条路成立**：托盘扩展被 shell 加载时，图标走的是 `~/.local/share/twin/tray.d/`
里的一份文件，不需要会话总线（那份文件由 shell 里的扩展读）。所以"没有会话总线"不再是托盘图标的
必然故障 —— 它只影响兜底路径。

### B3. 读覆盖表看到 `@a(sss) []`

正常，别误判。`gsettings reset` 的语义 = **删除该条目**，`get` 回到空列表就是这个结果。

### B4. "本机没有托盘宿主"

`twin` 报的是 **OK 并跳过，退出码 0**。这不是待办：别的发行版、没装
`ubuntu-appindicators`、没有 `gsettings`，都属于"这台机器就是这样"。工具绝不打印一条这台
机器永远跑不了的修复命令。

---

### B5. 自定义图标比程序自己的图标小一圈

**根因**：这个实例走的是**兜底那条路**（宿主覆盖表）。那条路只决定**画哪张图**，尺寸交给 `St.Icon`
按 actor 的**分配框**算，而那个框在 `IconActor._init()` 里按 `PANEL_ICON_SIZE || 16` 写死一次
（`PANEL_ICON_SIZE` 在 gnome-shell 49 已删）⇒ 自定义图标恒定 **16px**；程序自己发的位图那条路显式
给了宽高，所以跟着设置里的 `icon-size` 变。**这是宿主的 bug，不在你的配置里**，也不在 twin 画的那张
PNG 里（它是满铺的）—— 机制见 [`architecture.md`](architecture.md) §7.1。

要换的是**路**，不是配置：装上托盘扩展，让它自己画图标（顺带把框同步对）。

```bash
twin verify <id>                     # 看「托盘扩展」那一行，以及「托盘图标」走的是哪条路
twin install --ext                   # 装了就走另一条路（机器级，装一次；不跟着 twin install 走）
gnome-extensions info twin-tray@edwinhuish.github.io
```

| 「托盘扩展」那一行 | 含义 | 做法 |
|---|---|---|
| `已启用 → 托盘图标由它画` | 正常 | — |
| `已装入但未启用` | shell 还没扫到它（它只在启动时扫扩展目录） | **注销重登一次**（Wayland 无 `Alt+F2 → r`），再 `gnome-extensions enable twin-tray@edwinhuish.github.io` |
| `在 shell 的禁用列表里` | 被关掉过（shell 不会加载它） | `twin install --ext`（它会重新请求启用一次；shell 还没扫到 uuid 时同样要先注销重登） |
| `未装入`（不在扩展目录里） | 还没装过 —— 它不跟着 `twin install` 走 | `twin install --ext` |
| `读不到…扩展列表` | 这台机器没有 `gsettings` / 无会话总线 | 事实性跳过，身份链路照常 |

⚠️ 装完扩展**不会让已有实例自动搬家**：它们在下一次 `twin install` / `twin reinstall` 时才改走扩展
那条路。所以"装了扩展、图标还是小一圈"是预期内的，跑一次 `twin reinstall <id>` 即可。

换过去之后尺寸就挂在那条 `notify::icon-size` 上了 ⇒ 改 `icon-size` 立刻生效，**不用重启程序**。

### B6. 扩展已启用，托盘却还是原程序的图标

**根因**：让扩展把图标画上去要三件事同时对，按这个顺序查 —— 每一件都会留下可读的痕迹。

1. **表里有这个实例那一行吗？** 扩展读的是 `~/.local/share/twin/tray.d/<id>.json`，它不是扩展自己
   生成的。`twin verify <id>` 的「托盘图标」那一行会直接说"不存在""指向别处"。
   修法：`twin install <id>`（或 `reinstall`）—— 那次安装会重新选路并写这一行。
2. **扩展挂上宿主的渲染入口了吗？** 宿主换实现时扩展会**降级**：图标回落成程序自带的那张，并在日志里
   留一行警告（这就是它不说谎的方式）：
   ```bash
   journalctl -f -o cat /usr/bin/gnome-shell | grep twin-tray
   ```
   日志里没有那行警告，说明补丁挂上了 —— 那就回到第 1 条查表。
3. **匹配对得上吗？** `Id` 是严格相等，`level=none` / `wrap` 时那一行只是猜测（见 B1）。


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
| `内核在限制…：twin install 会用 sudo 装一份…` | 正常：`install` 会自己给副本装那份 profile（一次密码，之后不再问） | 不用管；不想给密码它会退回跳板 |
| `内核在限制…本机没有 sudo：走 Electron 跳板…` | 正常，但走的是"借别人的标签"那条路 | 不用管；想换成 profile，就在有 `sudo` 的终端里 `twin install <id>` |
| `内核在限制…候选里没有一个像 Electron` / `…也没有自带 userns 许可的已安装程序` | 既没有 `sudo`、也没有能静默驱动的跳板 | 装个 Electron 应用当跳板；或按它打印的 profile 手工装一份 |

另外：**跳板被卸载/改名**会让走那条路的实例突然起不来（走 profile 那条路的不受影响）。
`twin reinstall <id> --refresh` 会重挑一个。

### C2. X11 程序报 `cannot open display` / `No protocol specified`

**根因**：定义里给 `/tmp` 盖了空 tmpfs（`bwrap.extras` 里的一行 `--tmpfs /tmp`）——
`/tmp/.X11-unix/X0` 一起被砍掉了。

```bash
$EDITOR ~/.local/share/twin/<id>/profile.yml
#   在 bwrap.extras 里、那条 --tmpfs /tmp 的下面加一行 --bind /tmp /tmp（宿主那个，原地可写）——
#   同一份列表里写在后面的压得住它，所以这一行就够了，不必再去删上面那条
#   （那是这份定义里的文本，不是 twin 的常量）
twin reinstall <id>
# 或者重新推导一次：twin gen <source> --no-tmpfs && twin install
```

换成宿主那个 `/tmp` 之后它不是"什么都没有"，而是宿主那个 `/tmp`（**可写**，X11 socket
也在里面）—— 所以这一步不会顺带带来另一个问题（见 C5）。

`twin gen --explain` 的 `/tmp 策略` 一节会告诉你这个程序是 Wayland 还是 X11。同时支持两套的
程序会有一条警告：它今天走 Wayland，但哪天真退到 X11，私有 `/tmp` 就会踩这个坑 ——
那时要改的也是那份定义里 `/tmp` 那两行（把 `--bind /tmp /tmp` 移到 `--tmpfs /tmp` 下面）。

### C3. 程序起不来，报 `Can't create file: Read-only file system`（或 `Can't mkdir …`）

**根因**：一条路径映射的**目标**（程序看到那一侧）在沙箱里不存在，而沙箱的根是只读的 ——
只读的根里建不出挂载点。（`twin gen` 写的根是 `bwrap.root: dev`，根是可写的，bwrap 其实建得出来
—— 但它会把挂载点建在**宿主的家目录**里。所以"先把两端建出来"这一步一直留在启动器里。）

`twin` 会在挂载**两端**都先建出来（`twin install` 建自己那一侧，生成的启动器每次启动前再确认
一遍），所以正常情况不会遇到。若你在**透传区**手工写了 bind，那就踩了透传区"不享受这套补齐"这条：

```bash
# ❌ 透传区不会替你建挂载点
twin myapp -- --bind /some/new/dir /some/new/dir

# ✅ 写进 bind（享受挂载点补齐 ＋ ~/ 展开）
#    ~/.config/twin/rules.d/myapp.toml
#    bind = [ "~/.local/state/twin/myapp2/data:/some/new/dir" ]
twin rules myapp
```

⚠️ 单个**文件**（配置文件，不是目录）也能写进 bind，两端都要写成文件：形态由磁盘上已有的那一端
决定，两端都没在时按目录。所以"源端是文件、目标端还没建"时，`twin` 会把它建成**空文件** ——
不会被误建成目录，也不会去动程序已经写好的那份（见 [`development.md`](development.md) 的挂载点一节）。

### C3b. 程序报 `EISDIR` / 配置文件读出来是个目录

**根因**：映射的两端被建成了目录，而程序要的是文件 —— 典型是"把某个配置文件写进了 `bind`"。
`twin` 现在按形态建（见上一条），所以先确认这次落盘的启动器里是不是 `mount_pair`（而不是裸的
`mkdir -p`）：

```bash
grep -n "mount_pair\|mkdir" ~/.local/share/twin/<id>/launch
```

若那一行还是 `mkdir -p <文件路径>`，说明实例是旧版本落的盘：`twin reinstall <id>`。

⚠️ 两端形态**冲突**时（一端是文件、另一端已经是目录）twin 不让步：bwrap 会以
`Can't create file at <目录>: Is a directory` 明说，而不是默默挂上一个错的东西。

### C4. 网络断了，或程序报"已在运行"

**根因**：`isolation.unshare` 里放了 `net`。

```bash
twin verify <id>    # 会说明这一条被拒绝
```

改 `profile.yml` 去掉它，或接受"这个程序无法被这样隔离"。`net` 切断网络，也会打破基于
abstract socket 的单实例锁，而 bwrap 本来也隔离不了网络。

### C5. 程序报 `Failed to create socket directory` / `mktemp: Read-only file system`

**根因**：沙箱里的 `/tmp` 是**只读**的 —— 这份定义的 `bwrap.extras` 里既没有 `--tmpfs /tmp`、
也没有 `--bind /tmp /tmp`（把那条 `--bind /tmp /tmp` 删掉、或把根换成 `root: ro` 而没给 `/tmp`
补一条的实例会这样）。

**最典型的一例**是 Chromium / Electron：单实例锁的 socket 目录建在 `base::GetTempDir()`
（`$TMPDIR`，否则 `/tmp`）下的一个 `mkdtemp` 里，建不出来就打
`[ERROR:chrome/browser/process_singleton_posix.cc] Failed to create socket directory` ——
一条关于 socket 的报错，看着像 profile 出了问题。

```bash
grep -n "tmpfs\|/tmp" ~/.local/share/twin/<id>/profile.yml   # 决定就写在这份定义里
```

修复后必定有这两种之一：`bwrap.extras` 里的 `--tmpfs /tmp`（空的私有 `/tmp`），或它下面那一行
`--bind /tmp /tmp`（宿主那个 `/tmp`，原地可写）。**两行都留着是正常的** —— 程序走 Wayland 时是
前者的天下，X11 那一路靠后者把它换回来，机制就是"同一份列表里写在后面的胜出"。都没有，说明这份
定义更旧（那时基座在 twin 里、`bwrap.extra` 只装你追加的 token）—— 重新 `twin gen` 一份。

⚠️ 反过来，别用"加一行 `--tmpfs /tmp`"来救 X11 的程序：那样连 `/tmp/.X11-unix/X0` 一起
没了（见 C2）。X11 那一路要的正是宿主那个可写的 `/tmp`。

### C6. `错误：HOME is not set`

`twin` 拒绝在没有 HOME 时猜一个家目录。手写包裹曾用 `HOME=${HOME:-/root}` —— 在无 HOME
的上下文里会往 `/root` 建目录，那是**数据丢失**级别的默认值。

以真实用户身份运行（图形会话里的终端）。

---

## D. `wrap` 被拒绝

`twin` 的立场是"宁可不做，不做近似"。下面每一条拒绝都对应一次实测到的静默失败。

| 报错 | 含义 | 修法 |
|---|---|---|
| `id 已存在` | 显式 `--as` 撞到了已存在的东西 | 换个 `--as`，或确认后加 `--force` |
| `多合一二进制` | BusyBox / coreutils 之类靠 `argv[0]` 分派行为的程序，改名后会报 `unknown program` | 这类程序**不能**用 `alias`：用 `--level none`（只写菜单项）或 `--level wrap`（要沙箱、不要改名） |
| `找不到可用的源图标` | 源桌面项没有 `Icon=`，或图标文件不存在 | `--icon <png\|svg>` |
| `不是 Electron 程序（没有 resources/app*）` | 你显式指定的跳板不是 Electron，驱动不了 Node 模式 | 换一个，或删掉 `userns.trampoline` 用 `auto` |
| `--argv0 会破坏身份不变量` | 窗口 `app_id` 跟真实路径、托盘 `Id` 跟 `argv[0]`；分叉必然一方找不到桌面项 | 别用 |
| 报错原文「`--` 之后应以 bwrap 选项开头」 | 你想写 POSIX 的"选项到此为止"，而 twin 的 `--` 意思是"以下是 bwrap 参数" | 去掉 `--`，或学习 `--` 的真实含义 |
| `透传区不能再出现 --` | bwrap 会把它读成"选项结束"，后面全被当成程序名 | 要传字面的 `--` 用 `--flag=--` 形式 |
| `--level argument/env 还没实现` | 这两个级别没过实测 | 用 `none` / `wrap` / `alias` / `redirect` |
| `config.toml … 解析失败` | 文件存在但语法坏了 | **这是刻意的**：静默回退会让你的修改"什么都不发生"且毫无线索 |

---

## E. 中文 / 编码

| 症状 | 原因 | 修法 |
|---|---|---|
| 名字或关键词里有乱码 | 终端 locale 与桌面项编码不一致（桌面项本身是 UTF-8） | `locale` 检查；`twin inspect` 直接读源桌面项，对比是否一致 |
| 角标里的中文变成"豆腐块" | 内置点阵字体只覆盖 ASCII | 用数字/几何风格（`corner`＋数字，或 `dot`） |
| 搜索找不到这个实例 | 关键词没推导出来（源桌面项既无 `Keywords`，也无可用 `Name`/程序名） | 在桌面项正文里补一行 `Keywords=k1;k2`（路径就是 `twin install` 打印的那份"桌面项正文"），再 `twin reinstall <id>`；`install` 生成那份正文时会为这种情况打印一行警告 |

---

## F. 开发 / 构建相关的坑

见 [`development.md`](development.md)。最常见的两个：

| 症状 | 原因 |
|---|---|
| `go test` 报 `no space left on device` | `/tmp` 是小容量 tmpfs，Go 链接测试二进制时爆掉 ⇒ 设 `GOTMPDIR=/ TMPDIR=` |
| `command -v go` 找不到 go | 装在非标准前缀（如 `~/.g/go/bin/go`） |
