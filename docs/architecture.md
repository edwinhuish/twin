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
| **T2** | T1 ＋ bwrap 只读基座（含 overlay 别名） | 能建非特权 user namespace |
| **T3** | T2 ＋ 路径映射（`rules.d` / 实例快照）＋ tmpfs | 同上 |

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
| `/tmp` 是否 tmpfs | 程序支持 Wayland ⇒ 加 `--tmpfs /tmp`；只支持 X11 ⇒ **不加**（会砍掉 `/tmp/.X11-unix/X0`） |

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

`twin install` 写下面这些。第 0 件（`./profile.yml`）不是它写的 —— `twin gen` 写在**你当前所在的
目录**里，因为那是这份 wrap 里唯一需要人读一遍的东西，而"当前目录"是用户已经选好的那个位置。

| # | 路径 | 内容 | 何时写 |
|---|---|---|---|
| 1 | `~/.local/share/applications/<id>.desktop` | 身份锚点 | 总是 |
| 2 | `~/.local/share/appicons/<id>.png` | 合成好的图标（256×256 默认） | 总是 |
| 3 | `~/.local/share/twin/<id>/launch` | 薄启动器（bash，`exec` bwrap） | 总是 |
| 4 | `~/.local/share/twin/<id>/profile.yml` | 实例定义（**存档快照**） | 总是 |
| 5 | `~/.config/twin/rules.d/<id>.json` | L5 规则（扩展读） | 仅 `level=redirect` |
| 6 | `~/.local/state/twin/<id>/` | 私有状态目录（空目录） | 总是 |
| 7 | `~/.local/share/twin/bin/bwrap` | 私有 bwrap 副本 | 仅当程序需要嵌套 ns |
| 8 | `~/.local/share/twin/bin/bootstrap.js` | 跳板脚本 | 同上 |

### 5.1 实例定义

同一份 schema 落在两处，头注释各自指向读它的那条命令：

```yaml
# …profile.yml 的头 —— twin —— 实例定义（工作副本）。改完跑 `twin install` 按它落盘。

# twin —— 实例定义（存档快照）。改完跑 `twin reinstall wechat2` 重放；
# `twin install` 会用工作副本那份重新写出它。
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
  env: {}

isolation:
  tmpfs:
    - /tmp
  unshare: []
  nested_ns: false

bwrap:
  extra: []

tray:
  override: true

bind:
  - ~/.local/state/twin/wechat2/xwechat:~/.xwechat
```

两个文件都是**给人读、给人改的**：工作副本改完 `twin install`，存档快照改完 `twin reinstall <id>`。
真实文件里**每个键上面都有一行说明**（`twin gen` / `twin install` 不在终端重复它们），上面这份骨架
把注释省掉了。

注意几处刻意的设计：

- **状态目录用 `~/` 而非绝对路径**（`RelStateDir`）：文件要能搬到另一台机器重放，不能把今天的
  HOME 烤进去。
- **`tray.override` 总是写**（即使值是 `true`）："不覆盖托盘"本身也是一个决定，缺这段会让它
  和"这个字段存在之前写的老文件"无法区分。缺段的老文件按 `true` 读。
- **`nested_ns` 总是写**：它是**应用知识**（这个程序要不要自己建 ns），跟快照走；而"用哪个跳板、
  副本放哪"是**机器状态**，留在 `config.toml` 里，让下一台机器自己决定。

### 5.2 `launch`（薄启动器）

```bash
#!/bin/bash
set -euo pipefail
: "${HOME:?twin: HOME 未设置，拒绝启动}"

readonly ID='wechat2'
readonly STATE="$HOME/.local/state/twin/wechat2"
```

三个刻意的选择：

- **`exec` 而非 `wait`**：`exec` 之后进程表里不留下包装器的痕迹 —— 这正是"薄"的意义。
- **纯 shell 而非编译启动器**：够用，且生成物可读、可审。
- **`: "${HOME:?...}"` 显式失败**：手写版曾用 `HOME=${HOME:-/root}`，在无 HOME 的上下文里会
  往 `/root` 建目录 —— 那是**数据丢失**级别的默认值。`twin` 在这一点上选择硬失败。
- **路径折叠成 `$HOME/...`**：启动器跑在生成之后很久，把生成时刻的 HOME 烤进去会让它在任何
  其他 HOME 下静默错误（也会让上面那行守卫变成装饰）。

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

```
bwrap
  --ro-bind / /                     # 整个文件系统只读
  --dev /dev
  --dev-bind-try /dev/dri /dev/dri  # 显卡（没有就跳过）
  --proc /proc
  --bind /run/user /run/user        # ⚠️ 必须在 --ro-bind / / 之后
  [--tmpfs /tmp]                    # 由 /tmp 策略决定
  --overlay-src <targetdir>
  --tmp-overlay <targetdir>         # ⚠️ 必须在创建别名之前
  --ro-bind <target> <alias path>
  [--bind <源> <目标>]…             # 路径映射（源是数据实际所在）
  --setenv TWIN_INSTANCE <id>
  --die-with-parent
  [用户透传的 token…]                # 放最后，同目标挂载由它胜出
  --
  <alias path> "$@"
```

两条顺序上的硬约束，都是实测出来的：

1. `--tmp-overlay DIR` **必须**排在创建别名的 `--ro-bind … DIR/别名` 之前，否则挂载点建不出来
   （`Can't create file: Read-only file system`）。
2. `--bind /run/user /run/user` **必须**排在 `--ro-bind / /` 之后，否则 Wayland socket、
   会话总线、PipeWire 全都不可达。

另外，透传区放在**基座之后**是刻意的：bwrap 允许后写的挂载遮蔽同目标的前一个挂载，于是用户
的 token 天然有最终话语权，而 `twin` 不需要拥有任何一条冲突规则。

## 6. bwrap 参数继承机制

原则：**透传 token，不重新解析**。`twin` 不知道也不想知道任何 bwrap 选项的元数（arity）——
那是 bwrap 的事，在这里再实现一张表只会随每个 bwrap 版本腐烂。

- 分隔符是 `--`：它**之后**的 token 原样进 `bwrap.extra`，拼在基座之后、程序之前。
- 透传区**不享受**三件事（在别处都有的）：`mkdir` 自动补齐、`~/` 展开、语义校验。
  ⇒ 数据路径请写进 `rules.d` 或实例快照的 `bind`，不要写进透传区。
- 三条硬边界（拒绝，不是警告）：
  - `--`（会截断 bwrap 自己的选项解析，后面全被当成程序名）；
  - `--argv0`（会破坏"窗口 app_id 与托盘 Id 同名"的不变量）；
  - 需要文件描述符的选项（`--file` / `--bind-fd` / `--seccomp` / `--userns-block-fd` …）——
    启动器由桌面环境 exec，手里没有可用的 fd。
- 校验是 **lint 而不是 transform**：宁可警告，不悄悄改写用户写的东西。

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

角标不贴死画布边缘（越界会被裁），且在 16px 面板下只有 5–6px —— 所以几何符号优先，数字只是
一种风格。

**图标必须写两处**，两条链路互相独立：

| 落点 | 谁读 | 漏了会怎样 |
|---|---|---|
| `<id>.desktop` 的 `Icon=` | 启动器 / 面板 / 任务栏 / `Alt+Tab` / 概览 | 齿轮占位图 |
| GSettings `custom-icons`（`('<id>', '<png>', '<png>')`） | 托盘宿主 `ubuntu-appindicators` | 托盘仍是原程序的图标 |

托盘那条链路的完整优先级是：

```
custom-icons（按 SNI Id 严格匹配；值以 / 开头 ⇒ 当文件路径加载）
  → SNI IconName（配 IconThemePath 查主题）
    → SNI IconPixmap（应用内嵌位图，外部改不动）
```

只有**第一级**是外部可控的，而它**优先级最高**：命中后应用的 `IconName` / `IconPixmap`
被完全忽略。它还是**热重载**的（监听 `changed::custom-icons`）⇒ 当场写、当场生效，不用重登。

⚠️ 前提是 `Id` 必须唯一 —— 否则两个实例会同时命中同一条规则。这正好由身份层保证
（overlay 别名 = `argv[0]`）。

⚠️ 这是**全局单键**：写入必须 read-modify-write 整张表，绝不覆盖别人的条目。

## 8. 权限与 AppArmor

| 厚度 | 需要特权吗 |
|---|---|
| T1（含 L5 规则） | ❌ 零特权，只写用户目录 |
| T2 / T3 | ⚠️ 需要能建非特权 `userns` |
| T2 / T3 **且程序自带嵌套沙箱**（Electron / Chromium） | ❌ **仍然零特权** —— 走"私有副本 ＋ 跳板" |

细节、三个实测事实与三条退化路线：[`apparmor-userns.md`](apparmor-userns.md)。

`twin` **从不写 `/etc/`**。走投无路时它打印一份 profile 文本并以退出码 3 结束 ——
写 `/etc` 是一个 `wrap` 不该有的副作用，而使用者离同样的结果只差一次复制粘贴。

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
