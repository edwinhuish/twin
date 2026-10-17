# twin

> 给**已有**程序一个独立身份：它自己的窗口名、自己的托盘图标、自己的私有数据目录 ——
> 不碰原程序的一个字节，不需要 root，不需要守护进程。

`twin` 是一个 Go 写的命令行工具，用于在 Linux 桌面（GNOME / Wayland 优先）上把任意一个
已安装程序"再包一层"，让它以**另一个身份**运行。典型用途：同一个程序开两份互相隔离的实例
（两个账号、两套配置、两个任务栏图标）。

```console
$ twin gen wechat       # 写出一份实例定义 ~/.local/share/twin/wechat2/profile.yml
$ twin install wechat2  # 读它、落盘，然后自检这条身份链路的每一环
```

最短可用命令就是上面这两行 —— id、角标、状态路径、沙箱基座全部从源程序的 `.desktop` 与二进制里
推导出来，落盘前**逐条打印来源**，都可用选项覆盖。剩下那两样要改的东西也不靠选项：**实例定义**
（隔离哪些目录、怎么跑）是 `gen` 写的那份文件，**桌面项正文**（菜单里显示什么）由 `install` 生成
一次、之后归你 —— 改菜单里那几个字就是编辑它、再 `twin reinstall`。

---

## 它解决什么问题

一个程序在桌面上"看起来是谁"，由三条互不相干的通道决定，而这三条通道**没有任何一个**
听命令行参数的：

| 通道 | 由什么决定 | 谁在读 |
|---|---|---|
| 窗口身份 `app_id` | Wayland 下是 `/proc/self/exe` 的 **basename**（X11 下是 `WM_CLASS`） | GNOME Shell 的窗口→应用归属、任务栏图标合并 |
| 托盘身份 `SNI Id` | `argv[0]` 的 **basename** | 托盘宿主（`ubuntu-appindicators`）的图标查找表 |
| 搜索 / 显示名 | 桌面项文件的 `Name=` / `Keywords=` / 图标 | GNOME 应用网格、搜索 |

所以"改个名字"这件事，唯一的通用做法是**让程序真的从另一个路径名启动**，再补一个同名
的桌面项。`twin` 把这件事自动化了，用的是 bubblewrap 的零拷贝 overlay：原目录一个字节
都不改，只在命名空间里多出一个别名文件。

## 它不做什么

诚实地划边界，是这个工具设计的一半。以下事情 `twin` **不会**做，也**不假装**做了：

- ❌ 不修改、不复制原程序的目录（overlay 是零拷贝、只在命名空间内）
- ❌ 不创建 systemd 单元，不写 `/etc` 下除**一份 AppArmor profile** 以外的任何东西 —— 那份是
  "内核不放行非特权 userns、机器上又没有跳板"时的唯一退路，`twin install` 会问你密码去装它，
  **不给密码就用不了它**（那时打印 profile 全文，你自己决定）。
- ❌ 不隔离网络（`isolation.unshare` 里出现 `net` 会被直接拒绝）
- ❌ 不做"权限沙箱" —— 它给的是**身份**，不是安全边界。定义里的根就是整个宿主根
  （`bwrap.root: dev`，就是 `--dev-bind / /`，可写、设备可用；想收紧改成 `ro`）：沙箱在这里的职责是
  "换名字 ＋ 分开数据目录"，不是拦住不受信任的代码
- ❌ 不提供 GUI（为什么不做，见 [`docs/architecture.md`](docs/architecture.md) §7）

## 30 秒上手

```bash
# 0. 先自检这台机器能不能干活，输出会告诉你下一步敲什么
twin doctor

# 1. 只看不写：这个程序会被包成什么样
twin inspect wechat

# 2. 写定义：~/.local/share/twin/wechat2/profile.yml
#    每个键的说明都写在它的注释里（命令结束时报出这个路径与下一步敲哪条命令）
twin gen wechat

# 3. 改（可选）：`bwrap.bind` 那一节最容易猜错
$EDITOR ~/.local/share/twin/wechat2/profile.yml

# 4. 安装 ＋ 自检，然后从桌面项重新启动这个实例（⚠️ 见下面"为什么必须重启"）
twin install wechat2
twin verify wechat2

# 5. 想让菜单里显示别的字：这一步会生成桌面项正文
#    （~/.local/share/twin/wechat2/wechat2.desktop，路径就印在上面那条命令的输出里），
#    改它，再 reinstall 一次把它复制过去
$EDITOR ~/.local/share/twin/wechat2/wechat2.desktop
twin reinstall wechat2
```

一份定义可以放别处、可以有几份（多实例、多台机器共用）：

```bash
twin gen wechat -o ~/wraps/wx-work.yml
twin install -p ~/wraps/wx-work.yml
```

**为什么是两步**：一次 wrap 里唯一随程序而变、又猜错了不出声的，是"隔离哪些数据目录"。所以先把它
落成一份能读能改的 `profile.yml`，再安装 —— 而不是先装好、再在一屏输出里让你事后发现映射是错的。
默认落点是**实例自己的目录**（`~/.local/share/twin/<id>/profile.yml`），理由的另一半是**找得回来**：
它是这份 wrap 里唯一需要人读一遍的东西，而"当前目录"只是你这次站的地方 —— 换个 shell 就找不到了。
一个 id 一份落点也意味着包第二个程序不会撞上第一个的定义。想并排放好几份（diff、提交进仓库、拷到
另一台机器）就用 `-o`，然后以 `-p` 读它。

安装后你会发现多了这些东西：

```
~/.local/share/applications/wechat2.desktop     ← 身份锚点（逐字复制下面那份正文）
~/.local/share/appicons/wechat2.png             ← 带角标的图标
~/.local/share/twin/wechat2/launch           ← 薄启动器（bash，exec bwrap）
~/.local/share/twin/wechat2/profile.yml      ← 实例定义（`gen` 写下的那份，`install` 读的就是它，不改写）
~/.local/share/twin/wechat2/wechat2.desktop  ← 桌面项正文（`install` 生成一次，之后归你；install 只复制）
~/.local/state/twin/wechat2/                 ← 这个实例的私有数据目录
```

**为什么必须重启实例**：桌面项是**后写的**，而 GNOME 不会给一个**已经存在**的窗口重算
归属。生成完之后要**完全退出**再从这个新桌面项启动一次，才会看到新的图标与名字。

**托盘图标走两条路，一次只走一条**。首选是自带的那个小扩展
（`~/.local/share/gnome-shell/extensions/twin-tray@edwinhuish.github.io/`）：它被 shell 加载时，`twin`
把图标写进一份**中立文件表**（`~/.local/share/twin/tray.d/<id>.json`），由扩展在 shell 里画上去 ——
顺带解决了宿主的尺寸 bug（自定义图标的尺寸由宿主在 JS 里写死的 actor 框决定，`PANEL_ICON_SIZE` 在
gnome-shell 49 已删 ⇒ 恒 16px，外面补不了）。扩展**没被加载**（未装 / 未启用 / 被 shell 的禁用列表挡住 /
读不到列表）时回退到**宿主自己的覆盖表**（`custom-icons` 那个 dconf 键），也就是老路子：图标是对的，
只是比面板图标小一圈。走哪条、为什么，`twin install` 每次都会打印出来；两条**不会同时写**，切过去时
还会把宿主表里那一行删掉。

那份扩展**是机器级的**（一份服务所有实例、比实例活得久），因此**不由 `twin install` 写入**：
`install` / `reinstall` 只检查它，缺了就给出那一条命令 —— `twin install --ext`。装它是文件写入（幂等），
**启用它**要等 shell 下次扫扩展目录，也就是**注销重登一次**；那个命令会把原因一起打印出来，并因此以
退出码 3 收尾。机制：[`docs/architecture.md`](docs/architecture.md) §7.1。


**数据目录也值得看一眼**：`twin install` 会打印一节 `路径映射`，说明这次重定向了哪些目录、这份
知识从哪来（内置规则 / `rules.d` / 这份配置）。不同程序的数据目录完全不同，而隔离错一个目录要等到
两个实例抢同一个锁文件才暴露。**改它该编辑哪个文件**（`rules.d` 管这个程序**以后新建**的实例，
手上这份配置管**这一个**）和"还有哪些目录值得映射"是 `--explain` 里的内容 —— 它们是"这条映射错了"
之后才需要的东西，而默认输出的前提恰恰是它没错。改完那份定义重装一次：`twin install <id>` ——
`twin reinstall <id>` 读的是同一个文件、走的是同一条安装路径，敲哪个都行。

**输出默认是精简的**：`twin gen` 只报文件路径与下一步，`twin install` 报产物清单 ＋ `路径映射` ＋
下一步，都是一屏能看完的。自检只在**有问题**时才开口（那几条 `✅` 确认的正是这一次刚写下的文件），
并且带上修它的命令；`twin verify` 才是把每一条都列出来的地方。其余细节都要 `--explain`（或
`--dry-run`）才展开。报错时只打印那一行错误 —— 整份帮助的出口是 `twin --help`，逐命令的选项表是
`twin gen -h`。颜色只在终端上出现 —— 重定向、管道、`TERM=dumb` 一律纯文本，`NO_COLOR` 强制关掉、
`CLICOLOR_FORCE=1` 强制打开。字形（`✅` `⚠️` `❌` `➖`）与颜色表达同一件事，所以日志里也读得出来
哪一行失败。

陌生程序不知道该映射什么？`twin gen --explain` 会把这台机器上**真的存在**的 `~/.config/<程序名>`
之类目录列出来（只列，不套用）；`twin rules --init <source>` 则把探测结果写成一份规则草稿，其中
"目录真的存在"的几条生效、"二进制里有这个字符串"的几条注释掉，等你逐条确认。

## 原理速览

```
twin gen wechat                  ← 前半段：只配置
      │
      ├─ 1. 解析源桌面项 → 找到真实程序 /opt/wechat/wechat、图标、语言化的 Name/Keywords
      ├─ 2. 推导默认值   → id=wechat2  badge=2  bwrap.root=dev  等
      ├─ 3. 选身份级别   → alias（bwrap overlay 改名）。程序若写死 app_id，则 redirect
      ├─ 4. 从内置规则 / rules.d 算出路径映射
      └─ 5. 写出一份文件到实例自己的目录（~/.local/share/twin/<id>/）：profile.yml（定义），
            报出它的路径与下一步敲什么

twin install <id>                ← 后半段：按那份定义安装（reinstall 也走这条路）
      │
      ├─ 6. 解析沙箱方案 → 本机要不要私有 bwrap 副本 / 要不要跳板。
      │                    两者都没有、而程序又要嵌套 ns 时：用 sudo 装一份 AppArmor profile
      ├─ 7. 落盘 5~7 件产物（幂等）。那份定义**不重写**（改写会丢手写的注释）；桌面项正文不
      │                    在时按源桌面项生成一份（`--dry-run` 印的就是它），在时只**逐字复制**
      │                    （写回一次就等于把你的编辑回退掉）；只有定义放在别处（`-p`）时，
      │                    才在实例目录里写一份存档记录
      ├─ 8. 托盘覆盖：助手扩展被 shell 加载了 ⇒ 写它读的文件表，并删掉宿主表里那一行；
      │                    没加载 ⇒ 写宿主托盘覆盖表兜底（两条都热生效）
      │                    ＋ 检查托盘扩展本身（缺了会提醒装它：twin install --ext，
      │                       装完需注销重登一次才启用）
      └─ 9. 自检身份链路（target/别名/桌面项/图标/启动器/规则/嵌套 ns）
```

`launch` 最终长这样（`level=alias`，无嵌套 ns 需求时）：

```bash
#!/bin/bash
set -euo pipefail
: "${HOME:?twin: HOME 未设置，拒绝启动}"
readonly ID=wechat2
readonly STATE="$HOME/.local/state/twin/wechat2"

# 挂载点：两端都得先在宿主机上存在，形态按磁盘上已有的那一端（见下面第 4 条）
mount_point dir "$HOME/.local/state/twin/wechat2"
mount_pair "$HOME/.local/state/twin/wechat2/xwechat" "$HOME/.xwechat"
mount_pair "$HOME/Documents/xwechat_files" "$HOME/Documents/xwechat_files"
mount_pair "$HOME/.local/state/twin/wechat2/all_users" "$HOME/Documents/xwechat_files/all_users"

exec /usr/bin/bwrap \
  --dev-bind / / \
  --overlay-src /opt/wechat \
  --tmp-overlay /opt/wechat \
  --ro-bind /opt/wechat/wechat /opt/wechat/wechat2 \
  --bind "$HOME/.local/state/twin/wechat2/xwechat" "$HOME/.xwechat" \
  --bind "$HOME/Documents/xwechat_files" "$HOME/Documents/xwechat_files" \
  --bind "$HOME/.local/state/twin/wechat2/all_users" "$HOME/Documents/xwechat_files/all_users" \
  --proc /proc \
  --tmpfs /dev/shm \
  --tmpfs /tmp \
  --bind /tmp /tmp \
  --die-with-parent \
  --setenv TWIN_INSTANCE wechat2 \
  -- \
  /opt/wechat/wechat2 "$@"
```

（上面省掉了两个挂载点助手的正文 —— 生成器会把它们的定义连同理由一起写进脚本，每条映射一行调用；
`mount_point` 建一个挂载点，`mount_pair` 建一条映射的两端，根不建，它两端都是 `/`。这些 token
全部**来自 `profile.yml`**：`--dev-bind / /` 是 `bwrap.root: dev` 那一个词，三条 `--bind` 是
`bwrap.bind` 里的路径映射，`--proc` 到 `--die-with-parent` 那几行就是 `bwrap.extras` 的内容
（**行序即命令序**），`--setenv` 是 `bwrap.env` 里那几条加上 twin 自己的 `TWIN_INSTANCE`（它不在
定义里：这条变量说的就是"这次调用在哪个实例的沙箱里"，启动器靠它认出"已经在自己的沙箱里"，见
[`architecture.md`](docs/architecture.md) §5）。想换根、想加减隔离，改的就是那份定义。）

几处关键点，每一条都对应一次实测：

1. `--overlay-src` 必须排在 `--ro-bind … 别名` **之前**，否则挂载点建不出来；
2. 根挂载排在**所有映射之前**：映射要能覆盖基座，就不能被基座覆盖（bubblewrap 的规则是"后挂载
   遮蔽前者"）。它写在 `bwrap.root` 里，位置由生成器保证 —— 它在哪个键里都一样，永远第一条；
   反过来 `bwrap.extras` 那份列表整份排在**所有映射之后**（所以其中 `--remount-ro` 收得紧、宿主
   `/tmp` 那行也压得住上面那条 `--tmpfs /tmp`），而它内部的行序就是文件里的行序；
3. 程序名写成 `/opt/wechat/wechat2`（别名，只在命名空间里存在）⇒ `/proc/self/exe` 的
   basename 就是 `wechat2`，`argv[0]` 默认也是它 ⇒ **窗口与托盘的 id 天然一致**；
4. 每个 bind 的**两端**都要先在宿主机上存在。`--dev-bind / /` 之后 bwrap 其实建得出来，但它会把
   挂载点建在**宿主的家目录**里；换成只读的 `root: ro` 时更是完全建不了
   （`Can't mkdir parents …: Read-only file system`）。而**形态要判**：一条映射的两端必须
   同形（bwrap：`Can't create file at <目录>: Is a directory`），而 `mkdir -p` 打在**文件**上会报
   `Already exists` 并让这份脚本死在 `set -e` 上 —— 所以形态按磁盘上**已有的那一端**决定，两端
   都没在时按目录。这样"源是文件"的映射（单个配置文件，如 `vlcrc`）才走得通，`twin install`
   也会按同一条规则把私有侧建成文件而不是目录。
5. `/tmp` 必须是这两种之一：`bwrap.extras` 里的 `--tmpfs /tmp`（空的私有 `/tmp`）或同一份列表里、
   排在它**后面**的 `--bind /tmp /tmp`（宿主那个，原地可写 —— 行序就是机制，两行都留着是正常的）。
   只读的 `/tmp`
   是最糟的一种 —— 建不出临时目录的程序会在别处报错：Chromium 会打印
   `process_singleton_posix.cc: Failed to create socket directory` 然后放弃单实例锁。
6. token 只在**非加不可**时才加引号（`sandbox.Sh`）：会被 $HOME 折叠的那几个、含空格或 shell
   元字符的（例如 `twin_fail` 那句带 `→` 的说明）、以及空串。全量加引号不是"更保险"，是给读这份
   脚本的人加负担 —— 它正是在出问题时被打开的那份文件，而 `'--dev-bind' '/' '/'` 逼着人先剥掉
   引号才看得见映射。

## Electron / Chromium 的特殊情况

Chromium 系的程序（含所有 Electron 应用）启动时会**自己再建一个 user namespace** 来做
沙箱。这在 Ubuntu 上会撞上两个叠在一起的限制：

- 内核 `kernel.apparmor_restrict_unprivileged_userns=1`（Ubuntu 24.04+ 默认开，由
  `/usr/lib/sysctl.d/10-apparmor.conf` 钉住、每次开机回写）；
- `/usr/bin/bwrap` 的 AppArmor profile 会给它的**每个后代**叠上 `unpriv_bwrap`，而那份
  profile 带 `audit deny capability` ⇒ 后代 capability 全丢 ⇒ 嵌套 `unshare()` 恒 `EPERM`。

`twin` 有两条出路，首选第一条，第二条是兜底：

1. **一份挂在副本路径上的 AppArmor profile（首选）**：不依赖机器上恰好装了什么。`twin install`
   自己装它 —— 写 `/etc/apparmor.d/twin-bwrap`、`apparmor_parser -r`，两条命令都经 `sudo`，所以会
   **要求你输入一次密码**；不想给就按 Ctrl-C，这一趟会跳过这一步。装过一次就一直有效（AppArmor
   每次开机重读 `/etc/apparmor.d`），之后重装、重启都不会再问第二次。
2. **私有 bwrap 副本 ＋ 跳板（兜底）**：装上 profile 那一步走不成时 —— 没有 `sudo`、标准输入不是
   终端（脚本里跑）、或者那一次没装成 —— 才复制一份 bwrap 到没有 profile 的路径上（逃开限制二），
   运行时先 `exec` 一个自带 `flags=(unconfined) { userns, }` profile 的 Electron 程序（通常是
   VS Code），让内核在 exec 时**替换**掉 AppArmor 标签，再由它 spawn 副本 —— 副本及其后代就都带着
   那个许可了。借标签**不需要任何权限**，代价是依赖那个程序一直在；换一台机器重跑一次 `twin` 就
   重新发现一次，不绑死任何特定程序。

两条都不行时，`twin` 照常落全套产物、打印 profile 全文并以**退出码 3** 结束（身份是成立的，只差
这一条 profile）。

装好之后 `install` 会**回读**：`apparmor_parser` 返回 0 只说明内核收下了，还要在
`/sys/kernel/security/apparmor/profiles` 里真的看到 `twin-bwrap` 才算数 —— 看不到就说看不到，
不把查不到当成查过了。

完整机制、五条路线与取证记录：[`docs/apparmor-userns.md`](docs/apparmor-userns.md)。

## 命令一览

| 命令 | 作用 |
|---|---|
| `twin gen <source>` | **最常用**：写出一份实例定义（默认落在 `~/.local/share/twin/<id>/`，`-o` 可换地方）；id / 角标 / 状态路径 / 沙箱基座全部推导。桌面项正文不在这里 —— `install` 才生成它 |
| `twin install <id>` | **最常用**：读那个实例的定义，落盘（桌面项 / 图标 / 启动器 / 托盘）＋ 自检。配置在别处就 `-p <文件>`。需要 AppArmor profile 时用 `sudo` 装；托盘覆盖走哪条路由它当场决定并打印；托盘扩展本身只**检查**，缺了会给出下面那条命令 |
| `twin install --ext` | 装入 **shell 侧的托盘扩展**（机器级，一份服务所有实例，装一次即可；装完要注销重登一次才启用得上，之后各实例的托盘图标由它画，下次 install / reinstall 才改走那条路）。`twin reinstall --ext` 是同一个动作 |
| `twin inspect <source>` | 只读：源程序是什么、身份该怎么做、/tmp 策略 |
| `twin verify <id>` | 自检一个实例的身份链路 |
| `twin reinstall <id>` | 按实例目录里那份定义重新落盘（与 `twin install <id>` 是同一个动作） |
| `twin remove <id>` | 删除产物：先列出要删什么、问过你才动手（脚本里用 `--yes`） |
| `twin list` | 列出全部实例 |
| `twin doctor` | 环境自检（先跑这个），输出末尾直接告诉你下一步敲什么 |
| `twin rules [<app>]` | 列出内置规则 / 查看某程序生效的规则 |
| `twin rules --init <source>` | 探测这个程序的状态目录，写出一份 `rules.d` 草稿（不覆盖） |
| `twin config` | 打印 `config.toml` 模板（文件不存在时） |
| `twin version` | 版本 |

> 第一个参数永远是动词，第二个永远是它的宾语 —— 位置即含义，没有保留字表。
> 所以程序真名就叫 `list` 也不特殊：`twin gen list` 就是包它。

每个命令的**选项与默认值**：`twin <命令> -h`（它和实现同一次编译，所以这是唯一一份）。
**退出码、文件表、拒绝清单**：[`docs/cli.md`](docs/cli.md)；逐键语义：
[`docs/configuration.md`](docs/configuration.md)。

## 配置一览

三层，越具体越优先：

```
~/.local/share/twin/<id>/profile.yml     你正在编辑的那一份
  > ~/.config/twin/rules.d/<app>.toml      应用知识（支持 {id} 占位）
    > ~/.config/twin/config.toml           这台机器的环境
      > 编译进二进制里的内置默认
```

`profile.yml` 是**这一个实例**的全部意图：`gen` 写它，`install <id>` 与 `reinstall <id>` 读它
（两者读的是同一个文件，也都不会改写它）。定义放在别处（`-o` / `-p`）时，实例目录里那份才是
`install` 写下的**存档记录**，`reinstall` 重放的是它。
`config.toml` 不存在是正常的（用内置默认）。数组类字段（实例定义里的 `bwrap.extras` /
`bwrap.bind`，规则文件里的 `tmpfs` / `bind` / `[bwrap] args`）
按**低位在前**拼接，同目标挂载由后写的那个胜出（bubblewrap 自己的规则）。`bwrap.extras` 是交给
bwrap 的**其余全部**参数（沙箱基座那几行也在里面，一行一条），所以"这个程序拿到哪些参数"在文件里
看得见也改得动；而桌面项正文（显示名、关键词、分类）是一份单独的文件，由 `override.desktop` 指出
—— 想改菜单里显示的字，改的是它。

```bash
twin config > ~/.config/twin/config.toml   # 生成一份带注释的模板
```

谁写哪个文件、逐键说明与完整示例：[`docs/configuration.md`](docs/configuration.md)（文件表的查阅版在
[`docs/cli.md`](docs/cli.md)）。

## 退出码

**`3` 是给脚本用的信号**：身份已经成立，只差一步必须由真实图形会话或 root 执行（托盘覆盖表写不进去；
或内核限制 userns 而机器上既没有跳板、也装不了那份 profile）—— **"去图形会话里跑一下"和"这东西坏了"
是两件事**。其余几个码的含义，以及"事实性跳过（这台机器根本没有托盘宿主）算成功"这条边界，见
[`docs/cli.md`](docs/cli.md) 的退出码一节。

## 依赖与安装

要求：

- Linux ＋ bubblewrap（`sudo apt install bubblewrap`）
- Go **1.27+**（构建时）；运行时零第三方依赖，二进制自包含
- 可选：`python3-gi`（启用一道最能抓"静默失败"的自检）、`rsvg-convert` 或
  ImageMagick（SVG 源图标的光栅化；PNG/JPEG 走内置解码器）
- 桌面：GNOME ＋ `ubuntu-appindicators` 托盘宿主（托盘图标覆盖表是它提供的）

```bash
git clone <this repo> && cd twin
go build -o ~/.local/bin/twin ./cmd/twin     # 或 task install（见 Taskfile.yml）

# 或者装到系统目录
task build && sudo install -m755 bin/twin /usr/local/bin/twin

# 版本号可以在构建时注入
go build -ldflags "-X main.version=$(git describe --tags --always)" -o bin/twin ./cmd/twin
```

## 常见问题

**Q：我只有一个程序，为什么生成的 id 是 `wechat2` 而不是 `wechat`？**
因为 `wechat` 这个桌面项 id 已经被系统的那份占了。同名会撞车，而 GNOME 对重复 id 的处理是
**静默遮蔽其中一个**。`freeID` 从数字 2 开始找第一个没被占用的名字。

**Q：点图标没反应 / 名字还是旧的？**
生成完之后必须**完全退出**这个程序再启动（见上文"为什么必须重启实例"）。若窗口出现但任务栏
图标还是齿轮，说明 `Exec=` 指向的启动器**没有执行位** —— GLib 会因此丢弃**整个**桌面项，
而 `desktop-file-validate` 查不出这一点。`twin verify <id>` 会直接报出来。

**Q：托盘图标没变？**
两条路都按 **SNI Id 严格相等**匹配。只有 `level=alias|redirect`（真的改名了）才能预测
这个 id；`level=none` 时 id 由程序自己决定，那一行只是猜测（工具会提醒你）。先看 `twin verify <id>` 里
「托盘图标」那一行：它说的是**这次实际走的那条路**（助手扩展读的文件表 / 宿主覆盖表），以及那一条里
到底有没有本实例的条目。

**Q：自定义托盘图标比程序自己的图标小一圈？**
说明这次走的是**宿主覆盖表兜底**这条路：自定义图标由宿主交给 `St.Icon` 按**主题的** 16px 画，而程序
自己发的位图跟着面板设置里的 `icon-size` 走 —— 这是宿主的一个 bug，补不了在配置里。装一次扩展就换到
另一条路：**`twin install --ext`**（机器级，与装哪个实例无关；`twin reinstall --ext` 是同一个动作，
`twin install` / `twin reinstall` 只检查它，缺了会给这条命令）。它**要等 shell 下次启动才加载**：先看
`twin verify <id>` 里的「托盘扩展」那一行（`twin doctor` 里也有），"已装入但未启用"就注销重登一次，再跑
`gnome-extensions enable twin-tray@edwinhuish.github.io`；启用之后每个实例再 `twin install` / `twin
reinstall` 一次，它才改走这条路。见
[`docs/troubleshooting.md`](docs/troubleshooting.md) B5。


**Q：`gsettings set` 明明成功了却什么都没变？**
无会话总线时 `gsettings set` 会**假成功**（`exit 0`，只在 stderr 吐
`dconf-WARNING: failed to commit`）。所以 `twin` 写完**必须回读**，回读才是唯一判据。
`twin doctor` 里有一项专门探会话总线可达性。

**Q：Electron / Chromium 包裹后起不来？**
先看 `twin doctor` 里"非特权 userns"那一行，再读
[`docs/apparmor-userns.md`](docs/apparmor-userns.md)。

更多症状 → 原因 → 修法见 [`docs/troubleshooting.md`](docs/troubleshooting.md)。

## 文档索引

| 文档 | 内容 |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | 身份模型、三级阶梯、管线、产物模板、权限与边界、GUI 之辨 |
| [`docs/cli.md`](docs/cli.md) | 全部子命令**做什么**、退出码、文件表、拒绝清单；选项本身在 `twin <命令> -h` 里 |
| [`docs/configuration.md`](docs/configuration.md) | 三层配置逐键参考、覆盖规则、定义 vs 环境 |
| [`docs/apparmor-userns.md`](docs/apparmor-userns.md) | 嵌套 user namespace：三个实测事实、五条路线、profile 与跳板机制 |
| [`docs/troubleshooting.md`](docs/troubleshooting.md) | 症状 → 根因 → 修法，含全部实测踩坑 |
| [`docs/development.md`](docs/development.md) | 包结构、构建与测试、开发环境的坑 |
| [`AGENTS.md`](AGENTS.md) | 给 AI 协作者的项目约定：质量门、包结构、硬性规则、常见任务 |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | 贡献指南：环境准备、质量门、scratch HOME 验收、提交规范 |
| [`CHANGELOG.md`](CHANGELOG.md) | 变更日志 |

## 开发

质量门是 `gofmt -l .`（必须为空）＋ `go vet ./...` ＋ `go test ./...`；环境坑（`/tmp` 是小容量
tmpfs 时要设 `GOTMPDIR`、依赖真实机器状态的诊断性测试默认跳过）与构建细节一起写在
[`docs/development.md`](docs/development.md) —— 那里还有代码组织、各包的职责边界，以及开发过程中撞
出来的坑（Go `flag` 的解析行为、多合一二进制、`/usr/share/code/bin/code` 是 sh 脚本……）。贡献者的
环境准备、手工验收与提交规范：[`CONTRIBUTING.md`](CONTRIBUTING.md)。构建与质量门有
[`Taskfile.yml`](Taskfile.yml) 收口：`task build` / `task check` —— GitHub Actions 跑的也是同一条。

## 许可

MIT，见 [`LICENSE`](LICENSE)。
