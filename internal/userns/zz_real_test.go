package userns

import (
	"os"
	"testing"
)

// TestRealDiscover is a manual diagnostic rather than a unit test: it reads
// this machine's real /etc/apparmor.d, so on any other machine its output is
// merely "what happens to be installed here". It skips unless asked, which
// keeps it out of CI while still leaving `twin doctor`'s source of truth
// one command away:
//
//	TWIN_REAL_PROBE=1 go test -run TestRealDiscover -v ./internal/userns/
func TestRealDiscover(t *testing.T) {
	if os.Getenv("TWIN_REAL_PROBE") == "" {
		t.Skip("本机诊断：设 TWIN_REAL_PROBE=1 才跑")
	}
	ok, why := Restricted()
	t.Logf("限制 = %v（%s）", ok, why)

	all := Discover(DefaultDir)
	n := 0
	for _, tr := range all {
		if tr.Electron {
			n++
		}
	}
	t.Logf("共发现 %d 个候选，其中 Electron %d 个", len(all), n)
	for _, tr := range all {
		t.Logf("  electron=%-5v %s", tr.Electron, tr.Describe())
	}
	if p, found := Pick(all); found {
		t.Logf("选中：%s", p.Describe())
	} else {
		t.Logf("选中：无（应当退到 profile 指引）")
	}
}
