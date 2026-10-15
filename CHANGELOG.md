# 变更日志

本文件记录本项目的显著变更，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

> 本仓库之前的开发历史没有版本控制记录，所以本文件从下一次提交开始记。
> 当前开发中的版本是 `0.2.0-dev`（见 `cmd/twin/main.go` 的 `version` 默认值）。

## [Unreleased]

### Added

- **路径映射多了 `:dev` 模式（`--dev-bind`）**：`bind` 的一行写法现在有第三种模式 ——
  `源:目标:dev` 把它按 bwrap 的"allowing device access"那一档挂进去，给设备节点用。它是随
  "基座搬进 `profile.yml`"一起来的：那份定义一旦能把根收紧成 `--ro-bind / /`，它就得能表达
  "这条绑定带设备访问"，而这件事只有映射自己那一行能说。bwrap **没有只读的设备映射**
  （`--dev-bind` 天生可写），所以 `:dev` 与 `:ro` 不能同用 —— 同时写会被直接拒绝，而不是让其中
  一个悄悄赢：那样 `String()` 只能丢掉一个，等于改了用户写下的东西。`gen` 写的基座是
  `--dev-bind / /`，整个宿主本就带设备可用（含 `/dev`），所以这一条多半出现在收紧过根的定义里。
  （实测记一笔：bwrap 0.11 下 `--bind` 与 `--dev-bind` 对同一个设备文件结果相同，所以 `:dev`
  的价值在于**能写出来**，不是绕开 `--bind` 的某个限制。）
- **启动失败时，图形会话里会自己弹窗说清楚**：桌面项点出来的启动没有终端，脚本死在 `exec` 之前
  在用户眼里就是"点了没反应"，而这是 `twin verify` / `twin doctor` 补不回来的（等到能跑它们的
  时候，用户早忘了刚才点过什么）。现在生成的启动器自带这份报告：判据是**这一刻**是不是图形会话
  （没有控制终端 ＋ 有 `DISPLAY` / `WAYLAND_DISPLAY`），是就弹出窗口写明**哪一步没成**，把 twin
  自己的输出（`mkdir:` 那一句之类）一并显示出来，并写进 `~/.local/state/twin/<id>/launch-error.log`。
  弹窗工具按"**正文能选中复制**"挑：`zenity --text-info` → `kdialog --textbox` → `yad --text-info`，
  `xmessage` 与 `notify-send` 不在链里（复制不出来，这个窗口就白弹了）；三个都没有时只留报告文件，
  journal 里留一行它的路径。终端里跑的时候不弹也不写：那份错误本来就在用户眼前。这一层只管
  "**程序根本没启起来**"（启动器自己那几步 ＋ `exec` 本身）—— 程序跑起来之后退出码非零不算启动
  失败，`bootstrap.js` 特意逐字转发那个退出码。
  顺带修掉一个一直都在的静默失败：`mount_pair` 的源端建不出来时，它的失败会被目标端的成功盖住
  （调用方那条 `||` 让 `set -e` 在函数体内失效），最后由 bwrap 报一句跟真因无关的话
  （`Can't find source path …: Not a directory`）—— 现在那句话由启动器自己说。
- **托盘覆盖改成两条路，一次只走一条**：助手扩展被 shell 加载时**由它自己画图标** —— twin 写一份
  **中立文件表** `~/.local/share/twin/tray.d/<id>.json`（每实例一个文件，`twin remove` 删掉），扩展读它、
  在宿主渲染每个托盘项之前把自定义图标塞进宿主自己的"自定义图标"通道（`this._customIcons`）⇒ 画出来的
  东西与写 dconf 表**逐像素相同**，但**不需要会话总线**、也不必 read-modify-write 一个全局键。
  扩展不可用（未装 / 未启用 / 在 shell 的禁用列表里 / 列表读不到）时**回退到宿主 `custom-icons` 表**，
  也就是原来的行为。判据只有 `tray.ExtState.Usable()` 一个，`twin install` 选路、`--dry-run` 预演、
  `verify`、`doctor` 读的是同一个；走扩展那条路时还会把宿主表里本实例的那一行删掉（同一个图标不留
  两个来源）。**走了哪条每次都打印**：托盘那条输出明确写出"扩展读的那个文件"或"兜底：为什么"。
  扩展因此多了第二个职责（画图标），并会读 `disabled-extensions` —— 一个 uuid 可以同时在启用列表与
  禁用列表里，而后者才是"shell 会不会加载它"的答案。
- **`twin install --ext`（`twin reinstall --ext` 同形）**：装 shell 侧**托盘尺寸助手扩展**的开关 ——
  `install` 与 `reinstall` 是同一件事的两个入口（共用 `installAndCheck`），机器级那一半只在一边够得着，
  就等于「有哪些命令」取决于你走哪扇门；`reinstall --ext` 因此不接受 `<id>`（它不属于任何实例），
  `--refresh` 也一样。那份扩展是机器级的
  （一份服务所有实例、比任何一个实例活得久），挂在 `twin install` 上会让"包一个实例"顺手改掉一个
  与它无关的东西，并在每次重放快照时重做一遍 ⇒ 现在 `install` / `reinstall` 只**检查**它：缺了就在
  `🔍 自检` 那一段打一条 ⚠️（它是可选增强，不影响退出码），`verify` / `doctor` 各报一行。
  它与 `--dry-run` 同用（只报会写到哪几个文件），实例那三个选项（`-p` / `--force` / `--explain`）
  按名字拒绝而不是默默忽略。**它不占位置参数**：实例 id 收 `[A-Za-z0-9._-]`，任何裸词都同时是一个合法
  的实例 id，宾语位上的保留字会与用户能取的名字同形（`twin install extension` 装的是扩展，而
  `twin remove extension` 删的是实例）⇒ 机器级那一半走开关，宾语位留给实例。
- **`Taskfile.yml`**：构建与质量门收在一处 —— `task build`（版本号取 `git describe` 注入
  `main.version`）、`task check`（= `gofmt -l` ＋ `go vet` ＋ `go test`）。`task test` 自己把
  `GOTMPDIR` / `TMPDIR` 指到 `$HOME/.cache/gotmp`，本机那个"`/tmp` 只有 10 MB、链接测试二进制会
  `no space left`"的坑不必再手写环境变量。
- **托盘图标尺寸助手扩展 `twin-tray@edwinhuish.github.io`**：宿主对 `custom-icons` 的图标走
  `GIcon` → `St.Icon`，屏幕上多大由 `IconActor` 的**分配框**决定，而那个框只在 `_init()` 里按
  `PANEL_ICON_SIZE || 16` 写一次（`PANEL_ICON_SIZE` 在 gnome-shell 49 已删）⇒ 自定义图标永远 16px，
  而程序自己的位图跟着 `icon-size` 设置变。框是宿主在 JS 里写的，外面补不了，所以 **`twin install --ext`** 写一份
  **用户级**小时助手扩展进去（那件东西是机器级的，`twin install` / `twin reinstall` 只检查它
  并给出这条命令），把每个 `appindicator-*` 的框同步到它自己的 `icon-size`（同步发生在
  `notify::icon-size`，早于内容构建 ⇒ 位图那条路不受影响）。宿主是 **mode extension**，用户目录下的同名
  副本会被 shell 静默拒绝，因此助手是**新 uuid、并排运行**，不 fork 宿主。`verify` / `doctor` 各报它的
  状态；shell 只在启动时扫扩展目录 ⇒ 首次装好后要**注销重登一次**才启用得上，这一步不算失败
  （退出码仍 0）：它只是让图标变大，底下那条兜底的路一直在画。
- **`.github/workflows/ci.yml`**：push 到 `main` 与 PR 时装 Go（版本读 `go.mod`）、跑 `task check`，
  再 `task build` 并冒烟一次 `./twin version` —— 质量门只有 `Taskfile.yml` 一份定义，CI 不另抄。
- **终端输出着色 ＋ 状态字形**（新包 `internal/style`）：只在终端上着色，重定向 / 管道 /
  `TERM=dumb` 一律纯文本；`NO_COLOR` 强制关闭、`CLICOLOR_FORCE` 强制打开；stdout 与 stderr
  **各自判断**。判定走 `TCGETS` 而不是"是不是字符设备"（`/dev/null` 也是字符设备）。
  状态行统一用 `✅ / ⚠️ / ❌ / ➖`，与颜色表达同一件事，纯文本里也不丢信息。
- **`twin new --explain`**：展开完整说明（推导出的默认值、`路径映射` 的三种修改粒度、
  照抄即可的 `rules.d` 模板、手工验证命令）。`--dry-run` 同样展开。
- **两段式 wrap：`twin new` 配置、`twin new --publish` 安装。** `twin new wechat` 只写
  `~/.local/share/twin/wechat2/profile.yml`，并打印快照路径与下一步命令；
  改完再 `twin new wechat --publish` 落盘（桌面项 / 图标 / 启动器 / 宿主托盘 ＋ 自检）。
  没有待落盘的配置时 `--publish` 就是一次完整生成，选项与 `--` 透传区照常生效。
- `generate.SnapshotsFor` / `WriteSnapshot` / `CheckInstall`：两半按**解析后的源桌面项路径**相认，
  不重新推导 id —— 快照一写出来那个 id 就被占用了，重新推导只会悄悄挪到 `wechat3`。
- **快照的逐键说明**（`yamlz` 新增按键注释）：`profile.yml` 里每个键上面都写着它自己的说明。
  注释只参与渲染，`yamlz.Parse` 会丢掉它们 —— 手改出来的注释不会被下一次 `regen` 带偏。
- **`profile.yml` 里的路径可以写成相对的，基准是那份文件自己所在的目录**：四个字段收
  （`exec.target`、`icon.source`、`bind` 两侧、`isolation.tmpfs`），解析只在 `inst.Load` 里做一次
  —— 下游每个读者（bwrap 参数、建挂载点、图标、`verify`）拿到的是同一个绝对路径。让读者自己解析
  就等于按"用户当时站在哪个目录"解析，而那个目录没人选过。文件本身**不会**被改写回解析后的形式
  （相对形式留在盘上，配置、程序、状态目录才能放成一个整体搬走的 bundle）；`~` 照旧由
  `Paths.Expand` 在用的时候折叠成本机 home。两个例外：`bwrap.extra` 逐 token 照搬、依然不解析；
  `rules.d` 里的相对路径现在**指名报错**（退出码 2）—— 规则服务这个程序的所有实例，它的相对路径
  该相对规则目录还是相对每个实例，没有答案。四个字段的键注释也跟着补了这句（文件是唯一的说明书）。

### Changed

- **基座里每条挂载各归自己那个键，`args` 只留没有节点的那些**：`gen` 原先把整个基座平铺进
  `bwrap.args`，于是文件里既有 `bwrap.proc` / `bwrap.tmpfs` / `bwrap.env` 这些键，又有同样几件事
  以 token 形式躺在选项列表里 —— 想说出 `--tmpfs /dev/shm` 是"盖成空目录"还是"挂一个映射"，
  读者得先背下 bwrap 的选项表。现在 `--proc /proc` → `bwrap.proc`、`--tmpfs /dev/shm` 与私有的
  `/tmp` → `bwrap.tmpfs`、`--setenv TWIN_INSTANCE <id>` → `bwrap.env`。
  留在 `args` 的恰好是最该留在原处的几条：根挂载（必须排在所有挂载之前 —— 排在 overlay 别名之后
  会把程序的真名还回来）、宿主 `/tmp` 那一支的 `--bind /tmp /tmp`、以及 `--die-with-parent`（没有
  对应的键）。宿主 `/tmp` 之所以不进 `bwrap.bind`：那条映射的清单是"隔离哪些数据目录"，写进去之后
  `install` 会把它报成一条路径映射、还建议照抄进这个程序的 `rules.d`（实测）。
  ⚠️ 有自己节点的那几条由 `Build` 发在 `args` **之后**，所以 `--` 透传区里写一条同目标的挂载压不住
  它们；想改就改那个键。旧定义照旧工作 —— `Build` 逐字发 `args` 里的 token。`level none` 的定义
  不再留下一句永远不会执行的 `/tmp` 决定：那份列表被 `SessionWarning` 读着，X11 会话里会给一个
  根本没有沙箱的实例报警。

- **`profile.yml` 的 `bind` 搬进 `bwrap` 节点（现在叫 `bwrap.bind`）**：它和 `bwrap.args` 是同一次
  调用的两半 —— 先是选项列表，然后是这些挂载 —— 所以写在同一个节点里，读的人不必跨半个文件把
  "这条映射会变成哪条 bwrap 选项"拼出来。写法一个字没变（`源:目标[:ro|:dev]`）。**顶层 `bind` 指名
  报错**而不是当"没有映射"读掉：前者说"程序的数据挂在哪"，后者说"什么都没挂"，而后者的后果要到程序
  看不见自己的文件时才暴露，那时离这次编辑已经很远。`rules.d` 里的 `bind` **留在顶层**：那份文件
  描述的是应用知识（旁边还有 `keywords` / `tmpfs` 这些与 bwrap 无关的键），服务多个实例，不是一次
  调用。受影响的人只有一处要做：把 `bind:` 那一段缩进到 `bwrap:` 下面。

- **`bwrap` 节点收下四个「有形状的挂载」：`proc` / `tmpfs` / `remountRO` / `env`**（两个键同时搬家）。
  它们分头对应 `--proc` / `--tmpfs` / `--remount-ro` / `--setenv` —— 平铺的 token 列表说不出"这是
  一条 proc 挂载"或"这条路径被收回只读"，而 `bind` 的 `:ro` / `:dev` 早就说明白"一种挂载值得有一个
  说法"。**空值 = 没设置**：发出零个参数，也不会被读成"要默认的那个"（默认那几条写在 `args` 里，
  文件里看得见）。键在文件里的顺序就是它们在命令里的顺序，`remountRO` 排在**所有挂载之后** ——
  `--remount-ro` 必须压在它要收紧的那条挂载上面，否则等于空操作。
  ⚠️ 它还有一条边界，是实测出来的：只对**自己就是一条挂载**的路径有效（`bind` 的目标端、
  overlay / tmpfs 那条路径、整个 `/`）。宿主那一条大根挂载里的路径底下没有单独的挂载，点错了 bwrap
  直接拒绝启动：`Can't remount readonly on …: Unable to find … in mount table`。整根要收紧就换基座
  那一行（`--ro-bind / /`），别点这里。
  一起搬家的两个键**指名报错**，不读成别名（一个参数两个写法，就是一份文件开始跟自己吵架）：
  `isolation.tmpfs` → `bwrap.tmpfs`；`exec.env` → `bwrap.env`，而且换了形状 —— 从 `键: 值` 映射
  改成 `- 键:值` 列表，冒号只在第一个处断开（所以值里可以再有冒号，`PATH:/a:/b` 是合法的），值可以
  是空的（`LC_ALL:`），键名不能带冒号、空格或 `=`。
  顺带修掉一个一直没出声的 bug：`tmpfs` 里的 `~/…` 以前**原样**交给 bwrap（那是一个名叫 `~` 的
  目录），现在和 `bind` 一样在用的时候展开成本机 HOME。
  `rules.d` 里的 `bind` / `tmpfs` **仍在顶层**：那份文件描述的是应用知识，服务多个实例，不是一次
  调用。受影响的人只有一处要做：把 `isolation.tmpfs` 缩进进 `bwrap:`，把 `exec.env` 改写成列表。

- **沙箱基座搬进 `profile.yml`：`bwrap.extra` → `bwrap.args`**（行为变更）。原先基座是硬编码在
  `internal/sandbox` 里的（`--ro-bind / /`、`--dev /dev`、`--dev-bind-try /dev/dri /dev/dri`、
  `--proc /proc`、`--bind /run/user /run/user`，外加一个 `/tmp` 分支），于是**「这个程序拿到哪个根」
  成了定义里唯一随程序而变、又完全表达不了的决定**：需要 `--dev-bind / /` 的程序（要碰设备、要自己
  建沙箱）唯一能开口的地方是透传区，而透传区排在**所有映射之后** —— 在那里重新绑一次根，会把前面
  每一处挂载都盖掉，"改基座"于是与"改不了"是同一件事。
  现在 `bwrap.args` 是交给 bwrap 的**完整选项列表**，`twin gen` 把基座写进去、`sandbox.Build` 一字
  不改地照发，twin 不再自己补任何参数 —— 启动器里渲出来的、`--dry-run` 里看到的，就是文件里那一份。
  新基座：`--dev-bind / /`（整个宿主根含设备挂进来）＋ `--proc /proc` ＋ `--tmpfs /dev/shm` ＋
  `/tmp` 那条 ＋ `--die-with-parent` ＋ `--setenv TWIN_INSTANCE <id>`。
  `--dev-bind / /` 的取舍写在明处：**设备可用，代价是根不再是只读的**（twin 是身份工具，不是安全
  边界），`/run/user` 与 `/tmp` 的可写因此天然成立，专为"但求可写"而写的 `--bind X X` 全部不再需要；
  想收紧就把那一行换成 `--ro-bind / /`，代价是自己补回设备与 `/run/user`（文件里那段注释就在这一行
  旁边）。**`/tmp` 那一条属于基座**：它是"用哪个根"的延伸，所以写进 `bwrap.args` 而不是
  `isolation.tmpfs`（那条列表现在只装**额外**要盖成空目录的路径；落在它里面的 `/tmp` 会被 gen 归一化
  到基座那一行，免得同一处挂载印两遍）。两个方向都必须是**可写**的：走 tmpfs 的程序拿空的私有
  `/tmp`，X11 那一路拿宿主那个（tmpfs 会把 `/tmp/.X11-unix/X0` 一起带走）——"存在但写不进去"不是
  第三种选择，它正是只读根悄悄产生的状态。
  旧的 `bwrap.extra`（实例定义、`config.toml`、`rules.d` 三处同名）**指名报错**而不是当"没有额外
  token"读掉：两者语义不同（`extra` 只装你追加的，`args` 连基座一起装），读错会交给 bwrap 一个什么
  都没挂的命名空间，而那条失败信息里不会有一个字提到 twin。同理，会改名的 level 之下
  **`bwrap.args` 为空也指名报错**（bwrap 至少要有一个根挂载）。
  受影响的人只有一处要做：把旧的 `bwrap.extra` 内容搬进新生成的 `bwrap.args` 尾部。

- **裸 `twin`（无参数）等同 `twin --help`，退出码从 `2` 改成 `0`**：那时用户没有表达任何动作，
  能回答的只有"这个工具是什么" —— 而这里一个字都没写错，按用法错误收尾会让 `twin || …` 这种写法
  把一次提问报成故障。打印的内容本来就与 `--help` 逐字相同，变的只是退出码那把尺子。

- **受限机器上优先"让 twin 自己装那份 AppArmor profile"，跳板降为兜底**（行为变更）：
  `userns.trampoline = "auto"` 原先一上来就去找一个已安装的 Electron 应用、借它的标签让 bwrap 副本
  建出嵌套 user namespace —— 不需要任何权限，代价是把一个包裹的寿命挂在了别人家的程序上（那个应用
  卸载/改名，实例就起不来），而"这台机器上恰好装了什么"会悄悄决定一次安装的结果。现在先走 twin
  自己那份：`twin install` 写 `/etc/apparmor.d/twin-bwrap` 并 `apparmor_parser -r`（两条命令经
  `sudo`，**一次密码**；AppArmor 每次开机重读 `/etc/apparmor.d`，所以重装、重启都不再问），
  `sandbox.Resolve` 一旦认出它，启动器里写的就是私有副本、不需要跳板。装不成才退回跳板，而且
  理由与后果都打在 `🧱 沙箱方案` 那一段：问不到密码（没有 `sudo`、标准输入不是终端）或者那一次
  没装成时，"跳过自动安装"后面紧跟着"没装 profile：退回借一个已安装 Electron 的标签"。两条都不行
  才打印 profile 全文并以退出码 3 收尾。`userns.trampoline` 的取值含义随之调整（`"off"` 仍然是不走
  跳板，显式路径仍然优先于自己装），`doctor` 那一行与 `docs/apparmor-userns.md` 的路线表也跟着改
  （现在是从 A 到 D 五条）。取证方式：scratch HOME 下 `--dry-run` 打印的是"会用 sudo 装一份…" ——
  预演演的是真装会走的那条路，不是自己给自己预言一次失败；没有终端时真跑一次，打印"跳过自动安装"
  并退回 `/usr/share/code/code`，把它写进了启动器。

- **可选的东西不再挡在"还差一步"里**（行为变更）：托盘尺寸助手扩展只是把托盘图标画成面板那么大 ——
  没装它，兜底那条路照旧画图标，只是小一圈。原先 `verify` 把它标成 `Manual`，于是
  `twin install` / `twin reinstall` 会以**退出码 3** 收尾，并在末尾那段
  `✋ 还差一步（必须由真实图形会话执行…）` 清单里与"宿主表写不进去（无会话总线）"并列 ——
  一个可选项因此读起来和一件非做不可的事一模一样，而那个码正是脚本用来判断"回来再跑一次"的。
  现在：扩展的每个状态都只是 ⚠️（文案里写明是可选增强），不进退出码；`twin install --ext` 装完
  就是装完了（三步照旧打印，退出码 0）；末尾那段清单**整个删掉** —— 非 OK 项（含 `Manual` 的）
  一律就地打印在 `🔍 自检` 那一段里，退出码规则不变（`Manual` 且非 OK ⇒ 3）。

- **实例定义里那几处跟不上现状的键说明改掉了**（措辞变更）：`tray.override` 还说"把宿主托盘覆盖表
  指向本实例"，而托盘早就有两条路（助手扩展读的中立文件表优先，宿主表兜底 —— 走哪条是机器的事）；
  `icon.style` 把 `number` 写成"居中大数字"，其实它是右下角的圆形，而且三种风格现在都贴齐图标的
  右边缘与下边缘；`identity.level` 的 `redirect` 只说"多写一条扩展规则"，没说是哪份文件
  （`~/.config/twin/rules.d/<id>.json` 的 L5 规则，给 shell 扩展改判窗口→应用的映射）；
  `identity.wm_class` / `notify` 补上了"别依赖它（Wayland 认领窗口的是别名基名）"与"默认 true、
  总是写出来"；`bwrap.extra` 的"不猜 arity"改成"不猜每个选项的元数"，并写明它是
  `config.toml → rules.d → gen 的 -- 透传区` 合并后的最终列表。
- **角标改成贴齐图标的右边缘与下边缘**（行为变更）：原先它缩在画布内 4%（256px 下 10px），为的是
  不让圆角图标的右下角切到它。这个取舍是反的 —— 角标是 16px 面板里那张图上唯一还要数得清的东西，
  那道缝让它看起来浮在图上，而不是标在这个图标的角上。现在 `corner` / `number` / `dot` 三种风格都
  贴齐（圆形与两条边相切，圆点也是）：图标自己是圆角、或者图标主题再圆一次角裁到角标，都不再让
  角标让位。
- **`twin gen` 的落点改到实例自己的目录，`install` 不再拷贝一份定义**（行为变更）：原先 `gen` 写在
  当前目录的 `./profile.yml`，`install` 读它、并在 `~/.local/share/twin/<id>/profile.yml` 写出一份
  存档快照。现在 `gen` 直接写 `~/.local/share/twin/<id>/profile.yml`（`Paths.Profile(id)`；id 由
  `Plan` 推出，落点因此在 `planInstance` 之后才算），`install <id>` 与 `reinstall <id>` 读的就是
  那一份，`Materialize` 见到"定义已在此处"（`definitionIsHere`）**不重写它** —— 写回一次就是一次
  `yamlz` 解析往返，用户手写的注释与排版会没。理由有两条：**找得回来**（"当前目录"只是你当时站的地方，
  换个 shell 就找不到了，而它必须跟 `$HOME` 派生）与**一个 id 一份**（包第二个程序不会撞上第一个的
  定义）。要放别处仍旧 `-o`，那时才在实例目录里另写一份存档记录（`RoleSnapshot`）供 `reinstall`
  重放；两个角色从此是同一个文件名的两种来路，头注释各自指向读它的那条命令。
- **`twin install` 收 `<id>` 或 `-p <文件>`**（行为变更）：`twin install <id>` 读那个实例自己的定义
  （`gen` 之后最自然的下一步），`-p <文件>` 读你指定的那一份（配置放别处、或并排放几份时用它）。
  两者同时给、以及两个都不给都是用法错误（退出码 2）：同时给会让其中一个被静默忽略，都不给就只能猜
  要装哪个实例。`twin reinstall <id>` 与 `install <id>` 因此是同一件事的两个入口（同一个文件、同一段
  `installAndCheck`），动词保留。宾语位上从此只有实例 id，机器级那半仍是开关 `--ext`（任何裸词都是
  合法 id，保留字宾语会与用户能取的名字同形）。
- **`twin gen` 的下一步命令改成 `twin install <id>`**：定义不再落在当前目录，而"装哪一份"从此由
  实例 id 指名。
- **`reinstall --refresh` 补上的值不再写回文件**（行为变更）：那份定义现在是用户的文件，而解析往返会
  丢掉注释与排版 ⇒ 补上的字段只用于这一次落盘（输出里说明了这一点），想留住就照它改文件。
- **`twin list` / `twin verify` 说"还没落盘"而不是"上一次安装没走完"**（措辞变更）：定义由 `gen`
  写下，实例目录里因此可以先有文件、后装产物 —— 那种状态不是失败，只是还没装。
- **实例配置统一叫 `profile.yml`，`install` 的读取开关改成 `-p/--profile`**（行为变更）：
  `gen` 写在工作目录的那份原叫 `config.yml`，而 `install` 按它渲染出的存档一直叫 `profile.yml` ——
  同一个 schema、两个名字，读者要记两套词汇，文件之间也就多了一条"名字即角色"的隐含约定。现在
  两处同名，区分只剩头注释里那句"改完敲哪个命令"，而那句本来就必须写准。`-c/--config` 是随之而来
  的改名：读的那份文件不叫 config 了，选项也不该叫 config。旧拼写**被指名报错**（退出码 2，不再
  静默忽略），`gen` 的 `-o/--output` 不变 —— 它描述的是"这次写到哪"，不是文件的角色。
- **两段式 wrap：`twin new` 拆成 `twin gen`（写配置）与 `twin install`（落盘）**（行为变更）：
  一次 wrap 里唯一随程序而变、又猜错了不出声的是"隔离哪些数据目录"，所以先把它落成一份能读能改
  的文件，安装才是另一件事。`install` 因此**不接受任何描述实例的选项** ——
  "带着描述实例的选项去装一个已存在的实例"这件事不再是运行时判断，而是动词拆分本身。
- **`twin remove` 不带 `--yes` 时改成交互确认**（行为变更）：先列出将要删的文件，**再问一次**，只
  接受 `y` / `yes`（大小写都行），其余任何回答 —— 直接回车、拼错、Ctrl-D —— 都按"不删"处理。
  标准输入不是终端时不提问、只预览并给出那条显式命令：提问没人能回答就是卡住，这与装 AppArmor
  profile 那一步是同一条规矩。`--yes` 仍是不提问的那个开关，脚本与 CI 不受影响。
- **选项表移进 `twin <命令> -h`，文档不再复述它们**（`-h` 的输出因此变长）。默认值与可接受取值原先
  只写在 `docs/cli.md` 的表里 —— 于是"文档说默认 `alias`、程序默认别的"这种分叉没有任何一处能自证。
  现在它们与实现同一次编译。`--` 透传区、以及"`install <id> | -p <文件>` 二选一"这类 PrintDefaults
  打不出来的用法也收在 `-h` 里；`docs/cli.md` §3 随之从逐 flag 的选项表收成"选项在哪"。
- **`twin new` 默认不再安装任何东西**（行为变更）：它现在只写一份配置。要以前那样一步到位，
  用 `twin new wechat --publish`（或在没有待落盘配置时直接补 `--publish`）。
- **`twin new` 的默认输出压到最短**（行为变更）：只报快照路径与下一步，逐键说明搬进 `profile.yml`
  的注释里。唯一保留的一行是"一条映射都没有"的警告 —— 它是这个命令唯一可能留下、而别处不会再
  报的后果。为此 `bind` 改为**无条件写出**（空时是 `bind: []`），那句话在文件里才有落点。
- `twin regen` 与 `twin new --publish` 合并到**同一条安装路径**，所以不会出现"publish 会做、
  regen 不做"的差别。
- 有一份待落盘的配置时，`--name` / `--comment` / `--keywords` / `--categories` / `--icon` /
  `--badge` / `--level` / `--tmpfs` / `--no-tmpfs` / `--no-tray` 与 `--`
  透传区会被**拒绝**（退出码 2）：那份快照才是真源。同样地，待落盘的配置不只一份时要求 `--as`
  指名，拒绝猜。
- `twin list` 用 `← 未落盘` 标注配置已写、还没安装的实例，并在表尾给出收尾命令；
  `twin verify` 对未落盘实例只报这一件事（附收尾命令，退出码 `1`），不再连锁失败。
- `路径映射` 的"改它"提示改成两条明确的路：`rules.d` 管以后新建的实例，实例快照管这一个。
- **路径映射统一成一行一条字符串**（行为变更，配置文件格式变了）：`bind` 从 `[[bind]]` 表
  （`canonical` / `private`）改成 Docker Compose 卷的写法 `源:目标[:ro]` —— `源` 是数据实际所在，
  `目标` 是程序看到它的位置，与 bwrap 的 `--bind` 同向。单写一条路径表示"挂回自己"（原地升回
  可写）。`rules.d` 与实例快照用同一套写法，落盘、快照、`--dry-run` 预览也共用它。
  旧的 `[[bind]]` 表**被指名报错**，不会被当成"没有映射"读掉 —— 把"数据在哪"读成"数据共用"
  正是这个工具存在的理由所要防的那种静默失败。
- **CLI 移除 `--map` / `--rw`**（行为变更）：路径映射只能写进 `rules.d/<程序>.toml`（管以后新建
  的实例）或实例快照的 `bind`（管这一个）。一条映射正是最该在安装任何东西**之前**被读一遍的
  东西，而命令行参数没人会回头再看第二眼；`--tmpfs` 不受影响。
- 快照里的 `map.bind` 移到根层 `bind`：整个文件已经没有别的东西需要 `map` 这一层。
- **实例快照改为 YAML，文件名 `instance.toml` → `profile.yml`**（新包 `internal/yamlz`，自写的
  最小 YAML 子集）：schema 本来就是嵌套的（`identity` / `exec` / `isolation`），
  嵌套文件读起来是层次，而不是一堵 header 墙。旧 `instance.toml` **不再被读取**，但只要它还在
  就会被**指名报错**（`twin list` / `twin verify` / `twin regen`），而不是让一个真实例从所有
  命令里悄悄消失。

### Fixed

- **沙箱里的 `/tmp` 是只读的**：`--ro-bind / /` 不会把宿主的 `/tmp` 原样留着，它把 `/tmp` 变成
  只读 —— 而没被 `isolation.tmpfs` 盖住的 `/tmp` 就落在这个状态里。于是任何要建临时目录的程序都
  在别处报错：Chromium 的单实例 socket 建在 `base::GetTempDir()`（`$TMPDIR`，否则 `/tmp`）下的
  一个 `mkdtemp` 里，`mkdir` 拿到 `EROFS` 就打印
  `[ERROR:chrome/browser/process_singleton_posix.cc] Failed to create socket directory` 并放弃
  单实例锁 —— 一条关于 socket 的报错，来自一个用户刚包上的程序（WorkBuddy / Electron 实测）。
  现在 `/tmp` 两条路都通、两条都可写：走 tmpfs 的程序拿到一个空的私有 `/tmp`，不走的（X11 那一路，
  tmpfs 会把 `/tmp/.X11-unix/X0` 一起带走）拿到宿主的 `/tmp`。

- **生成的启动器把"源是文件"的映射建成了目录**：`mkdir -p` 有个它表达不了的事 —— 挂载点要在
  宿主机上先存在（bwrap 看到的根是只读的，`Can't mkdir parents …: Read-only file system`），
  但一条映射的两端必须是同一种东西（文件挂到目录上 bwrap 直接拒绝）。旧写法对每个端一律
  `mkdir -p`，于是单个配置文件的映射（`bind = ["…/vlcrc:~/vlcrc"]`）被造成一个目录，
  程序写自己的配置时拿到 `EISDIR`；而若那一端**已经**是文件，`mkdir -p` 会报 `Already exists`，
  `set -e` 于是让整个启动器在 exec 之前就停住 —— 程序根本不会启动。现在启动器先判形态：两端
  按磁盘上**已有的那一端**取形，都不在时按目录；文件那一侧建的是它所在的目录 ＋ `touch` 它自己，
  已经存在的路径一个指头都不碰（那是程序自己的数据）。`twin install` 用同一条规则建私有侧，
  否则它会先把那个私有副本造成目录，而启动器不碰已经存在的路径 —— 那个错误会一直留着。

- 图标合成先裁掉透明边距：官方图标画的时候留了约 4.5% 的透明边距（给不同桌面环境的间距差别
  留余地），而程序自己往托盘发的位图是满铺的 —— 于是 twin 生成的图标在托盘里看着比它替换掉的那
  张"小一圈"。这不是缩放比例不对，是画布里有空气：`Compose()` 现在先 `trimAlpha` 再 `scaleTo`，
  让作品占满画布，并把这件事写进合成报告（只在真的裁掉东西时出现）。

- 文档里残留的旧动词（`twin new` / `twin new --publish` / `twin regen`）：README、`docs/*` 与
  `CONTRIBUTING.md` 改成现行的 `twin gen` ＋ `twin install` / `twin reinstall <id> --refresh`。
  手工验收的命令补齐了两段式 —— `gen` 把 `config.yml` 写在**当前目录**，原先那种写法会在仓库根目录
  里落一份配置。
- `inst.List` 不再把 `~/.local/share/twin/bin/` 当成一个坏掉的实例 —— 只要这台机器包过
  Chromium 系程序（`bin/` 里是共享的 bwrap 副本与跳板脚本，没有 `profile.yml`），
  `twin list` 就会整个失败。没有快照的空目录一并跳过。
- README 里 `launch` 示例行末尾残留的一段无关文本。

- **安装阶段的默认输出精简为一屏**：产物清单 ＋ `路径映射` ＋ 下一步。推导值、规则模板、候选状态
  目录、"改它该编辑哪个文件"、手工验证步骤移入 `--explain`（它们本来就在 `profile.yml` 里，或一条
  命令可达）。`路径映射` 的默认形态压到"几条 / 来源 / 每条指向哪"。
- **自检只在有问题时才开口**（行为变更）：`twin install` 默认不打印那几条 `✅` —— 它们确认的正是这
  一次刚写下的文件 —— 只在有非 OK 项时打出该节，并附上修它的命令；通过项也不再带修复命令（那会给
  读者一条他没有的问题的命令）。枚举每一条仍然是 `twin verify` 的职责，`--explain` 也照旧全列。
- 成功的步骤不再各自留一句备注（如图标那句 `wrote 256x256 PNG to …`）：那个路径就在上面几行的产物
  清单里，重复一遍只是英文噪音。
- **桌面项文案收进 `desktop:` 一节**（配置文件格式变了）：`name` / `name_en` / `comment` /
  `comment_en` / `keywords` / `categories` / `source` 从根上移进一节，它们回答的是同一个问题，且
  留空一律表示"沿用源桌面项"。旧写法**仍然读得懂**（根上的 `name` 没有第二种解释），下一次
  `install` / `reinstall` 会把它改写成 `desktop:`。
- **用法错误只打印那一行**（行为变更）：未知子命令、未知选项不再附带整份 `usage`（Go 的 `flag`
  默认会把 `Usage of gen:` 连选项表一起印出来）；help 的出口是 `twin --help`，逐命令的选项表是
  `twin <命令> -h`（退出码 `0`，那是提问不是犯错）。
- `twin list` / `twin remove` / `twin doctor` / `twin verify` / `twin inspect` 的输出统一带上
  字形与段落标题；`twin remove` 的预演把存在的产物打 `✅`、不存在的打 `➖`，末尾给出下一步命令。
- `twin list` 末尾点出 `twin remove <id>` —— 它是卸载分身的唯一动词，不该只藏在 `--help` 里。
- `generate.Unwrap` 补上契约测试：只删本实例的产物、默认保留私有数据、**绝不牵连别的实例**
  （它是 twin 唯一不可逆的操作，静默回归等于丢数据）。
- 文档：`docs/cli.md` 新增"输出与颜色"一节与 `--explain` 说明；`docs/development.md` 与
  `AGENTS.md` 补上 `internal/style`、依赖方向与"加/改一段终端输出"的约定。

- 面向 AI 协作者的项目约定 [`AGENTS.md`](AGENTS.md)（构建、结构、硬性规则、常见任务），
  以及 [`CLAUDE.md`](CLAUDE.md) 入口。
- 面向人类贡献者的 [`CONTRIBUTING.md`](CONTRIBUTING.md)：质量门、scratch HOME 验收流程、
  提交与合并请求要求。
- 本变更日志。
- 初始化 git 仓库（首个提交）。

### 现有能力（截至本版本已实现）

为免"第一个提交等于一次发布"造成误解，这里把当前已有的能力一次性列清；后续变更按上面的
小节增量记录。

- 身份级别：`none`（只写桌面项与图标）、`alias`（bwrap overlay 零拷贝改名，主力）、
  `redirect`（改名 ＋ L5 规则）。`argument` / `env` 经实测不成立，**故意不实现**。
- 子命令：`new` / `inspect` / `verify` / `regen` / `remove` / `list` / `doctor` / `rules` /
  `config` / `version`。
- 三层配置：CLI > `profile.yml`（实例快照）> `rules.d/<app>.toml`（应用知识）>
  `config.toml`（本机环境）> 内置默认。
- 两段式 wrap：`twin new` 写配置（`profile.yml`）、`twin new --publish` 安装；没有待落盘的配置
  时 `--publish` 就是一次完整生成。
- `/tmp` 策略、路径映射（`bind` 的 `源:目标[:ro]` 写法，写在 `rules.d` 或实例快照里）、
  bwrap 透传区（lint 而非改写）。
- 纯 Go 图标管线（PNG/JPEG 解码、缩放、角标蒙版、5×7 点阵字体）；SVG 走外部工具三级降级。
- 宿主托盘覆盖表（`custom-icons`）的 read-modify-write ＋ 回读。
- 嵌套 user namespace 的"私有 bwrap 副本 ＋ 跳板"机制（Electron / Chromium 场景）。
- 退出码 3：身份已成立但还差一步必须由人执行（写托盘表 / 装 AppArmor profile）。

[Unreleased]: https://github.com/edwinhuish/twin/commits/main
