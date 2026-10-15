package tray

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edwinhuish/twin/internal/inst"
)

const tableIcon = "/home/e/.local/share/appicons/wechat2.png"

// 同一份内容渲染两次必须逐字节相同：install 每次都要写这张表，而扩展在监视那个目录 ——
// 一次没变的重放如果内容抖了，shell 就会收到一次没必要的重读事件。
func TestOverrideRendersTheSameBytesEveryTime(t *testing.T) {
	first, err := NewOverride("wechat2", tableIcon).Render()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewOverride("wechat2", tableIcon).Render()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("两次渲染不一致：\n%s\n%s", first, second)
	}

	row, err := ParseOverride(first)
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != "wechat2" || row.Icon != tableIcon {
		t.Errorf("往返之后丢了字段：%+v", row)
	}
}

// 内容没变就不该碰文件：扩展按目录事件重读整张表，mtime 一抖就是一次白重读。
func TestEnsureOverrideDoesNotRewriteIdenticalBytes(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	row := NewOverride("wechat2", tableIcon)

	out, err := EnsureOverride(p, row)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Written {
		t.Error("第一次应当落盘")
	}
	path := p.TrayTable("wechat2")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// 文件系统的 mtime 精度有限，等一下再看才有意义。
	time.Sleep(10 * time.Millisecond)
	out, err = EnsureOverride(p, row)
	if err != nil {
		t.Fatal(err)
	}
	if out.Written {
		t.Error("内容相同不该重写")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("没变化的一次调用动了文件的 mtime")
	}
}

// 「这个实例没有这一行」是一个状态，不是错误：自检和预演都要靠这个区别去说不同的话。
func TestReadOverrideTellsMissingFromUnreadable(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}

	row, found, err := ReadOverride(p, "wechat2")
	if err != nil || found || row != nil {
		t.Errorf("文件不在应当是 (nil, false, nil)：%+v %v %v", row, found, err)
	}

	if err := os.MkdirAll(filepath.Dir(p.TrayTable("wechat2")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.TrayTable("wechat2"), []byte("{ 这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadOverride(p, "wechat2"); err == nil {
		t.Error("文件在但读不了，不能当成「没有这一行」")
	}
}

func TestRemoveOverrideIsQuietWhenThereIsNothingToRemove(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	if _, err := EnsureOverride(p, NewOverride("wechat2", tableIcon)); err != nil {
		t.Fatal(err)
	}

	gone, err := RemoveOverride(p, "wechat2")
	if err != nil || !gone {
		t.Errorf("文件在，应当删掉并说删了：%v %v", gone, err)
	}
	if _, found, err := ReadOverride(p, "wechat2"); err != nil || found {
		t.Errorf("删完之后应当读不到了：%v %v", found, err)
	}
	gone, err = RemoveOverride(p, "wechat2")
	if err != nil || gone {
		t.Errorf("再删一次不该报错，也不该说删了：%v %v", gone, err)
	}
}

// 表目录就在实例目录旁边，而 twin list 会把那里不认识的目录挨个过一遍：一个实例都没有的
// 表目录不能被当成坏实例报出来。
func TestTrayTableDirIsNotAnInstance(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	if _, err := EnsureOverride(p, NewOverride("wechat2", tableIcon)); err != nil {
		t.Fatal(err)
	}
	list, err := inst.List(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("tray.d 不是实例：%v", list)
	}
}
