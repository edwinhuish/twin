# 设计：身份是怎么被"读"出来的

> 本文是 `twin` 的设计依据。所有结论都标注了取证方式 —— 这份文档里的每一条**都是实测
> 出来的**，不是从文档推出来的。

---

## 1. 先纠正直觉：身份不是"设"出来的，是被"读"出来的

想给一个程序改名，第一反应是"找个地方把名字设一下"。桌面环境里**没有**这样的地方。

一个程序在桌面上"看起来是谁"，由三条互不相干的通道决定：

| 通道 | Wayland 下由什么决定 | 谁在读 |
|---|---|---|
| 窗口身份 `app_id` | `/proc/self/exe` 的 **basename** | GNOME Shell 的窗口→应用归属、任务栏图标合并、`Alt+Tab` |
| 托盘身份 `SNI Id` | `argv[0]` 的 **basename** | 托盘宿主扩展的图标查找表 |
| 显示 / 搜索 | 桌面项文件的 `Name=` / `Keywords=` / `Icon=` | 应用网格、搜索 |

三条通道里，只有第三条是"一个文件"，前两条**在任何命令行参数里都拿不到**。

- 窗口身份：GNOME Shell 读的是进程的真实可执行路径。想让它变，只能**让程序真的从另一个
  路径名启动**。
- 托盘身份：`libdbusmenu` / SNI 协议里，`Id` 取自 `argv[0]`。它和窗口身份是**两条独立的
  读取路径**，但它们**必须同名** —— 否则其中一个找不到桌面项。

⇒ 所以唯一的通用做法是：**改名启动 ＋ 补一个同名桌面项**。`twin` 自动化的是这件事。

> ⚠️ 一条必须记住的坑：源桌面项里的 `StartupWMClass=` **不需要**，也不该依赖。
> 实测 Wayland 下 `app_id` 走"基名↔基名"匹配即可被认领；这一行只对 X11 的 `WM_CLASS` 才必需。

## 2. 身份注入阶梯

| 级别 | 做什么 | 状态 |
|---|---|---|
| `none` | 只写桌面项 ＋ 图标，程序**原样**启动（不经过 bwrap） | ✅ 已实现（T1） |
| `alias` | 用 bwrap overlay 给真实程序造一个**别名路径**并从那启动 | ✅ **已实现，主力** |
| `argument` | 靠程序自己的命令行参数改名 | ❌ 未实现（Qt 类程序忽略它） |
| `env` | 靠环境变量改名 | ❌ 未实现 |
| `redirect` | 同 `alias` 改名，**外加**写一条 L5 规则给 shell 扩展改判窗口归属 | ✅ 已实现 |

`argument` / `env` **故意不实现**：这个工具的立场是"宁可不做，不做近似"。把没验证过的级别
做成选项，等于把坑留给使用者。`--level` 传这两个值会被明确拒绝，并说明只有哪三个通过了实测。

`alias` 为什么能改窗口名：

```bash
--overlay-src /opt/wechat --tmp-overlay /opt/wechat   # 目录在命名空间内可写（一份 tmpfs 副本）
--ro-bind /opt/wechat/wechat /opt/wechat/wechat2      # 在副本里造出别名
-- /opt/wechat/wechat2                                # 从这个路径启动
```

`/proc/self/exe` 于是解析为 `/opt/wechat/wechat2` ⇒ basename = `wechat2` ⇒ `app_id` = `wechat2`。
而 `argv[0]` 默认就是 exec 的那条路径 ⇒ 托盘 `SNI Id` 也是 `wechat2`。**两条通道天然一致**。

这也是为什么 `--argv0` 被拒绝（见 §5）：窗口跟真实路径、托盘跟 `argv[0]`，让它们分叉就必然有
一方找不到桌面项。

### `redirect` 什么时候需要

有些程序把 `app_id` **写死在代码里**（不读 `/proc/self/exe`），改名对窗口归属无效。这时：

- 仍然走 `alias` 的改名（bwrap 会让该进程**留下可取证的身份痕迹**：`mountinfo` 里的别名路径、
  私有根目录、`argv[0]`）；
- 额外写 `~/.config/twin/rules.d/<id>.json`，交给 shell 扩展改判窗口→应用的映射。

⚠️ L5 规则里**不写 `require`**：`require` 是一票否决，而改名后 `argv[0]` 就是别名，任何含原名的
条件都会永不命中。规则里的证据必须是**进程级**的（`app_id` 被写死 ⇒ 窗口属性没有区分度）。

## 3. 三种厚度

厚度与身份级别是**两个正交的轴**。后者决定"叫什么是谁"，前者决定"跑在什么里"。

| 厚度 | 内容 | 需要什么 |
|---|---|---|
| **T1** | 桌面项 ＋ 图标 ＋（`redirect` 时）L5 规则 | 零特权，`level=none` 时连 bwrap 都不需要 |
| **T2** | T1 ＋ bwrap 基座（`profile.yml` 的 `bwrap` 节点，含 overlay 别名） | 能建非特权 user namespace |
| **T3** | T2 ＋ 路径映射（`rules.d` / 实例定义）＋ tmpfs | 同上 |

`twin` 默认给 T3；`level=none` 时退到 T1。

## 4. 管线

```
inspect ──▶ plan ──▶ materialize ──▶ verify
（只读）   （纯函数）  （幂等落盘）    （回读自证）
```

各阶段各自遵守一条硬规则：

- **inspect 只读**：不改任何东西，不写任何文件。它的输出就是"我打算怎么做"。
- **plan 纯函数**：不碰磁盘（除了读），把 CLI ＋ 各配置层合成一份 `inst.Instance`。所有默认值
  在这一步推导并**记录来源**。
- **materialize 幂等**：重复跑只覆盖自己的产物，永不碰源程序、永不碰系统目录。
- **verify 回读**：**重新读硬盘上的东西**，不看内存里的期望 —— 这是它和"我以为我写对了"的
  唯一区别。

### 默认值是怎么推导的

| 值 | 默认来源 |
|---|---|
| `id` | 源桌面项基名 ＋ **最小空闲序号（从 2 开始）** |
| `desktop.name` | 源 `Name[<当前语言>]` ＋ 序号（如 `微信2`）；没有语言化变体则用 `Name` |
| `desktop.name_en` | 源 `Name` ＋ 序号；与 `desktop.name` 相同则不写 `Name[en]` |
| `desktop.comment` | 源 `Comment[<当前语言>]` → `Comment` → 合成一句 |
| `desktop.keywords` | 显式 > rules.d > 源 `Keywords[<语言>]`/`Keywords` > 从源名与程序名合成 |
| `badge` | `id` 尾部的数字串（`wechat2` → `2`） |
| `badge_style` | `config.toml` → `corner` |
| `level` | CLI → rules.d → `config.toml` → `alias` |
| `/tmp` 是否 tmpfs | 程序支持 Wayland ⇒ 加 `--tmpfs /tmp`；只支持 X11 ⇒ **不加**（会砍掉 `/tmp/.X11-unix/X0`）。不加时用的是宿主那个 `/tmp`，**可写**（这一条不是"顺带"：只读的 `/tmp` 会让 Chromium 的单实例 socket 建不出来，见 §5） |

> `id` 为什么从 2 开始：`wechat` 这个 id 通常已被**系统的那份桌面项**占用，而同名会撞车 ——
> GNOME 对重复桌面项 id 的处理是**静默遮蔽其中一个**。`freeID` 找的是第一个既不是 twin
> 实例、也不被用户/系统桌面项占用的名字。

所有推导出来的值都会打印出来，**带来源**（下面是真机实测的一次输出；这台机器上
`wechat2` 已被占用，所以取了 3）：

```
推导出的默认值（都可用选项覆盖）
  id               wechat3   ← 源桌面项基名 wechat + 最小空闲序号
  level            alias   ← config.toml defaults.level
  desktop.name     微信3   ← 源 Name[zh_CN]="微信" + 序号 3
  desktop.name_en  wechat3   ← 源 Name + 序号 3
  desktop.comment  微信桌面版   ← 源 Comment[zh_CN]
  desktop.keywords 微信;wechat;   ← 源无 Keywords；由 Name[zh_CN]、Name 推得
  badge            3   ← 从 id 的尾部数字提取
```

一个看不见来源的默认值，就是一个没人能纠正的默认值。

## 5. 产物与模板

`twin install` 写下面这些。第 4 件（`~/.local/share/twin/<id>/profile.yml`）不是它写的 ——
`twin gen` 写的就是那一份，`install <id>` 读回同一份且**不重写它**（一次解析往返会丢掉手写的注释）；
只有 `-p` 指到别处时，它才在这个位置写一份存档记录。落点在实例自己的目录里，因为它必须从下一个
shell 里还找得到，而且**一个 id 一份**（想放别处用 `-o`，再用 `-p` 读）。

| # | 路径 | 内容 | 何时写 |
|---|---|---|---|
| 1 | `~/.local/share/applications/<id>.desktop` | 身份锚点 | 总是 |
| 2 | `~/.local/share/appicons/<id>.png` | 合成好的图标（256×256 默认） | 总是 |
| 3 | `~/.local/share/twin/<id>/launch` | 薄启动器（bash，`exec` bwrap） | 总是 |
| 4 | `~/.local/share/twin/<id>/profile.yml` | 实例定义（`gen` 写的就是你编辑的那一份；`-p` 指别处时这里放的是 install 写下的存档记录） | 定义：`twin gen`；存档记录：仅 `install -p <别处>` |
| 5 | `~/.config/twin/rules.d/<id>.json` | L5 规则（扩展读） | 仅 `level=redirect` |
| 6 | `~/.local/state/twin/<id>/` | 私有状态目录（空目录） | 总是 |
| 7 | `~/.local/share/twin/bin/bwrap` | 私有 bwrap 副本 | 仅当程序需要嵌套 ns |
| 8 | `~/.local/share/twin/bin/bootstrap.js` | 跳板脚本（借别人的 AppArmor 标签那条路用） | 仅当这一趟走的是跳板 —— 走"自己装的 profile"时不需要它 |
| 9 | `~/.local/share/twin/tray.d/<id>.json` | 托盘文件表（扩展读它画图标，§7.1 的**首选路径**） | 每次 install；扩展不可用时**不写**（那份是兜底） |
| 10 | `~/.local/share/gnome-shell/extensions/twin-tray@edwinhuish.github.io/` | 托盘扩展（`metadata.json` ＋ `extension.js`：读上面那份表画图标 ＋ 同步图标框尺寸） | **不是** `twin install`：`twin install --ext`（机器级，一份服务所有实例） |

### 5.1 实例定义

同一份 schema 落在两处，头注释各自指向读它的那条命令（两个角色 = 同一个文件名的两种来路）：

```yaml
# …profile.yml 的头 —— twin —— 实例定义。改完跑 `twin install wechat2` 按它落盘；
# `twin reinstall wechat2` 重放的就是这一份。
# （`-o` 写到别处时这句换成 `twin install -p <那份路径>`：`install <id>` 读的是实例目录里那份）

# twin —— 实例定义（存档记录）。`twin install` 按你指定的那份配置写出它；
# 改完跑 `twin reinstall wechat2` 重放。
id: wechat2

desktop:
  name: 微信2
  name_en: WeChat2
  comment: 微信
  keywords: 微信;wechat;WeChat;
  source: /usr/share/applications/wechat.desktop

identity:
  level: alias
  wm_class: wechat2     # = id，仅当 level 会改名时写；X11/XWayland 才真正需要
  notify: true          # → 桌面项 StartupNotify=true

icon:
  source: /usr/share/icons/hicolor/512x512/apps/wechat.png
  badge: "2"
  style: corner

exec:
  target: /opt/wechat/wechat
  alias: wechat2

isolation:
  unshare: []
  nested_ns: false

bwrap:                 # 这一个节点就是这次调用；键的顺序就是命令里的顺序 —— extras 除外
  proc: /proc          # 挂一个 proc 到这条路径（基座写的就是它）
  tmpfs:               # 盖成空目录的路径（含基座那两条：/dev/shm 与私有 /tmp）
    - /dev/shm
  bind:                # 挂载都写在这里；第一条是根挂载，永远排在最前（写在第几行都一样）
    - /:/:dev
    - ~/.local/state/twin/wechat2/xwechat:~/.xwechat
  remountRO: []        # 把哪些路径收回只读（只能作用在自己就是一条挂载的路径上）
  env:                 # 注入沙箱的环境变量，一条一个 `键:值`（基座那条 TWIN_INSTANCE 也在里面）
    - TWIN_INSTANCE:wechat2
  extras:              # 没有自己节点的那些 token；基座里**没有自己节点**的那几条在这里
    - --bind           # 宿主那个 /tmp，可写（程序走 X11、不要私有 /tmp 时才有这一条）
    - /tmp
    - /tmp
    - --die-with-parent

tray:
  override: true
```

那些文件都是**给人读、给人改的**：常态只有一份（`gen` 写进实例目录、你改、再 `twin install <id>` 或
`twin reinstall <id>`）；`install -p <别处>` 时实例目录里那份是存档记录，改它就直接 `twin reinstall <id>`。
真实文件里**每个键上面都有一行说明**（`twin gen` / `twin install` 不在终端重复它们），上面这份骨架
把注释省掉了。

注意几处刻意的设计：

- **状态目录用 `~/` 而非绝对路径**（`RelStateDir`）：文件要能搬到另一台机器重放，不能把今天的
  HOME 烤进去。`~/` 由 `Paths.Expand` 在**用的时候**折叠成本机 home；而同一种"不烤进机器路径"的
  诉求还有第二种写法 —— **相对路径，基准是这份文件所在的目录**（`inst.Load` 解析一次），
  它让"配置 + 程序 + 状态目录"能放成一个整体搬走的 bundle。`bwrap.extras` 两种都不解析：那是 bwrap
  的选项列表，逐 token 照搬。
- **凡是会变成 bwrap 参数的东西都在 `bwrap` 节点里**：`args`（原始 token 列表）、`bind`、以及
  `proc` / `tmpfs` / `remountRO` / `env` 这四个**有形状的挂载**。后四个各有自己的键，是因为每个都
  是**一种**挂载——一个 proc、一个用完即弃的 tmpfs、一条被收回只读的路径——而平铺的 token 列表说不出
  这件事；`bind` 的 `:ro` / `:dev` 说的是同一类事。四者在文件里的顺序就是它们在命令里的顺序，于是
  "这条设置会变成哪条 bwrap 选项"不必跨半个文件拼。注意 `rules.d` 里的 `bind` / `tmpfs` 仍在顶层：
  那份文件描述的是"这个程序的数据放在哪"这类**应用知识**（旁边还有 `keywords` / `categories`），
  它服务多个实例，不是一次调用。
- **`tray.override` 总是写**（即使值是 `true`）："不覆盖托盘"本身也是一个决定，缺这段会让它
  和"这个字段存在之前写的老文件"无法区分。缺段的老文件按 `true` 读。
- **`nested_ns` 总是写**：它是**应用知识**（这个程序要不要自己建 ns），跟定义走；而"要不要给副本
  装一份 profile、用哪个跳板、副本放哪"是**机器状态**，留在 `config.toml` 里，让下一台机器自己决定。

### 5.2 `launch`（薄启动器）

```bash
#!/bin/bash
set -euo pipefail
: "${HOME:?twin: HOME 未设置，拒绝启动}"

readonly ID='wechat2'
readonly NAME='微信2'
readonly STATE="$HOME/.local/state/twin/wechat2"

# ── 启动失败 ── 判 GUI、抓 twin 自己的输出、twin_dialog、twin_fail（见 failHelpers）
# ── 挂载点 ──   mount_point / mount_pair，每一条命令后面都跟着它自己的 twin_fail
exec … 2>&3
twin_fail "$?" '无法启动 bwrap' '/usr/bin/bwrap'
```

六个刻意的选择：

- **`exec` 而非 `wait`**：`exec` 之后进程表里不留下包装器的痕迹 —— 这正是"薄"的意义。
- **纯 shell 而非编译启动器**：够用，且生成物可读、可审。
- **`: "${HOME:?...}"` 显式失败**：手写版曾用 `HOME=${HOME:-/root}`，在无 HOME 的上下文里会
  往 `/root` 建目录 —— 那是**数据丢失**级别的默认值。`twin` 在这一点上选择硬失败。
- **路径折叠成 `$HOME/...`**：启动器跑在生成之后很久，把生成时刻的 HOME 烤进去会让它在任何
  其他 HOME 下静默错误（也会让上面那行守卫变成装饰）。
- **失败必须自己开口**（`failHelpers`）：桌面项启动的进程没有终端，脚本死在 `exec` 之前，在
  用户眼里就是"点了没反应"——而这正是 `verify`/`doctor` 补不回来的一种失败（等到能跑它们的
  时候，用户早忘了刚才点过什么）。所以**每一步都带一句自己的说法**（`… || twin_fail "$?" …`），
  图形会话里把它弹成一个窗口，并写进 `$STATE/launch-error.log`。判据是"这一刻是不是图形会话"
  （没有控制终端 ＋ 有显示），所以判在脚本里；终端里跑的时候一句都不多说 —— 那份错误本来就在
  用户眼前。弹窗链按**正文能选中复制**挑（`zenity --text-info` → `kdialog --textbox` →
  `yad --text-info`），`xmessage` 与 `notify-send` 因此不在链里。
- **`exec` 之前与之后的输出是两个 fd**：报告文件只装 twin 自己的话（`2>"$LOG"`），程序自己的
  stderr 在 `exec` 时用 `2>&3` 原样还回去 —— 那份文件没人轮转，接下一整个程序的输出是另一回事。
  `exec` 还得单独关掉两件默认行为：非交互 shell 里它失败会**直接退出**（与 `set -e` 无关），
  所以最后一段是 `shopt -s execfail` ＋ `set +e`，它后面那行 `twin_fail` 才是"程序没能启动"的
  唯一出口。

> 这一层只管"**程序根本没启起来**"（启动器自己那几步 ＋ `exec` 本身）。程序跑起来之后退出码非零
> 不算启动失败：`bootstrap.js` 特意逐字转发它，把"退出应用"报成"启动失败"才是错的。

### 5.3 `<id>.desktop`

```ini
[Desktop Entry]
Type=Application
Version=1.0
Name=微信2
Name[en]=wechat2
Comment=微信桌面版
Exec=/home/you/.local/share/twin/wechat2/launch %U
Icon=/home/you/.local/share/appicons/wechat2.png
Terminal=false
Categories=Utility;
Keywords=微信;wechat;
StartupWMClass=wechat2
StartupNotify=true
```

- `Icon=` 用**绝对路径**（不走图标主题查找）。
- 基名必须等于 `id`（GNOME 按桌面项基名做匹配）。
- `StartupWMClass` 只在 `level` 会改名时写，且**不该被依赖**：Wayland 下认领窗口的是基名
  （2026-10-11 实测）。它对 X11/XWayland 才有意义。
- ⚠️ **`Exec=` 指向的文件必须有执行位**：GLib 会因此**静默丢弃整个桌面项**，而
  `desktop-file-validate` 查不出这一点。这是本项目最贵的一个坑（见 `docs/troubleshooting.md`）。

### 5.4 bwrap 调用（`level=alias` 的基座）

**基座是那份定义里的一段文本，不是 twin 的一个常量；而且每条各归自己那个键。** `twin gen`
（`sandbox.BaseFor`）把它写进定义时按**挂载的种类**分派：有形状的进同名的键，没形状的才留在一个
扁平列表里。`sandbox.Build` 一字不改地照发；twin 不再自己补任何选项，所以"最终跑的是哪些参数"
在文件里就能整份读到、也能整份改掉。

**有一处例外要记住**：`bwrap.extras` 写在节点的**最后**，而它在命令里排在根挂载之后、其余各条
**之前**。它不是"最后才加的那几个 token"（那是一份合并后的完整列表，见 §6），只是因为它是这份
定义里唯一一个**文件位置不决定命令位置**的键，放在末尾才不会让文件暗示一个它没有的顺序。其余键
的文件顺序就是命令顺序。

```
# bwrap.bind / bwrap.proc / bwrap.tmpfs / bwrap.env（gen 写的基座，用户可改）
  /:/:dev                           # 根挂载：整个宿主根（含设备）挂进来，就是 --dev-bind / /
  --proc /proc
  --tmpfs /dev/shm                  # 空的私有共享内存
  --tmpfs /tmp                      # 空的私有 /tmp（选了私有那一条时；另一条见下）
  --setenv TWIN_INSTANCE <id>

# bwrap.extras（同样由 gen 写，用户可改；写在节点最后，但位置与命令无关）
  --bind /tmp /tmp                  # 宿主那个 /tmp，可写（不要私有 /tmp 时才有这一条）
  --die-with-parent
```
```
bwrap
  --dev-bind / /                    # 根挂载：从 bwrap.bind 里提到最前（见下）
  <bwrap.extras 整个列表>             # 原样，跟在根挂载后面（与它在文件里的位置无关）
  [--proc <bwrap.proc>]…            # 其余各条从这一行往下，顺序照文件里的键
  [--tmpfs <bwrap.tmpfs 里的路径>]…
  --overlay-src <targetdir>
  --tmp-overlay <targetdir>         # ⚠️ 必须在创建别名之前
  --ro-bind <target> <alias path>
  [--bind|--ro-bind|--dev-bind <源> <目标>]…   # 路径映射（源是数据实际所在；:ro / :dev 选后两个）
  [--remount-ro <bwrap.remountRO 里的路径>]…   # ⚠️ 必须在所有挂载之后
  [--setenv <bwrap.env 里的键> <值>]…
  --
  <alias path> "$@"
```

为什么基座要写进文件：**"这个程序拿到哪个根"是这份定义里唯一随程序而变、又曾经完全表达不了的决定。**
基座原先是硬编码在 Go 里的 `--ro-bind / /`，一个要碰设备、或自己要建沙箱的程序需要
`--dev-bind / /`，而它唯一能开口的地方是透传区 —— 透传区排在**所有映射之后**，在那里重新绑一次根
会把前面每一处挂载都盖掉。于是"改基座"与"改不了"变成了同一件事。现在换根＝改 `bind` 里那条
`/:/:dev`（挂载写在挂载的节点里），位置由 `Build` 保证：**两端都是 `/` 的那一条永远是第一条**，
写在文件的第几行都一样 —— 它要是排在 overlay 别名之后，宿主的目录会盖回程序的运行路径上，别名
和程序一起消失。

`--dev-bind / /` 的取舍是明确的：设备可用（`/dev`、`/dev/dri` 都在），代价是根**不再是只读的**，
`/run/user` 与 `/tmp` 的可写因此天然成立 —— 那些"专为但求可写而写"的 `--bind X X` 全都不需要了。
`twin` 是**身份**工具，不是安全边界（§9）：它构建的命名空间是为了"改名 ＋ 隔离数据目录"，
不是为了跑不受信任的代码。想收紧的人把 `/:/:dev` 换成 `/:/:ro` 即可（三段：源、目标、模式 ——
两段的 `/:/ro` 读成"把根挂到 `/ro`"，`Check` 会指名拒掉），代价是自己补回设备与 `/run/user`
（`--dev-bind /dev /dev`、`--bind /run/user /run/user`），否则 Wayland socket、会话总线、PipeWire
全断 —— 这一条写在文件里那段注释里，就在那一行旁边。

三条顺序上的硬约束，都是实测出来的：

1. `--tmp-overlay DIR` **必须**排在创建别名的 `--ro-bind … DIR/别名` 之前，否则挂载点建不出来
   （`Can't create file: Read-only file system`）。
2. 根挂载与定义里那份列表**必须**排在所有映射之前（`Build` 先发根挂载，再发那份列表）。理由与
   第 1 条同源、方向相反：后写的挂载遮蔽同目标的前一个挂载，所以映射要能覆盖基座，就不能被基座
   覆盖。
3. `--remount-ro` **必须**排在它所收紧的那条挂载之后（`sandbox.Build` 把它发在最后），否则那条可写
   的挂载会盖在它上面，收紧变成空操作。它还有一条自己的边界：只对**自己就是一条挂载**的路径有效，
   宿主那一条大根挂载里的路径底下没有单独的挂载 —— 点错了 bwrap 直接拒绝启动
   （`Can't remount readonly on …: Unable to find … in mount table`）。

在这三条之前，启动器还要先把**所有挂载点**建出来。`--dev-bind / /` 之后 bwrap 其实建得了挂载点，
但那会把挂载点建到**宿主的家目录**里、而且一律建成目录；先建出来就没有这个问题，而且换成只读根时
这一条是必需的（`/:/:ro` 之后的根是只读的，bwrap 自己建不了：`Can't mkdir parents …: Read-only
file system`）。唯一不建挂载点的是根挂载本身 —— 它两端都是 `/`，本来就在那儿。建的时候要判形态
—— 一条映射的两端必须是同一种东西（文件挂到目录上 bwrap 会拒绝：
`Can't create file at <目录>: Is a directory`），而 `mkdir -p` 打在**文件**上会报 `Already exists`，
`set -e` 于是让整个启动器在 exec 之前就死掉。所以形态由磁盘上**已有的那一端**决定，两端都没在时
按目录（空目录读作"还没启动过"，空文件读作"配置是空的" —— 后者是句谎话）。这一判据有两份实现，
各管一段时间：启动器里是 `mount_point` / `mount_pair` 两个 shell 函数（每次启动都判），
`twin install` 里是 `sandbox.EnsureMountPoints`（落盘时判，免得把一个本该是文件的私有副本造成目录
—— 启动器不碰已经存在的路径，那个错误会一直留着）。改这条规则要**两处一起改**。

**`/tmp` 必须是可写的，而且两种写法都要看得见。** 一个建不出临时目录的程序会以跟 twin 毫无关系的
样子失败。实测的那一例是 Chromium：`process_singleton_posix.cc` 在 `base::GetTempDir()`
（`$TMPDIR`，否则 `/tmp`）下 `CreateUniqueTempDir()`，`mkdir` 拿到 `EROFS` 就打印
`[ERROR:chrome/browser/process_singleton_posix.cc] Failed to create socket directory` 并放弃单实例锁
—— 一条关于 *socket* 的报错，来自一个用户刚刚才包上的程序。所以两条路都保证它可写：走 tmpfs 的
程序拿到一个空的私有 `/tmp`（`bwrap.tmpfs` 里的一条），不走 tmpfs 的程序（X11 那一路，tmpfs 会把
`/tmp/.X11-unix/X0` 一起带走）拿到**宿主的 `/tmp`，可写**（`bwrap.extras` 里的 `--bind /tmp /tmp`）。
"存在但写不进去的 /tmp" 不是第三种选择。

宿主 `/tmp` 那一条留在 `bwrap.extras` 而不是进 `bwrap.bind`：它在默认那个可写的根下**什么也不做**
（整个宿主根本来就带着一个可写的 `/tmp`），它管的是"哪天把根收紧成 `/:/:ro` 时 /tmp 仍然可写"
（`args` 排在根挂载之后，所以那条收紧压不住它）。而 `bwrap.bind` 是"隔离哪些数据目录"的清单
—— `install` 会把它整段报成路径映射，还会建议照抄进这个程序的 `rules.d`，一条 twin 自己的
`/tmp` 混在里面（它不是根挂载，滤不掉），等于把基座的决定说成用户的数据布局（实测：它就是这么
被报出来、并被写进那份规则草稿的）。

## 6. bwrap 选项列表的合并

原则：**传 token，不重新解析**。`twin` 不知道也不想知道任何 bwrap 选项的元数（arity）——
那是 bwrap 的事，在这里再实现一张表只会随每个 bwrap 版本腐烂。

- `bwrap.extras` 拼的是**这份列表自己**，按位置、低位在前：`gen` 写的基座（没有自己节点的那几条）
  → `config.toml` 的 `[bwrap] args` → `rules.d` 的 `[bwrap] args` → `gen` 命令行上 `--` 之后那一段。
  后写的遮蔽同目标的前一个挂载，而 twin 不需要拥有任何一条冲突规则 —— bubblewrap 自己就是规则。
  ⚠️ 基座里**有**自己节点的那几条（根挂载在 `bind`，`proc` / `tmpfs` / `env` 在各自那个键）不在这份
  列表里，而 `Build` 把根挂载发在所有东西**之前**、把其余几条发在这份列表**之后**：所以这几层能压住
  根挂载（在 `--` 里绑一次根，就是不动文件换个根），却压不住 `proc` / `tmpfs` / `env` —— 想改那三个
  就改那个键；两个键描述同一处挂载本来也不是这份定义打算表达的。
- `--` 之后那一段**是写进文件的一份**，不是每次启动的覆盖：它落在基座与全局/规则层之后，
  于是它在**列表内部**有最终话语权，而列表整体排在根挂载之后、路径映射之前。
- 这些 token **不享受**三件事（在别处都有的）：挂载点自动补齐（含形态判断）、`~/` 展开、语义校验。
  ⇒ 数据路径请写进 `rules.d` 或实例定义里的 `bind`，不要写进 `bwrap.extras`。
- 三条硬边界（拒绝，不是警告）：
  - `--`（会截断 bwrap 自己的选项解析，后面全被当成程序名）；
  - `--argv0`（会破坏"窗口 app_id 与托盘 Id 同名"的不变量）；
  - 需要文件描述符的选项（`--file` / `--bind-fd` / `--seccomp` / `--userns-block-fd` …）——
    启动器由桌面环境 exec，手里没有可用的 fd。
- 校验是 **lint 而不是 transform**：宁可警告，不悄悄改写用户写的东西。
- 旧键 `bwrap.extra`（以及 `config.toml` / `rules.d` 里的同名键）**指名报错**：它只装用户追加的
  token，而 `bwrap.extras` 装的是交给 bwrap 的完整列表（基座也在里面，只是分节点写），读错会交给
  bwrap 一个什么都没挂的命名空间，失败信息里一个字都不会提到 twin。没有根挂载同理（`bind` 里没有
  `/:/…`、`args` 也空着）—— 有沙箱就必须有一个根挂载，`/:/ro` 这种两段写法也会被指名拒掉。

## 7. 图标管线

```
源图标 ──解析──▶ 底图 ──缩放──▶ 合成角标 ──▶ <id>.png ──写两处引用──▶ 生效
```

**解析源图标**（按序）：源桌面项 `Icon=` 是绝对路径 ⇒ 直接用；是主题名 ⇒ 在
`config.toml` 的 `icon.theme`（默认 `hicolor`）下按尺寸目录查找。
**找不到 ⇒ 直接拒绝生成**，而不是静默出一个齿轮占位图。

**光栅化**：PNG / JPEG 走 Go 内置解码器（零外部依赖）。SVG 与其他格式交给外部工具，按序尝试
`python3-gi` 的 GdkPixbuf → `rsvg-convert` → ImageMagick。三样都没有时只有 PNG/JPEG 源图标
可用（`doctor` 会报一行）。

**合成角标**（纯 Go，自绘位图蒙版 ＋ 5×7 点阵字体）：

| 风格 | 形状 | 适用 |
|---|---|---|
| `corner`（默认） | 右下角圆角方块 | 数字/短文本 |
| `number` | 右下角圆形 | 数字 |
| `dot` | 小圆点，无文字 | 16px 面板下最清晰 |

角标**贴齐**图标的右边缘与下边缘（图标右下角是圆角也照贴，被图标主题再裁一刀就裁）：16px 面板下
它只有 5–6px，是那张图上唯一还要数得清的东西，缩在圆角里侧只会显得浮在图上 —— 所以几何符号优先，
数字只是一种风格。

**图标必须写两处**，两条链路互相独立：

| 落点 | 谁读 | 漏了会怎样 |
|---|---|---|
| `<id>.desktop` 的 `Icon=` | 启动器 / 面板 / 任务栏 / `Alt+Tab` / 概览 | 齿轮占位图 |
| 托盘覆盖（两条路之一：`custom-icons` 那个键，或本文 §7.1 的文件表） | 托盘宿主 `ubuntu-appindicators`，或 §7.1 的助手扩展 | 托盘仍是原程序的图标 |

走宿主覆盖表时，托盘那条链路的完整优先级是：

```
custom-icons（按 SNI Id 严格匹配；值以 / 开头 ⇒ 当文件路径加载）
  → SNI IconName（配 IconThemePath 查主题）
    → SNI IconPixmap（应用内嵌位图，外部改不动）
```

只有**第一级**是外部可控的，而它**优先级最高**：命中后应用的 `IconName` / `IconPixmap`
被完全忽略。它还是**热重载**的（监听 `changed::custom-icons`）⇒ 当场写、当场生效，不用重登。
§7.1 的助手扩展把同一级**替换掉**（塞进宿主的自定义图标通道，见那里）：命中后同样忽略程序自己那两样，
同样是热生效，但那一份表是文件、也不需要会话总线。

⚠️ 前提是 `Id` 必须唯一 —— 否则两个实例会同时命中同一条规则。这正好由身份层保证
（overlay 别名 = `argv[0]`）。

⚠️ 这是**全局单键**：写入必须 read-modify-write 整张表，绝不覆盖别人的条目。§7.1 那份文件表按实例
一个文件，没有这个约束 —— 这也是它更结实的第二个理由。

### 7.1 托盘图标：两条路，一次只走一条

`twin` 让托盘显示自己的图标有两条路，**同一个图标不留两个来源**：

| | 首选：助手扩展自己画 | 兜底：宿主覆盖表 |
|---|---|---|
| 图标从哪来 | `~/.local/share/twin/tray.d/<id>.json`（twin 写的一份**中立文件表**） | `custom-icons` 那个 dconf 键 |
| 谁把它画上去 | `twin-tray@edwinhuish.github.io` 在 shell 进程内 | 宿主自己（上一节那条链） |
| 需要会话总线吗 | **不需要**（写文件 ＋ 进程内生效） | 需要（无总线时 `gsettings set` 会假成功） |
| 尺寸 | 跟随面板 `icon-size`（见下） | 恒主题的 16px（见下） |

选哪条由 `tray.ExtState.Usable()` 在 **install 时**决定一次：扩展的文件在用户扩展目录里 **且**
uuid 在 shell 的启用列表里 **且** 不在它的禁用列表里 **且** 两个列表都读得到。刻意保守 ——
拿不准就走兜底，因为兜底是所有机器都能走通的那条，而反过来判错的代价是**那个实例一个图标都没有**，
屏幕上还没有任何东西解释为什么。走了哪条每次都打印（`twin install` 的输出里那一行），切到扩展这条路时
还会把宿主表里本实例的那一行**删掉**：两条路都指向同一张图不算冲突，但它就是"一个事实两个来源"，
而且留着的那一行正是 `twin remove` 之后还会指着图标的那一行。

**扩展怎么把图标画上去。** 复用宿主自己的渲染路径，不重写一遍：

- `IconActor._updateIconByType(iconType, iconSize)` 是宿主**每一次**真正设置图标的入口，它先同步读
  `this._customIcons`（`SNIconType → 绝对路径` 的 Map），有值就走宿主那条"自定义图标"实现（绝对路径
  加载、attention 回落、`Gio.EmblemedIcon` 包装……全是上游的代码）。扩展在它之前把值塞进同一个 Map，
  于是屏幕上画出来的东西与写 dconf 表**逐像素相同**，差别只在谁把它放进去。
- 判定键是 `this._indicator.id` —— 与宿主自己拿 dconf 表去找那一行用的是同一个键，所以两条路匹配到
  的托盘项完全一致，换路不会换掉"哪个图标被替换"。（`Main.panel.statusArea` 的 role 后缀是另一个东西
  —— 那是 `Util.indicatorId()` 拼出来的 `busName@objectPath`，不能当匹配键。）
- 补丁只碰**被补的那个函数本来就在用的三个名字**：`_updateIconByType`（补丁目标）、`this._customIcons`
  （注入点）、`this._indicator.id`（判定键）。类原型从状态区里现成的实例上取（`Object.getPrototypeOf`），
  所以既不 import 宿主的模块、也不去猜它装在哪个目录。宿主把这些名字改名，最坏结果是**判据拿不到
  ⇒ 一行 `console.warn`、图标回到程序自带的那张**，而不是把托盘画坏。
- ⚠️ 已知副作用（与走 dconf 那条路完全相同）：`_customIcons` 一非空，`_updateIconByType(OVERLAY, …)`
  就查不到 overlay 的自定义项、把 overlay 徽标清掉（上游行为）。没有 overlay 图标的程序不受影响。

**尺寸那一半（宿主的一个 bug）。** 命中覆盖表的那一项被宿主包成 `GIcon`（`_setGicon`）交给 `St.Icon`
画，而**程序自己发**的位图走 `_setImageContent(content, S, S)` —— 后者显式给 actor 定了宽高，前者只给
内容，屏幕上多大由 actor 的**分配框**决定（`st-icon.c` 全文件没有一处 `clutter_actor_set_size()`，
它只改"纹理加载多大"）。那个框在 `IconActor._init()` 里只写一次：

```js
this.height = iconSize * themeContext.scale_factor;   // iconSize = PANEL_ICON_SIZE || 16
```

而 `PANEL_ICON_SIZE` 在 gnome-shell 49 就被删掉了（45–48 有，但没 export），所以它恒为 **16**。此后只有
`notify::scale-factor` 会重刷这个框；`_updateIconSize()` 把 `set_icon_size(this._iconSize)` 写上了，
**漏了框**。于是：设置里 `icon-size` 调到 30，程序自己的位图跟着变 30，twin 的自定义图标仍是 16
（Yaru 给 `.system-status-icon` 的 1.091em）—— 同一排图标里小一圈。

这一环只能在 shell 里面补（框是宿主在 JS 里写的，外面没有接口），所以扩展还做第二件事：把每个
`appindicator-*` 的 `IconActor` 的框同步到它自己的 `icon-size` 属性。时机是安全的 —— 同步发生在
`notify::icon-size`，而图标内容是之后才建的（`_updateWhenFullyReady` 的顺序是 尺寸 → 类名 →
custom-icons → 内容）⇒ 位图那条路稍后仍会用自己的显式宽高覆盖它，**位图图标不受影响**。
`icon_size > 0` 这个条件同时排除了主题自己算的那条路（主题走 `st_icon_update_icon_size`，不会让这个
属性变正）。

几个刻意的选择：

- **不 fork 宿主。** 系统那份在 `/usr/share/gnome-shell/modes/ubuntu.json` 里被列为 **mode extension**，
  用户目录下的同名副本会被 `extensionSystem.js` **静默拒绝**（连 uuid 都不登记）⇒ fork 这条路走不通。
  助手是**一个新 uuid**，与宿主**并排**运行。
- **不做成"写 dconf 的桥"。** 有一条更短的实现：扩展读同一份文件表，再在 shell 进程内把值写进
  `custom-icons`（进程内写 dconf 不会假成功，也不用管会话总线）。它被换掉的理由是：那样两条路的**产出
  是同一个全局键**，于是"谁在写"永远说不清，而且仍然浪费在 read-modify-write 与按 SNI Id 严格匹配上
  —— 扩展既然在 shell 里，就没有理由绕道 dconf。
- **装是文件写入，启用是会话动作；而且这件事是机器级的，所以它归 `twin install` 的一个开关。** 一份扩展服务所有
  实例，挂到 `twin install` 上就成了"包一个实例"的副作用，重放定义时还会重做一遍 ⇒ 写入归
  `twin install --ext`，`twin install` / `twin reinstall` 只检查并警告。shell **只在启动时**扫一遍
  扩展目录，所以一个它没见过的 uuid **在下次登录前没有任何命令能启用它**。这是 shell 的事实，
  不是这里的缺陷 ⇒ `twin install --ext` 报"注销重登
  一次"，而不是反复重试。那一步也**不改退出码**：扩展只把图标画成面板那么大，没它兜底那条路照旧
  画图标，所以它是 ⚠️ 而不是"还差一步必须人做"。它也不会让已有实例自动搬家：那些实例在下一次
  `install` / `reinstall` 时才改走这条路（否则屏幕上那个"小一圈"的图标会像个新 bug）。
- **停用等于没装过。** disable 时补丁被还原、我们塞进去的那两个值当且仅当还是我们塞的值时被删掉、
  每个图标请宿主重渲染一次（`_invalidateIcon`），文件监视器与所有信号一并断开 —— 屏幕上的图标跟着
  退回程序自带的那张，而不是留着我们的最后一张图。


## 8. 权限与 AppArmor

| 厚度 | 需要特权吗 |
|---|---|
| T1（含 L5 规则） | ❌ 零特权，只写用户目录 |
| T2 / T3 | ⚠️ 需要能建非特权 `userns` |
| T2 / T3 **且程序自带嵌套沙箱**（Electron / Chromium） | ⚠️ **安装时**要一次 `sudo`，给私有副本装一份 AppArmor profile；**运行期仍然零特权** |

细节、三个实测事实与五条路线：[`apparmor-userns.md`](apparmor-userns.md)。

`twin` 只在 `twin install` / `twin reinstall` 里写 `/etc/apparmor.d/twin-bwrap`，只经 `sudo`、只跑那两条
命令（`install` ＋ `apparmor_parser -r`）；问不到密码（没有 `sudo`、标准输入不是终端）或者那一次没成
时，它一个字都不往里写，改为退回"私有副本 ＋ 跳板"，两条都不行才打印 profile 全文并以退出码 3 结束
—— 写 `/etc` 从来不是 `wrap` 的副作用，它是一次显式的根动作，而使用者离同样的结果只差一次复制粘贴。
运行期全用户态，没有 root 守护进程。

## 9. 边界：包裹层永远做不到的事

- ❌ 改不了**已经存在**的窗口的归属。桌面项是后写的，GNOME 不会重算 ⇒ **必须重启实例**。
- ❌ 隔离不了网络。`isolation.unshare` 里出现 `net` 会被直接拒绝：它切断网络，也会打破基于
  abstract socket 的单实例锁，而 bwrap 本来也隔离不了网络命名空间之外的东西。
- ❌ 拦不住程序自己写死的绝对路径（如果它不读 `app_id`，那是 `redirect` ＋ 扩展的事）。
- ❌ 给不了安全边界。这是**身份**工具，不是沙箱工具 —— 它构建的 bwrap 基座是为了"改名＋隔离
  数据目录"，不是为了让不受信任的代码安全运行。

## 10. 复杂度住在哪 —— 以及为什么没有 GUI

复杂度**不在**界面里，而在于"这台机器能不能建 ns""哪个程序需要哪种级别""托盘表怎么写进去"。
这些是**判定**，不是**输入**。

- GUI 会让"看一眼默认值从哪来"更难，而不是更容易 —— 而可审计的默认值正是本工具的核心价值。
- GUI 会掩盖退出码 3 这个关键信号（"还差一步必须人做"）。
- 唯一 GUI 真正占优的场景是"可视化地调图标角标"，而那只需要一个 `--dry-run` ＋ 图片查看器。

结论：不做 GUI。**如果哪天要做，唯一值得做的是角标预览。**

## 11. 实测记录（本文的事实来源）

| 事实 | 取证方式 |
|---|---|
| 窗口 `app_id` 取自 `/proc/self/exe` basename | `WAYLAND_DEBUG=1` 抓 `xdg_toplevel.set_app_id` |
| 托盘 `SNI Id` 取自 `argv[0]` basename | 读 `ubuntu-appindicators` / `custom-icons` 的匹配逻辑 |
| overlay 改名零拷贝且在宿主不可见 | `sandbox.ProbeOverlay`（探针还会检查别名**没有**泄漏到宿主） |
| `Exec=` 无执行位 ⇒ GLib 丢弃整个桌面项 | `doctor` 里一对"好/坏"探针桌面项交叉验证 |
| 系统 bwrap 里嵌套 `unshare` = EPERM | 真机探针（`twin` 的三次探查） |
| 私有副本无标签 = EPERM；VS Code 当跳板 ⇒ 标签被换成 unconfined ＋ 嵌套 userns OK | 真机探针 |
| `gsettings set` 会假成功（`exit 0` 但没写） | 对比 `set` 的退出码与随后的 `get` |
| `custom-icons` 热重载 | 写入后托盘图标**当场**变化，不需重登 |
| `custom-icons` 那条路不听 `icon-size`（`_setGicon` ⇒ 主题框），pixmap 那条听（`_setImageContent` ⇒ 显式框） | 从 `/usr/lib/gnome-shell/libshell-18.so` 抽出宿主 JS ＋ 读 `st-icon.c` 源码，再用托盘截图逐像素分类 |
| `PANEL_ICON_SIZE` 在 gnome-shell ≥ 49 已整条删除（45–48 有但未 export）⇒ `DEFAULT_ICON_SIZE` 恒 16 | `gresource extract /usr/lib/gnome-shell/libshell-18.so /org/gnome/shell/ui/panel.js` 后 grep |
| 用户目录下与 mode extension 同 uuid 的副本被**静默拒绝**（且不登记 uuid ⇒ 系统版照常加载） | 读 `extensionSystem.js` 的 mode 过滤那一段，并实测一份 fork 副本装上去不生效 |
| `IconActor._updateIconByType()` 是设置图标的**唯一**入口，且同步读 `this._customIcons`（`SNIconType → 绝对路径字符串`） | 读 `appIndicator.js` 的 `_updateIconByType` / `_updateCustomIcons` / `_setGicon`（上游 `gnome-shell-extension-appindicator`），§7.1 的注入点由此确定 |
| 状态区 role 后缀**不是** SNI `Id`（是 `Util.indicatorId()` 拼的 `busName@objectPath`），匹配只能靠 `_indicator.id` | 读 `indicatorStatusIcon.js` 的 `addToStatusArea('appindicator-' + uniqueId)` 与 `util.js` 的 `indicatorId()` |
| 一个 uuid 可以同时躺在 `enabled-extensions` 与 `disabled-extensions` 里；后者跨会话保留，只有 `enableExtension()`（`gnome-extensions enable`）会把它摘掉，而且**只在这个 uuid 已被 shell 扫到**时才摘得掉 | `gresource extract /usr/lib/gnome-shell/libshell-18.so /org/gnome/shell/ui/extensionSystem.js`，读 `enableExtension` / `disableExtension` 两个函数：前者 `filter` 掉禁用列表并检查 `_extensions.has(uuid)`，后者 `push` 进去 |
