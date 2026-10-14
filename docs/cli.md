# 命令参考

```console
$ twin --help
```

---

## 1. 调用形态

```
twin <动词> [参数…]               # 动词不带宾语（list / doctor / config / install）
twin <动词> <宾语> [选项]         # 大部分动词带一个宾语（gen / inspect / verify …）
```

**第一个参数永远是动词，第二个永远是它的宾语** —— 位置即含义。没有隐式动词，也没有
保留字表：所以程序真名无论叫 `list` 还是 `gen`，照常写就行，不需要任何逃生口写法。

```bash
twin gen list          # 包一个名叫 list 的程序
twin inspect doctor    # 预览名叫 doctor 的程序
```

> **为什么不能写 `twin wechat`？** 早期版本允许把首个参数当隐式动词的宾语，代价是它的
> 含义取决于是否命中一张保留字表 —— 读一行命令得先背那张表，还得额外解释"程序名撞上动词时
> 怎么办"。取消隐式动词同时消灭了**保留字表**和**逃生口**两样特例，一个从未实现的
> `completion` 死字也顺带清掉了。
>
> `install` 不带宾语（它读一份配置，宾语在文件里），所以它是这条规则的第一个例外 —— 而它不需要
> 任何特例代码：没给宾语的那些动词本来就不看第二个 token。

`<source>` 可以是：`.desktop` 文件的路径、桌面项 id（`wechat`），或程序名。
查找顺序是 `~/.local/share/applications` → `/usr/local/share/applications` →
`/usr/share/applications`。

---

## 2. 子命令

本节只说每个命令**做什么、为什么**。**选项与默认值是 `twin <命令> -h` 的事**（`twin gen -h`、
`twin install -h` …）—— 那里与实现同一次编译，在这里再抄一份只会多出一处会烂的副本。

### `twin doctor`

环境自检。**先跑这个。** 输出末尾直接告诉你下一步敲什么。

检查项（本机实测输出 15 行、`0 警告 / 0 失败`。每一项都显式报告，跑不了就报"跳过"而不是
默认为好）：

| 检查项 | 判据 |
|---|---|
| `HOME` | 存在且是绝对路径（手写脚本曾退回 `/root`，那是数据风险） |
| 会话类型 | `WAYLAND_DISPLAY` / `DISPLAY` / `XDG_SESSION_TYPE` |
| `bubblewrap` | 在不在 PATH、版本号 |
| `bwrap` 能建命名空间 | **真的跑一次**（"装了"和"能用"是两回事） |
| `overlay` 零拷贝改名 | 跑一次 overlay ＋ 别名，且检查别名**没有泄漏到宿主** |
| `非特权 userns` | 内核是否限制；受限时**有没有跳板可用**（判据是后者，不是前者） |
| `GLib` 桌面项自检可用 | 用一对"好/坏"探针桌面项交叉验证检查器本身可信 |
| `SVG/其他格式光栅化` | `python3-gi` GdkPixbuf / `rsvg-convert` / ImageMagick |
| `托盘宿主` | 有没有 appindicator 宿主；覆盖表读不读得到 |
| `会话总线` | 能不能连（决定 `gsettings set` 会不会**假成功**） |
| 可写 `~/.local/share/…` 等 5 个目标目录 | 找最近的存在祖先实测写一个临时文件 |

退出码：有 `FAIL` ⇒ `1`，否则 `0`（`WARN` 不影响退出码）。

### `twin inspect <source>`

**只读。** 看这个程序会被包成什么样，一个字节都不写。

```
源桌面项   /usr/share/applications/wechat.desktop
显示名     微信   ← 源 Name[zh_CN]（实例会再追加序号）
说明       微信桌面版   ← 源 Comment[zh_CN]
Exec       /usr/bin/wechat %U
真实程序   /opt/wechat/wechat
所在目录   /opt/wechat
源图标     /usr/share/icons/hicolor/512x512/apps/wechat.png
Categories Utility;
配置       ~/.config/twin/config.toml（不存在，用内置默认；`twin doctor` 可打印模板）
Keywords   微信;wechat;
           ← 源无 Keywords；由 Name[zh_CN]、Name 推得

🔧 工具箱探测（按二进制内字符串）
  Wayland 支持  是
  X11 支持      是
  Qt            是
  GTK           否
  多合一二进制  否

📂 /tmp 策略：可用 --tmpfs /tmp —— 本会话有 WAYLAND_DISPLAY，该程序会走 Wayland。
  ⚠️ 但它两套平台插件都带着：若哪天真退到 X11，--tmpfs /tmp 会砍掉 /tmp/.X11-unix/X0 而连不上 X。

📝 备注
  · /usr/bin/wechat 是指向 /opt/wechat/wechat 的链接 —— 别名要建在 /opt/wechat 里（$ORIGIN 与 /proc/self/exe 都看真实路径）
  · Wayland 原生 + 静态链接的 Qt：窗口 app_id 由 Qt 从可执行文件真实路径派生（已实测）
  · 同时含 Wayland 与 X11 平台插件：本会话有 WAYLAND_DISPLAY 时按 Wayland 走
  · 应用规则 wechat：内置规则（3 条路径映射 ⇒ 快照里的 bind）
```

`显示名` / `说明` 打印的是**语言化**取值（`Name[<当前语言>]` 优先），因为 `wrap` 就是这么
推导的 —— 预览与真实写入必须一致，否则这个工具的预览就是一句谎话。

`--name` 只影响预览：它会**真的**参与 keywords 的推导（因为你显式给了名字），所以能在不写盘
的前提下看到"命名 ↔ 关键词"的联动结果。

### `twin gen <source>`

**只写一份配置，不安装任何东西。**

一次 wrap 里唯一随程序而变、又猜错了不出声的东西是路径映射（隔离错一个目录，要到两个实例抢同一个
锁文件时才暴露）。所以先把它落成一份能读能改的文件，再安装：

| 命令 | 做什么 |
|---|---|
| `twin gen wechat` | 在当前目录写 `profile.yml`（id、显示名、路径映射全部推导出来写进去），然后说清下一步敲什么 |
| `$EDITOR profile.yml` | 改 `bind` / `desktop.name` / `desktop.keywords` / `identity.level`；不改也行，推导出来的默认值通常够用 |
| `twin install` | 读那份配置：写实例快照 → 落盘（桌面项 / 图标 / 启动器 / 宿主托盘）＋ 自检 |

**配置为什么在当前目录？** 它是这份 wrap 里唯一需要人读一遍的东西，所以放在人**已经选好的**那个
目录里：能 diff、能提交、能拷到另一台机器、能同时留好几份。实例目录下的
`~/.local/share/twin/<id>/profile.yml` 是另一个东西 —— `install` 写下的**存档**，寿命与含义都不同。

**输出文件已存在就拒绝**（退出码 1），除非 `--force`：那个文件正是你要改的东西，盖掉它比报个错
难收拾得多。

```console
$ twin gen wechat
✅ 已写出配置 profile.yml

👉 下一步
  1. 改 profile.yml —— 每个键的说明都写在文件里的注释里（不改也能装）
  2. 安装：twin install
```

`gen` 的输出刻意短：它只做两件事 —— **只写一个文件**（桌面项、图标、启动器、私有状态目录都等
`install`），并且**把路径指出来**。逐键的说明、`bind` 的语义、`rules.d` 的存在，全都写在**那份文件
的注释里**：读到它的时候人正在编辑它，这比在终端里滚过去有用，也不会和文件两处说法各表一套。
要看推导值与**这一份配置的**路径映射一节，加 `--explain`。

终端上只多留一行，给"**一条映射都没有**"的程序：

```console
$ twin gen foo
✅ 已写出配置 profile.yml
  ⚠️ 没有隔离任何数据目录：这个程序若有单实例锁（Electron / Qt / GTK 系常见），两个实例会互相干扰 —— 见配置里的 bind

👉 下一步
  …
```

它是这个命令唯一可能留下、而别处不会再报的后果（共享状态要到两个实例抢同一个锁文件时才暴露），
所以它留在终端上；其余情况一律不多说。

### `twin install`

**读一份配置，把它变成实例。** 读不到那份配置就直接报错并让你去跑 `twin gen`，不会去猜别的路径。

它**不接受任何描述实例的选项**：包哪个程序、叫什么名字、隔离哪些目录，全都在那份文件里。一个 flag
和一个文件可以互相矛盾，而其中只有一个是用户刚刚花了时间改的那份。

```console
$ twin install
✅ 已安装 wechat2
  桌面项   /home/you/.local/share/applications/wechat2.desktop
  图标     /home/you/.local/share/appicons/wechat2.png
  启动器   /home/you/.local/share/twin/wechat2/launch
  实例定义 /home/you/.local/share/twin/wechat2/profile.yml
  托盘覆盖 已写入 custom-icons（该表热重载，立刻生效）

📁 路径映射
  3 条，来源：配置 profile.yml
  私有  ~/.local/state/twin/wechat2/xwechat:~/.xwechat

👉 下一步
  1. 桌面项是后写的 —— 请**重新启动**这个实例，否则窗口还是旧的名字和图标。
  2. 确认身份与托盘：twin verify wechat2
     改了实例快照要重放：twin reinstall wechat2
```

安装阶段的输出也刻意短：产物清单 ＋ `路径映射` ＋ 下一步。"要动手时才需要"的东西（规则模板全文、
候选状态目录、改哪个文件、推导值、手工验证命令）用 `--explain` 展开。`--dry-run` 是**完整落盘**的
预演 —— 桌面项正文、`profile.yml` 正文、bwrap 调用、托盘将写入的那一行全都在，而且由真实落盘用的
**同一份代码**渲染。

**自检只在有问题时才开口。** 那几条 `✅` 确认的正是这一次刚刚写下的文件，把它们列出来是产物清单
和下一步之间的一页废话；因此默认只打印非 OK 的项，并附上修它的命令：

```console
🔍 自检
⚠️ 托盘图标覆盖表                custom-icons 里没有 "wechat2" 的条目（托盘会继续显示应用自己的图标）
                          → gsettings set org.gnome.shell.extensions.appindicator custom-icons "..."
```

`--explain` 是唯一的例外：那里要的就是完整报告，于是整份自检（和 `twin verify` 一样的那些行）
都会打印出来。

`twin verify` 是另一回事：把每一条都列出来正是它的职责，跳过项（本机没这个能力）也在里面。

**实例目录里那份不是工作副本。** `install` 会按配置重新渲染它（存档角色，头注释指向
`twin reinstall`），所以 `install` 与 `reinstall` 写出的字节一致；改完 `profile.yml` 再 `install`
一次，实例就更新了。反过来，`reinstall` **只**读存档快照、不看配置 —— 它要的是确定性。

路径映射是这份产物里**唯一随程序而变、且猜错了不出声**的部分：隔离错一个目录，要到两个实例
抢同一个锁文件时才暴露。两个命令各有各的交代方式：`gen` 把它写进配置的 `bind`（连同"空着意味着
什么"的注释），只在一条都没有时于终端补一句警告；`install` 把它展开成单独一节，因为它这时已经
变成了启动器里的 `--bind`。这一节说明共有几条、知识来自哪一层（内置规则 / `rules.d` / **这一份
配置**）。

**"改它该编辑哪个文件"在 `--explain` 里**，不在默认输出里。两条路都要说清楚 —— `rules.d` 管的是
**以后新建**的实例，手上这份文件管的是**这一个** —— 但那是"这条映射是错的"之后才需要的说明，
而默认输出的前提恰恰是它没错。同理还有"这个程序可能的状态目录（未应用，仅供参考）"那一块探测
结果。默认输出只留事实：几条、来自哪一层、每一条是什么。

#### 需要 root 的那一步：AppArmor profile

本机若限制非特权 user namespace（Ubuntu 默认开），而程序自己又要建嵌套 ns（Chromium / Electron
类），私有 bwrap 副本就需要一份挂在它路径上的 AppArmor profile。`twin install` 会**自己装上**：

```console
$ twin install
🧱 沙箱方案
  · config.toml 里 userns.trampoline = "off"：不走跳板，需要一份挂在私有副本上的 profile

  🔐 需要 root 装一份 AppArmor profile
      将写 /etc/apparmor.d/twin-bwrap（twin-bwrap 挂到 bwrap 副本上）。sudo 会要求输入密码 ——
      不想给就 Ctrl-C，下面会打印同样的命令。

[sudo] password for you:
  ✅ /etc/apparmor.d/twin-bwrap 已写出并用 apparmor_parser 加载，内核的已加载列表里也能读到 twin-bwrap
```

三条纪律：

- **只在终端上问密码。** 标准输入不是终端（脚本、管道、CI）时不碰 sudo，退回打印 profile 全文并以
  退出码 3 结束 —— 那是这个工具从第一天起就有的诚实行为，不是失败。
- **干的事就是打印出来的那两条命令**：写 `/etc/apparmor.d/twin-bwrap`、`apparmor_parser -r`。没有
  别的系统改动，没有 AppArmor 之外的配置，也没有常驻进程。
- **回读才算数。** `apparmor_parser` 返回 0 只说明内核收下了；twin 还会读
  `/sys/kernel/security/apparmor/profiles` 确认 `twin-bwrap` 真在列表里。那台机器不让非 root 读这份
  列表时，它明说"只确认到退出码"，而不是把查不到当成查过了。

`--dry-run` 只报告这一步、绝不碰 `/etc`。装完之后 `sandbox.Resolve` 会认出这份 profile（判据是
`/etc/apparmor.d/twin-bwrap` 里的附着路径），于是**不再需要跳板**，`reinstall` 也不会再问一次密码。
`twin reinstall <id>` 同样会走这一步：一台机器需要它，就无论在装还是在重放时需要它。

`--explain` 把这一节再摊开一点，并附上一份照抄即可的规则模板：

```console
📁 路径映射
  3 条，来源：配置 profile.yml
  私有  ~/.local/state/twin/wechat2/xwechat:~/.xwechat
  共用  ~/Documents/xwechat_files:~/Documents/xwechat_files   （原地升回可写，数据不搬家）
  私有  ~/.local/state/twin/wechat2/all_users:~/Documents/xwechat_files/all_users
  改：~/.config/twin/rules.d/wechat.toml —— 影响这个程序以后新建的实例（twin rules --init wechat 生成草稿）
      只改这一个：编辑 profile.yml 里的 bind，再 twin install

  📋 照抄下面这份（就是规则文件的全部内容）：

    app = "wechat"

    bind = [
      "~/.local/state/twin/{id}/xwechat:~/.xwechat",
      "~/Documents/xwechat_files",
      "~/.local/state/twin/{id}/all_users:~/Documents/xwechat_files/all_users",
    ]

  源:目标 ⇒ 私有副本，两个实例互不干扰；单写一条路径 ⇒ 与该目录共用，只把只读升回可写；
  源:目标:ro ⇒ 只读挂载。
```

模板里的 `{id}` 占位符 —— 本实例的私有状态目录会被写回成 `~/.local/state/twin/{id}` —— 照抄出来的
规则服务这个程序的**所有**实例，而不是被烤死在某一个 id 上。上面路径按 `$HOME` 缩写了，工具打印的
是绝对路径。

一个**没有规则**的程序，默认输出就这两行 —— 这一句警告是这个命令唯一可能留下、而别处不会再报的
后果（共享状态要到两个实例抢同一个锁文件时才暴露）：

```console
📁 路径映射
  0 条：数据目录与源程序共用（本实例只做身份包裹）。
  ⚠️ 这个程序没有规则：它若有单实例锁或共享配置（Electron / Qt / GTK 系很常见），两个实例会互相干扰。
```

"那该映射什么"要加 `--explain` 才接上（候选目录只列**未应用**的，见下面的 `twin rules --init`）：

```console
  💡 这个程序可能的状态目录（未应用，仅供参考）
    ~/.config/foo                                目录存在
    ~/.local/state/foo                           目录存在
    要隔离就先生成一份规则草稿：twin rules --init foo
  改：~/.config/twin/rules.d/foo.toml —— 影响这个程序以后新建的实例（twin rules --init foo 生成草稿）
      只改这一个：编辑 profile.yml 里的 bind，再 twin install
```

### `twin verify <id>`

自检一个**已安装**实例的身份链路。**重新读硬盘**，不信内存里的期望。

实例目录在、桌面项却不在（上一次安装死在写快照与写身份锚点之间）时，它不吃一遍连锁失败，而是把
这一件事说清楚并给出收尾命令，退出码 `1`：

```console
❌ wechat3 的桌面项不在：上一次安装没走完（快照 /home/you/.local/share/twin/wechat3/profile.yml 在，桌面项 / 图标 / 启动器不全）
  重来一次：twin reinstall wechat3
```

| 检查项 | 失败意味着 |
|---|---|
| `exec.target` 存在 / 可执行 | 真实程序不见了 |
| 别名只在命名空间内 | 别名污染了宿主文件系统（overlay 没生效） |
| 桌面项存在 / 可用 / 基名 == id | 身份锚点缺失或名字对不上 |
| GLib 接受该桌面项 | **最贵的一项**：`Exec=` 无执行位 ⇒ GLib 丢弃整个桌面项 |
| 图标存在 / 可解码 | 图标没生成或坏了 |
| 启动器存在 / 可执行 | 同上，GLib 会因此丢弃整个桌面项 |
| 私有状态目录 | 不存在（首次启动会自动建，只报 WARN） |
| 托盘图标覆盖表 | 表里那一条是否真的指向本实例（写入被关掉时报告"按设置跳过"） |
| L5 规则文件 | `level=redirect` 时缺文件或 JSON 非法 |
| 嵌套 user namespace | 副本／跳板链路不完整（**双向查**：该有的没有 ⇒ FAIL；不该有却有 ⇒ WARN） |

`嵌套 user namespace` 那一项会解析**启动器文本**，确认跳板确实在启动器里 —— 因为跳板被卸载时，
快照里的每条路径都还在，别的检查全都抓不到。

### `twin list`

列出全部实例。

```console
📋 实例
id               level      别名     程序                     桌面项
wechat2          alias      wechat2  wechat                   wechat2.desktop
foo3             alias      foo3     foo                      foo3.desktop  ← 安装未完成

⚠️ 有 1 个桌面项缺失（上一次安装没走完）：twin reinstall <id>

删除某一个：twin remove <id>（不带 --yes 会先列出来问你一次）
```

`← 安装未完成` 不是"桌面项被谁删了"，而是**上一次 `install` 死在半路**：快照写下了，身份锚点没写全。
`twin reinstall <id>` 就是收尾命令 —— 它按那份快照把整件事重放一遍。

### `twin reinstall <id>`

按**实例目录里那份快照**重新落盘。它是"这个实例已经装好了，现在要按存档重来一次"那条路。

它不看 `profile.yml`：配置可能已经被拷走、提交进仓库、或者删掉了，而"上次到底装了什么"只剩存档这一
份记录。要按新配置更新实例，改配置再 `twin install` —— 两者走**同一条安装路径**，所以不可能出现
"install 会做、reinstall 不做"的事。

**默认只读快照**，因为重放必须是确定性的：碰一下 `config.toml` 不该静默改写一排实例。`-h` 里那个
开关是"再读一遍 `config.toml` / `rules.d`"的显式出口，而且它**只做加法与填空**（逐字段的行为见
[`configuration.md`](configuration.md) 的优先级一节）。

若今天的会话与快照里的决定矛盾（例如快照记着"走 Wayland"而当前只有 X11），会**警告**而不是默默重放。

### `twin remove <id>`

删除产物。默认先列出将要删的文件（存在的打 `✅`、不存在打 `➖`）**再问一次**：回答 `y` / `yes`
（大小写都行）才动手，其余任何回答 —— 直接回车、拼错、Ctrl-D —— 都按"不删"处理。

- 标准输入不是终端（脚本 / 管道 / CI）时**不提问**，只预览并给出那条显式命令。提问没人能回答就是
  卡住，这和装 AppArmor profile 那一步是同一条规矩。
- `--yes` 是不提问的那个开关（脚本里用它），私有数据要不要一起删是 `--purge` —— 两个开关的语义在
  `-h` 里。
- 安装没走完的实例也照删 —— 快照本来就写在那儿，撤销它就是把那个目录清掉。
- 私有状态目录默认保留（那是唯一不可逆的部分），保留的那份下次同名重建会接着用。
- 会顺带移除宿主托盘表里该 `id` 的条目：表是全局的，而这条是"一个即将不存在的程序"的声明，
  留着会继续把托盘指向刚被删掉的图标。

### `twin config`

`~/.config/twin/config.toml` 不存在 ⇒ 打印带注释的模板；存在 ⇒ 原样打印它。

```bash
twin config > ~/.config/twin/config.toml
```

### `twin rules [<app>]`

- 无参数：列出内置规则名，并打印写一条用户规则的骨架。
- 带参数：显示该程序**最终生效**的规则，含它来自内置还是你的文件。

### `twin rules --init`

**探测这个程序把状态放在哪，写出一份 `rules.d/<app>.toml` 草稿**（要正好一个参数：源桌面项、程序名或
id；文件已存在则拒绝，绝不覆盖）。这是全部命令里唯一会把"探测结果"变成文件的一步 —— 因为是你要它做的。

两条证据来源，草稿里分得很清楚：

| 证据 | 强度 | 草稿里 |
|---|---|---|
| 这台机器上目录**真的存在**（`~/.config/<名>`、`~/.local/share/<名>`、`~/.cache/<名>`、`~/.local/state/<名>`、`~/.<名>`） | 事实：程序已经在那儿写过东西了 | 写成**生效**的 `bind` 条目（默认按私有副本：源是私有目录，目标是这个真实路径） |
| 同上，但目录名只是**相近**（`~/.xwechat` 对 `wechat`） | 也是事实（目录确实存在），但那句话里的"属于这个程序"是推断 | 同样生效，证据写成 `目录存在（名字相近）` |
| 二进制里的 `~/…` 路径字面量 | 猜测：可能属于可选功能、别的平台，或一个只是被链接进来的库 | **注释掉** |

点目录（`~/.<名>` 那种）是按**名字匹配**找的，不是按名字查的：程序给目录起的名字和它在桌面项里
的名字经常不一样，只查 `~/.wechat` 会漏掉 `~/.xwechat`。`~/.config`、`~/.cache`、`~/.local`
这三个是整机共用的根，名字撞上也不作数。

字面量还要过两道闸：只保留落在状态根（或 `~/.<名>`）下面的路径，而且那一段必须与程序名有关 ——
Chromium 会写 `~/.config/autostart`、Qt 会读 `~/.config/fontconfig`，把它们当"这个程序的私有数据"
绑掉，会让半个桌面跟着坏，而症状看起来像是 twin 的 bug。

探测到的东西也可以只看不写：`twin gen --explain` 打印的 `路径映射` 一节里同样列出"**未应用**"的
候选目录（只含存在性证据那一条，因为它在每次生成时都要跑一遍）。

### `twin version`

打印版本（构建时可用 `-ldflags "-X main.version=…"` 注入）。

---

## 3. 选项在哪

**选项表只在一个地方：`twin <命令> -h`。**

```console
$ twin gen -h          # gen 的全部选项：默认值、可接受取值、被拒绝的取值
$ twin install -h      # install 只读配置，所以它只有"读哪一份 / 怎么预演"这几个选项
$ twin reinstall -h    # 重放存档的那一个开关
```

这是唯一的出处，因为**默认值是会静默出错的那类信息**：`--level` 不说清默认走哪个配置层，"我明明在
`config.toml` 里写了 alias"就没有第二处能对质。所以本文件不再逐条复述它们。

选项分两类，这条划分是 `gen` / `install` 拆分的地基（`-h` 表达不了它）：

| 类别 | 描述什么 | 例子 |
|---|---|---|
| **描述实例** | 会进 `profile.yml`，进而进快照 | 显示名、角标、`--level`、`--tmpfs`、`--no-tray` |
| **描述这次动作** | 只影响这一次调用 | 写到哪、要不要覆盖、`--dry-run`、`--explain` |

只有 `gen` 拥有第一类。`install` 的选项表里一个实例选项都没有，所以"带着描述实例的选项去装一个已存在
的实例"这件事**无法表达** —— 它不是一条运行时判断，而是动词拆分本身：包哪个程序、隔离哪些目录，全在
它读的那份文件里。

**路径映射没有 CLI 选项**，两条路都是文件：`rules.d/<程序>.toml` 管这个程序**以后新建**的实例，手上
那份配置的 `bind` 管**这一个**。一条映射正是最该在安装任何东西**之前**被读一遍的东西，而命令行参数
没人会回头再看第二眼 —— 所以它先落成文件（见 [`configuration.md`](configuration.md)）。

`--` 透传区的形式、边界与它写进哪个键，都在 `-h` 里（它不是 flag，PrintDefaults 打不出来，所以那几行
是手写的）。这里只留 `-h` 装不下的那条**理由**：透传参数拼在**基座之后** ⇒ 同目标的挂载由**它**
胜出（bubblewrap 自己就是"后挂载遮蔽前者"），所以它不是一个"额外选项"，而是**覆盖 bwrap 基座**的那
只手 —— 这也是为什么它的校验只到 lint 为止：改写它等于替用户猜他要覆盖什么。

---

## 4. 输出与颜色

判定只有一条原则：**颜色是给人看的，纯文本是给机器看的。**

| 情形 | 结果 |
|---|---|
| stdout / stderr 是终端 | 着色 ＋ emoji 字形 |
| 重定向到文件、管道、`TERM=dumb` | 纯文本（**不含**任何转义序列） |
| `NO_COLOR` 非空 | 强制关掉，优先级最高 |
| `CLICOLOR_FORCE` 非空且不为 `0` | 强制打开（管道喂给会渲染转义的 pager 时用） |

- **stdout 与 stderr 各自判断**：`twin gen wechat > log` 里 `log` 是纯文本，而落在终端上的
  `⚠️ 提醒` 依然有颜色。
- 判断用的是 `TCGETS`，不是"是不是字符设备" —— `/dev/null` 也是字符设备，否则
  `twin gen wechat > /dev/null` 会拿到一堆转义序列。
- 字形与颜色表达**同一件事**：`✅` 通过、`⚠️` 降级、`❌` 失败、`➖` 跳过（事实性跳过，不是失败）。
  所以 `2>&1 | tee log` 之后依然读得出来哪一行失败。
- 退出码不受颜色影响，`NO_COLOR` 也不改变退出码。

---

## 5. 退出码

| 码 | 含义 |
|---|---|
| `0` | 成功。**包括事实性跳过**：`--no-tray` / 整机 `tray_override = false` / 这台机器根本没有托盘宿主 |
| `1` | 失败，或自检里有 ❌ |
| `2` | 用法错误（未知子命令、未知选项、非法 `--level`、`--` 之后不是 bwrap 选项、`-o` 与 `--output` 同时给出、`HOME` 未设置…） |
| `3` | **身份已经成立，但还差一步必须由人执行。** 目前两个来源：<br>① 托盘覆盖表写不进去（无会话总线 —— 安装脚本、无图形会话）<br>② 内核限制 `userns` 而机器上既没有跳板、`install` 也没法装 profile（没有 `sudo`，或标准输入不是终端、问不了密码）⇒ profile 全文已经打印给你 |

用法错误**只打印那一行错误**，不复述整份帮助：帮助的出口是 `twin --help`，某个命令的选项表是
`twin <命令> -h`（退出码 `0`，那是提问不是犯错）。把一份 `usage` 倒在一处笔误下面，只会把真正
有用的那一行顶出屏幕。

退出码 3 的意义在于把**"去图形会话里跑一下"**和**"这东西坏了"**分开。脚本可以据此决定是
提示用户、还是报错退出。

---

## 6. 文件表

| 路径 | 是什么 | 谁写 |
|---|---|---|
| `~/.config/twin/config.toml` | 环境层：这台机器的默认 | 你（`twin config` 出模板） |
| `~/.config/twin/rules.d/<app>.toml` | 应用知识层 | 你 |
| `~/.config/twin/rules.d/<id>.json` | L5 规则（仅 `level=redirect`） | 生成器 |
| `~/.local/share/applications/<id>.desktop` | 身份锚点 | 生成器 |
| `~/.local/share/appicons/<id>.png` | 图标 | 生成器 |
| `~/.local/share/twin/<id>/launch` | 薄启动器 | 生成器 |
| `./profile.yml`（或 `-o` 指的路径） | 工作副本：你编辑的那一份 | 生成器（`twin gen`），你改，再 `twin install` |
| `~/.local/share/twin/<id>/profile.yml` | 实例存档快照 | 生成器（`twin install` 按配置渲染）；你改，再 `twin reinstall <id>` |
| `~/.local/share/twin/bin/bwrap` | 私有 bwrap 副本（共享，一台机器一份） | 生成器（仅需要时） |
| `~/.local/share/twin/bin/bootstrap.js` | 跳板脚本（共享） | 生成器（仅需要时） |
| `/etc/apparmor.d/twin-bwrap` | 挂在私有副本上的 profile（要 root） | `twin install` 经 `sudo`（仅需要时） |
| `~/.local/state/twin/<id>/` | 私有数据（持久） | 运行期 |

⚠️ **别把产物放到 `/opt` 或任何 root 属主的位置**：手写版的 `wechat2.sh` 就是这样，结果图标和
脚本都改不动。

---

## 7. 拒绝清单（宁可不做，不做近似）

| 输入 | 为什么拒绝 |
|---|---|
| `--level argument` / `env` | 没过实测：Qt 类程序忽略 `argv[0]`/参数改名，LD_PRELOAD 方案不稳定 |
| 透传区里的 `--` | 会截断 bwrap 的选项解析，后面全被当成程序名 |
| `--argv0` | 窗口 `app_id` 跟真实路径、托盘 `Id` 跟 `argv[0]`；分叉必然有一方找不到桌面项 |
| `--file` `--bind-fd` `--ro-bind-fd` `--bind-data` `--ro-bind-data` `--seccomp` `--add-seccomp-fd` `--block-fd` `--userns-block-fd` `--userns` `--userns2` `--pidns` `--sync-fd` `--info-fd` `--json-status-fd` `--args` | 需要已打开的 fd，而启动器由桌面环境 exec，手里没有 |
| `isolation.unshare` 含 `net` | 切断网络，也打破 abstract-socket 单实例锁；bwrap 本来也隔离不了它 |
| **多合一二进制**（BusyBox / coreutils 之类靠 `argv[0]` 分派行为的） | 改名之后它会报 `unknown program` —— 改名对这类程序是破坏性的 |
| alias 与真实文件名相同 | 那是空操作，改名了但名字没变 |
| 源图标缺失且未给 `--icon` | 拒绝静默生成齿轮占位图 |
| `Exec=` 目标没有执行位 | 生成前就拒（GLib 会因此丢弃整个桌面项，且查不出来） |
| `id` 含 `/`、`\0`，或以 `.`、数字以外开头 | id 同时是桌面项基名与别名文件名，必须干净 |

---

## 8. 典型用法

日常那两行（`twin gen <source>` ＋ `twin install`）在 README 的"30 秒上手"里；选项本身由
`twin <命令> -h` 负责。下面这几组是**要动文件**的用法，也就是 `-h` 演示不了的那部分。

```bash
# 让两份实例彻底分家（连配置目录一起隔离）：映射写进配置，再安装
twin gen wechat --as wx-alt
$EDITOR profile.yml
#   bind:
#     - ~/.local/state/twin/wx-alt/config:~/.config/wechat
#     - ~/.local/state/twin/wx-alt/xwechat:~/.xwechat
twin install

# 一个只想多开小号、其它全共享的浏览器
twin gen google-chrome --as chrome-alt
$EDITOR profile.yml
#   bind:
#     - ~/.local/state/twin/chrome-alt/profile:~/.config/google-chrome
twin install

# 底层的额外隔离（透传）
twin gen wechat -- --unshare-pid
twin install

# 改完实例快照重放（install 与 reinstall 是同一条路）
$EDITOR ~/.local/share/twin/wechat2/profile.yml
twin reinstall wechat2

# 先看要删什么，再删（不带 --yes 会问你一次：回答 y / yes 才动手）
twin remove wechat2
twin remove wechat2 --yes              # 脚本里这么写：不问，直接删
twin remove wechat2 --yes --purge      # ⚠️ 连私有数据一起删，不可逆
```
