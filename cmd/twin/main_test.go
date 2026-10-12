package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/generate"
	"github.com/edwinhuish/twin/internal/inst"
)

// The documented form is `twin new <source> --as <id>`: the source comes
// first. Go's flag package stops at the first non-flag token, which used to
// swallow every flag placed after the source — --as, --name, --badge, and even
// --dry-run all vanished without a word. parseFL must see through that.
func TestParseFLKeepsFlagsAfterPositional(t *testing.T) {
	cases := [][]string{
		{"wechat", "--as", "wechat2", "--badge", "2"},
		{"--as", "wechat2", "wechat", "--badge", "2"},
		{"--as", "wechat2", "--badge", "2", "wechat"},
		{"--as=wechat2", "wechat", "--badge=2"},
	}
	for _, args := range cases {
		fs := flag.NewFlagSet("new", flag.ContinueOnError)
		as := fs.String("as", "", "")
		badge := fs.String("badge", "", "")
		pos, err := parseFL(fs, args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(pos) != 1 || pos[0] != "wechat" {
			t.Errorf("%v: positional = %q, want [wechat]", args, pos)
		}
		if *as != "wechat2" {
			t.Errorf("%v: --as = %q, want wechat2", args, *as)
		}
		if *badge != "2" {
			t.Errorf("%v: --badge = %q, want 2", args, *badge)
		}
	}
}

func TestParseFLKeepsBoolAfterPositional(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "")
	pos, err := parseFL(fs, []string{"wechat", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 1 || pos[0] != "wechat" {
		t.Fatalf("positional = %q", pos)
	}
	if !*dry {
		t.Fatal("--dry-run placed after the positional was dropped")
	}
}

func TestParseFLRejectsUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	if _, err := parseFL(fs, []string{"wechat", "--nope"}); err == nil {
		t.Fatal("an unknown flag must be an error, not silently ignored")
	}
}

// ---------------------------------------------------------------- `--`

func TestSplitPassthroughWithoutSeparator(t *testing.T) {
	head, tail, err := splitPassthrough([]string{"wechat", "--as", "wechat2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(head, " ") != "wechat --as wechat2" {
		t.Errorf("head = %q", head)
	}
	if len(tail) != 0 {
		t.Errorf("tail = %q, want empty", tail)
	}
}

func TestSplitPassthroughKeepsTokensVerbatim(t *testing.T) {
	head, tail, err := splitPassthrough([]string{
		"wechat", "--as", "wechat2", "--", "--unshare-pid", "--setenv", "TWIN_FOO", "bar",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 3 || head[0] != "wechat" {
		t.Errorf("head = %q", head)
	}
	want := "--unshare-pid --setenv TWIN_FOO bar"
	if strings.Join(tail, " ") != want {
		t.Errorf("tail = %q, want %q", tail, want)
	}
}

func TestSplitPassthroughEmptyTail(t *testing.T) {
	head, tail, err := splitPassthrough([]string{"wechat", "--"})
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 1 || head[0] != "wechat" || len(tail) != 0 {
		t.Fatalf("head=%q tail=%q", head, tail)
	}
}

// `twin -- wechat` is the POSIX "end of options" reading, which is not what
// twin means by `--`. Catching it early beats forwarding a stray source.
func TestSplitPassthroughRefusesNonDashHead(t *testing.T) {
	if _, _, err := splitPassthrough([]string{"--", "wechat"}); err == nil {
		t.Fatal("a passthrough region that starts with a non-option must be refused")
	}
}

// A `--` inside the passthrough would end bwrap's option parsing and swallow
// every following token as the program name.
func TestSplitPassthroughRefusesSecondSeparator(t *testing.T) {
	if _, _, err := splitPassthrough([]string{"wechat", "--", "--unshare-pid", "--", "--foo"}); err == nil {
		t.Fatal("a `--` inside the passthrough region must be refused")
	}
}

// ---------------------------------------------------------------- path maps

// The rules.d template is meant to be pasted, and a rules.d file serves every
// instance of a program. An id baked into the printed paths would make the
// pasted copy serve exactly one instance — which is what the {id} placeholder
// exists to prevent.
func TestRuleTemplateUnbakesInstanceID(t *testing.T) {
	p := inst.Paths{Home: "/home/someone"}
	it := &inst.Instance{ID: "wechat2"}
	if got := dedupStateDir(p, it, "~/.local/state/twin/wechat2/xwechat"); got != "~/.local/state/twin/{id}/xwechat" {
		t.Errorf("dedupStateDir = %q, want ~/.local/state/twin/{id}/xwechat", got)
	}
	// A shared directory must survive untouched: rewriting an unrelated path
	// would turn "shared" into "private" in the template the user copies.
	if got := dedupStateDir(p, it, "~/Documents/xwechat_files"); got != "~/Documents/xwechat_files" {
		t.Errorf("dedupStateDir rewrote an unrelated path: %q", got)
	}
}

// The file name offered to the user is what twin will read back next time, so
// it has to be the key the rule was found under — the desktop basename is only
// the fallback for a program that has no rule yet.
func TestRuleKeyPrefersTheMatchedKey(t *testing.T) {
	insp := &generate.Inspection{
		RuleKey:       "xwechat",
		SourceDesktop: "/usr/share/applications/wechat.desktop",
	}
	if got := ruleKeyFor(insp); got != "xwechat" {
		t.Errorf("ruleKeyFor = %q, want xwechat", got)
	}
	bare := &generate.Inspection{SourceDesktop: "/usr/share/applications/wechat.desktop"}
	if got := ruleKeyFor(bare); got != "wechat" {
		t.Errorf("ruleKeyFor fallback = %q, want wechat", got)
	}
}
