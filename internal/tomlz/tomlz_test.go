package tomlz

import (
	"reflect"
	"strings"
	"testing"
)

// designDoc is the §5.1 example verbatim — the shape we must round-trip.
const designDoc = `
id      = "wechat2"
name    = "微信2"
name_en = "WeChat 2"
comment = "第二个微信"
source  = "/usr/share/applications/wechat.desktop"

[identity]
level    = "alias"      # alias | none | argument | env | redirect
wm_class = "wechat2"
notify   = true

[icon]
source = "/usr/share/icons/hicolor/512x512/apps/wechat.png"
badge  = "2"
style  = "corner"

[exec]
target = "/opt/wechat/wechat"
alias  = "wechat2"
argv0  = ""
env    = {}

[isolation]
tmpfs   = ["/tmp"]
unshare = []

[[map.bind]]
canonical = "~/.xwechat"
private   = "~/.local/state/twin/wechat2/xwechat"

[[map.bind]]
canonical = "~/.cache/wechat#2"
private   = "~/.local/state/twin/wechat2/cache"
`

func TestParseDesignDoc(t *testing.T) {
	root, err := Parse([]byte(designDoc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := root.Str("id"); got != "wechat2" {
		t.Errorf("id = %q", got)
	}
	if got := root.Str("name"); got != "微信2" {
		t.Errorf("name = %q", got)
	}
	if got := root.Tree("identity").Str("level"); got != "alias" {
		t.Errorf("identity.level = %q (comment stripping broke?)", got)
	}
	if !root.Tree("identity").Bool("notify") {
		t.Error("identity.notify should be true")
	}
	if got := root.Tree("exec").Tree("env").Len(); got != 0 {
		t.Errorf("exec.env should be empty inline table, got %d keys", got)
	}
	if got := root.Tree("isolation").StrList("tmpfs"); !reflect.DeepEqual(got, []string{"/tmp"}) {
		t.Errorf("isolation.tmpfs = %#v", got)
	}
	binds := root.TablesAt("map.bind")
	if len(binds) != 2 {
		t.Fatalf("want 2 [[map.bind]], got %d", len(binds))
	}
	// The '#' inside a quoted value must survive comment stripping.
	if got := binds[1].Str("canonical"); got != "~/.cache/wechat#2" {
		t.Errorf("canonical = %q", got)
	}
}

func TestRoundTrip(t *testing.T) {
	root, err := Parse([]byte(designDoc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out := string(Encode(root))
	again, err := Parse([]byte(out))
	if err != nil {
		t.Fatalf("re-Parse:\n%s\n%v", out, err)
	}
	if !reflect.DeepEqual(root, again) {
		t.Errorf("round-trip mismatch\n--- encoded ---\n%s", out)
	}
	if !strings.Contains(out, "[[map.bind]]") {
		t.Errorf("encoder lost the array of tables:\n%s", out)
	}
}

func TestEscapesAndTypes(t *testing.T) {
	doc := "a = \"x\\ty\\n\\u4e2d\"\nb = 'raw \\n stays'\nc = -3\nd = [\"p\", \"q\"]\ne = { k = \"v\" }\n"
	root, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := root.Str("a"); got != "x\ty\n中" {
		t.Errorf("a = %q", got)
	}
	if got := root.Str("b"); got != `raw \n stays` {
		t.Errorf("b = %q", got)
	}
	if got := root.Int("c"); got != -3 {
		t.Errorf("c = %d", got)
	}
	if got := root.Tree("e").Str("k"); got != "v" {
		t.Errorf("e.k = %q", got)
	}
}

// A doc we refuse must be refused loudly, not half-parsed.
func TestRejects(t *testing.T) {
	cases := map[string]string{
		"dotted key":       "a.b = 1\n",
		"missing equals":   "just words\n",
		"unterminated str": `a = "oops` + "\n",
		"bad value":        "a = @nope\n",
		"unterminated hdr": "[identity\n",
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}
