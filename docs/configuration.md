# 配置参考

`twin` 有**三层**用户可写的配置，加一层编译进二进制的内置默认。分层不是装饰 ——
三层**因为不同的理由、以不同的频率**变化，混起来会产生静默的连锁变更。

| 层 | 文件 | 它描述什么 | 什么时候改 |
|---|---|---|---|
| 环境 | `~/.config/twin/config.toml` | **这台机器** | 换机器、换桌面会话 |
| 应用知识 | `~/.config/twin/rules.d/<app>.toml` | **某个程序** | 你了解了一个新程序的习惯 |
| 实例快照 | `~/.local/share/twin/<id>/profile.yml` | **这一个实例** | 你想改这一个实例 |
| 内置 | （编译进二进制） | 出厂默认 | 永不 |

---

## 1. 优先级

```
CLI 参数
  > profile.yml     （实例快照）
    > rules.d/<app>.toml （应用知识）
      > config.toml      （环境）
        > 内置默认
```

**数组类字段**（`bwrap.extra`、`tmpfs`、`bind`）按**低位在前拼接**，同目标由后写的遮蔽：

```
最终 bwrap.extra = config.extra ++ rules.extra ++ CLI(-- 之后的 token)
```

同目标挂载的冲突规则**不需要 twin 实现** —— bubblewrap 自己就是"后挂载遮蔽前者"。

### `wrap` 与 `regen` 看到的层数不同

| 动作 | 参与的层 |
|---|---|
| `wrap`（新建） | CLI > rules.d > config.toml > 内置 |
| `regen`（重放） | CLI > profile.yml > 内置（**不读** config / rules） |
| `regen --refresh` | 上面 ＋ config.toml / rules.d（**只补空字段、只追加缺的 token**） |

这是刻意的：**`regen` 必须是确定性的**。如果它默认依赖 `config.toml`，那么改动一个全局默认
就会"顺手"改掉一排已有实例 —— 那正是最不该发生的静默变更。

`--refresh` 只做**加法与填空**，绝不改写快照里已有的值：

| 字段 | `--refresh` 的行为 |
|---|---|
| `keywords` / `categories` | 快照里**为空**时才补（rules.d → 源桌面项） |
| `bwrap.extra` | 把 config ＋ rules 的 token **追加**进来；已存在的 token 跳过（幂等） |
| 其余（id / name / level / tmpfs / binds） | **不动** —— 它们一定已被写满，"空"与"用户要空"分不开 |

⇒ 一句话：`regen` 让**派生文件与快照一致**，而不是让快照跟随环境。

---

## 2. `config.toml`（环境层）

```bash
twin config > ~/.config/twin/config.toml
```

### `[defaults]`

| 键 | 默认 | 说明 |
|---|---|---|
| `level` | `"alias"` | 默认身份级别。只能是 `none` / `alias` / `redirect` |
| `icon_size` | `256` | 生成图标的边长（像素）。必须是正数 |
| `badge_style` | `"corner"` | `corner` \| `number` \| `dot` |
| `tmpfs_unless_x11` | `true` | 程序支持 Wayland 时是否自动加 `--tmpfs /tmp`。**在你确信二进制特征扫描判错的机器上关掉它** |
| `tray_override` | `true` | 生成实例时是否把宿主的托盘图标覆盖表指向本实例 |

> `tray_override` 是一个**整机**开关，理由很实在：值得关掉它的原因是机器的属性（换一套托盘
> 宿主、或这个会话另有托盘方案），不是某一个程序的属性。

### `[tools]`

| 键 | 默认 | 说明 |
|---|---|---|
| `bwrap` | `""` | 留空 ⇒ 用 PATH 里的 bwrap |
| `bwrap_copy` | `"auto"` | 私有 bwrap 副本：`""` 不做副本；`"auto"` 需要时复制到 `~/.local/share/twin/bin/bwrap`；或一个显式路径 |
| `python` | `"python3"` | 仅用于 SVG 光栅化与 GLib 桌面项自检 |

### `[userns]`

| 键 | 默认 | 说明 |
|---|---|---|
| `trampoline` | `"auto"` | `"auto"` 自动探测最佳跳板；`"off"` 不用跳板（受限机器上会打印 profile 并退出 3）；或一个显式路径（**必须是 Electron 程序**） |

### `[icon]`

| 键 | 默认 | 说明 |
|---|---|---|
| `theme` | `"hicolor"` | 源 `Icon=` 是主题名时的查找主题 |

### `[bwrap]`

| 键 | 默认 | 说明 |
|---|---|---|
| `extra` | `[]` | 追加到**所有**实例 bwrap 选项区尾部。**一个元素 = 一个 token**，原样拼接 |

⚠️ 一个文件写坏了会**硬报错**而不是静默回退到内置默认 —— 否则你改了 `config.toml`、什么都没
发生，而没有任何线索能告诉你为什么。

### 完整模板

```toml
[defaults]
level       = "alias"
icon_size   = 256
badge_style = "corner"
tmpfs_unless_x11 = true
tray_override = true

[tools]
bwrap  = ""
bwrap_copy = "auto"
python = "python3"

[userns]
trampoline = "auto"

[icon]
theme = "hicolor"

[bwrap]
extra = []
```

---

## 3. `rules.d/<app>.toml`（应用知识层）

一个**程序**的固有知识，与"你开了几个实例"无关。`{id}` 在实例化时替换成实例 id，
所以一条规则服务任意多个实例。

**文件名匹配**：文件的基名（去掉 `.toml`）可以是**源桌面项基名**（`wechat`）或**真实程序基名**。
两者都试，先命中的赢。

```toml
app = "wechat"

level    = "alias"
keywords = "wechat;微信;im"
# categories 留空 ⇒ 继承源桌面项

tmpfs = ["/tmp"]

# 写法同 Docker Compose 的卷：源:目标[:ro]。源是数据实际所在，目标是程序看到它的位置
# （bwrap 的 --bind 也是这个方向）。
bind = [
  "~/.local/state/twin/{id}/xwechat:~/.xwechat",
  "~/Documents/xwechat_files",                                # 单写一条路径 ⇒ 原地升回可写，数据不搬家
  "~/.local/state/twin/{id}/all_users:~/Documents/xwechat_files/all_users",
]

[bwrap]
extra = []
```

| 键 | 语义 |
|---|---|
| `app` | 这条规则的名字（信息性；匹配靠文件名） |
| `level` | 该程序的默认身份级别 |
| `keywords` | 搜索关键词。规则里的显式值 **>** 任何推导 |
| `categories` | 留空 ⇒ 继承源桌面项 |
| `tmpfs` | 数组，**替换**内置 |
| `bind` | 字符串数组，**替换**内置。每条是 `源:目标[:ro]`；单写一条路径 ⇒ 该路径挂回自己（原地升级可写） |
| `[bwrap] extra` | 数组，**替换**内置 |

### 合并语义（与内置同名字段）

**逐字段**合并 —— 文件里写了的字段赢，没写的继承内置。所以"只想改 keywords"的文件不必把三条
路径抄一遍。

⚠️ 但**数组类字段是替换而非追加**：文件里一旦出现 `bind` / `tmpfs` / `[bwrap] extra`，
就以文件里的为准。写了 `bind = […]` 的人是在描述他想要的**全部**映射，半继承会让这个文件变成谎言。

⚠️ 单写一条路径（`"~/.foo"`）不是空操作：在 `--ro-bind / /` 之后，它把该目录从只读**升回可写**
（与手写包裹里的 `--bind A A` 同义）。

⚠️ 旧的 `[[bind]]` 表（`canonical` / `private`）会被**指名报错**，不会被当成"没有映射"读掉：
把它读成"数据共用"会让一个说明了数据在哪的文件变成一句谎话，而屏幕上什么都看不出来。

### 内置规则

`twin rules` 列出全部内置规则。内置表刻意很短 —— **静默猜错一个路径，比开口要用户
自己写一条 `bind` 更糟**。目前内置的只有已实测过的那个用例：

| 规则 | 内容 |
|---|---|
| `wechat` | `~/.xwechat` → 私有；`~/Documents/xwechat_files` → **共享**（原地升级可写）；`.../all_users` → 私有 |

> 为什么收件目录共享、`all_users` 私有：微信把账号数据放在 `~/Documents/xwechat_files/<wxid>/`，
> 把**整机**级的提供商状态放在 `.../all_users`。两份实例共享前者（收到的文件两边都能看）没有
> 问题，真正会撞车的是后者。

内置表短，意味着**多数程序没有规则**，而"要隔离哪些目录"正是最容易猜错、又最不容易发现的一条
知识（隔离错一个目录，到两个实例抢同一个锁文件时才暴露）。所以 `twin new` 每次都会打印实际生效
的映射、它的来源、该编辑哪个文件，并附一份照抄即可的模板（见 [`cli.md`](cli.md) 的 `twin new`）。

不想凭空猜路径，可以让 twin 先探测一遍：`twin rules --init <源桌面项|程序名>` 会写出这份文件的
**草稿** —— 目录真的存在的那几条写成生效的 `bind` 条目，二进制里扫出来的字面量则全部注释掉
（`cli.md` 里有一条 `twin rules --init` 的逐项说明）。

---

## 4. `profile.yml`（实例快照）

一个实例的**全部意图**，也是唯一的真源文件。手改它然后 `twin regen <id>`。

文件是 YAML：下面每个小节是一个键，表里的键名要拼成点号路径 —— `identity.level`
指的就是 `identity:` 缩进块里的 `level:`。缩进只能用空格。

**每个键上面都写着它自己的说明**，`twin new` 生成的快照就是这样：阶段的解释留在文件里，
人读到它的时候正好在编辑它。下面这些表是同一份内容的查阅版。

### 顶层

| 键 | 说明 |
|---|---|
| `id` | 实例 id（= 桌面项基名 = 别名文件名） |
| `name` / `name_en` | 显示名 / 英文名 |
| `comment` / `comment_en` | 说明 |
| `keywords` | 搜索关键词（`;` 分隔） |
| `categories` | 分类（`;` 分隔） |
| `source` | 源桌面项路径（信息性，`regen` 会重读它取 Categories/Keywords） |

### `identity`

| 键 | 说明 |
|---|---|
| `level` | `none` \| `alias` \| `redirect` |
| `wm_class` | 写进桌面项的 `StartupWMClass`。默认 = `id`，且**只在 level 会改名时写**（X11/XWayland 才真正需要它，Wayland 下认领窗口的是基名） |
| `notify` | 写进桌面项的 `StartupNotify=`。默认 `true`。它总是被写出来（"要不要"本身是一个决定） |

### `icon`

| 键 | 说明 |
|---|---|
| `source` | 源图标路径 |
| `badge` | 角标文字 |
| `style` | `corner` \| `number` \| `dot` |

### `exec`

| 键 | 说明 |
|---|---|
| `target` | 真实程序的**绝对路径** |
| `alias` | 别名文件名（只在命名空间内存在）。窗口 `app_id` ＝ 它，托盘 `SNI Id` 也 ＝ 它 |
| `argv0` | **保留字段**，默认留空（空 ⇒ `argv[0]` 自动取别名）。填了就必须等于 `alias` |
| `env` | 额外注入沙箱的环境变量（`--setenv`） |

### `isolation`

| 键 | 说明 |
|---|---|
| `tmpfs` | 在沙箱内被盖成空目录的路径列表 |
| `unshare` | 额外 unshare 的命名空间。**含 `net` 会被拒绝** |
| `nested_ns` | 这个程序要不要自己建嵌套 user namespace（Chromium/Electron 类）。**总是写**，见下 |

### `bwrap`

| 键 | 说明 |
|---|---|
| `extra` | 合并后的最终透传 token 列表（config ＋ rules ＋ CLI） |

### `tray`

| 键 | 说明 |
|---|---|
| `override` | 要不要把宿主托盘覆盖表指向本实例。**总是写** —— "不覆盖"本身也是一个决定，缺这段会让它和"这个字段存在之前写的老文件"无法区分（缺段老文件按 `true`） |

### `bind`

数组，每条是 `源:目标[:ro]`。**总是写**（空时是 `bind: []`）—— "一条映射都没有"本身是一个决定。

| 写法 | 说明 |
|---|---|
| `源:目标` | 私有副本：程序在 `目标` 看到的其实是 `源`，两个实例互不干扰 |
| `源:目标:ro` | 只读挂载：程序看得见但改不动 |
| 单写一条路径 | 该路径挂回自己，只把只读升回可写（数据不搬家） |

---

## 5. 什么进快照，什么每次重算

这是整个配置设计里唯一需要判断力的一条：

| 类别 | 例子 | 进快照？ | 理由 |
|---|---|---|---|
| **意图** | id、名字、level、bind 列表、tmpfs 列表 | ✅ | 用户对这个实例明确表达过的东西 |
| **推导结果** | 由 id 推出的 badge、由源 Name 推出的中文名 | ✅ | 落成全量值，文件自解释，不留 `null` |
| **环境** | bwrap 路径、python 路径、图标尺寸（由本机 DPI 决定） | ❌ | 换机器/换会话就变，写进快照必然过期 |
| **源的现状** | `Categories` / `Keywords` / `Icon=`（实例里留空表示"继承"时） | ❌ | 源桌面项可能被系统更新覆盖 ⇒ `regen` 时重读 |
| **应用知识** | 该程序是否需要嵌套 ns | ✅ | 它跟着程序走，不跟着机器走 |
| **机器状态** | 用哪个跳板、副本放哪 | ❌ | 留在 `config.toml`，让下一台机器自己决定 |
| **会话类型** | Wayland / X11 | ⚠️ 部分 | 已固化成 `isolation.tmpfs`；`regen` 时会**警告**它与当前会话不符 |

⇒ 一句话：**"这台机器现在什么样"每次重算；"我要什么"写死在文件里。**

---

## 6. 实操

```bash
# 生成一份带注释的 config.toml
twin config > ~/.config/twin/config.toml

# 看某个程序最终生效的规则（含它来自内置还是你的文件）
twin rules wechat

# 看内置规则清单
twin rules

# 给一个新程序写规则：让 twin 先探测一遍，或者自己动手
twin rules --init myapp
$EDITOR ~/.config/twin/rules.d/myapp.toml

# 改一个实例
$EDITOR ~/.local/share/twin/wechat2/profile.yml
twin regen wechat2
```

### `profile.yml` 是给人读的

它的 header 就写着这一点：

```yaml
# twin —— 真源文件（YAML）。改完跑 `twin regen wechat2` 重新落盘。
# 这个文件是这个实例的唯一定义：`twin new --publish` 与 `twin regen` 只重放它，
# 不再看命令行。每个键的说明就写在它自己上面。
# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`badge` 那种纯数字尤其要）。

# 实例 id：桌面项基名、别名文件名、这个目录名，三者都是它。
id: wechat2
# 显示名（窗口标题与菜单里看到的）。name_en / comment_en 是可选英文版。
name: 微信2
# 搜索关键词，`;` 分隔。留空则沿用源桌面项。
keywords: 微信;wechat;
…
exec:
  # 沙箱内建在 target 同目录下的别名文件名。窗口 app_id 取自真实路径名，
  # 托盘 SNI Id 取自 argv[0]，两者都等于它 —— 让它们分叉，就有一个找不到桌面项。
  alias: wechat2
…
# 隔离哪个数据目录。整段替换内置规则（不是追加）。
# 写法同 Docker Compose 的卷：源:目标[:ro]，源是数据实际所在，目标是程序看到它的位置。
#   源路径:目标路径        ⇒ 私有副本，两个实例互不干扰
#   只写一条路径           ⇒ 该路径挂回自己，只把只读升回可写（数据不搬家）
#   源路径:目标路径:ro     ⇒ 只读挂载，程序看得见但改不动
# 空着 = 数据目录与源程序共用；程序若有单实例锁（Electron / Qt / GTK 系常见），
# 两个实例会互相干扰。这里只影响这一个实例；要让这个程序以后新建的实例也用同样
# 的映射，写 ~/.config/twin/rules.d/<程序>.toml（twin rules --init 生成草稿）。
bind:
  - ~/.local/state/twin/wechat2/xwechat:~/.xwechat
```
