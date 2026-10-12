# 命令参考

```console
$ twin --help
```

---

## 1. 调用形态

```
twin <动词> [参数…]               # 动词不带宾语（list / doctor / config）
twin <动词> <宾语> [选项]         # 大部分动词带一个宾语（new / inspect / verify …）
```

**第一个 token 永远是动词，第二个永远是它的宾语** —— 位置即含义。没有隐式动词，也没有
保留字表：所以程序真名无论叫 `list` 还是 `new`，照常写就行，不需要任何逃生口写法。

```bash
twin new list          # 包一个名叫 list 的程序
twin inspect doctor    # 预览名叫 doctor 的程序
```

> **为什么不能写 `twin wechat`？** 早期版本允许把首个 token 当隐式 `new` 的宾语，代价是它的
> 含义取决于是否命中一张保留字表 —— 读一行命令得先背那张表，还得额外解释"程序名撞上动词时
> 怎么办"。取消隐式动词同时消灭了**保留字表**和**逃生口**两样特例，一个从未实现的
> `completion` 死字也顺带清掉了。这也是为什么动词叫 `new` 而不叫 `wrap`：`twin wrap` 是叠词。

`<source>` 可以是：`.desktop` 文件的路径、桌面项 id（`wechat`），或程序名。
查找顺序是 `~/.local/share/applications` → `/usr/local/share/applications` →
`/usr/share/applications`。

---

## 2. 子命令

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

### `twin inspect <source> [--name <显示名>]`

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

工具箱探测（按二进制内字符串）
  Wayland 支持  是
  X11 支持      是
  Qt            是
  GTK           否
  多合一二进制  否

/tmp 策略：可用 --tmpfs /tmp —— 本会话有 WAYLAND_DISPLAY，该程序会走 Wayland。
  ⚠️ 但它两套平台插件都带着：若哪天真退到 X11，--tmpfs /tmp 会砍掉 /tmp/.X11-unix/X0 而连不上 X。

备注
  · /usr/bin/wechat 是指向 /opt/wechat/wechat 的链接 —— 别名要建在 /opt/wechat 里（$ORIGIN 与 /proc/self/exe 都看真实路径）
  · Wayland 原生 + 静态链接的 Qt：窗口 app_id 由 Qt 从可执行文件真实路径派生（已实测）
  · 同时含 Wayland 与 X11 平台插件：本会话有 WAYLAND_DISPLAY 时按 Wayland 走
  · 应用规则 wechat：内置规则（3 条路径映射 ⇒ [[map.bind]]）
```

`显示名` / `说明` 打印的是**语言化**取值（`Name[<当前语言>]` 优先），因为 `wrap` 就是这么
推导的 —— 预览与真实写入必须一致，否则这个工具的预览就是一句谎话。

`--name` 只影响预览：它会**真的**参与 keywords 的推导（因为你显式给了名字），所以能在不写盘
的前提下看到"命名 ↔ 关键词"的联动结果。

### `twin new <source> [选项]`

生成一个实例。全部选项见 §3。

- 落盘前会把**推导出的每个默认值连同来源**打印出来。
- 会 lint 透传区（只警告，不改写）。
- 会检查 id 冲突：显式 `--as` 撞到已存在的东西时拒绝，除非 `--force`。
- 落盘后**自己**写宿主托盘覆盖表，然后跑一次自检。
- 落盘后会打印一节 **`路径映射`**（`--dry-run` 同样打印）。

路径映射是这份产物里**唯一随程序而变、且猜错了不出声**的部分：隔离错一个目录，要到两个实例
抢同一个锁文件时才暴露。所以它必须出现在输出里，而不是只躺在 `instance.toml` 里。这一节会说明
共有几条、知识来自哪一层（内置规则 / `rules.d` / `--map`）、**改它该编辑哪个文件**，并附上一份
照抄即可的 `rules.d` 模板。程序没有规则时，它会明说"什么都没重定向"并点出这个风险，再列出
**探测到的**候选目录（标着`未应用`，见下面的 `twin rules --init`）。

```
路径映射
  3 条，来源：内置规则 wechat
  私有  ~/.xwechat                             → ~/.local/state/twin/wechat2/xwechat
  共用  ~/Documents/xwechat_files   （原地升回可写，数据不搬家）
  私有  ~/Documents/xwechat_files/all_users    → ~/.local/state/twin/wechat2/all_users

  要改（挑一个粒度）：
    · 影响这个程序以后新建的实例：写 ~/.config/twin/rules.d/wechat.toml，
      再 twin new <source> --as <id> --force 让这一个跟上
    · 只改这一个实例（不动规则）：编辑 ~/.local/share/twin/wechat2/instance.toml 里的 [[map.bind]]，再 twin regen wechat2
    · 只改这一次（不落规则）：twin new <source> --as <id> --map '<canonical>:<private>'

  照抄下面这份（就是规则文件的全部内容）：

    app = "wechat"

    [[bind]]
    canonical = "~/.xwechat"
    private   = "~/.local/state/twin/{id}/xwechat"

    [[bind]]
    canonical = "~/Documents/xwechat_files"
    private   = "~/Documents/xwechat_files"

    [[bind]]
    canonical = "~/Documents/xwechat_files/all_users"
    private   = "~/.local/state/twin/{id}/all_users"

  private 与 canonical 相同（或整个 private 留空）⇒ 与该目录共用，只把只读升回可写；
  private 指向别处 ⇒ 私有副本，两个实例互不干扰。
```

模板里的 `private` 会把本实例的 id 写回成 `{id}` 占位符 —— 照抄出来的规则服务这个程序的**所有**
实例，而不是被烤死在某一个 id 上。上面路径按 `$HOME` 缩写了，工具打印的是绝对路径。

一个**没有规则**的程序长这样（模板部分省略）——`路径映射` 一节的职责就是把"没映射任何东西"
这句话连同"那该映射什么"一起说清楚：

```
路径映射
  没有生效的映射：数据目录与源程序共用（本实例只做身份包裹）。
  ⚠️ 这个程序没有规则。它若有单实例锁或共享配置（Electron / Qt / GTK 系很常见），
     两个实例会互相干扰 —— 需要隔离时写一条规则。

  这个程序可能的状态目录（**未应用**，仅供参考）
    ~/.config/foo                                目录存在
    ~/.local/state/foo                           目录存在
    要隔离就先生成一份规则草稿：twin rules --init foo
```

### `twin verify <id>`

自检一个**已生成**实例的身份链路。**重新读硬盘**，不信内存里的期望。

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

```
id               level      别名     程序                     桌面项
wechat2          alias      wechat2  wechat                   wechat2.desktop
foo3             redirect   foo3     foo                      foo3.desktop  ← 桌面项缺失
```

### `twin regen <id> [--refresh]`

改了 `instance.toml` 后重新落盘。

- **默认只读 CLI 与快照**（重放必须是确定性的）。碰一下 `config.toml` 不该静默改写一排实例。
- `--refresh` 才重读 `config.toml` 与 `rules.d`，而且**只补快照里为空的字段** ＋ 追加缺失的
  bwrap token。
- 若今天的会话与快照里的决定矛盾（例如快照记着"走 Wayland"而当前只有 X11），会**警告**而不是
  默默重放。

### `twin remove <id> [--yes] [--purge]`

删除产物。

- **不加 `--yes` 只打印**：将要删的文件、私有状态目录（默认保留）、托盘表里那一条会怎样。
- `--purge` 连私有状态目录一起删（那是**唯一**不可逆的部分）。
- 不带 `--purge` 时状态目录保留 —— 下次同名重建会接着用。
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

### `twin rules --init <源桌面项 | 程序名 | id>`

**探测这个程序把状态放在哪，写出一份 `rules.d/<app>.toml` 草稿**（已存在则拒绝，绝不覆盖）。
这是全部命令里唯一会把"探测结果"变成文件的一步 —— 因为是你要它做的。

两条证据来源，草稿里分得很清楚：

| 证据 | 强度 | 草稿里 |
|---|---|---|
| 这台机器上目录**真的存在**（`~/.config/<名>`、`~/.local/share/<名>`、`~/.cache/<名>`、`~/.local/state/<名>`、`~/.<名>`） | 事实：程序已经在那儿写过东西了 | 写成**生效**的 `[[bind]]`（`private` 默认给私有副本） |
| 同上，但目录名只是**相近**（`~/.xwechat` 对 `wechat`） | 也是事实（目录确实存在），但那句话里的"属于这个程序"是推断 | 同样生效，证据写成 `目录存在（名字相近）` |
| 二进制里的 `~/…` 路径字面量 | 猜测：可能属于可选功能、别的平台，或一个只是被链接进来的库 | **注释掉** |

点目录（`~/.<名>` 那种）是按**名字匹配**找的，不是按名字查的：程序给目录起的名字和它在桌面项里
的名字经常不一样，只查 `~/.wechat` 会漏掉 `~/.xwechat`。`~/.config`、`~/.cache`、`~/.local`
这三个是整机共用的根，名字撞上也不作数。

字面量还要过两道闸：只保留落在状态根（或 `~/.<名>`）下面的路径，而且那一段必须与程序名有关 ——
Chromium 会写 `~/.config/autostart`、Qt 会读 `~/.config/fontconfig`，把它们当"这个程序的私有数据"
绑掉，会让半个桌面跟着坏，而症状看起来像是 twin 的 bug。

探测到的东西也可以只看不写：`twin new` 打印的 `路径映射` 一节里同样列出"**未应用**"的候选目录
（只含存在性证据那一条，因为它在每次生成时都要跑一遍）。

### `twin version`

打印版本（构建时可用 `-ldflags "-X main.version=…"` 注入）。

---

## 3. `new` 选项

### 身份与命名

| 选项 | 默认 | 说明 |
|---|---|---|
| `--as <id>` | 源基名 ＋ 最小空闲序号（从 2 起） | 实例 id。**同时是**窗口 `app_id` 与托盘 `SNI Id` |
| `--name <显示名>` | 源 `Name[<语言>]` ＋ 序号 | 显式给出时，若源 Keywords 不含中文，该名字会被前置进关键词 |
| `--name-en <英文名>` | 源 `Name` ＋ 序号 | 与 `--name` 相同时不写 `Name[en]` |
| `--comment <说明>` | 源 `Comment[<语言>]` → `Comment` | |
| `--comment-en <英文说明>` | — | 写 `Comment[en]=` |
| `--keywords <k1;k2;>` | 见 §"默认值推导" | 搜索关键词，覆盖各层 |
| `--categories <C;D;>` | rules.d → 源桌面项 | 分类 |
| `--level <级别>` | 各配置层 → `alias` | `none` \| `alias` \| `redirect`（`argument`/`env` 未实现，会被拒绝） |

### 图标

| 选项 | 默认 | 说明 |
|---|---|---|
| `--icon <png\|svg>` | 源桌面项的 `Icon=` | 源图标；找不到就拒绝生成 |
| `--badge <文字>` | `id` 尾部数字 | 角标文字。可用字符仅 `0-9 A-Z` 与 `-+.!?/` 和空格 |
| `--badge-style <风格>` | `config.toml` → `corner` | `corner` \| `number` \| `dot` |

### 隔离与路径

| 选项 | 可重复 | 说明 |
|---|---|---|
| `--map <canonical>:<private>` | ✅ | 路径映射。程序看到 `canonical`，数据实际存在 `private`。两端都会自动 `mkdir -p` |
| `--rw <dir>` | ✅ | 把这个目录在沙箱内**升回可写**（等价于 `--map dir:dir`；在 `--ro-bind / /` 之后原地升级） |
| `--tmpfs <dir>` | ✅ | 让该目录在沙箱内为空（一份 tmpfs 盖上去） |
| `--no-tmpfs` | — | 完全不要 tmpfs。**X11 程序必须如此**（`--tmpfs /tmp` 会砍掉 `/tmp/.X11-unix/X0`） |

> `--map` / `--rw` / `--tmpfs` 与透传区不同：它们**享受** `~/` 展开与 `mkdir` 补齐。
> 数据路径请用 `--map`，不要塞进 `--` 后面的透传区。

### 托盘与覆盖

| 选项 | 默认 | 说明 |
|---|---|---|
| `--no-tray` | 否 | 不写宿主托盘覆盖表（这个实例的托盘图标保持程序自己的） |

整机级别的开关是 `config.toml` 的 `defaults.tray_override = false`。

### 执行控制

| 选项 | 说明 |
|---|---|
| `--force` | 覆盖已存在但**不是** twin 生成的桌面项／实例 |
| `--dry-run` | 只打印将要写什么（文件清单、桌面项全文、`instance.toml` 全文、bwrap 调用、托盘将写入的行、推导值），一个字节不落盘 |

### bwrap 透传

```
twin new <source> [选项] -- <bwrap 选项…>
```

第一个 `--` 之后的 token **原样**透传给 bwrap，拼在基座之后、程序之前：

```bash
twin new wechat -- --unshare-pid --setenv TWIN_FOO bar
```

- 排在基座之后 ⇒ 同目标的挂载由**它**胜出（bwrap 后挂载遮蔽前者）。
- ⚠️ 不接受：`--`（会截断 bwrap 的解析）、`--argv0`（破坏身份不变量）、需要 fd 的选项。
- ⚠️ 透传区**不享受** `mkdir` 补齐与 `~/` 展开。
- 校验是 lint（警告），不是改写。

---

## 4. 退出码

| 码 | 含义 |
|---|---|
| `0` | 成功。**包括事实性跳过**：`--no-tray` / 整机 `tray_override = false` / 这台机器根本没有托盘宿主 |
| `1` | 失败，或自检里有 ❌ |
| `2` | 用法错误（未知选项、非法 `--level`、`--` 之后不是 bwrap 选项、`HOME` 未设置…） |
| `3` | **身份已经成立，但还差一步必须由人执行。** 目前两个来源：<br>① 托盘覆盖表写不进去（无会话总线 —— 安装脚本、无图形会话）<br>② 内核限制 `userns` 且机器上找不到跳板 ⇒ 要你 `sudo` 装一份 profile |

退出码 3 的意义在于把**"去图形会话里跑一下"**和**"这东西坏了"**分开。脚本可以据此决定是
提示用户、还是报错退出。

---

## 5. 文件表

| 路径 | 是什么 | 谁写 |
|---|---|---|
| `~/.config/twin/config.toml` | 环境层：这台机器的默认 | 你（`twin config` 出模板） |
| `~/.config/twin/rules.d/<app>.toml` | 应用知识层 | 你 |
| `~/.config/twin/rules.d/<id>.json` | L5 规则（仅 `level=redirect`） | 生成器 |
| `~/.local/share/applications/<id>.desktop` | 身份锚点 | 生成器 |
| `~/.local/share/appicons/<id>.png` | 图标 | 生成器 |
| `~/.local/share/twin/<id>/launch` | 薄启动器 | 生成器 |
| `~/.local/share/twin/<id>/instance.toml` | 实例快照（真源） | 生成器，你改，再 regen |
| `~/.local/share/twin/bin/bwrap` | 私有 bwrap 副本（共享，一台机器一份） | 生成器（仅需要时） |
| `~/.local/share/twin/bin/bootstrap.js` | 跳板脚本（共享） | 生成器（仅需要时） |
| `~/.local/state/twin/<id>/` | 私有数据（持久） | 运行期 |

⚠️ **别把产物放到 `/opt` 或任何 root 属主的位置**：手写版的 `wechat2.sh` 就是这样，结果图标和
脚本都改不动。

---

## 6. 拒绝清单（宁可不做，不做近似）

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

## 7. 典型用法

```bash
# 最省事：全部推导
twin new wechat

# 先看再写
twin inspect wechat
twin new wechat --dry-run

# 明确指定身份
twin new wechat --as wx-work --name "微信·工作" --badge W --badge-style number

# 让两份实例彻底分家（连配置目录一起隔离）
twin new wechat --as wx-alt \
  --map '~/.config/wechat:~/.local/state/twin/wx-alt/config' \
  --map '~/.xwechat:~/.local/state/twin/wx-alt/xwechat'

# 一个只想多开小号、其它全共享的浏览器
twin google-chrome --as chrome-alt --map '~/.config/google-chrome:~/.local/state/twin/chrome-alt/profile'

# 底层的额外隔离（透传）
twin new wechat -- --unshare-pid

# 改完快照重放
$EDITOR ~/.local/share/twin/wechat2/instance.toml
twin regen wechat2

# 先看要删什么，再删
twin remove wechat2
twin remove wechat2 --yes
twin remove wechat2 --yes --purge      # ⚠️ 连私有数据一起删，不可逆
```
