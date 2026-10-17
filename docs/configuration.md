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

**数组类字段**（`bwrap.extras`、`bwrap.bind`、`tmpfs`）按**低位在前拼接**，同目标由后写的遮蔽：

```
最终 bwrap.extras = gen 写的基座那几行 ++ config.args ++ rules.args ++ gen 命令行 `--` 那一段
```

`bwrap.extras` 是**交给 bwrap 的其余全部选项**（基座也在里面，见 §4），所以"这个程序拿到哪个根"
第一次成了这份文件能回答的问题 —— 原先基座硬编码在 twin 里，想换根只有透传区一条路，而透传区
排在所有映射之后，重新绑一次根会把前面的挂载全盖掉。

⚠️ 基座里**只有根挂载**不在这份列表里：它是 `bwrap.root` 那一个词（`ro` / `rw` / `dev`），
而 `Build` 把它发在所有东西**之前**。其余每一条（`--proc /proc`、`--tmpfs /dev/shm`、宿主 `/tmp`
那条、`--die-with-parent`）都是这份列表里的一行 —— 想改哪一条就改那一行，**行序就是命令里的顺序**，
所以"被谁压住"是写在文件里的，不必先去背 bwrap 的选项表。

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

`--refresh` 只做**加法**，绝不改写那份定义里已有的值：`bwrap.extras` 把 config ＋ rules 的
参数行追加进来（已经在里面的跳过，所以是幂等的），其余字段（id / source / override / isolation /
`bwrap.bind` / `bwrap.env`）一律不动 —— 它们一定已被写满，"空"与"用户就是要空"分不开。基座也不动：
它属于那份定义本身。**桌面项正文完全不参与** —— 它由 `install` 按源桌面项生成一次，从第一个
字节起就是用户的文档（见 §4 的 `override` 一节）。
逐字段的行为写在 `twin reinstall -h` 里。

补上的值**只用在这一次落盘上**：那份定义是你的文件，`install` / `reinstall` 都不改写它（改写会丢掉
手写的注释），所以想让它们留下来，就照 `--refresh` 打印的那几行自己改进文件里。命令的输出里也这么
写着 —— 否则"补了又没了"就是一次没人看得见的静默回退。

⇒ 一句话：`reinstall` 让**派生文件与那份定义一致**，而不是让定义跟随环境。

### 路径怎么写：绝对 / `~` / 相对

这些字段是路径：`bind` 的两侧、rules.d 里的 `tmpfs`、`profile.yml` 里的 `source.exec`、
`source.desktop`、`override.desktop`、`override.icon.source`。它们接受三种写法：

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

- **`bwrap.extras` 不解析**：那是 bwrap 的选项列表，一行一条、按空格拆开原样交给它，twin 不认、
  也猜不出哪个 token 是路径（[`architecture.md`](architecture.md) §2.1）。在那里写相对路径，等于
  交给 bwrap 按**启动器的工作目录**解析 —— 那个目录没人选过。`~` 是例外：它是这一份列表里唯一
  在发出去之前会展开成 `$HOME` 的东西（规则里的 `--tmpfs ~/.cache/x` 要指向用它的那台机器）。
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
| `tmpfs_unless_x11` | `true` | 程序支持 Wayland 时，新实例的 `bwrap.extras` 里写一行 `--tmpfs /tmp`（空的私有 `/tmp`）；关掉则改成写一行 `--bind /tmp /tmp`（宿主那个，原地可写，只是与宿主共享）。**在你确信二进制特征扫描判错的机器上关掉它**。两条都必须**可写** —— 只读的 `/tmp` 会让程序在别处报错 |
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
| `args` | `[]` | 追加到每个**新**实例 `bwrap.extras` 的尾部（排在它那份基座之后）。**一个元素 = 一条完整参数**，原样发给 bwrap、不做替换。写进实例定义之后就不再回读这里 —— 改这份文件只影响以后新建的实例 |

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

# 盖成空目录的路径：生成实例时各写成 bwrap.extras 里的一行 --tmpfs；
# 写 /tmp 就是那份定义里的私有 /tmp（见 §4）
tmpfs = ["/tmp"]

# 写法同 Docker Compose 的卷：源:目标[:ro|:rw|:dev][:try]。源是数据实际所在，目标是程序看到它的位置
# （bwrap 的 --bind 也是这个方向）。
bind = [
  "~/.local/state/twin/{id}/xwechat:~/.xwechat",
  "~/Documents/xwechat_files",                                # 单写一条路径 ⇒ 原地升回可写，数据不搬家
  "~/.local/state/twin/{id}/all_users:~/Documents/xwechat_files/all_users",
  # 程序第一次运行才会建的目录加 :try（源不在就整条跳过、不报错）；:try 要跟在写全的模式后面
  # "~/.config/myapp:~/.config/myapp:ro:try",
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
| `tmpfs` | 数组，**替换**内置；生成实例时每项写成 `bwrap.extras` 里的一行 `--tmpfs <路径>`（基座自己那条 `/dev/shm` 也在同一份列表里）。它保持"一串路径"的写法，因为其中 `/tmp` 那一项同时是一个决定（`gen` 会据此挑 `/tmp` 那一支） |
| `bind` | 字符串数组，**替换**内置。每条是 `源:目标[:ro\|:rw\|:dev][:try]`；单写一条路径 ⇒ 该路径挂回自己（原地升级可写）；`:try` 是"源不在就跳过"（见 §4 的 `bwrap.bind`）。生成时整段替换 `bwrap.bind` |
| `[bwrap] args` | 数组，**替换**内置；追加到实例 `bwrap.extras` 的尾部（基座与路径映射之后）。一条 = 一条完整参数（`--dev-bind /dev/dri /dev/dri`），与 config.toml 和 `bwrap.extras` 同一个形状 |

### 合并语义（与内置同名字段）

**逐字段**合并 —— 文件里写了的字段赢，没写的继承内置。所以"只想改 keywords"的文件不必把三条
路径抄一遍。

⚠️ 但**数组类字段是替换而非追加**：文件里一旦出现 `bind` / `tmpfs` / `[bwrap] args`，
就以文件里的为准。写了 `bind = […]` 的人是在描述他想要的**全部**映射，半继承会让这个文件变成谎言。

⚠️ 单写一条路径（`"~/.foo"`）＝ 把它原地挂回自己（与手写包裹里的 `--bind A A` 同义）。这**只在
那份定义把根挂成只读时**才改变什么：`twin gen` 写的 `bwrap.root` 是 `dev`（整个宿主根，可写），
而那一行的意思是"根换成 `ro` 之后，把这一条从只读升回可写"。

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

文件是 YAML：下面每个小节是一个键，表里的键名要拼成点号路径 —— `isolation.level`
指的就是 `isolation:` 缩进块里的 `level:`。缩进只能用空格。

**一个键后面什么都不写（`extras:`）就是空值**，和 `extras: []` / `keywords: ""` 是同一件事 ——
把一整段列表删光之后留在那儿的那一行正是它，所以它读得进去。空值的读法由键自己决定：路径是
空串、列表是零条、开关是 false，不存在"猜它是 null 还是空串"这一步。

**每个键上面都写着它自己的说明**，`gen` 生成的配置就是这样：阶段的解释留在文件里，
人读到它的时候正好在编辑它。下面这些表是同一份内容的查阅版。

**四个节点，按"这是谁的话"分**：`source` 是这个程序在哪，`override` 是 twin 覆盖了什么，
`isolation` 是它怎么跑，`bwrap` 是这一次 bwrap 调用拿到什么。旧的定义里那几节
（`exec` / `identity` / `desktop` / `icon` / `tray`）**一律指名报错并给出新位置**，不会被
当成"没设置"读掉 —— 读成"没设置"会让两份定义各表一套，而终端上一个字都不说。

### 顶层

| 键 | 说明 |
|---|---|
| `id` | 实例 id（= 桌面项基名 = 别名文件名 = 这个目录名） |

### `source`

| 键 | 说明 |
|---|---|
| `exec` | 真实程序的路径。可以绝对、`~/` 开头、或相对（基准是这份文件所在的目录） |
| `args` | 程序的静态参数：一行一条完整命令（选项和它的值写在同一行，按空格拆开），原样接在程序路径之后、桌面环境给的 `%U` / `%F` 展开出来的文件/URL **之前**。整条参数以 `~/` 开头时才会在启动时折叠成本机 HOME（和 `bwrap.extras` 的 token 同一条规则，所以 `--opt=~/x` 里的那个 `~` 不会被展开）；值里不能有空格。`gen` 写的是空列表；源桌面项 `Exec` 里那些参数**不会自动搬过来**（`twin inspect` 会把它们列出来），要用就抄进这里 |
| `desktop` | 源桌面项路径。生成下面那份正文时从这里取显示名、图标、分类；留空也能生成，只是什么都要自己填 |

别名不再是一个键：**它永远是 `id`**。窗口 `app_id` 与托盘 `SNI Id` 都等于它，而它又是桌面项基名 ——
分叉了就会有一个找不到桌面项，所以没有第二处可以写它（旧的 `exec.alias` / `exec.argv0` 都被拒了）。

`args` 刻意**不在 `bwrap` 节点里**：它不是这次 bwrap 调用的参数，而是跟在**程序**后面的参数 ——
沙箱开着（`alias` / `redirect`）和关着（`none`）都照样发出去，放在 `bwrap` 里就会在 `none` 那一档
凭空消失。位置本身也是说明：`exec` 说跑哪个程序，`args` 说它带什么参数。

### `override`

twin 覆盖了什么。这一节的形状是这份配置里唯一一处需要解释的：

| 键 | 说明 |
|---|---|
| `desktop` | 桌面项**正文**所在的文件。`gen` 在定义里写的就是与本文件同目录的 `<id>.desktop`，但**它不写那份文件** —— 由 `install` 在找不到它时按源桌面项生成一份（`Exec` / `Icon` / `StartupWMClass` 都填好），此后每次 `install` 只是把它**逐字复制**到 `~/.local/share/applications/<id>.desktop`，**生成过之后它就是正文** |
| `wm_class` | 生成正文时写进 `StartupWMClass` 的值。默认 = `id`，且只在 level 会改名时写。⚠️ 别依赖它：Wayland 下认领窗口的是别名基名（这一行只对 X11/XWayland 有意义），而且正文生成过一次之后，改这里不再有影响 |
| `icon.source` | 源图标：png/jpeg/svg 路径，或图标主题里的名字。路径写法同 `source.exec` |
| `icon.badge` | 角标文字（`style = dot` 不用它）。纯数字要加引号，否则会被读成数字 |
| `icon.style` | `corner` \| `number` \| `dot` |
| `tray` | 要不要让 twin 管这个实例的托盘图标。**总是写** —— "不覆盖"本身也是一个决定，缺这一段会和"这个字段存在之前写的老文件"分不开（缺段老文件按 `true`）。走哪条路（助手扩展读的 `~/.local/share/twin/tray.d/<id>.json`，还是回退到宿主 `custom-icons` 覆盖表）是**机器**的事（[`architecture.md`](architecture.md) §7.1），不由这个键决定 |

**为什么正文是一份文件、不是五个键**：它是这一份包裹里唯一"改写"而不是"阅读"的东西 ——
显示名、说明、关键词、菜单分类 —— 而"在配置里改一行、再 install 一遍"其实是用两步做一件
文本编辑。现在改它就是在改最终结果；`install` 复制的就是它，`--dry-run` 印的也是它。
它也顺便回答了"装出来的到底是什么"。

`install` 只**生成一次**（文件已存在就不重写）：那份文件从第一个字节起就是你的文档，重写一次就
等于用推导覆盖你的编辑 —— 与这份 `profile.yml` 同一条规矩。反过来说，"改配置里的
`icon.badge` 却没反应"是设计如此：正文里写着 `Icon=` 的那一行才是结果。

它也不在 `gen` 那一步生成，理由有两条。一，这份配置里**没有任何一个键描述那些字**：`gen` 写它
只能靠几个命令行选项，而那些选项只在"文件还不存在"时有用，装过一次之后就静默失效 —— 一个
"改了没反应、也不报错"的 flag 是这个工具最不该有的东西。二，它由 `install` 生成与它由 `gen`
生成落点相同（都在本节这个 `desktop` 键指的那份文件里），早一步写出来换不到任何东西。想改菜单
里那几个字：装完之后编辑那份文件，再 `twin reinstall <id>`。

### `isolation`

| 键 | 说明 |
|---|---|
| `level` | `none` \| `alias` \| `redirect` |
| `unshare` | 额外 unshare 的命名空间。**含 `net` 会被拒绝** |
| `nested_ns` | 这个程序要不要自己建嵌套 user namespace（Chromium/Electron 类）。**总是写**，见下 |

### `bwrap`

这一个节点就是这次 bwrap 调用，一共四个键：**根**（`root`）、**挂载**（`bind`）、**环境**（`env`）、
**其余全部**（`extras`）。

| 键 | 对应 | 说明 |
|---|---|---|
| `root` | `--ro-bind / /` / `--bind / /` / `--dev-bind / /` | 程序跑在哪个根下，一个词：`ro` / `rw` / `dev`。**总是写**。`dev` 是整个宿主根含设备（`gen` 写的就是它）；收紧成 `ro` 的代价是设备与 `/run/user` 要自己用 `extras` 补回来（`--dev-bind /dev /dev`、`--bind /run/user /run/user`），否则 Wayland socket、会话总线、PipeWire 全断。它是**整个沙箱的根**，所以 `Build` 永远把它发在**其余所有挂载之前**（排在 overlay 别名之后会把宿主目录盖回程序的运行路径上）—— 这个位置不由文件决定，于是它有自己的键。空 = 这个键没说什么，根由下面的 `bind`（老定义里那条 `/:/…`）或 `extras` 里的 `--dev-bind / /` 说；三处都没有才被拒（`level: none` 不在此列，它不过 bwrap）。**说两遍也会被拒**：两条挂载都会发出去，谁赢就成了发射顺序的事 |
| `bind` | `--bind` / `--ro-bind` / `--dev-bind` | 隔离哪个数据目录。**总是写**（空时是 `bind: []`）—— "一条映射都没有"本身是一个决定。写法见下。⚠️ **这里不写根挂载**（新定义用 `root`）：两端都是 `/` 的那一条照旧被 `Build` 提到所有挂载之前、被「路径映射」那一节滤掉，只是它现在只出现在老定义里 |
| `env` | `--setenv` | 注入沙箱的环境变量，一条一个 `键=值`，后面的同名者胜。等号只在**第一个**处断开，所以值里可以再有等号与冒号（`OPTS=--a=b`、`PATH=/a:/b`）；值可以为空（写 `键=`），键名不能带空格。`gen` 写的 `TWIN_INSTANCE=<id>` 就在这一条里 |
| `extras` | （原样） | 交给 bwrap 的**其余全部参数**，一行一条完整命令（选项和它的值写在同一行，按空格拆开）。它是一份**合并后**的列表：`gen` 写的基座 ＋ config.toml 的 `[bwrap] args` ＋ rules.d 的 `[bwrap] args` ＋ `gen` 的 `--` 透传区，低位在前。**总是写**，写在节点的最后 |

**位置**：`root` 与 `bind` 在前、`env` 在后（挂载要先落地，环境变量与顺序无关），`extras` 在**所有
挂载之后、`env` 之前**。这不是文风问题，是两条硬约束撞在一起的结果：

- `--remount-ro <路径>` **必须排在它收紧的那条挂载之后**，否则那条挂载会把它盖回去、又变回可写；
  写早了 bwrap 直接拒绝启动（`Can't remount readonly on …: Unable to find … in mount table`，实测）。
  它也因此只能作用在**自己就是一条挂载**的路径上：`bind` 的目标端、overlay/tmpfs 那条路径、或
  整个 `/` —— 根（`root` 那条）底下的普通路径没有单独的挂载项。
- `extras` 里**后写的压住先写的**，这也正是宿主 `/tmp` 那一支的写法：程序走 Wayland 时
  `gen` 写一行 `--tmpfs /tmp`（空的私有 `/tmp`），只走 X11 的程序不能要它（`--tmpfs /tmp` 会把
  `/tmp/.X11-unix/X0` 一起带走，于是连不上 X），那时基座在那**之后**再写一行 `--bind /tmp /tmp`
  把宿主那个挂回来 —— 同一条列表里的行序就是全部机制，不必再去别处删一行。

`extras` 的一个已知边界：**值里不能有空格**。一行按空格拆开就是它的全部理解方式 ——
twin 不知道任何 bwrap 选项的元数，也不打算开始猜（[`architecture.md`](architecture.md) §2.1）。
带空格的路径属于 `bind`（那一侧是真解析的），不属于这里。

为空就是没设置：空值不发出任何参数，也不会被理解成"要默认的那个" —— 默认的那几条是 `gen`
写在这四个键里的，是文件里看得见的一段文本。

基座（`gen` 写的，可改）。除了 `root` 那一个词，**每一条都是这份列表里的一行**：

```
# bwrap.root —— 唯一一条不是一个选项行的
dev                   整个宿主根（含设备）挂进来 ⇒ 设备可用、根可写（就是 --dev-bind / /）

# bwrap.extras —— 一次调用剩下的一切，按命令顺序
--proc /proc
--tmpfs /dev/shm      空的私有共享内存
--tmpfs /tmp          空的私有 /tmp（选了私有那一条时）
--bind /tmp /tmp      宿主那个 /tmp 原地可写（不要私有 /tmp 时才有这一行）
--die-with-parent

# bwrap.env
TWIN_INSTANCE=<id>
```

#### `bwrap.bind`

数组，每条是 `源:目标[:ro|:rw|:dev][:try]`。

| 写法 | 说明 |
|---|---|
| `源:目标` | 私有副本：程序在 `目标` 看到的其实是 `源`，两个实例互不干扰 |
| `源:目标:ro` | 只读挂载（`--ro-bind`）：程序看得见但改不动 |
| `源:目标:dev` | 带设备访问（`--dev-bind`，bwrap 的原话是"allowing device access"）：给设备节点用。bwrap 没有只读的设备映射，所以 `:dev` 不能与 `:ro` 同用 |
| 单写一条路径 | 该路径挂回自己，只把只读升回可写（数据不搬家） |
| 末尾加 `:try` | 用 bwrap 自己的 `--bind-try` / `--ro-bind-try` / `--dev-bind-try`（它的原话是"equal to --bind but ignores non-existent SRC"）：**源不在就整条跳过，不报错**。给"程序第一次运行才会建的目录"用 —— 普通 `--bind` 会把"还没有这个目录"变成"程序起不来"，而在 twin 这一侧替它建一个空的源，等于替这个程序回答了它的数据在哪。⚠️ `:try` 要跟在写全的模式后面（`源:目标:ro:try`）：三段的 `源:目标:try` 会被读成一个访问模式，而两段的 `x:try` 从第一天起就是"目标叫 try" |

`:dev` 的用处是让"把根收紧成 `root: ro`"的定义仍能表达"这条绑定带设备访问"：`gen` 写的根就是
`dev`（`--dev-bind / /`），整个宿主本就带设备可用（含 `/dev`），那时它是多余的。

⚠️ `rules.d` 里的 `bind` 仍在顶层（见 §3）：那份文件描述的是"这个程序的数据放在哪"这类**应用知识**，
旁边还有 `keywords` / `tmpfs` 这些与 bwrap 无关的键，它服务多个实例，不是一次调用。写法两边一样。

---

## 5. 什么进定义，什么每次重算

这是整个配置设计里唯一需要判断力的一条：

| 类别 | 例子 | 进 `profile.yml`？ | 理由 |
|---|---|---|---|
| **意图** | id、名字、level、`bwrap` 里那几张列表 | ✅ | 用户对这个实例明确表达过的东西 |
| **推导结果** | 由 id 推出的 badge、由源 Name 推出的中文名 | ✅ | 落成全量值，文件自解释，不留 `null` |
| **环境** | bwrap 路径、python 路径、图标尺寸（由本机 DPI 决定） | ❌ | 换机器/换会话就变，写进文件必然过期 |
| **源的现状** | `Categories` / `Keywords` / `Icon=` | ❌ | 源桌面项可能被系统更新覆盖 ⇒ 只用来**生成**那份正文（`override.desktop` 指的文件），之后那份文件说了算 |
| **应用知识** | 该程序是否需要嵌套 ns | ✅ | 它跟着程序走，不跟着机器走 |
| **机器状态** | 用哪个跳板 / 要不要自己装 profile、副本放哪 | ❌ | 留在 `config.toml`，让下一台机器自己决定 |
| **会话类型** | Wayland / X11 | ⚠️ 部分 | 已固化成基座那一行 `/tmp`（`bwrap.extras` 里的 `--tmpfs /tmp` 或 `--bind /tmp /tmp`）；`reinstall` 时会**警告**它与当前会话不符 |

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

# 改菜单里显示的东西：改的是那份正文，不是配置
$EDITOR ~/.local/share/twin/wechat2/wechat2.desktop && twin install wechat2
```

### 它是给人读的

header 就写着这一点 —— 而且**每种来路各有各的那一句**：

```yaml
# --- profile.yml（twin gen 写出，落在实例目录里）---
# twin —— 实例定义。改完跑 `twin install wechat2` 按它落盘，`twin reinstall wechat2` 重放的就是它。
# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`override.icon.badge` 那种纯数字尤其要）。
# 每个键的说明写在它自己上面 —— 终端不再重复一遍。

# --- profile.yml（twin gen -o <文件> 写出，落在你指定的地方）---
# twin —— 实例定义。改完跑 `twin install -p ~/wraps/wx.yml` 按它落盘，`twin reinstall wechat2`
# 重放的是实例目录里那份记录。
# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`override.icon.badge` 那种纯数字尤其要）。
# 每个键的说明写在它自己上面 —— 终端不再重复一遍。

# --- profile.yml（twin install -p <别处> 写出的存档记录）---
# twin —— 实例定义（存档记录）。`twin install` 按你指定的那份配置写出它，改完跑
# `twin reinstall wechat2` 重放。
# 缩进只能用空格；拿不准要不要加引号的字符串就加上（`override.icon.badge` 那种纯数字尤其要）。
# 每个键的说明写在它自己上面 —— 终端不再重复一遍。

# 实例 id：桌面项基名、别名文件名、这个目录名，三者都是它。
id: wechat2
…
source:
  # 真实程序的路径。可以绝对、~/ 开头、或相对 —— 相对时基准是这份文件所在的目录。
  exec: /opt/wechat/wechat
  # 程序的静态参数，一行一条完整命令（按空格拆开），原样接在程序路径之后、桌面环境给的
  # 文件/URL 之前：`--ozone-platform=wayland`、`--disable-gpu`（整条以 ~/ 开头的参数在启动时才
  # 换成本机 HOME）。源桌面项 Exec 里那些参数不会自动搬过来（`twin inspect` 会把它们列出来）。
  args: []
…
override:
  # 桌面项**正文**所在的文件（相对路径的基准同上，`gen` 写的是与本文件同目录的
  # <id>.desktop）。install 每次把它逐字复制到 ~/.local/share/applications/<id>.desktop；
  # 文件不在时会按源桌面项生成一份（Exec / Icon / StartupWMClass 都填好），此后它就是正文。
  desktop: ./wechat2.desktop
  icon:
    # 图标上的角标文字（style = dot 不用它）。纯数字要加引号，否则会被读成数字。
    badge: "2"
…
bwrap:
  # 程序运行在哪个根下：ro = --ro-bind / /（整个宿主根只读，代价是设备与 /run/user
  # 要自己用 extras 补回来），rw = --bind / /，dev = --dev-bind / /（带设备访问，`gen` 写的就是它）。
  # 发在其余所有挂载之前（这一条的位置不由文件决定），整个沙箱里只有这一个根。
  root: dev
  # 隔离哪个数据目录（整段替换内置规则）：`源:目标[:ro|:rw|:dev][:try]`，同 Docker 的卷
  # （:ro 只读、:rw 可读写、:dev 带设备访问，单写一条路径则挂回自己；末尾的 :try 要写全模式，
  # 源不在就整条跳过、不报错）。
  # 根不在这里 —— 它是 `root` 那个键。空 = 数据目录与源程序共用。
  bind:
    - ~/.local/state/twin/wechat2/xwechat:~/.xwechat
```
