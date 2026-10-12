# 变更日志

本文件记录本项目的显著变更，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

> 本仓库之前的开发历史没有版本控制记录，所以本文件从下一次提交开始记。
> 当前开发中的版本是 `0.2.0-dev`（见 `cmd/twin/main.go` 的 `version` 默认值）。

## [Unreleased]

### Added

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
- 三层配置：CLI > `instance.toml`（实例快照）> `rules.d/<app>.toml`（应用知识）>
  `config.toml`（本机环境）> 内置默认。
- `/tmp` 策略、路径映射（`--map` / `--rw` / `--tmpfs`）、bwrap 透传区（lint 而非改写）。
- 纯 Go 图标管线（PNG/JPEG 解码、缩放、角标蒙版、5×7 点阵字体）；SVG 走外部工具三级降级。
- 宿主托盘覆盖表（`custom-icons`）的 read-modify-write ＋ 回读。
- 嵌套 user namespace 的"私有 bwrap 副本 ＋ 跳板"机制（Electron / Chromium 场景）。
- 退出码 3：身份已成立但还差一步必须由人执行（写托盘表 / 装 AppArmor profile）。

[Unreleased]: https://github.com/edwinhuish/twin/commits/main
