// twin-tray@edwinhuish.github.io —— twin 的宿主侧一环。
//
// twin 给一个程序换了身份之后，还要给它换一张托盘图标，做法是把图标路径写进宿主的
// 覆盖表（org.gnome.shell.extensions.appindicator / custom-icons）。宿主对那张表有
// 两条绘制路径，而它们的**尺寸来源不同**：
//
//   · 程序自己发的位图：_createIconFromPixmap() 算出 scaledSize 后调
//     _setImageContent(content, scaledSize, scaledSize) —— 显式给 actor 定了宽高，
//     所以它跟着 icon-size 设置一起变。
//   · custom-icons 指向的文件：被包成 GIcon（_setGicon），尺寸交给 St.Icon 按 actor 的
//     **分配框**画出来 —— st-icon.c 全文件没有一处 clutter_actor_set_size()，屏幕上多大
//     由框决定，不由纹理决定。
//
// 而那个框在 IconActor._init() 里只写了一次：
//
//     this.height = iconSize * themeContext.scale_factor;   // iconSize = DEFAULT_ICON_SIZE
//
// DEFAULT_ICON_SIZE = Panel.PANEL_ICON_SIZE || 16，而 PANEL_ICON_SIZE 在 gnome-shell 49
// 就被删掉了（45–48 有，但没导出），所以它恒为 16。此后只有 notify::scale-factor 那条路
// 会重刷这个框；_updateIconSize() 把 set_icon_size(this._iconSize) 写上了，却漏了框。
// 于是症状是：设置里 icon-size 改成 30，程序自己的位图变成 30，而 twin 的自定义图标
// 仍是 16（Yaru 主题给 .system-status-icon 的 1.091em）。
//
// 本扩展只做一件事：让每个 appindicator 的 IconActor 的框跟它自己的 icon-size 属性同步。
// 时机是安全的 —— notify::icon-size 在 _updateIconSize() 里就发出来了，而图标内容是之后
// 才建的（_updateWhenFullyReady 的顺序是 尺寸 → 类名 → custom-icons → 内容），所以位图那条
// 路稍后仍会用自己的显式宽高覆盖我们，位图图标不受影响。icon_size > 0 这个条件同时排除了
// 别的 actor：只有宿主的 _updateIconSize() 会调 set_icon_size()，主题里的 icon-size 走的是
// 另一条路（st_icon_update_icon_size 里的 theme_icon_size），不会让这个属性变正。
//
// 它不 fork 宿主：系统目录里那份被导入的仍是同一个模块，我们只是在运行时补一次尺寸同步，
// disable 时把连接全部拆掉。宿主改了内部结构也不会把托盘弄坏 —— 最坏的结果是回到 16px，
// 也就是装这个扩展之前的样子。

import St from 'gi://St';

import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as Extension from 'resource:///org/gnome/shell/extensions/extension.js';

// 宿主把状态区里的指示器注册成 appindicator-<SNI Id>（indicatorStatusIcon.js 的
// addIconToPanel），所以 role 名前缀就是"这个指示器是宿主画的"的判据 —— 不去 import
// 宿主的模块，也不读它的私有字段。
const ROLE_PREFIX = 'appindicator-';

export default class TwinTrayExtension extends Extension.Extension {
    enable() {
        // actor → {sizeId, destroyId}
        this._watched = new Map();
        // [box, signalId]
        this._boxSignals = [];

        this._hookBoxes();
        const found = this._scan();
        console.debug(`[twin-tray] 已接管 ${found} 个托盘图标的尺寸`);
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

        delete this._watched;
        delete this._boxSignals;
    }

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
            this._watch(icon);
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
