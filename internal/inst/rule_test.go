package inst

import (
	"encoding/json"
	"strings"
	"testing"
)

func ruleFor(t *testing.T) (*RedirectRule, []byte) {
	t.Helper()
	it := base()
	it.Level = LevelRedirect
	rule := NewRedirectRule(it, "/home/u/.local/share/appicons/wechat2.png", it.AliasPath())
	body, err := rule.Render()
	if err != nil {
		t.Fatal(err)
	}
	return rule, body
}

// The rule must name this instance, point at this instance's desktop entry, and
// carry evidence; a rule that fails any of those is ignored by the extension
// without a word, which is the failure this check exists to catch.
func TestRedirectRuleRoundTrip(t *testing.T) {
	_, body := ruleFor(t)

	got, err := ParseRedirectRule(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate("wechat2"); err != nil {
		t.Errorf("自产的规则应当自洽：%v", err)
	}
	if got.Target != "wechat2.desktop" {
		t.Errorf("target = %q", got.Target)
	}
	if !got.Enabled {
		t.Error("新规则必须 enabled")
	}
	if !got.Carries("/opt/wechat/wechat2") {
		t.Errorf("证据里应当有别名路径，得到 %+v", got.Match)
	}
	// The private data directory *name* must not leak: 设计 §5.4 calls that a
	// fingerprint in /proc/*/mountinfo.
	if strings.Contains(string(body), ".xwechat") {
		t.Error("规则不该带上私有目录名（.xwechat）")
	}
	if !strings.Contains(string(body), "/.local/state/twin/wechat2") {
		t.Error("应当带有不含家目录前缀的私有状态根")
	}
}

// Render is what regen replays, so it must be byte-stable.
func TestRedirectRuleRenderIsStable(t *testing.T) {
	_, a := ruleFor(t)
	_, b := ruleFor(t)
	if string(a) != string(b) {
		t.Error("同样的实例必须渲染出同样的字节")
	}
	// And it must be real JSON, not a hand-rolled approximation.
	var any map[string]any
	if err := json.Unmarshal(a, &any); err != nil {
		t.Fatalf("不是合法 JSON：%v", err)
	}
}

func TestRedirectRuleValidateCatchesDrift(t *testing.T) {
	cases := map[string]func(*RedirectRule){
		"别的实例的 id":    func(r *RedirectRule) { r.ID = "other" },
		"target 指错地方": func(r *RedirectRule) { r.Target = "other.desktop" },
		"没有证据":        func(r *RedirectRule) { r.Match = RedirectMatch{} },
		"被停用":         func(r *RedirectRule) { r.Enabled = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rule, _ := ruleFor(t)
			mutate(rule)
			if err := rule.Validate("wechat2"); err == nil {
				t.Error("应当报错")
			}
		})
	}
}
