# 嵌套 user namespace：Electron / Chromium 为什么起不来，以及怎么修

> 这一页解释一件**光看产物查不出来**的故障：`launch` 与桌面项全都对，程序却 FATAL 退出。
> 真因不在 `twin` 写的东西里，在本机的 AppArmor 策略里。

---

## 1. 症状

包裹一个 Electron 应用（WorkBuddy、VS Code、Discord…）或任何 Chromium 系程序后：

- 双击桌面项没有反应，或窗口一闪即退；
- 日志里只有 `FATAL`，没有别的线索；
- `twin verify <id>` **全绿**（因为文件确实都写对了）；
- 换成一个普通的 GTK/Qt 程序，同一个包裹方式完全正常。

## 2. 症结：两个叠在一起的限制

Chromium 系程序启动时会**自己再建一层** user namespace 来做沙箱
（`unshare(CLONE_NEWUSER|CLONE_NEWNS)`）。在 Ubuntu 上这会撞上两个独立的事实：

### 事实 1 —— 内核只放行"profile 里写了 `userns`"的程序

```
/usr/lib/sysctl.d/10-apparmor.conf
  kernel.apparmor_restrict_unprivileged_userns = 1
```

⚠️ 这个文件**每次开机都会被写回**，任何动 `/usr/lib/sysctl.d` 的包安装后也会重跑
`sysctl --system` 把它写回 1。**所以"手工把它改成 0"不是一个解法**，只是一个明天就失效的
临时状态。

### 事实 2 —— `/usr/bin/bwrap` 的 profile 会给**每个后代**剥掉 capability

```
/etc/apparmor.d/bwrap-userns-restrict

profile bwrap /usr/bin/bwrap flags=(unconfined) {
  allow userns,                                  # ← 它自己能用 userns，没问题
  ...
  allow pix /** -> &bwrap//&unpriv_bwrap,        # ← 但每个后代都被叠上这份子 profile
}

profile unpriv_bwrap {
  audit deny capability,                         # ← 后代 capability 全丢
  ...
}
```

⇒ 在 bwrap 里跑的 Chromium **建不出嵌套 user namespace**，实测 `unshare(...)` 直接 **EPERM**，
于是它的 namespace 沙箱退到 SUID 沙箱、再 FATAL。

> 🔴 最反直觉的一点：**这条与 sysctl 是 0 还是 1 完全无关。** 所以"调 sysctl"这条路从方向上
> 就是错的 —— 真正的阻塞是 AppArmor 的 profile 叠加。

### 事实 3 —— 私有副本绕开事实 2，但立刻撞上事实 1

把 bwrap 复制一份到没人挂 profile 的路径上（比如
`~/.local/share/twin/bin/bwrap`），它就不再被叠加 `unpriv_bwrap` 了。
但它也因此**没有任何 profile 给它 `userns` 许可** ⇒ 在事实 1 之下，它自己**也**建不出 userns。

实测：**副本 ＋ 无标签 = EPERM**。

### 事实 4 —— 跳板把 2 和 3 接起来

`exec` 一个路径上挂着 `flags=(unconfined) { userns, }` 的二进制的**它自己的路径**：
内核在 exec 时把这个进程的 AppArmor 标签**替换**成 `unconfined`，此后**每个后代都继承**它。
再让这个进程 spawn 那份私有副本 ⇒ 副本就建得出嵌套 userns 了。

实测：用 VS Code 当跳板 ⇒ 标签变成 `vscode (unconfined)`，嵌套 userns **OK**。

---

## 3. 三条路线

```
程序需要嵌套 user namespace 吗？
  │  （由二进制里的字符串判定：chrome-sandbox / ELECTRON_RUN_AS_NODE / libcef.so）
  ├─ 不需要 ─────────────────────────▶ 路线 A：直接用系统 bwrap
  │                                     不复制、不引跳板，普通程序一个字节都不多写
  └─ 需要
       ├─ 内核没限制 userns ─────────▶ 路线 A′：私有副本，直接用
       └─ 内核在限制
            ├─ 找到 Electron 跳板 ──▶ ⭐ 路线 B：私有副本 + 跳板   （默认）
            └─ 一个都没有 ──────────▶ 路线 C：照常落全套产物
                                        ＋ 打印一份 AppArmor profile 文本
                                        ＋ 退出码 3
```

| 路线 | 前提 | twin 做什么 | 退出码 |
|---|---|---|---|
| **A** | 内核未限制，或程序不需要嵌套 ns | 系统 bwrap；**不产生副本、不引入跳板** | 0 |
| **A′** | 内核未限制但程序需要嵌套 ns | 私有副本（不再需要跳板） | 0 |
| **B** ⭐ | 内核受限 ＋ 机器上装了**任意一个 Electron**（VS Code 即可） | 私有副本 ＋ 跳板 | 0 |
| **C** | 两者都没有（纯服务器、没装任何 Electron） | 照常落全套产物，再打印 profile 文本 | **3** |

路线 A 优先是有意的：**用系统 bwrap 意味着发行版修 bwrap 时这个实例自动跟着受益**，而不是
守着一份可能过期的副本。副本只在真的需要时才产生。

## 4. 为什么跳板必须是 Electron

跳板要在**不开窗口**的前提下跑一段脚本。Electron 二进制在 `ELECTRON_RUN_AS_NODE=1` 下就是
一个纯 Node 解释器 —— 这是唯一一个**通用、无参、静默**的驱动方式。

浏览器 profile 同样给了 `userns`（`chrome` / `microsoft-edge-stable` / `flatpak` / `loupe`
在实测机器上都有），但也**没有**哪个通用开关能让 `/opt/google/chrome/chrome` 去跑我们的
bootstrap：不加参数它会开窗口，加参数就是它自己那一套命令行，不是"执行这个 js 文件"。

所以：

- `userns.Pick` 只在候选里挑**像 Electron 的**（判据是安装目录布局里有 `resources/app.asar`
  / `resources/app` / `libelectron.so` —— **不扫描 200 MB 的二进制**）；
- 你显式指定的跳板（`userns.trampoline = "<路径>"`）也会被校验"像不像 Electron"，
  不像就拒绝。

### ⚠️ 一个必须排除的坑：脚本

`/usr/share/code/bin/code` 是**一个 `sh` 脚本**。它的路径上确实挂着 `vscode` profile，
所以它会出现在候选列表里、看起来完美。但它**拒了 Node 模式**：

```
You are trying to start Visual Studio Code as a super user...
```

⇒ `Discover` 读文件头两个字节，见到 `#!` 就跳过。**必须这么做**，否则本机会选中那个脚本：
`--dry-run` 看着一切正常，**真跑起来才炸**。真正的 ELF 在旁边的 `/usr/share/code/code`，
排序时 `/usr/` 路径优先。

## 5. 跳板是怎么被"发现"的

`twin` 每次 `wrap` / `regen` 都重新扫一遍 `/etc/apparmor.d`，找这样的 profile：

- **路径附着**（`profile <名> <路径> …`，而不是抽象的 `profile <名> { … }`）；
- **带 `flags=(unconfined)`**；
- 并且**真的有 `userns` 规则**（注释里出现这个词不算）。

然后对每个候选检查：文件存在、可执行、**不是脚本**。排序规则：Electron 优先 → `/usr/` 路径
优先 → 路径字典序（⇒ 同一台机器上结果可复现）。

**这个设计的关键点是"每次重新发现"**：换一台机器只要重跑一次 `twin new <source>`，它就挑
**那台机器**的跳板。所以一个包裹是**可搬运**的 —— 不绑死任何特定程序，也不要求预先把机器
准备好。

真实机器上的实测结果：

```
11 个候选，其中 Electron 3 个
  /usr/share/code/code        (vscode)
  /opt/WaaChat/waachat
  /opt/WorkBuddy/workbuddy
非 Electron 8 个（flatpak / loupe / rootlesskit / runc / QtWebEngineProcess / chrome / msedge ×2）
⇒ 选中 /usr/share/code/code
```

## 6. 生成的启动器长什么样（路线 B）

```bash
mkdir -p "$HOME/.local/state/twin/workbuddy2"

exec env ELECTRON_RUN_AS_NODE=1 \
  '/usr/share/code/code' \
  "$HOME/.local/share/twin/bin/bootstrap.js" \
  "$HOME/.local/share/twin/bin/bwrap" \
  --ro-bind / / \
  ... \
  -- '/opt/WorkBuddy/workbuddy2' "$@"
```

三层各自解决一件事：

| 层 | 解决什么 |
|---|---|
| `exec env ELECTRON_RUN_AS_NODE=1 <跳板>` | 让内核在 exec 时把标签换成 `unconfined`（事实 4） |
| `<bootstrap.js>` | 只跑脚本、不开窗口；并且**删掉** `ELECTRON_RUN_AS_NODE`（否则被包裹的程序若自己也是 Electron，会以 Node 模式启动 —— 进程在、窗口不出来） |
| `<副本 bwrap>` | 没有 profile 附着 ⇒ 不被叠加 `unpriv_bwrap`（事实 2） |

`bootstrap.js` 还会做一件事：**自检标签是否真的被换掉**。读 `/proc/self/attr/current`，
若仍是 `unconfined`（说明跳板程序的 profile 不见了 —— 换了机器、应用被卸载、升级后不再带
profile），它会提前打印人能看懂的原因，而不是让 bwrap 抛一个
`setting up uid map: Permission denied`：

```
[twin] 警告: 当前 AppArmor 标签是 "unconfined"（没有被换掉）。
          跳板程序的 profile 可能已经不存在了。
          修法: twin regen <实例 id>   —— 会重新挑一个跳板。
```

## 7. 路线 C：兜底

内核受限、机器上又没有任何 Electron 时，`twin` **不会**自己写 `/etc/`：写 `/etc` 是一个
`wrap` 不该有的副作用，而使用者离同样的结果只差一次复制粘贴。

它的做法是：

1. **照常落全套产物** —— 身份与文件都是完整的（`verify` 甚至全绿）；
2. 打印一份 AppArmor profile 文本，指向私有副本的路径；
3. 以**退出码 3** 结束。

```
还差一步：这台机器上没有可用的跳板
  这个程序运行时要自己建一个嵌套 user namespace，而内核只放行
  「profile 里写了 userns 许可」的程序（/usr/lib/sysctl.d/10-apparmor.conf）。
  私有 bwrap 副本故意不带 profile，所以它自己建不了。

  两条路，任选其一：
    1) 装一个 Electron 应用（VS Code 就够了），再跑一次 `twin regen <id>`；
       它会自动发现新跳板，什么都不用配置。
    2) 把下面这份 profile 存成 /etc/apparmor.d/twin-bwrap 并加载：
       ...
```

profile 的形状（`userns.ProfileHint` 生成）：

```
abi <abi/4.0>,
include <tunables/global>

profile "twin-bwrap" "<副本绝对路径>" flags=(unconfined) {
  userns,
}
```

```bash
sudo apparmor_parser -r /etc/apparmor.d/twin-bwrap
```

🔴 **关键是绝不写** `pix … -> &unpriv_bwrap` 那种 `deny capability` 的子 profile ——
否则后代又会丢掉 capability，绕回事实 2。

装完之后运行期**全用户态**（不需要 root 守护进程），特权动作天然属于安装脚本。

## 8. 相关旋钮

| 位置 | 键 | 作用 |
|---|---|---|
| `config.toml` | `[tools] bwrap_copy` | `"auto"`（默认）／`""` 不做副本（Electron 会起不来，会警告）／显式路径 |
| `config.toml` | `[userns] trampoline` | `"auto"`（默认）／`"off"`（→ 路线 C）／显式路径（必须是 Electron） |
| `profile.yml` | `isolation.nested_ns` | 这个程序要不要嵌套 ns。**总是写**：它是应用知识，跟快照走 |

`nested_ns` 与"用哪个跳板"分隔在两个文件里是刻意的：前者跟着**程序**，后者跟着**机器**。

## 9. `doctor` 与 `verify` 各自看什么

`doctor` 里的一行：

```
OK   非特权 userns   内核在限制（...10-apparmor.conf 把 ...设成 1），但发现 3 个 Electron
                     跳板可用，例如 /usr/share/code/code —— Chromium / Electron 会走
                     「私有 bwrap 副本 + 跳板」这条路
```

🔴 判据是**"有没有跳板可用"**，不是"限制本身"。内核在限制**不是**问题，限制之下无路可走才是。

`verify` 里的一行（`checkNestedNS`）：

| 情况 | 报告 |
|---|---|
| 副本不存在 | FAIL |
| 副本在，但启动器里没有跳板（而机器又在限制） | FAIL |
| 启动器里有跳板，但那个文件已经不是 Electron 了 | WARN |
| 齐全 | OK |

它会解析**启动器文本**把跳板挖出来 —— 因为那才是真正会跑的东西。跳板被卸载时，快照里的每条
路径都还在，别的检查全都抓不到。

## 10. 边界与已知局限

- ⚠️ **跳板是外部依赖**：那个应用被卸载/改名，实例就起不来了。这正是 `verify` 里那条 FAIL
  的用途 —— 它在故障发生时告诉你去 `regen`。
- ⚠️ 路线 B 在真机上**还没跑过一次真启动**：`--dry-run` 只证明了**选择**是对的（三条事实由
  独立探针取证）。要验真启动：生成后从桌面项启一次，再 `twin verify <id>` 回读。
- ⚠️ 副本只在 `wrap` / `regen` 时与系统 bwrap 同步字节。**过期的副本是一个静默问题** ——
  它会一直正常工作，直到某天"bwrap 的某个修复"对你变得重要。所以同步发生在人正看着输出的
  时刻。

## 11. 事实来源

| 事实 | 取证方式 |
|---|---|
| sysctl 被钉在 1，且每次开机回写 | 读 `/usr/lib/sysctl.d/10-apparmor.conf` ＋ `userns.Restricted()` |
| 系统 bwrap 里嵌套 unshare ⇒ EPERM | 真机探针（`unshare(CLONE_NEWUSER\|CLONE_NEWNS)`） |
| 私有副本无标签 ⇒ EPERM | 真机探针 |
| VS Code 当跳板 ⇒ 标签 `vscode (unconfined)` ＋ 嵌套 userns OK | 真机探针（`/proc/self/attr/current`） |
| `/usr/share/code/bin/code` 是 sh 且拒 Node 模式 | 直接执行 ＋ 读文件头 |
| 路线 C 全链路（scratch HOME ＋ `trampoline = "off"`） | 产物照常落盘、`verify` 全绿、打印 profile、**退出码 3** |
