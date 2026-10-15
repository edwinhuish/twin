# 配置参考

`twin` 有**三层**用户可写的配置，加一层编译进二进制的内置默认。分层不是装饰 ——
三层**因为不同的理由、以不同的频率**变化，混起来会产生静默的连锁变更。

| 层 | 文件 | 它描述什么 | 什么时候改 |
|---|---|---|---|
| 环境 | `~/.config/twin/config.toml` | **这台机器** | 换机器、换桌面会话 |
| 应用知识 | `~/.config/twin/rules.d/<app>.toml` | **某个程序** | 你了解了一个新程序的习惯 |
| 实例 | `~/.local/share/twin/<id>/profile.yml`（`gen` 写；`install <id>` / `reinstall <id>` 读） | **这一个实例** | 你想改这一个实例 |
| 存档 | 同一个路径，但只在定义放在别处时才有：`install -p <文件>` 写下的记录（`reinstall` 读） | 同一个实例的安装记录 | 想按存档重来一次时 |
| 内置 | （编译进二进制） | 出厂默认 | 永不 |

前两层是"知识"，后两层是"意图"。**意图只有一份文件**：`twin gen` 写的 `profile.yml` 就住在实例
目录里，`twin install <id>` 与 `twin reinstall <id>` 读的都是它。头注释里那句"改完敲哪个命令"指的
就是它。唯一会出现第二份的时候，是定义被你放到别处（`-o` / `-p`）—— 那时实例目录里那份才是
`install` 写下的**存档记录**，`reinstall` 重放的是它。

---

## 1. 优先级

```
profile.yml      （你正在编辑的那一份：gen 写，install / reinstall 读）
  > rules.d/<app>.toml （应用知识）
    > config.toml      （环境）
      > 内置默认
```

**数组类字段**（`bwrap.extras`、`tmpfs`、`bind`）按**低位在前拼接**，同目标由后写的遮蔽：

```
最终 bwrap.extras = gen 写的基座里「没有自己节点」的那些 ++ config.args ++ rules.args ++ gen 命令行 `--` 那一段
```

`bwrap.extras` 是**交给 bwrap 的完整选项列表**（基座也在里面，见 §4），所以"这个程序拿到哪个根"
第一次成了这份文件能回答的问题 —— 原先基座硬编码在 twin 里，想换根只有透传区一条路，而透传区
排在所有映射之后，重新绑一次根会把前面的挂载全盖掉。

⚠️ 基座里有**自己节点**的那几条不在这份列表里，各写在各键上：根挂载在 `bwrap.bind`（`Build` 把它
发在所有东西**之前**），`bwrap.proc` / `bwrap.tmpfs` / `bwrap.env` 由 `Build` 发在这份列表
**之后**。所以想改它们就改键，别在 `--` 里写一条同目标的挂载指望压住它们。

同目标挂载的冲突规则**不需要 twin 实现** —— bubblewrap 自己就是"后挂载遮蔽前者"。

注意顶层那格不再是"命令行参数"：`install` 不接受任何描述实例的选项，所以没有第二份意图来源。
`gen` 的推导选项换来的是**一份文件**，之后只有文件说话。

### `install` 与 `reinstall` 看到的层数不同

| 动作 | 参与的层 |
|---|---|
| `gen`（推导） | CLI > rules.d > config.toml > 内置 |
| `install`（按定义装） | profile.yml > rules.d > config.toml > 内置 |
| `reinstall`（重放同一份定义） | profile.yml > 内置（**不读** config / rules） |
| `reinstall --refresh` | 上面 ＋ config.toml / rules.d（**只补空字段、只追加缺的 token**） |

这是刻意的：**`reinstall` 必须是确定性的**。如果它默认依赖 `config.toml`，那么改动一个全局默认
就会"顺手"改掉一排已有实例 —— 那正是最不该发生的静默变更。（`install` 读环境是另一回事：它读的
是**你刚刚交出来的那份配置**，没有第二份意图能和它冲突。）

`--refresh` 只做**加法与填空**，绝不改写那份定义里已有的值：**为空**的
`desktop.keywords` / `desktop.categories` 才补（rules.d → 源桌面项），`bwrap.extras` 把 config ＋
rules 的 token 追加进来（已存在的跳过，所以是幂等的），其余字段（id / name / level / tmpfs / binds）
一律不动 —— 它们一定已被写满，"空"与"用户就是要空"分不开。基座也不动：它属于那份定义本身。
逐字段的行为写在 `twin reinstall -h` 里。

补上的值**只用在这一次落盘上**：那份定义是你的文件，`install` / `reinstall` 都不改写它（改写会丢掉
手写的注释），所以想让它们留下来，就照 `--refresh` 打印的那几行自己改进文件里。命令的输出里也这么
写着 —— 否则"补了又没了"就是一次没人看得见的静默回退。

⇒ 一句话：`reinstall` 让**派生文件与那份定义一致**，而不是让定义跟随环境。

### 路径怎么写：绝对 / `~` / 相对

这些字段是路径：`bind` 的两侧、`tmpfs`（rules.d 与实例定义都有）、`profile.yml` 里再加
`exec.target`、`icon.source`、`bwrap.proc`、`bwrap.remountRO`。它们接受三种写法：

| 写法 | 基准 | 什么时候用 |
|---|---|---|
| `/…` | 没有基准 —— 它就是它 | 系统路径（`/opt/…`、`/usr/share/…`） |
| `~/…` | **用它的那台机器的 `$HOME`** | 默认写法。文件里存的是意图，折叠成绝对路径发生在**用的时候** |
| `state/x`、`./x` | **这份 `profile.yml` 自己所在的目录** | 配置、程序、状态目录放成一个能整体搬走的 bundle |

相对路径在 `inst.Load` 里解析，**一次，而且只解析成绝对路径**：之后读它的每一处（bwrap 参数、建挂载点、
图标、`verify`）看到的是同一个答案，谁也编不出第二个基准 —— 解析发生在读者那一侧，就等于按"你当时站
在哪个目录"解析。**那份文件不会被改写回解析后的样子**：盘上留着你写的相对路径，所以 bundle 可以整体
搬走、可以提交进仓库。

两个例外，都值得记：

- **`bwrap.extras` 不解析**：那是 bwrap 的选项列表，逐 token 原样交给它，twin 不认、也猜不出哪个
  token 是路径（[`architecture.md`](architecture.md) §2.1）。在那里写相对路径，等于交给 bwrap 按
  **启动器的工作目录**解析 —— 那个目录没人选过。
- **`rules.d` 里不接受相对路径**（指名报错，退出码 2）：规则是给这个程序**所有实例**用的共享知识，
  它的相对路径该相对规则目录、还是相对每个实例，没有答案。那里写 `~/` 或绝对路径。

命令行上给的路径照旧按**当前目录**算（`--icon ./x.png`、`-o bundle/wrap.yml`）：那是你敲命令时自己
选的位置，和"文件里的相对路径"不是一回事。

`install -p <别处>` 落地之后，实例目录里那份**存档记录**里是解析后的绝对路径：记录必须自洽
（`reinstall <id>` 只读它，而你可以在任何目录敲它），而且它本来就是"这次到底装了什么"，不是你要
搬走的那份意图。

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
| `tmpfs_unless_x11` | `true` | 程序支持 Wayland 时，新实例的 `bwrap.tmpfs` 里写 `/tmp`（空的私有 `/tmp`）；关掉则改成在 `bwrap.extras` 里写 `--bind /tmp /tmp`（宿主那个，可写，只是与宿主共享）。**在你确信二进制特征扫描判错的机器上关掉它**。两条都必须**可写** —— 只读的 `/tmp` 会让程序在别处报错 |
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
| `trampoline` | `"auto"` | `"auto"`（受限机器上先让 `twin install` 自己装那份 AppArmor profile，装不了才退回自动挑一个跳板）；`"off"` 不用跳板（装不了 profile 就是打印全文 ＋ 退出码 3）；或一个显式路径（**必须是 Electron 程序**，优先于自己装） |

### `[icon]`

| 键 | 默认 | 说明 |
|---|---|---|
| `theme` | `"hicolor"` | 源 `Icon=` 是主题名时的查找主题 |

### `[bwrap]`

| 键 | 默认 | 说明 |
|---|---|---|
| `args` | `[]` | 追加到每个**新**实例 `bwrap.extras` 的尾部（排在它那份基座之后、路径映射之前）。**一个元素 = 一个 token**，原样拼接、不做替换。写进实例定义之后就不再回读这里 —— 改这份文件只影响以后新建的实例 |

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
args = []
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

# 盖成空目录的路径（bwrap.tmpfs）；写 /tmp 就是那份定义里的私有 /tmp（见 §4）
tmpfs = ["/tmp"]

# 写法同 Docker Compose 的卷：源:目标[:ro|:dev]。源是数据实际所在，目标是程序看到它的位置
# （bwrap 的 --bind 也是这个方向）。
bind = [
  "~/.local/state/twin/{id}/xwechat:~/.xwechat",
  "~/Documents/xwechat_files",                                # 单写一条路径 ⇒ 原地升回可写，数据不搬家
  "~/.local/state/twin/{id}/all_users:~/Documents/xwechat_files/all_users",
]

[bwrap]
args = []
```

| 键 | 语义 |
|---|---|
| `app` | 这条规则的名字（信息性；匹配靠文件名） |
| `level` | 该程序的默认身份级别 |
| `keywords` | 搜索关键词。规则里的显式值 **>** 任何推导 |
| `categories` | 留空 ⇒ 继承源桌面项 |
| `tmpfs` | 数组，**替换**内置；写进实例的 `bwrap.tmpfs`，`/tmp` 那一项就留在那里（基座自己那条 `/dev/shm` 也在同一处） |
| `bind` | 字符串数组，**替换**内置。每条是 `源:目标[:ro\|:dev]`；单写一条路径 ⇒ 该路径挂回自己（原地升级可写） |
| `[bwrap] args` | 数组，**替换**内置；追加到实例 `bwrap.extras` 的尾部（基座之后、路径映射之前） |

### 合并语义（与内置同名字段）

**逐字段**合并 —— 文件里写了的字段赢，没写的继承内置。所以"只想改 keywords"的文件不必把三条
路径抄一遍。

⚠️ 但**数组类字段是替换而非追加**：文件里一旦出现 `bind` / `tmpfs` / `[bwrap] args`，
就以文件里的为准。写了 `bind = […]` 的人是在描述他想要的**全部**映射，半继承会让这个文件变成谎言。

⚠️ 单写一条路径（`"~/.foo"`）＝ 把它原地挂回自己（与手写包裹里的 `--bind A A` 同义）。这**只在
那份定义把根挂成只读时**才改变什么：`twin gen` 写的根挂载是 `/:/:dev`（整份宿主根，可写），而
那一行的意思是"换成 `/:/:ro` 之后，把这一条从只读升回可写"。

⚠️ **两端也可以是一个文件**（单个配置文件，不是目录）：`"~/.local/state/twin/{id}/vlcrc:~/vlcrc"`。
两端必须同形，而 twin 靠**磁盘上已经存在的那一端**判断形态（两端都没在时按目录）—— 所以这条路
在"程序自己那份配置已经存在"时最稳（twin 照它的形态建私有侧）。两端形态冲突时 twin 不让步：
bwrap 会以 `Can't create file at <目录>: Is a directory` 明说。私有侧是**空文件**起步，
与目录型映射的"空目录起步"是同一件事。

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
知识（隔离错一个目录，到两个实例抢同一个锁文件时才暴露）。所以 `twin gen` 每次都会打印实际生效
的映射、它的来源、该编辑哪个文件，`--explain` 还会附一份照抄即可的规则模板。

不想凭空猜路径，可以让 twin 先探测一遍：`twin rules --init <源桌面项|程序名>` 会写出这份文件的
**草稿** —— 目录真的存在的那几条写成生效的 `bind` 条目，二进制里扫出来的字面量则全部注释掉
（`cli.md` 里有一条 `twin rules --init` 的逐项说明）。

---

## 4. 实例定义（`profile.yml`）

**一个实例一份，落在实例自己的目录里。** `twin gen` 写的就是
`~/.local/share/twin/<id>/profile.yml`（要放别处用 `-o`，之后用 `install -p <文件>` 读），而
`twin install <id>` 与 `twin reinstall <id>` 读的是同一份、也都不会改写它 —— 改写就是一次解析往返，
你手写的注释会没（`yamlz.Parse` 丢掉注释，那份文件的注释**只由 `gen` 挂上**）。要更新实例就改它、
再跑一次，或直接重跑 `twin gen` 重新生成。

第二份只在定义放别处时出现：`install -p <文件>` 会把**同一份键**渲染进实例目录（存档记录，
`twin reinstall <id>` 读它）—— 那时实例目录里那份的头注释指向 `reinstall`，你手上那份指向 `install`。

文件是 YAML：下面每个小节是一个键，表里的键名要拼成点号路径 —— `identity.level`
指的就是 `identity:` 缩进块里的 `level:`。缩进只能用空格。

**每个键上面都写着它自己的说明**，`gen` 生成的配置就是这样：阶段的解释留在文件里，
人读到它的时候正好在编辑它。下面这些表是同一份内容的查阅版。

### 顶层

| 键 | 说明 |
|---|---|
| `id` | 实例 id（= 桌面项基名 = 别名文件名） |

### `desktop`

生成出来的那份 `.desktop` 的文案。**留空一律表示"沿用源桌面项"** —— 这一组键回答的是同一个问题，
所以它们在一节里，而不是散在根上。

| 键 | 说明 |
|---|---|
| `name` / `name_en` | 显示名 / 英文名 |
| `comment` / `comment_en` | 说明 |
| `keywords` | 搜索关键词（`;` 分隔） |
| `categories` | 分类（`;` 分隔） |
| `source` | 源桌面项路径（信息性，`install` / `reinstall` 会重读它取 Categories/Keywords） |

> 键名前面那份版本把它们放在根上。那个写法**仍然读得懂**（根上的 `name` 没有第二种解释），
> 所以不用为了升级去改手上已有的实例；下一次 `install` / `reinstall` 会把文件改写成
> `desktop:` 一节。

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

### `isolation`

| 键 | 说明 |
|---|---|
| `unshare` | 额外 unshare 的命名空间。**含 `net` 会被拒绝** |
| `nested_ns` | 这个程序要不要自己建嵌套 user namespace（Chromium/Electron 类）。**总是写**，见下 |

### `bwrap`

这一个节点就是这次 bwrap 调用：`extras` 是原始选项列表，其余每个键都是**一种挂载的写法**
（它对应哪条 bwrap 选项，见右列）。键的顺序就是它们在命令里出现的顺序 —— 唯一的例外是
`extras`，它写在最后，却在命令里排在根挂载之后、其余各条之前（见 §4 末尾）。

| 键 | 对应 | 说明 |
|---|---|---|
| `extras` | （原样） | 交给 bwrap 的**完整选项列表**：`gen` 写的基座里没有自己节点的那几条（宿主 `/tmp`、`--die-with-parent`）→ config ＋ rules ＋ CLI 的追加，低位在前。它是跑起来那一份，不是一份建议 —— twin 不再自己补任何选项。**总是写**，而且写在节点的最后；空列表本身没问题（根挂载在 `bind` 里），两处都没有根挂载才会被拒绝。`--dry-run` 印出来的、启动器里 exec 的就是它 |
| `proc` | `--proc` | 挂一个 proc 到这条路径。`gen` 写的是 `/proc`；留空 = 一个都不挂 |
| `tmpfs` | `--tmpfs` | 盖成空目录的路径（写后即弃，宿主看不见；代价是那里原本的东西也看不见）。`gen` 写的是 `/dev/shm`（空的私有共享内存）与 `/tmp`（程序走 Wayland 时的私有 `/tmp`）；只走 X11 的程序不能要后者，那时 `gen` 改成在 `extras` 里写一条 `--bind /tmp /tmp` 把宿主那个挂回来 |
| `bind` | `--bind` / `--ro-bind` / `--dev-bind` | 隔离哪个数据目录。**总是写**（空时是 `bind: []`）—— "一条映射都没有"本身是一个决定。写法见下。⚠️ 第一条是**根挂载** `/:/:dev`：它是整个宿主根（就是 `--dev-bind / /`），永远发在所有挂载之前，**不算**这个程序的数据映射（终端列「路径映射」时会滤掉它）。宿主 `/tmp` 那条跟着根走，留在 `extras` 里 |
| `remountRO` | `--remount-ro` | 把哪些路径**收回只读**，排在所有挂载之后，所以能压住 `bind` 给出的那种可写。⚠️ 只能作用在**自己就是一条挂载**的路径上（`bind` 的目标端、overlay/tmpfs 那条路径、或整个 `/`）：宿主本来就在那一条大根挂载里的路径底下没有单独的挂载，bwrap 会拒绝启动（`Can't remount readonly on …: Unable to find … in mount table`） |
| `env` | `--setenv` | 注入沙箱的环境变量，一条一个 `键=值`，后面的同名者胜。等号只在**第一个**处断开，所以值里可以再有等号与冒号（`OPTS=--a=b`、`PATH=/a:/b`）；值可以为空（写 `键=`），键名不能带空格。`gen` 写的 `TWIN_INSTANCE=<id>` 就在这一条里 |

为空就是没设置：空值不发出任何参数，也不会被理解成"要默认的那个" —— 默认的那几条是 `gen`
写在这几个键里的，是文件里看得见的一段文本。

基座（`gen` 写的，可改）。**每条挂在它自己那个键里**，只有没有对应键的那几条留在 `extras`
（写在最后，理由见下）：

```
# bwrap.bind / bwrap.proc / bwrap.tmpfs / bwrap.env
/:/:dev               整个宿主根（含设备）挂进来 ⇒ 设备可用、根可写（就是 --dev-bind / /）
--proc /proc
--tmpfs /dev/shm      空的私有共享内存
--tmpfs /tmp          空的私有 /tmp（选了私有那一条时）
--setenv TWIN_INSTANCE <id>

# bwrap.extras：没有自己节点的那几条
--bind /tmp /tmp      宿主那个 /tmp，可写（不要私有 /tmp 时才有这一条）
--die-with-parent
```

根挂载写在 `bind` 的第一条，但位置不由文件决定：`Build` 把两端都是 `/` 的那一条提到**所有挂载
之前**（排在 overlay 别名之后会把程序的真名还回来）。收紧成只读就是把它改成 `/:/:ro`（三段：
源、目标、模式 —— 两段的 `/:/ro` 读成"把根挂到 `/ro`"，会被指名拒掉），代价是设备与 `/run/user`
要自己补回来（`--dev-bind /dev /dev`、`--bind /run/user /run/user`），否则 Wayland socket、会话
总线、PipeWire 全断 —— 这份文件里那段注释就写在那一行旁边。

`extras` 写在节点的**最后**，位置同样不由它自己决定 —— 它是这份定义里唯一一个文件位置不读的键：
`Build` 把它发在根挂载之后、上面那几个有形状的键**之前**（有形状的那几条得能压住它，而基座剩下
的那两条属于基座旁边）。所以它写在末尾不是"最后才加的那几个 token"：它是一份**合并后**的完整
列表（基座剩下的两条 ＋ config.toml ＋ rules.d ＋ `gen` 的 `--` 透传区），名字说的是"上面那些有
形状的挂载之外的那些"。

#### `bwrap.bind`

数组，每条是 `源:目标[:ro|:dev]`。

| 写法 | 说明 |
|---|---|
| `源:目标` | 私有副本：程序在 `目标` 看到的其实是 `源`，两个实例互不干扰 |
| `源:目标:ro` | 只读挂载（`--ro-bind`）：程序看得见但改不动 |
| `源:目标:dev` | 带设备访问（`--dev-bind`，bwrap 的原话是"allowing device access"）：给设备节点用。bwrap 没有只读的设备映射，所以 `:dev` 不能与 `:ro` 同用 |
| 单写一条路径 | 该路径挂回自己，只把只读升回可写（数据不搬家） |

`:dev` 的用处是让"把根收紧成 `/:/:ro`"的定义仍能表达"这条绑定带设备访问"：`gen` 写的根挂载就是
`/:/:dev`（`--dev-bind / /`），整个宿主本就带设备可用（含 `/dev`），那时它是多余的。

⚠️ `rules.d` 里的 `bind` 仍在顶层（见 §3）：那份文件描述的是"这个程序的数据放在哪"这类**应用知识**，
旁边还有 `keywords` / `tmpfs` 这些与 bwrap 无关的键，它服务多个实例，不是一次调用。写法两边一样。

### `tray`

| 键 | 说明 |
|---|---|
| `override` | 要不要让 twin 管这个实例的托盘图标。**总是写** —— "不覆盖"本身也是一个决定，缺这段会让它和"这个字段存在之前写的老文件"无法区分（缺段老文件按 `true`）。走哪条路（助手扩展读的 `~/.local/share/twin/tray.d/<id>.json`，还是回退到宿主 `custom-icons` 覆盖表）是**机器**的事（[`architecture.md`](architecture.md) §7.1），不由这个键决定 |

---

## 5. 什么进定义，什么每次重算

这是整个配置设计里唯一需要判断力的一条：

| 类别 | 例子 | 进 `profile.yml`？ | 理由 |
|---|---|---|---|
| **意图** | id、名字、level、`bwrap` 里那几张列表 | ✅ | 用户对这个实例明确表达过的东西 |
| **推导结果** | 由 id 推出的 badge、由源 Name 推出的中文名 | ✅ | 落成全量值，文件自解释，不留 `null` |
| **环境** | bwrap 路径、python 路径、图标尺寸（由本机 DPI 决定） | ❌ | 换机器/换会话就变，写进文件必然过期 |
| **源的现状** | `Categories` / `Keywords` / `Icon=`（实例里留空表示"继承"时） | ❌ | 源桌面项可能被系统更新覆盖 ⇒ `install` / `reinstall` 时重读 |
| **应用知识** | 该程序是否需要嵌套 ns | ✅ | 它跟着程序走，不跟着机器走 |
| **机器状态** | 用哪个跳板 / 要不要自己装 profile、副本放哪 | ❌ | 留在 `config.toml`，让下一台机器自己决定 |
| **会话类型** | Wayland / X11 | ⚠️ 部分 | 已固化成基座那条 `/tmp` 的取舍（`bwrap.tmpfs` 里的 `/tmp`，或 `args` 里的 `--bind /tmp /tmp`）；`reinstall` 时会**警告**它与当前会话不符 |

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

# 改一个实例（install 与 reinstall 读的是同一份定义，敲哪个都行）
$EDITOR ~/.local/share/twin/wechat2/profile.yml && twin install wechat2
$EDITOR ~/.local/share/twin/wechat2/profile.yml && twin reinstall wechat2
```

### 它是给人读的

header 就写着这一点 —— 而且**每种来路各有各的那一句**：

```yaml
# --- profile.yml（twin gen 写出，落在实例目录里）---
# twin —— 实例定义。改完跑 `twin install wechat2` 按它落盘，`twin reinstall wechat2` 重放的就是它。
# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`badge` 那种纯数字尤其要）。
# 每个键的说明写在它自己上面 —— 终端不再重复一遍。

# --- profile.yml（twin gen -o <文件> 写出，落在你指定的地方）---
# twin —— 实例定义。改完跑 `twin install -p ~/wraps/wx.yml` 按它落盘，`twin reinstall wechat2`
# 重放的是实例目录里那份记录。
# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`badge` 那种纯数字尤其要）。
# 每个键的说明写在它自己上面 —— 终端不再重复一遍。

# --- profile.yml（twin install -p <别处> 写出的存档记录）---
# twin —— 实例定义（存档记录）。`twin install` 按你指定的那份配置写出它，改完跑
# `twin reinstall wechat2` 重放。
# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`badge` 那种纯数字尤其要）。
# 每个键的说明写在它自己上面 —— 终端不再重复一遍。

# 实例 id：桌面项基名、别名文件名、这个目录名，三者都是它。
id: wechat2
…
desktop:
  # 显示名（窗口标题与菜单里看到的）。
  name: 微信2
  # 搜索关键词，`;` 分隔。留空则沿用源桌面项。
  keywords: 微信;wechat;
…
exec:
  # 沙箱内建在 target 同目录下的别名文件名：窗口 app_id 与托盘 SNI Id 都等于它，
  # 分叉了就有一个找不到桌面项。
  alias: wechat2
…
bwrap:
  # 隔离哪个数据目录（整段替换内置规则）：`源:目标[:ro|:dev]`，同 Docker 的卷
  # （:ro 只读、:dev 带设备访问，单写一条路径则挂回自己）。`gen` 的第一条是根挂载 /:/:dev，
  # 整个宿主根可写 = --dev-bind / /，永远排最前，收紧成只读改成 /:/:ro。空 = 数据目录与源程序共用。
  bind:
    - /:/:dev
    - ~/.local/state/twin/wechat2/xwechat:~/.xwechat
```
