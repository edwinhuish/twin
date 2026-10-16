package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/tomlz"
)

// ------------------------------------------------------------ name matching

// The bar exists because binaries are full of paths that belong to the toolkit
// rather than to the program. Binding ~/.config/dconf or ~/.config/fontconfig
// away would break half the desktop, and the breakage would look like a twin
// bug — so a bare path literal must not be enough on its own.
func TestRelatedToRejectsSharedToolkitDirs(t *testing.T) {
	cases := []struct {
		segment string
		names   []string
		want    bool
	}{
		{"autostart", []string{"wechat"}, false},
		{"fontconfig", []string{"wechat"}, false},
		{"dconf", []string{"code"}, false},
		{"gtk-3.0", []string{"wechat"}, false},
		{"xwechat", []string{"wechat"}, true},
		{"Code", []string{"code"}, true},
		{"google-chrome", []string{"google-chrome"}, true},
		{"Telegram Desktop", []string{"telegram-desktop"}, true},
		{"ab", []string{"ab"}, false}, // too short to mean anything
	}
	for _, c := range cases {
		if got := relatedTo(c.segment, c.names); got != c.want {
			t.Errorf("relatedTo(%q, %v) = %v, want %v", c.segment, c.names, got, c.want)
		}
	}
}

// A path literal is a leaf the program built at run time. Binding the leaf
// would be meaningless; the unit a person can decide about is the first segment
// under a state root.
func TestReduceStatePathTakesOneSegment(t *testing.T) {
	cases := []struct {
		raw   string
		names []string
		want  string
		ok    bool
	}{
		{"~/.config/Foo/BrowserMetrics/2026-10-12", []string{"foo"}, "~/.config/Foo", true},
		{"~/.xwechat", []string{"wechat"}, "~/.xwechat", true},
		{"~/.local/share/foo/files", []string{"foo"}, "~/.local/share/foo", true},
		{"~/.cache/foo", []string{"foo"}, "~/.cache/foo", true},
		{"~/.config/foo/", []string{"foo"}, "~/.config/foo", true},
		// Not this program's own directory.
		{"~/.config/autostart", []string{"foo"}, "", false},
		// A state root with no program segment below it.
		{"~/.config", []string{"foo"}, "", false},
		{"~/.local/share", []string{"foo"}, "", false},
		// A file the program opens, not a directory it owns.
		{"~/Documents/report.pdf", []string{"foo"}, "", false},
	}
	for _, c := range cases {
		got, ok := reduceStatePath(c.raw, c.names)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("reduceStatePath(%q) = (%q, %v), want (%q, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestPrivateForNamesTheCopyAfterItsPurpose(t *testing.T) {
	cases := map[string]string{
		"~/.config/foo":      "~/.local/state/twin/{id}/config",
		"~/.local/share/foo": "~/.local/state/twin/{id}/share",
		"~/.local/state/foo": "~/.local/state/twin/{id}/state",
		"~/.cache/foo":       "~/.local/state/twin/{id}/cache",
		"~/.xwechat":         "~/.local/state/twin/{id}/xwechat",
	}
	for in, want := range cases {
		if got := PrivateFor(in); got != want {
			t.Errorf("PrivateFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// ------------------------------------------------------------ discovery

func TestSuggestStateDirsUsesExistenceAsEvidence(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config/foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	insp := &Inspection{
		SourceDesktop: "/usr/share/applications/foo.desktop",
		Target:        fakeBinary(t, "nothing to see"),
	}
	hints := SuggestStateDirs(inst.Paths{Home: home}, insp)
	if len(hints) != 1 {
		t.Fatalf("hints = %+v, want exactly the one existing directory", hints)
	}
	if hints[0].Path != "~/.config/foo" || !hints[0].Strong || hints[0].Evidence != "目录存在" {
		t.Errorf("hint = %+v", hints[0])
	}

	// The shallow probe must not read the binary at all: `twin gen` runs it on
	// every wrap, and a second full read of a 150 MB bundle is not free.
	shallow := SuggestStateDirs(inst.Paths{Home: home}, &Inspection{
		SourceDesktop: "/usr/share/applications/foo.desktop",
		Target:        fakeBinary(t, `X="$HOME/.cache/foo/sessions"`),
	})
	if len(shallow) != 1 {
		t.Errorf("shallow probe picked up binary literals: %+v", shallow)
	}
}

// WeChat keeps everything in ~/.xwechat — a name the desktop entry never
// mentions. A probe that only looked up ~/.wechat would miss the tool's
// flagship case, which is why the dotfile scan matches by name.
func TestSuggestStateDirsFindsDotfileNamespace(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".xwechat", ".config", ".local", ".cache", ".bashrc"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	insp := &Inspection{
		SourceDesktop: "/usr/share/applications/wechat.desktop",
		Target:        "/opt/wechat/wechat",
	}
	hints := SuggestStateDirs(inst.Paths{Home: home}, insp)

	// .config / .local / .cache belong to the machine, and .bashrc is only
	// there because MkdirAll made it a directory — none of them may show up.
	if len(hints) != 1 {
		t.Fatalf("hints = %+v, want just ~/.xwechat", hints)
	}
	if hints[0].Path != "~/.xwechat" || !hints[0].Strong {
		t.Errorf("hint = %+v", hints[0])
	}
	if hints[0].Evidence != "目录存在（名字相近）" {
		t.Errorf("a name match must be labelled as one: %q", hints[0].Evidence)
	}
}

func TestSuggestStateDirsDeepReadsBinaryLiterals(t *testing.T) {
	home := t.TempDir()
	insp := &Inspection{
		SourceDesktop: "/usr/share/applications/foo.desktop",
		Target: fakeBinary(t, `open("$HOME/.config/foo/settings.json")
open("$HOME/.local/state/foo/state.db")
open("$HOME/.config/autostart/foo.desktop")
open("%s/.config/foo/%s")`),
	}
	hints := SuggestStateDirsDeep(inst.Paths{Home: home}, insp)

	var got []string
	for _, h := range hints {
		got = append(got, h.Path)
		if h.Strong {
			t.Errorf("%q was reported as a fact, but nothing exists there", h.Path)
		}
	}
	want := "~/.config/foo ~/.local/state/foo"
	if strings.Join(got, " ") != want {
		t.Errorf("hints = %v, want %q", got, want)
	}
}

// A format placeholder is not a path: %s/.config/foo would otherwise turn into
// a suggestion that has no meaning at all.
func TestBinaryStatePathsRejectsFormatPlaceholders(t *testing.T) {
	path := fakeBinary(t, `%s/.config/foo/bin
--config=%s/.cache/foo/config.json
~/.config/foo%02d.log`)
	got := binaryStatePaths(path, []string{"foo"})
	if strings.Join(got, " ") != "~/.config/foo" {
		t.Errorf("binaryStatePaths = %v, want just ~/.config/foo", got)
	}
}

// ------------------------------------------------------------ rule draft

// The whole point of the draft is that a guess never silently becomes a bind:
// only "this directory exists" is written as a live entry.
func TestRuleDraftKeepsGuessesCommentedOut(t *testing.T) {
	draft := RuleDraft("foo", "/usr/share/applications/foo.desktop", []StateHint{
		{Path: "~/.config/foo", Evidence: "目录存在", Strong: true},
		{Path: "~/.cache/foo", Evidence: "二进制内有这个字符串"},
	})
	if !strings.Contains(draft, "# \"~/.local/state/twin/{id}/cache:~/.cache/foo\",") {
		t.Errorf("the guessed entry is not commented out:\n%s", draft)
	}

	root, err := tomlz.Parse([]byte(draft))
	if err != nil {
		// The draft is meant to be pasted and then read back by twin itself, so
		// "it parses" is the minimum bar for it being useful.
		t.Fatalf("the draft is not valid TOML: %v\n%s", err, draft)
	}
	if got := root.Str("app"); got != "foo" {
		t.Errorf("app = %q, want foo", got)
	}
	binds, err := root.StrListStrict("bind")
	if err != nil {
		t.Fatalf("草稿里的 bind 读不出来：%v", err)
	}
	if len(binds) != 1 {
		t.Fatalf("live binds = %d, want 1 (the guessed one must stay inert)", len(binds))
	}
	if got, want := binds[0], "~/.local/state/twin/{id}/config:~/.config/foo"; got != want {
		t.Errorf("live bind = %q, want %q", got, want)
	}
}

// A draft for a program we learned nothing about still has to be a useful
// starting point and a valid file — it is the only thing `--init` can offer.
func TestRuleDraftWithoutEvidenceIsStillValid(t *testing.T) {
	draft := RuleDraft("foo", "/usr/share/applications/foo.desktop", nil)
	root, err := tomlz.Parse([]byte(draft))
	if err != nil {
		t.Fatalf("empty draft is not valid TOML: %v\n%s", err, draft)
	}
	if len(root.TableList("bind")) != 0 {
		t.Errorf("an evidence-free draft must apply nothing")
	}
	if !strings.Contains(draft, "~/.config/foo") {
		t.Errorf("empty draft should still show the shape:\n%s", draft)
	}
}
