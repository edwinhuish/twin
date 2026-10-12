package desk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`/usr/bin/wechat %U`, []string{"/usr/bin/wechat"}},
		{`/opt/wechat/wechat`, []string{"/opt/wechat/wechat"}},
		{`"/opt/my app/run" --flag %f`, []string{"/opt/my app/run", "--flag"}},
		{`prog --x=a\ b`, []string{"prog", "--x=a b"}},
		{`prog 100%%`, []string{"prog", "100%"}},
		{`env FOO=1 prog %F`, []string{"env", "FOO=1", "prog"}},
	}
	for _, c := range cases {
		got, err := Tokenize(c.in)
		if err != nil {
			t.Errorf("Tokenize(%q): %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("Tokenize(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Tokenize(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestTokenizeRejectsGarbage(t *testing.T) {
	for _, in := range []string{`prog %Z`, `prog "unterminated`, `prog %`} {
		if _, err := Tokenize(in); err == nil {
			t.Errorf("Tokenize(%q): expected an error", in)
		}
	}
}

// A home directory with a space used to be silently fatal: the generated entry
// would never start and nothing would say why.
func TestExecLineQuotesAwkwardPaths(t *testing.T) {
	got := ExecLine("/home/a b/.local/share/twin/x/launch", "%U")
	want := `"/home/a b/.local/share/twin/x/launch" %U`
	if got != want {
		t.Errorf("ExecLine = %q, want %q", got, want)
	}
	// Tokenize deliberately drops field codes: they expand to file arguments,
	// not to something we can resolve statically. So the round-trip must come
	// back with the program alone (length 1), not with "%U" appended.
	back, err := Tokenize(got)
	if err != nil {
		t.Fatalf("Tokenize(%q): %v", got, err)
	}
	if len(back) != 1 || back[0] != "/home/a b/.local/share/twin/x/launch" {
		t.Errorf("round-trip lost the path: %q", back)
	}
}

// The trap that produced a gear instead of an icon on 2026-10-11: GLib drops
// the whole entry when Exec is not executable, and desktop-file-validate says
// nothing. Our validator must be louder than the standard one here.
func TestValidateCatchesNonExecutableExec(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "run-me")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "x.desktop")
	body := "[Desktop Entry]\nType=Application\nName=x\nExec=" + prog + "\nIcon=x\n"
	if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	issues := Validate(entry, dir)
	var fatal bool
	for _, is := range issues {
		if is.Fatal {
			fatal = true
			if !strings.Contains(is.Msg, "execute bit") {
				t.Errorf("unexpected message: %s", is.Msg)
			}
		}
	}
	if !fatal {
		t.Fatal("a non-executable Exec target must be fatal for GLib")
	}

	if err := os.Chmod(prog, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, is := range Validate(entry, dir) {
		if is.Fatal {
			t.Errorf("after chmod +x the entry should validate, got: %s", is.Msg)
		}
	}
}

func TestValidateCatchesMissingExec(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "x.desktop")
	body := "[Desktop Entry]\nType=Application\nName=x\nExec=" + filepath.Join(dir, "nope") + "\n"
	if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var fatal bool
	for _, is := range Validate(entry, dir) {
		if is.Fatal {
			fatal = true
		}
	}
	if !fatal {
		t.Fatal("missing Exec target must be fatal")
	}
}

func TestParseLocalizedNameAndComments(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "x.desktop")
	body := "# a comment\n[Desktop Entry]\nType=Application\nName=微信\nName[en]=WeChat\n" +
		"Comment=第二个 # 井号在值里\nExec=/bin/sh\nIcon=wechat\nCategories=Network;\n" +
		"Keywords=chat;\nKeywords[zh_CN]=聊天;\n" +
		"GenericName=Instant Messenger\nGenericName[zh_CN]=即时通讯\n"
	if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := Parse(entry)
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "微信" || e.Names["en"] != "WeChat" {
		t.Errorf("names: %q / %q", e.Name, e.Names["en"])
	}
	if e.Comment != "第二个 # 井号在值里" {
		t.Errorf("comment lost text after #: %q", e.Comment)
	}
	if e.Categories != "Network;" {
		t.Errorf("categories = %q", e.Categories)
	}
	if e.Keywords != "chat;" || e.KeywordLists["zh_CN"] != "聊天;" {
		t.Errorf("keywords: %q / %q", e.Keywords, e.KeywordLists["zh_CN"])
	}
	if e.GenericName != "Instant Messenger" || e.GenericNames["zh_CN"] != "即时通讯" {
		t.Errorf("generic names: %q / %q", e.GenericName, e.GenericNames["zh_CN"])
	}
	// These must not fall through into Extra, which is what a parser that
	// forgot the localised forms would do — silently losing them.
	for _, leaked := range []string{"Keywords[zh_CN]", "GenericName", "GenericName[zh_CN]"} {
		if _, ok := e.Extra[leaked]; ok {
			t.Errorf("%q leaked into Extra", leaked)
		}
	}
}

// Keywords and GenericName are localised the same way Name is: matched against
// the environment locale, never guessed from whichever translation exists.
func TestLocalizedKeywordsAndGenericName(t *testing.T) {
	e := &Entry{
		KeywordLists: map[string]string{"zh_CN": "聊天;", "en": "chat;"},
		GenericNames: map[string]string{"zh_CN": "即时通讯", "en": "Instant Messenger"},
	}
	if got, tag := e.LocalizedKeywords("zh_CN", "zh"); got != "聊天;" || tag != "zh_CN" {
		t.Errorf("zh keywords: %q via %q", got, tag)
	}
	if got, tag := e.LocalizedGenericName("en_US", "en"); got != "Instant Messenger" || tag != "en" {
		t.Errorf("en generic name: %q via %q", got, tag)
	}
	if got, _ := e.LocalizedKeywords("de_DE", "de"); got != "" {
		t.Errorf("de must not borrow another language, got %q", got)
	}
	// A nil map is a legitimate state (an entry with no localised values at
	// all) and must read as "nothing", not panic.
	var empty Entry
	if got, _ := empty.LocalizedKeywords("zh_CN"); got != "" {
		t.Errorf("nil map yielded %q", got)
	}
}

// The hand-written 微信2 carries Comment[en]=Second WeChat, and the real
// wechat.desktop carries Comment[zh_CN]. A wrapper that can only emit one
// Comment silently drops that.
func TestRenderWritesLocalizedComment(t *testing.T) {
	got := string(Render{
		Name:      "微信2",
		NameEn:    "WeChat2",
		Comment:   "第二个微信",
		CommentEn: "Second WeChat",
		Exec:      "/x/launch %U",
		Icon:      "/x/i.png",
	}.Bytes())
	for _, want := range []string{"Comment=第二个微信\n", "Comment[en]=Second WeChat\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Deriving a display name is only safe when the locale is read from the
// environment, not guessed from which Name[...] happens to exist.
func TestLocaleTags(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want []string
	}{
		{map[string]string{"LANG": "zh_CN.UTF-8"}, []string{"zh_CN", "zh"}},
		{map[string]string{"LANG": "de_DE.UTF-8@euro"}, []string{"de_DE", "de"}},
		{map[string]string{"LANG": "C"}, nil},
		{map[string]string{"LANG": "POSIX"}, nil},
		{map[string]string{"LANG": "C", "LC_MESSAGES": "zh_CN.UTF-8"}, []string{"zh_CN", "zh"}},
		{map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "zh_CN.UTF-8"}, []string{"zh_CN", "zh"}},
	}
	for _, c := range cases {
		for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
			t.Setenv(k, "")
		}
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		got := LocaleTags()
		if len(got) != len(c.want) {
			t.Errorf("%v: LocaleTags = %q, want %q", c.env, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%v: LocaleTags = %q, want %q", c.env, got, c.want)
			}
		}
	}
}

func TestLocalizedNamePicksByLocale(t *testing.T) {
	e := &Entry{Names: map[string]string{"en": "WeChat", "zh_CN": "微信"}}
	if got, tag := e.LocalizedName("zh_CN", "zh"); got != "微信" || tag != "zh_CN" {
		t.Errorf("zh: got %q via %q", got, tag)
	}
	if got, tag := e.LocalizedName("en_US", "en"); got != "WeChat" || tag != "en" {
		t.Errorf("en: got %q via %q", got, tag)
	}
	// The important half: an unmatched locale must yield nothing, never the
	// first Name[...] that happens to be in the file.
	if got, tag := e.LocalizedName("de_DE", "de"); got != "" || tag != "" {
		t.Errorf("de must not borrow another language, got %q via %q", got, tag)
	}
}
