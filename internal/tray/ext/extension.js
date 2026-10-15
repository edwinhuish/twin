// twin-tray@edwinhuish.github.io —— twin 的宿主侧一环。
//
// 这个扩展做两件事，都由"托盘图标长什么样"这一件事派生：
//
//   1. **画哪张图**：twin 写一份中立的文件表（~/.local/share/twin/tray.d/<id>.json，
//      见 internal/tray/table.go），本扩展读它，在宿主渲染每个托盘项之前把自定义图标
//      塞进去。宿主那张 dconf 表（custom-icons）是另一条路，两条只用一条：
//      twin 在 install 时按"本扩展是否被 shell 加载"来选，选中的那条才写。
//   2. **画多大**：让每个 appindicator 的 IconActor 的框跟它自己的 icon-size 属性同步
//      （宿主把框钉死在 16px，见下面那段）。
//
// 为什么图标要在这里塞：宿主的 `IconActor._updateIconByType(iconType, iconSize)` 是
// **每一次**真正设置图标的入口，它先同步读 `this._customIcons`（一个 SNIconType → 绝对
// 路径的 Map），有值就走宿主的自定义图标实现，否则回落到程序推来的 name / pixmap。我们
// 在那个读之前把值塞进同一个 Map，于是**复用的是宿主自己的那条渲染路径**（绝对路径、
// 主题样式、attention 回落、emblemed 包装全都不用重写）—— 换句话说，屏幕上画出来的东西
// 与"写 dconf 表"那条路逐像素相同，差别只在谁把它放进去。
//
// 那条路要会话总线才写得进去（gsettings 在无总线时会"假成功"），文件不需要 —— 这是
// twin 把首选路径放在这里的全部理由。
//
// 补丁点只碰宿主自己那个函数本来就在用的三个名字：`_updateIconByType`（补丁的目标）、
// `this._customIcons`（注入点）、`this._indicator.id`（判定键，宿主也用它去 dconf 表里
// 找自己那一行）。所以宿主内部一改名，最坏结果是**判据拿不到 → 一行警告、图标回到程序
// 自带的那张**，而不是把托盘画坏；不 import 宿主的模块、不扫 actor 树去找 IconActor，
// 类原型是从状态区里现成的实例上取的（同一个类，谁先建都一样）。
//
// ⚠️ 已知副作用（与写 dconf 表那条路完全相同）：一旦 `_customIcons` 非空，
// `_updateIconByType(OVERLAY, …)` 会查不到 overlay 的自定义项而把 overlay 徽标清掉
// （上游行为）。没有 overlay 图标的程序不受影响。
//
// 关于尺寸那件事（宿主的一个 bug）：宿主对 custom-icons 指向的文件走 `GIcon` →
// `St.Icon`，屏幕上多大由 IconActor 的**分配框**决定（`st-icon.c` 全文件没有一处
// `clutter_actor_set_size()`），而那个框只在 `IconActor._init()` 里写一次：
//
//     this.height = iconSize * themeContext.scale_factor;   // iconSize = DEFAULT_ICON_SIZE
//
// DEFAULT_ICON_SIZE = Panel.PANEL_ICON_SIZE || 16，而 PANEL_ICON_SIZE 在 gnome-shell 49
// 就被删掉了（45–48 有，但没导出），所以它恒为 16。此后只有 notify::scale-factor 那条路
// 会重刷这个框；_updateIconSize() 把 set_icon_size(this._iconSize) 写上了，却漏了框。
// 于是症状是：设置里 icon-size 改成 30，程序自己的位图变成 30，而 twin 的自定义图标
// 仍是 16（Yaru 主题给 .system-status-icon 的 1.091em）。
//
// 时机是安全的 —— notify::icon-size 在 _updateIconSize() 里就发出来了，而图标内容是之后
// 才建的（_updateWhenFullyReady 的顺序是 尺寸 → 类名 → custom-icons → 内容），所以位图那条
// 路稍后仍会用自己的显式宽高覆盖我们，位图图标不受影响。icon_size > 0 这个条件同时排除了
// 别的 actor：只有宿主的 _updateIconSize() 会调 set_icon_size()，主题里的 icon-size 走的是
// 另一条路（st_icon_update_icon_size 里的 theme_icon_size），不会让这个属性变正。
//
// 它不 fork 宿主：系统目录里那份被导入的仍是同一个模块，我们只是在运行时补一次尺寸同步、
// 在每个图标设上去之前塞一次自定义项，disable 时把连接与补丁全部拆掉。

import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import St from 'gi://St';

import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as Extension from 'resource:///org/gnome/shell/extensions/extension.js';

// 宿主把状态区里的指示器注册成 appindicator-<SNI Id>（indicatorStatusIcon.js 的
// addIconToPanel），所以 role 名前缀就是"这个指示器是宿主画的"的判据 —— 不去 import
// 宿主的模块，也不读它的私有字段。
const ROLE_PREFIX = 'appindicator-';

// appIndicator.js 的 SNIconType：NORMAL=0 / ATTENTION=1。它没有被 export，所以这里按
// 字面量对齐；两处必须一致。只塞前两个：twin 的图标表对普通态与提醒态就是同一张图。
const SN_ICON_NORMAL = 0;
const SN_ICON_ATTENTION = 1;

// 一次 install 会写一个文件，重放一次可能连写几个 —— 合并成一次重读，别每个事件都重扫。
const RELOAD_DEBOUNCE_MS = 250;

/* twin 写的那份表在哪。user_data_dir 跟着 XDG_DATA_HOME 走，与 twin 侧的
 * Paths.TrayTableDir 是同一个推导（~/.local/share/twin/tray.d）。 */
function trayDir() {
    return GLib.build_filenamev([GLib.get_user_data_dir(), 'twin', 'tray.d']);
}

export default class TwinTrayExtension extends Extension.Extension {
    enable() {
        // actor → {sizeId, destroyId}
        this._watched = new Map();
        // [box, signalId]
        this._boxSignals = [];
        // [Gio.FileMonitor] —— 表目录（或它的父目录）的文件监视器
        this._monitors = [];
        // actor → 我们塞进去的那个路径（还原时只删自己塞的，不碰宿主自己的 dconf 行）
        this._injected = new Map();
        // SNI Id → 图标绝对路径
        this._table = new Map();
        // 挂上的补丁（null = 还没挂上：还没有任何一个托盘图标）
        this._patch = null;
        this._reloadSource = 0;
        this._warned = false;

        this._readTable();
        this._hookBoxes();
        const found = this._scan();
        this._armMonitors();
        console.debug(`[twin-tray] 已接管 ${found} 个托盘图标（尺寸＋表里的自定义图标），`
            + `twin 写的表里有 ${this._table.size} 条`);
    }

    disable() {
        for (const [actor, ids] of this._watched) {
            actor.disconnect(ids.sizeId);
            actor.disconnect(ids.destroyId);
        }
        this._watched.clear();

        for (const [box, id] of this._boxSignals)
            box.disconnect(id);
        this._boxSignals = [];

        // 先摘掉补丁，再还原每个图标：还原后要让宿主自己重渲染一次，否则屏幕上还留着
        // 我们最后塞进去的那张图，直到程序下次推图标 —— 停用一个扩展不该留下痕迹。
        this._unpatch();
        this._restoreAll();

        for (const monitor of this._monitors)
            monitor.cancel();
        this._monitors = [];

        if (this._reloadSource) {
            GLib.source_remove(this._reloadSource);
            this._reloadSource = 0;
        }

        delete this._watched;
        delete this._boxSignals;
        delete this._injected;
        delete this._table;
    }

    // ------------------------------------------------------------ 图标表

    /* 读表目录里的每个 *.json。目录不存在是常态（还没有实例需要这条路），
     * 单个文件读坏了只丢那一行：一条坏行不能让整机托盘变回原图。 */
    _readTable() {
        const table = new Map();
        const dir = Gio.File.new_for_path(trayDir());

        let names = [];
        try {
            const children = dir.enumerate_children(
                'standard::name', Gio.FileQueryInfoFlags.NONE, null);
            let info;
            while ((info = children.next_file(null)) !== null)
                names.push(info.get_name());
            children.close(null);
        } catch (e) {
            this._table = table;
            return;
        }

        for (const name of names) {
            if (!name.endsWith('.json'))
                continue;
            try {
                const [ok, bytes] = GLib.file_get_contents(
                    GLib.build_filenamev([trayDir(), name]));
                if (!ok)
                    continue;
                const text = typeof bytes === 'string'
                    ? bytes : new TextDecoder().decode(bytes);
                const row = JSON.parse(text);
                if (typeof row?.id === 'string' && row.id
                    && typeof row?.icon === 'string' && row.icon)
                    table.set(row.id, row.icon);
            } catch (e) {
                console.warn(`[twin-tray] ${name} 读不了，跳过这一行：${e}`);
            }
        }
        this._table = table;
    }

    /* 监视表目录；它还不存在时退一级监视父目录（twin 会建出它），每次重读之后
     * 重新决定监视哪一级 —— 目录是在我们眼皮底下被建出来的。 */
    _armMonitors() {
        for (const monitor of this._monitors) {
            try {
                monitor.cancel();
            } catch {
                // 已经没了就算了
            }
        }
        this._monitors = [];

        const dir = trayDir();
        const watched = GLib.file_test(dir, GLib.FileTest.IS_DIR)
            ? dir
            : GLib.path_get_dirname(dir);

        try {
            const monitor = Gio.File.new_for_path(watched)
                .monitor_directory(Gio.FileMonitorFlags.WATCH_MOVES, null);
            monitor.connect('changed', () => this._scheduleReload());
            this._monitors.push(monitor);
        } catch (e) {
            // 父目录也不在（这台机器还没有任何实例）：这时没有文件事件可听，表靠 enable 与
            // 每次 _scan（新托盘图标出现）重读 —— 装完实例、再启动程序，正好撞上。
            console.debug(`[twin-tray] 监视不了 ${watched}（表只在新图标出现时重读）：${e}`);
        }
    }

    _scheduleReload() {
        if (this._reloadSource)
            return;
        this._reloadSource = GLib.timeout_add(GLib.PRIORITY_DEFAULT, RELOAD_DEBOUNCE_MS, () => {
            this._reloadSource = 0;
            this._reload();
            return GLib.SOURCE_REMOVE;
        });
    }

    _reload() {
        this._readTable();
        this._armMonitors();
        // 两个方向都要看：表里新增的行要落到已经画着的图标上，被删掉的行要从屏幕上退回去。
        const actors = new Set([...this._watched.keys(), ...this._injected.keys()]);
        for (const actor of actors)
            this._refresh(actor);
    }

    // ------------------------------------------------------- 宿主渲染入口

    /* 把自定义图标塞进宿主的 `_customIcons`，早于原实现读它（它是同步读的）。
     *
     * 判据是 `this._indicator.id`，与宿主自己拿 dconf 表去找那一行用的是同一个键 ——
     * 所以两条路匹配到的托盘项完全相同，切换路径不会改变"哪个图标被换掉"。 */
    _inject(actor) {
        const id = actor?._indicator?.id;
        const icon = typeof id === 'string' ? this._table.get(id) : null;
        const custom = actor?._customIcons;
        if (!icon || !custom || typeof custom.set !== 'function')
            return;

        custom.set(SN_ICON_NORMAL, icon);
        custom.set(SN_ICON_ATTENTION, icon);
        this._injected.set(actor, icon);
    }

    /* 还原一个图标：只删我们塞进去的那两个值（值变了说明宿主自己重写过，
     * 那就不是我们的了），再请宿主重渲染一次。 */
    _restore(actor) {
        const icon = this._injected.get(actor);
        if (icon === undefined)
            return;
        this._injected.delete(actor);

        const custom = actor?._customIcons;
        if (custom && typeof custom.get === 'function' && typeof custom.delete === 'function') {
            for (const key of [SN_ICON_NORMAL, SN_ICON_ATTENTION]) {
                if (custom.get(key) === icon)
                    custom.delete(key);
            }
        }
        this._rerender(actor);
    }

    _restoreAll() {
        for (const actor of [...this._injected.keys()])
            this._restore(actor);
    }

    /* 表变了之后让一个图标跟上：有值就塞进去再重渲染，值没了就还原。
     * 只有重渲染才能把屏幕上的旧图换掉（宿主缓存了 gicon），所以这一步只在值真的变了
     * 的时候做 —— 每次 child-added 都会扫一遍状态区，白重渲染会让整排图标闪。 */
    _refresh(actor) {
        const id = actor?._indicator?.id;
        const icon = typeof id === 'string' ? this._table.get(id) : null;
        if (!icon) {
            this._restore(actor);
            return;
        }
        const had = this._injected.get(actor);
        this._inject(actor);
        if (had !== icon)
            this._rerender(actor);
    }

    /* 请宿主重新算一次图标。`_invalidateIcon` 是宿主自己的刷新入口
     * （`changed::custom-icons`、主题变化走的都是它）。 */
    _rerender(actor) {
        try {
            if (typeof actor?._invalidateIcon === 'function')
                actor._invalidateIcon();
        } catch (e) {
            console.debug(`[twin-tray] 刷新托盘图标失败：${e}`);
        }
    }

    /* 给宿主的 IconActor 挂补丁。原型从状态区里现成的实例上取：`_updateIconByType`
     * 定义在它自己的类上，而同一份 JS 模块只有一个类实例 —— 所以取一次就够所有图标用，
     * 不必 import 宿主的模块，也不必去猜它装在哪个目录。
     *
     * 收的是原型而不是那个实例：补丁活到 disable，没必要为它留着一个 actor 的引用。 */
    _patchIconActor(proto) {
        if (this._patch)
            return;
        const original = proto?._updateIconByType;
        if (typeof original !== 'function') {
            this._warnOnce('宿主的 IconActor 里没有 _updateIconByType：'
                + '托盘图标只能走宿主自己那套（自定义图标不会生效）');
            return;
        }

        const self = this;
        const replacement = function (iconType, iconSize) {
            // 判定失败绝不能让图标渲染整体失败 —— 回落成宿主自己那张图即可。
            try {
                self._inject(this);
            } catch (e) {
                self._warnOnce(`塞自定义托盘图标失败（回落成应用自带的图标）：${e}`);
            }
            return original.call(this, iconType, iconSize);
        };

        // ESM 严格模式：给不可写属性赋值会抛，包起来当作"没挂上"。
        try {
            proto._updateIconByType = replacement;
        } catch (e) {
            this._warnOnce(`挂不上托盘补丁：${e}`);
            return;
        }
        if (proto._updateIconByType !== replacement) {
            this._warnOnce('挂不上托盘补丁：宿主把方法定义成不可写的了');
            return;
        }
        this._patch = {proto, original, replacement};
    }

    _unpatch() {
        const patch = this._patch;
        this._patch = null;
        if (!patch)
            return;
        try {
            // 只在没人接着盖住我们的时候还原。
            if (patch.proto._updateIconByType === patch.replacement)
                patch.proto._updateIconByType = patch.original;
        } catch {
            // 还原失败不影响停用流程
        }
    }

    _warnOnce(message) {
        if (this._warned)
            return;
        this._warned = true;
        console.warn(`[twin-tray] ${message}`);
    }

    // ------------------------------------------------------------ 状态区

    // 状态区的三个盒子是 Panel 的直接子节点（panel.js 的 _init 里 _leftBox / _centerBox /
    // _rightBox 各 add_child 一次），而 addToStatusArea 把指示器 insert_child_at_index 进
    // 这三个盒子里的一个 —— 所以 child-added 正好是"又有程序起托盘图标了"的信号，不必去碰
    // _rightBox 这种私有字段，也不必轮询。
    _hookBoxes() {
        for (const box of Main.panel.get_children()) {
            try {
                this._boxSignals.push([box, box.connect('child-added', () => this._scan())]);
            } catch (e) {
                // 面板布局换成了不可连接的 actor：尺寸补丁照常作用于已经存在的图标，
                // 只是后起的程序要等下次 enable 才被接管。不因此让整个扩展失败。
                console.debug(`[twin-tray] 状态区盒子不可监听：${e}`);
            }
        }
    }

    _scan() {
        let found = 0;
        for (const [role, indicator] of Object.entries(Main.panel.statusArea)) {
            if (!role.startsWith(ROLE_PREFIX))
                continue;
            const icon = indicator?.icon;
            if (!icon)
                continue;
            this._patchIconActor(Object.getPrototypeOf(icon));
            this._watch(icon);
            // 补丁是"从这一刻起"生效的：这个图标可能在我们挂补丁之前就画好了，
            // 所以刚接管的图标要按表重渲染一次（_refresh 只在值真的变了时才动手）。
            this._refresh(icon);
            found++;
        }
        return found;
    }

    _watch(actor) {
        if (this._watched.has(actor))
            return;

        // 尺寸不是我们推出来的，是宿主自己算好的那个值（_updateIconSize 把设置读进
        // this._iconSize 再 set_icon_size()）。我们只负责把它同步到框上，所以设置一改、
        // 扩展一升级、宿主一换实现，这个补丁都跟着走。
        const sizeId = actor.connect('notify::icon-size', () => this._sync(actor));
        const destroyId = actor.connect('destroy', () => {
            actor.disconnect(sizeId);
            this._watched.delete(actor);
            this._injected.delete(actor);
        });

        this._watched.set(actor, {sizeId, destroyId});
        this._sync(actor);
    }

    _sync(actor) {
        const size = actor.icon_size;
        if (!(size > 0))
            return; // 宿主还没把尺寸写进来（构造期）：等 notify::icon-size
        const {scaleFactor} = St.ThemeContext.get_for_stage(global.stage);
        // 与宿主自己在 notify::scale-factor 里写的那两行一模一样：宽留给首选尺寸
        // （= icon_size），高显式钉住。两边算的是同一个数，所以不会互相打架。
        actor.height = size * scaleFactor;
        actor.width = -1;
    }
}
