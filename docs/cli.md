# 命令参考

```console
$ twin --help
```

---

## 1. 调用形态

```
twin <动词> [选项]                # 动词不带宾语（list / doctor / config）
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
> `install` 的宾语是**实例**：`twin install <id>`（读那个实例自己的定义）或 `-p <文件>`。机器级的
> 那一半走开关：`twin install --ext`。这不是口味问题：实例 id 收 `[A-Za-z0-9._-]`，**任何裸词都是
> 合法的实例 id**，所以宾语位上一个保留字必然与用户能取的名字同形 —— 写成 `twin install extension`
> 的话，那一句装的是扩展，而 `twin remove extension` 删的是一个实例：同一个词在两个动词下指相反的
> 东西。机器级的那一半因此写成开关，而不是宾语。

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
| `非特权 userns` | 内核是否限制；受限时**这台机器上有没有一条走得通的路**（自己装 profile ／ 借 Electron 跳板），判据是后者，不是前者 |
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
  · 应用规则 wechat：内置规则（3 条路径映射 ⇒ bwrap.bind）
```

`显示名` / `说明` 打印的是**语言化**取值（`Name[<当前语言>]` 优先），因为 `wrap` 就是这么
推导的 —— 预览与真实写入必须一致，否则这个工具的预览就是一句谎话。

`/tmp 策略` 说的是这条选择：加 `--tmpfs /tmp` ⇒ 程序看到的是一个空的可写 `/tmp`（宿主看不见，
代价是 `/tmp` 里原本的东西也看不见 —— X11 的 socket 就在里面）；不加 ⇒ 程序用的是**宿主那个
`/tmp`，同样可写**（两个实例与宿主共享它）。两条都不是"没有 /tmp"：只读的 `/tmp` 会让一堆程序
以别处的报错告终（Chromium 是 `Failed to create socket directory`，见
[`troubleshooting.md`](troubleshooting.md) C5）。

`--name` 只影响预览：它会**真的**参与 keywords 的推导（因为你显式给了名字），所以能在不写盘
的前提下看到"命名 ↔ 关键词"的联动结果。

### `twin gen <source>`

**只写一份配置，不安装任何东西。**

一次 wrap 里唯一随程序而变、又猜错了不出声的东西是路径映射（隔离错一个目录，要到两个实例抢同一个
锁文件时才暴露）。所以先把它落成一份能读能改的文件，再安装：

| 命令 | 做什么 |
|---|---|
| `twin gen wechat` | 写一份配置（默认 `~/.local/share/twin/wechat2/profile.yml`；id、显示名、路径映射全部推导出来写进去），然后说清文件落在哪、下一步敲什么 |
| `$EDITOR ~/.local/share/twin/wechat2/profile.yml` | 改 `bwrap.bind` / `desktop.name` / `desktop.keywords` / `identity.level`；不改也行，推导出来的默认值通常够用 |
| `twin install wechat2` | 读那份定义 → 落盘（桌面项 / 图标 / 启动器 / 托盘）＋ 检查托盘扩展 ＋ 自检。`twin reinstall wechat2` 是同一个动作的另一个入口 |
| `twin install --ext` | 装入 shell 侧的托盘扩展（机器级；`install` / `reinstall` 只检查它）。`twin reinstall --ext` 是同一个动作的另一个入口 |

**定义为什么在实例自己的目录里？** 两个理由。一是**找得回来**：它是这份 wrap 里唯一需要人读一遍的
东西，落点必须从下一个 shell、下一台终端、明天都还找得到，而"当前目录"只是你这次站的地方。二是
**一个 id 一份**：包第二个程序时不会撞上第一个的定义，也不会互相覆盖。要把配置放在别处（并排放几份、
提交进仓库、拷到另一台机器），`gen -o <文件>` 指过去，之后用 `install -p <文件>` 读它 —— 那时
实例目录里会另写一份**存档记录**，`reinstall <id>` 重放的是它。

**输出文件已存在就拒绝**（退出码 1），除非 `--force`。撞上默认落点（那个实例的定义已经在了）时，
报错直接给下一步：`twin install <id>` —— 那份文件不是"另一个输出文件"，它就是这个实例。

**头注释跟着落点走**：写在实例目录里的那份指向 `twin install <id>`，`-o` 写到别处的那份指向
`twin install -p <你给的那个路径>` —— 否则它会把你送去读实例目录里那份记录，而不是你手上这份。

```console
$ twin gen wechat
✅ 已写出配置 /home/you/.local/share/twin/wechat2/profile.yml

👉 下一步
  1. 改 /home/you/.local/share/twin/wechat2/profile.yml —— 每个键的说明都写在文件里的注释里（不改也能装）
  2. 安装：twin install wechat2
```

`gen` 的输出刻意短：它只做两件事 —— **只写一个文件**（桌面项、图标、启动器、私有状态目录都等
`install`），并且**把落点指出来**。逐键的说明、`bwrap.bind` 的语义、`rules.d` 的存在，全都写在**那份文件
的注释里**：读到它的时候人正在编辑它，这比在终端里滚过去有用，也不会和文件两处说法各表一套。
要看推导值与**这一份配置的**路径映射一节，加 `--explain`。

终端上只多留一行，给"**一条映射都没有**"的程序：

```console
$ twin gen foo
✅ 已写出配置 /home/you/.local/share/twin/foo2/profile.yml
  ⚠️ 没有隔离任何数据目录：这个程序若有单实例锁（Electron / Qt / GTK 系常见），两个实例会互相干扰 —— 见配置里的 bwrap.bind

👉 下一步
  …
```

它是这个命令唯一可能留下、而别处不会再报的后果（共享状态要到两个实例抢同一个锁文件时才暴露），
所以它留在终端上；其余情况一律不多说。

### `twin install`

**读一份定义，把它变成实例。** 两种指名方式，二选一：

- `twin install <id>`：读那个实例自己的定义（`~/.local/share/twin/<id>/profile.yml`）—— `twin gen`
  之后最自然的下一步，id 就印在 gen 的输出里；
- `twin install -p <文件>`：读你指定的那一份（配置放在别处时用它）。这时实例目录里会另写一份存档记录。

两者同时给、或两个都不给，都是用法错误（退出码 2）：同时给会让其中一个被静默忽略，都不给就只能
猜要装哪个实例。读不到那份定义就直接报错并让你去跑 `twin gen`，不会去猜别的路径。

它**不接受任何描述实例的选项**：包哪个程序、叫什么名字、隔离哪些目录，全都在那份定义里。一个 flag
和一个文件可以互相矛盾，而其中只有一个是用户刚刚花了时间改的那份。

**它不重写那份定义。** `install <id>` 与 `reinstall <id>` 读的是同一个文件，头注释里写着"改完跑
`twin install <id>`"；写回去就是一次解析往返，`yamlz.Parse` 会丢掉你手写的每一条注释（见
configuration.md §4）。只有 `-p` 指到别处时，实例目录里才写一份 `RoleSnapshot` 的存档记录。

```console
$ twin install wechat2
✅ 已安装 wechat2
  桌面项   /home/you/.local/share/applications/wechat2.desktop
  图标     /home/you/.local/share/appicons/wechat2.png
  启动器   /home/you/.local/share/twin/wechat2/launch
  实例定义 /home/you/.local/share/twin/wechat2/profile.yml
  托盘覆盖 已写入 /home/you/.local/share/twin/tray.d/wechat2.json（助手扩展读它画图标，不再写宿主表）
  托盘扩展 twin-tray@edwinhuish.github.io 已启用 —— 托盘图标由它接管

📁 路径映射
  3 条，来源：配置 /home/you/.local/share/twin/wechat2/profile.yml
  私有  ~/.local/state/twin/wechat2/xwechat:~/.xwechat

👉 下一步
  1. 桌面项是后写的 —— 请**重新启动**这个实例，否则窗口还是旧的名字和图标。
  2. 确认身份与托盘：twin verify wechat2
     改了实例定义要重放：twin reinstall wechat2
```

安装阶段的输出也刻意短：产物清单 ＋ `路径映射` ＋ 下一步。"要动手时才需要"的东西（规则模板全文、
候选状态目录、改哪个文件、推导值、手工验证命令）用 `--explain` 展开。`--dry-run` 是**完整落盘**的
预演 —— 桌面项正文、定义正文（定义已在原处时不重写，预演会明确说出来）、bwrap 调用、托盘将写入的
那一行全都在，而且由真实落盘用的**同一份代码**渲染。

托盘那半段是个**二选一**，判据只有一个：用户扩展目录下的
`twin-tray@edwinhuish.github.io/` 是否**在 shell 的启用列表里、且不在它的禁用列表里**（两个列表都读得到
才算数）。在 ⇒ 写扩展读的那份文件表（`~/.local/share/twin/tray.d/<id>.json`），并把宿主覆盖表里本实例的
那一行删掉；不在（未装 / 未启用 / 被禁用列表挡住 / 读不到列表）⇒ 写宿主覆盖表兜底。两条路**只写一条**，
而**走了哪条每次都打印**：同一个图标不留两个来源，也不该让人靠猜。这就是那句"回退到 custom-icons"
的全部含义 —— 兜底仍是它，首选换成了扩展自己画。

`twin install` / `twin reinstall` **只检查**那份扩展，**不写它** —— 那一件是机器级的，见本节末尾那条命令。
理由不是"顺手多写个文件"：一份扩展服务所有实例、比实例活得久，挂到实例安装上就成了"包一个实例"的副作用，
每次重放定义还会重做一遍。写入它是幂等的（逐字节比对后才写，因为 shell 只看 mtime），**启用它**却要等
shell 下次扫扩展目录（注销重登一次）；那一步连同原因一起打印，但**不改退出码**：它是可选增强，没它图标
也在，只是小一圈（那一行因此在 `🔍 自检` 里是 ⚠️，不是 "还差一步"）。装完扩展**不会**让已有
实例自动搬家：它们在下一次 `install` / `reinstall` 时才改走扩展那条路（否则屏幕上那个"小一圈"的图标会
像个新 bug）。机制见 [`architecture.md`](architecture.md) §7.1。


**自检只在有问题时才开口。** 那几条 `✅` 确认的正是这一次刚刚写下的文件，把它们列出来是产物清单
和下一步之间的一页废话；因此默认只打印非 OK 的项，并附上修它的命令：

```console
🔍 自检
⚠️ 托盘图标                      custom-icons 里没有 "wechat2" 的条目（托盘会继续显示应用自己的图标；助手扩展已装入但未启用）
                          → twin reinstall wechat2
```

`--explain` 是唯一的例外：那里要的就是完整报告，于是整份自检（和 `twin verify` 一样的那些行）
都会打印出来。

`twin verify` 是另一回事：把每一条都列出来正是它的职责，跳过项（本机没这个能力）也在里面。

**一份文件、两个入口。** `install <id>` 与 `reinstall <id>` 读的是同一个文件
（`~/.local/share/twin/<id>/profile.yml`），所以改完它再跑哪一个都一样，而两者都不会去改动它。
只有 `-p <文件>` 那条路（定义放在别处）会让 `install` 在实例目录里再写一份存档记录，供
`reinstall <id>` 重放 —— 那份才是"上次到底装了什么"的存档。

路径映射是这份产物里**唯一随程序而变、且猜错了不出声**的部分：隔离错一个目录，要到两个实例
抢同一个锁文件时才暴露。两个命令各有各的交代方式：`gen` 把它写进配置的 `bwrap.bind`（连同"空着意味着
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
$ twin install wechat2
  🔐 需要 root 装一份 AppArmor profile
      将写 /etc/apparmor.d/twin-bwrap（twin-bwrap 挂到 bwrap 副本上）。sudo 会要求输入密码 ——
      不想给就 Ctrl-C，这一步跟着跳过，下面那一段会说明这次改走哪条路。

[sudo] password for you:
  ✅ /etc/apparmor.d/twin-bwrap 已写出并用 apparmor_parser 加载，内核的已加载列表里也能读到 twin-bwrap

🧱 沙箱方案
  · /etc/apparmor.d/twin-bwrap 里的 profile twin-bwrap 挂在 /home/you/.local/share/twin/bin/bwrap 上：
    私有副本可以直接建 user namespace，不需要跳板
```

问不到密码时它不碰 `sudo`，而是重新选一次路：

```console
$ twin install wechat2            # 标准输入不是终端
  本机需要 AppArmor profile（要 root），但标准输入不是终端，没法让你输密码：跳过自动安装
🧱 沙箱方案
  · 没装 profile：退回借一个已安装 Electron 的标签
  · 跳板 /usr/share/code/code（profile vscode，Electron）
```

三条纪律：

- **只在终端上问密码。** 标准输入不是终端（脚本、管道、CI）时不碰 sudo：这一步跳过，然后按"这台
  机器上还没有 profile"重新选路 —— 有 Electron 跳板就借它的标签（**不需要任何权限**），一个都没有
  才退回打印 profile 全文并以退出码 3 结束。那是这个工具从第一天起就有的诚实行为，不是失败。
- **干的事就是打印出来的那两条命令**：写 `/etc/apparmor.d/twin-bwrap`、`apparmor_parser -r`。没有
  别的系统改动，没有 AppArmor 之外的配置，也没有常驻进程。
- **回读才算数。** `apparmor_parser` 返回 0 只说明内核收下了；twin 还会读
  `/sys/kernel/security/apparmor/profiles` 确认 `twin-bwrap` 真在列表里。那台机器不让非 root 读这份
  列表时，它明说"只确认到退出码"，而不是把查不到当成查过了。

**这一步是首选路线，跳板是它的兜底。** 受限机器上 `install` 先瞄自己的 profile：装成了，
`sandbox.Resolve` 会认出它（判据是 `/etc/apparmor.d/twin-bwrap` 里的附着路径）⇒ 启动器里写的就是
私有副本，**不再需要跳板**，重装、重启都不会再问一次密码；装不成（没 `sudo`、没终端、或那一次没
成）才重新解析一遍、退回跳板，并由 `🧱 沙箱方案` 那一段说清走的是哪条。两条都不行才打印 profile
全文。`twin reinstall <id>` 走的是同一条路：一台机器需要这一步，就无论在装还是在重放时需要它。

`--dry-run` 只报告这一步、绝不碰 `/etc`：它停在"首选路线"那个答案上，因为那才是真装时会走的路
（预演要演的是成功的样子，而不是自己给自己预言一次失败）。

`--explain` 把这一节再摊开一点，并附上一份照抄即可的规则模板：

```console
📁 路径映射
  3 条，来源：配置 /home/you/.local/share/twin/wechat2/profile.yml
  私有  ~/.local/state/twin/wechat2/xwechat:~/.xwechat
  共用  ~/Documents/xwechat_files:~/Documents/xwechat_files   （原地升回可写，数据不搬家）
  私有  ~/.local/state/twin/wechat2/all_users:~/Documents/xwechat_files/all_users
  改：~/.config/twin/rules.d/wechat.toml —— 影响这个程序以后新建的实例（twin rules --init wechat 生成草稿）
      只改这一个：编辑 /home/you/.local/share/twin/wechat2/profile.yml 里的 bwrap.bind，再 twin install wechat2

  📋 照抄下面这份（就是规则文件的全部内容）：

    app = "wechat"

    bind = [
      "~/.local/state/twin/{id}/xwechat:~/.xwechat",
      "~/Documents/xwechat_files",
      "~/.local/state/twin/{id}/all_users:~/Documents/xwechat_files/all_users",
    ]

  源:目标 ⇒ 私有副本，两个实例互不干扰；单写一条路径 ⇒ 与该目录共用，只把只读升回可写；
  源:目标:ro ⇒ 只读挂载；源:目标:dev ⇒ 带设备访问（--dev-bind，给设备节点目录用）。
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
      只改这一个：编辑 /home/you/.local/share/twin/foo2/profile.yml 里的 bwrap.bind，再 twin install foo2
```

#### 机器级的那一份：`--ext`（`install` 与 `reinstall` 同形）

托盘扩展**不是**一件实例产物：一份服务所有实例，而且比任何一个实例活得久。所以它**不由
`twin install` 写入** —— 否则"包一个实例"这个动作会顺手改掉一个与它无关的东西，而且每重放一次
就重做一遍。它归 `install` 的一个开关 —— 而 `reinstall` 也认这个开关，因为「装一份定义」与
「把它再放一遍」本来就是同一件事的两个入口（两者共用 `installAndCheck`）：机器级那一半若只在一边
够得着，那「有哪些命令」就取决于你走的是哪扇门，而不是取决于机器上缺什么。

```console
$ twin install --ext        # 或 twin reinstall --ext —— 同一段代码
✅ 托盘扩展 twin-tray@edwinhuish.github.io
  文件     /home/you/.local/share/gnome-shell/extensions/twin-tray@edwinhuish.github.io（本次写入／更新）
  作用     托盘图标由它自己画（读 twin 写的那份表，不写宿主扩展的 dconf 键）
  启用     还没有 —— 在装的当下做不到（扩展"twin-tray@edwinhuish.github.io"不存在）

🔔 让它生效还要三步（可选增强：不做也不影响任何实例的托盘图标）
  1. 注销重登一次：shell 只在启动时扫一遍扩展目录，它现在还不认识这个 uuid。
  2. gnome-extensions enable twin-tray@edwinhuish.github.io
  在此之前，各实例仍走宿主覆盖表兜底 —— 图标是对的，只是比面板图标小一圈。
  3. 重登之后，让已装的实例改走这条路：twin reinstall <id>（一条命令一个实例）。
```

`install` / `reinstall` 那边只**检查**它：缺了就在 `🔍 自检` 那一段打一条 ⚠️，并给出这条命令。
扩展是**可选增强**，所以它**不占退出码** —— 底下那条兜底的路一直在画图标，只是小一圈；把可选项混进
"必须人做"那个码里，脚本就分不出"回来再跑一次"和"你坏了"。再装一次是幂等的：文件逐字节比对，内容一样就
整个跳过（shell 只看 mtime，白碰它反而像是有新版）。`--dry-run` 只报会写到哪几个文件。

这个开关**换掉的是首选路径，不是唯一路径**：装了扩展并且 shell 加载了它，实例的托盘覆盖走扩展读的那份
文件表（不需要会话总线）；没装 / 没启用 / 被 shell 的禁用列表挡住 / 列表读不到，就还是写宿主那个 dconf 键
—— 也就是"写不进去要会话总线"那条老路。判据、两条路的产出与"装了之后为什么图标还是小的"都在上面
`twin install` 那一节。

### `twin verify <id>`

自检一个**已安装**实例的身份链路。**重新读硬盘**，不信内存里的期望。

实例目录在、桌面项却不在（`gen` 写完定义还没装，或上一次安装死在最后一步）时，它不吃一遍连锁失败，
而是把这一件事说清楚并给出收尾命令，退出码 `1`：

```console
❌ wechat3 的桌面项不在：这个实例还没落盘（定义 /home/you/.local/share/twin/wechat3/profile.yml 在，桌面项 / 图标 / 启动器不全），或者上一次安装没走完
  落盘：twin install wechat3（等价的重放入口：twin reinstall wechat3）
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
| 托盘图标 | **按 install 用过的同一个判据**只查那条路：扩展被加载 ⇒ 查它读的那份文件表（条目在不在、指得对不对、宿主表里那一行删没删）；没加载 ⇒ 查宿主表。两个方向都报，因为"多了一条"和"少了一条"同样是两个来源在抢一个托盘项 |
| 托盘扩展 | 装了没 / 启用了没 / 有没有被 shell 的禁用列表挡住（这三条决定上一条查的是哪半边；修法永远是 `twin install --ext` —— 它不属于某一个实例） |
| L5 规则文件 | `level=redirect` 时缺文件或 JSON 非法 |
| 嵌套 user namespace | 副本不在 ⇒ WARN；启动器里的跳板不见了 ⇒ FAIL、不再是 Electron ⇒ WARN。**启动器里没有跳板不是问题** —— 那是"自己装 profile"这条路的正常样子，副本上那份 profile 会被当作凭据写出来 |

`嵌套 user namespace` 那一项会解析**启动器文本**，确认跳板确实在启动器里 —— 因为跳板被卸载时，
定义里的每条路径都还在，别的检查全都抓不到。

### `twin list`

列出全部实例。

```console
📋 实例
id               level      别名     程序                     桌面项
wechat2          alias      wechat2  wechat                   wechat2.desktop
foo3             alias      foo3     foo                      foo3.desktop  ← 还没落盘

⚠️ 有 1 个没有桌面项（刚 gen 出来还没装，或上次安装没走完）：twin install <id>

删除某一个：twin remove <id>（不带 --yes 会先列出来问你一次）
```

`← 还没落盘` 不是"桌面项被谁删了"，而是这个实例**还没装**（`gen` 写了定义就走了），或者上一次
`install` 死在半路。两种情况都由 `twin install <id>` 收尾 —— 它按那份定义把整件事做一遍。

### `twin reinstall <id>`

按**实例目录里那份定义**重新落盘。它与 `twin install <id>` 是同一件事的两个入口：同一个文件、同一段
代码（`installAndCheck`），说得出区别的只有意图 —— "把这个实例重来一遍"。

它只认 `<id>`：那份定义可能已经被拷走、提交进仓库、或者 `-o` 时压根不在这个位置，而 `reinstall` 要的
是确定性。要按新配置更新实例，改那份定义再跑它（或 `twin install <id>`）。

**默认只读那份定义**，因为重放必须是确定性的：碰一下 `config.toml` 不该静默改写一排实例。`-h` 里那个
开关是"再读一遍 `config.toml` / `rules.d`"的显式出口，而且它**只做加法与填空**（逐字段的行为见
[`configuration.md`](configuration.md) 的优先级一节）。补上的值只用在这一趟落盘上：那份文件是你的，
重放不会去改写它（`-h` 里也这么写着）。

若今天的会话与定义里的决定矛盾（例如记着"走 Wayland"而当前只有 X11），会**警告**而不是默默重放。

带 `--ext` 时它**不重放任何实例**，而是装那份机器级助手扩展 —— 与 `twin install --ext` 同一段代码，
所以也不接受 `<id>`（它不属于任何实例）与 `--refresh`（那个开关读的是定义）。`--dry-run` 反过来只在
`--ext` 下有意义：重放是确定性的，写下的字节与上次那份 install 写的相同，没有可预演的东西。

### `twin remove <id>`

删除产物。默认先列出将要删的文件（存在的打 `✅`、不存在打 `➖`）**再问一次**：回答 `y` / `yes`
（大小写都行）才动手，其余任何回答 —— 直接回车、拼错、Ctrl-D —— 都按"不删"处理。

- 标准输入不是终端（脚本 / 管道 / CI）时**不提问**，只预览并给出那条显式命令。提问没人能回答就是
  卡住，这和装 AppArmor profile 那一步是同一条规矩。
- `--yes` 是不提问的那个开关（脚本里用它），私有数据要不要一起删是 `--purge` —— 两个开关的语义在
  `-h` 里。
- 还没装、或安装没走完的实例也照删 —— 那份定义（可能还有半套产物）就在那儿，撤销它就是把那个目录清掉。
- 私有状态目录默认保留（那是唯一不可逆的部分），保留的那份下次同名重建会接着用。
- 会顺带移除**两处**托盘声明：宿主表里该 `id` 的条目（表是全局的），以及扩展读的那份文件表
  （`~/.local/share/twin/tray.d/<id>.json`）。两条都是"一个即将不存在的程序"的声明 —— 留着会继续
  把托盘指向刚被删掉的图标，也就会让扩展每次重读都拿到一张不存在的图。

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
| **描述实例** | 会进 `profile.yml` | 显示名、角标、`--level`、`--tmpfs`、`--no-tray` |
| **描述这次动作** | 只影响这一次调用 | 写到哪、要不要覆盖、`--dry-run`、`--explain` |

只有 `gen` 拥有第一类。`install` 的选项表里一个实例选项都没有，所以"带着描述实例的选项去装一个已存在
的实例"这件事**无法表达** —— 它不是一条运行时判断，而是动词拆分本身：包哪个程序、隔离哪些目录，全在
它读的那份文件里。

**路径映射没有 CLI 选项**，两条路都是文件：`rules.d/<程序>.toml` 管这个程序**以后新建**的实例，手上
那份配置的 `bwrap.bind` 管**这一个**。一条映射正是最该在安装任何东西**之前**被读一遍的东西，而命令行参数
没人会回头再看第二眼 —— 所以它先落成文件（见 [`configuration.md`](configuration.md)）。

`--` 透传区的形式、边界与它写进哪个键，都在 `-h` 里（它不是 flag，PrintDefaults 打不出来，所以那几行
是手写的）。这里只留 `-h` 装不下的那条**理由**：它写进实例定义 `bwrap.args` 的**尾部**（基座里没有
自己节点的那几条与 config/rules 层之后），所以同一份列表里同目标的挂载由**它**胜出（bubblewrap
自己就是"后挂载遮蔽前者"）—— 它不是一个"额外选项"，而是这几行里最终说话的那几个 token。而
`bwrap.args` 整份又排在路径映射**之前**，于是两件事同时成立：映射覆盖基座，`--` 覆盖基座。
⚠️ 边界只有这一份列表：基座里有自己节点的那几条（`bwrap.proc` / `bwrap.tmpfs` / `bwrap.env`）由
`Build` 发在它**之后**，在 `--` 里写一条同目标的挂载压不住它们 —— 想改就改那个键。校验只到 lint
为止：改写它等于替用户猜他要覆盖什么。

**基座就在那份定义里，而且分节点写**（`gen` 写的 `bwrap` 节点，多数在各自同名的键上，见
[`configuration.md`](configuration.md) §4）—— 这正是"不要偷偷给 bwrap 加参数"的落点：想换根
（比如把 `--dev-bind / /` 换成 `--ro-bind / /`）就是改那份定义里的一行，不必去猜 twin 还加了什么。

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
| `0` | 成功。**包括事实性跳过**：`--no-tray` / 整机 `tray_override = false` / 这台机器根本没有托盘宿主（`tray_override = false` 时托盘扩展也一并跳过） |
| `1` | 失败，或自检里有 ❌ |
| `2` | 用法错误（未知子命令、未知选项、非法 `--level`、`--` 之后不是 bwrap 选项、`-o` 与 `--output` 同时给出、`HOME` 未设置…） |
| `3` | **身份已经成立，但还差一步必须由人执行。** 目前两个来源：<br>① 托盘覆盖走宿主表那条路而表写不进去（无会话总线 —— 安装脚本、无图形会话；走托盘扩展那条路时不需要它，扩展读的是一份文件）<br>② 内核限制 `userns`，而 twin 既没能给副本装成那份 profile（没 `sudo`、没终端问密码、或那一次没成）、也没有可借标签的 Electron 跳板 ⇒ profile 全文已经打印给你<br>托盘扩展**不在**这里：它是可选增强，装好而 shell 还没扫到、或者干脆没装，都只是 ⚠️（`install` / `reinstall` 的 `🔍 自检`、`verify`、`doctor` 各报一行），退出码仍是 `0` —— 兜底那条路照旧画图标 |

用法错误**只打印那一行错误**，不复述整份帮助：帮助的出口是 `twin --help`，某个命令的选项表是
`twin <命令> -h`（退出码 `0`，那是提问不是犯错）。把一份 `usage` 倒在一处笔误下面，只会把真正
有用的那一行顶出屏幕。

**裸 `twin`（一个参数都没有）按退出码 `0` 收尾**：它和 `twin --help` 是同一件事 —— 没有动作可做，
能回答的只有"这个工具是什么"，而这里一个字都没写错。把它记成用法错误，会让 `twin || …` 这种写法
把一次提问报成故障。

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
| `~/.local/share/twin/<id>/profile.yml` | 实例定义：你编辑的那一份（`-o` 可以指到别处，那时用 `install -p <文件>` 读它） | 生成器（`twin gen`），你改，再 `twin install <id>` 或 `twin reinstall <id>` |
| 同上，定义在别处时 | 存档记录：`install` 按你指定的那份配置写出，`reinstall <id>` 重放它 | 生成器（`twin install -p <文件>`） |
| `~/.local/share/twin/bin/bwrap` | 私有 bwrap 副本（共享，一台机器一份） | 生成器（仅需要时） |
| `~/.local/share/twin/bin/bootstrap.js` | 跳板脚本（共享） | 生成器（仅需要时） |
| `~/.local/share/twin/tray.d/<id>.json` | 中立托盘文件表：扩展读它画本实例的图标（**首选路径**；扩展不可用时这份文件不写） | 生成器（`twin install` / `twin reinstall`），`twin remove` 删掉 |
| `~/.local/share/gnome-shell/extensions/twin-tray@edwinhuish.github.io/` | 托盘扩展：画自定义托盘图标（读上面那份表）＋ 让它跟随面板尺寸（宿主把它钉死在 16px，见 architecture.md §7.1）。**机器级**：一份服务所有实例 | 生成器（`twin install --ext` / `twin reinstall --ext`，**不是** `twin install`） |
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
# 让两份实例彻底分家（连配置目录一起隔离）：映射写进定义，再安装
twin gen wechat --as wx-alt
$EDITOR ~/.local/share/twin/wx-alt/profile.yml
#   bwrap:
#     bind:
#       - ~/.local/state/twin/wx-alt/config:~/.config/wechat
#       - ~/.local/state/twin/wx-alt/xwechat:~/.xwechat
twin install wx-alt

# 一个只想多开小号、其它全共享的浏览器
twin gen google-chrome --as chrome-alt
$EDITOR ~/.local/share/twin/chrome-alt/profile.yml
#   bwrap:
#     bind:
#       - ~/.local/state/twin/chrome-alt/profile:~/.config/google-chrome
twin install chrome-alt

# 底层的额外隔离（透传）
twin gen wechat -- --unshare-pid
twin install wechat2

# 改完定义重放（install 与 reinstall 是同一条路，读的是同一个文件）
$EDITOR ~/.local/share/twin/wechat2/profile.yml
twin reinstall wechat2

# 先看要删什么，再删（不带 --yes 会问你一次：回答 y / yes 才动手）
twin remove wechat2
twin remove wechat2 --yes              # 脚本里这么写：不问，直接删
twin remove wechat2 --yes --purge      # ⚠️ 连私有数据一起删，不可逆
```
