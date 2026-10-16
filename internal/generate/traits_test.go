package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/inst"
)

// bits writes a fake "binary" — ScanTraits is a byte scan, so a file whose
// bytes contain the needle is indistinguishable from a real one.
func fakeBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The multi-call phrase is the only thing separating "renaming works" from
// "renaming breaks the program", and level=alias is the whole point of this
// tool — so the trait must fire on both spellings that appear in the wild.
func TestScanTraitsMultiCall(t *testing.T) {
	cases := map[string]string{
		"coreutils": "... coreutils: unknown program '(multi-call binary) ...",
		"busybox":   "BusyBox v1.37.0 ... BusyBox is a multi-call binary that combines many common Unix",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			tr, err := ScanTraits(fakeBinary(t, body))
			if err != nil {
				t.Fatal(err)
			}
			if !tr.MultiCall {
				t.Errorf("%s 应被识别为多合一二进制", name)
			}
			if !hasNote(tr.Notes, "多合一") {
				t.Errorf("应给出解释性备注，得到 %v", tr.Notes)
			}
		})
	}
}

func TestScanTraitsPlainBinaryIsNotMultiCall(t *testing.T) {
	tr, err := ScanTraits(fakeBinary(t, "just a normal program\x00libgtk-4.so\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if tr.MultiCall {
		t.Error("普通程序不该被误判为多合一")
	}
	if !tr.GTK {
		t.Error("针脚选对了就该看到 GTK")
	}
}

func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// scanTraits is exercised indirectly above; the plan-level guard is what
// actually protects the user, so assert on the message it produces.
func TestPlanRefusesMultiCallAtAliasLevel(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	insp := wechatInspection()
	insp.Traits.MultiCall = true

	_, _, _, err := Plan(p, Options{}, insp)
	if err == nil {
		t.Fatal("多合一二进制 + alias 应当被拒绝")
	}
	if !strings.Contains(err.Error(), "多合一") {
		t.Errorf("错误要说明原因（多合一），得到 %v", err)
	}

	// level=none never renames, so the same target is allowed there.
	if _, _, _, err := Plan(p, Options{Level: inst.LevelNone}, insp); err != nil {
		t.Errorf("level=none 不该因多合一被拒：%v", err)
	}
}

// A missing icon is refused before any write, so a failed wrap cannot leave a
// launcher behind with no desktop entry to claim it.
func TestPlanRefusesMissingIcon(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	insp := wechatInspection()
	insp.IconSource = ""

	_, _, _, err := Plan(p, Options{}, insp)
	if err == nil || !strings.Contains(err.Error(), "源图标") {
		t.Fatalf("缺图标应当被拒绝，得到 %v", err)
	}
}
