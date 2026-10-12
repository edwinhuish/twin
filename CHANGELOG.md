# 变更日志

本文件记录本项目的显著变更，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

> 本仓库之前的开发历史没有版本控制记录，所以本文件从下一次提交开始记。
> 当前开发中的版本是 `0.2.0-dev`（见 `cmd/twin/main.go` 的 `version` 默认值）。

## [Unreleased]

### Added

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

### Changed

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

- `inst.List` 不再把 `~/.local/share/twin/bin/` 当成一个坏掉的实例 —— 只要这台机器包过
  Chromium 系程序（`bin/` 里是共享的 bwrap 副本与跳板脚本，没有 `profile.yml`），
  `twin list` 就会整个失败。没有快照的空目录一并跳过。
- README 里 `launch` 示例行末尾残留的一段无关文本。

- **安装阶段的默认输出精简为一屏**：产物清单 ＋ `路径映射` ＋ 自检 ＋ 下一步。推导值、规则
  模板、手工验证步骤移入 `--explain`（它们本来就在 `profile.yml` 里，或一条命令可达）。
  `路径映射` 的默认形态也压到"几条 / 来源 / 每条指向哪 / 改哪个文件"。
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
