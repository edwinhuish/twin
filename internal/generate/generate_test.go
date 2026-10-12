package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinhuish/twin/internal/config"
	"github.com/edwinhuish/twin/internal/desk"
	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
)

func clearLocale(t *testing.T, lang string) {
	t.Helper()
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", lang)
}

// WriteSnapshot is the whole of the configuring half: one file, and the same
// bytes a one-step wrap would have written. Anything else on disk at that point
// would be an install the user has not asked for yet.
func TestWriteSnapshotWritesOnlyTheConfig(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	it, _, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	path, err := WriteSnapshot(p, it)
	if err != nil {
		t.Fatal(err)
	}
	if path != p.Profile(it.ID) {
		t.Errorf("path = %q，期望 %q", path, p.Profile(it.ID))
	}
	for _, f := range []string{p.Desktop(it.ID), p.Icon(it.ID), p.Launch(it.ID), p.StateDir(it.ID)} {
		if sysx.Exists(f) {
			t.Errorf("配置阶段不该写出 %s", f)
		}
	}
	body, err := it.Render()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("快照内容与 Render 不一致：\n%s", got)
	}
}

// The two halves find each other by *resolved* source path, so the same
// instance answers to `twin new wechat` and to a path spelled out in full —
// and a snapshot from another program is never a candidate.
func TestSnapshotsForMatchesBySource(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	mk := func(id, source string, desktop bool) {
		t.Helper()
		if err := os.MkdirAll(p.InstanceDir(id), 0o755); err != nil {
			t.Fatal(err)
		}
		it := &inst.Instance{ID: id, Source: source, Target: "/opt/wechat/wechat", Level: inst.LevelAlias}
		body, err := it.Render()
		if err != nil {
			t.Fatal(err)
		}
		if err := sysx.WriteFileAtomic(p.Profile(id), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if desktop {
			if err := sysx.WriteFileAtomic(p.Desktop(id), []byte("[Desktop Entry]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("wechat2", "/usr/share/applications/wechat.desktop", true)
	mk("wechat3", "/usr/share/applications/wechat.desktop", false)
	mk("other2", "/usr/share/applications/other.desktop", false)

	got, err := SnapshotsFor(p, "/usr/share/applications/wechat.desktop")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("拿到 %d 份快照，期望 2：%+v", len(got), got)
	}
	byID := map[string]bool{}
	for _, s := range got {
		byID[s.Instance.ID] = s.Published
	}
	if !byID["wechat2"] {
		t.Error("wechat2 有桌面项，应当算已落盘")
	}
	if byID["wechat3"] {
		t.Error("wechat3 没有桌面项，应当算未落盘")
	}
}

// The install half may overwrite its own desktop entry (that is what a
// republish is) but never somebody else's. Ownership is read back out of the
// file, not inferred from the snapshot that happens to exist next to it.
func TestCheckInstallTellsOursFromTheirs(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	foreign := filepath.Join("/usr/share/applications", "demo.desktop")
	if sysx.Exists(foreign) {
		t.Skipf("这台机器上真的有 %s，无法用它当「别人的桌面项」", foreign)
	}

	write := func(exec string) {
		t.Helper()
		body := "[Desktop Entry]\nType=Application\nName=Demo\nExec=" + exec + "\n"
		if err := sysx.WriteFileAtomic(p.Desktop("demo"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("/usr/bin/other %U")
	if err := CheckInstall(p, "demo", false); err == nil {
		t.Error("别人的桌面项必须被拒绝，除非 --force")
	}
	if err := CheckInstall(p, "demo", true); err != nil {
		t.Errorf("--force 应当放行：%v", err)
	}

	write(p.Launch("demo") + " %U")
	if err := CheckInstall(p, "demo", false); err != nil {
		t.Errorf("我们自己的桌面项应当可以重新落盘：%v", err)
	}

	// A file that does not parse is not evidence of ownership either.
	if err := os.WriteFile(p.Desktop("demo"), []byte("not a desktop entry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckInstall(p, "demo", false); err == nil {
		t.Error("解析不了的桌面项不能当成自己的")
	}
}

// Unwrap is the only destructive thing twin does, and its contract is narrow on
// purpose: our own files go, the program's private data survives unless asked
// for, and no other instance is touched. Every one of those is a way to lose
// data if it regresses silently, so all three are asserted here.
func TestUnwrapRemovesOursAndSparesTheRest(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	write := func(f string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ours := []string{p.Desktop("demo"), p.Icon("demo"), p.Launch("demo"), p.RuleFile("demo")}
	other := []string{p.Desktop("other"), p.Profile("other")}
	for _, f := range append(append([]string{}, ours...), other...) {
		write(f)
	}
	write(filepath.Join(p.StateDir("demo"), "profile"))

	removed, err := Unwrap(p, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range ours {
		if sysx.Exists(f) {
			t.Errorf("%s 应当被删掉", f)
		}
	}
	if !sysx.Exists(p.StateDir("demo")) {
		t.Error("没给 --purge，私有状态目录必须留着（下次同名重建要接得上）")
	}
	if len(removed.Kept) == 0 {
		t.Error("留下的东西必须报出来，否则用户以为已经删干净了")
	}
	for _, f := range other {
		if !sysx.Exists(f) {
			t.Errorf("%s 属于别的实例，不该被动到", f)
		}
	}

	if _, err := Unwrap(p, "demo", true); err != nil {
		t.Fatal(err)
	}
	if sysx.Exists(p.StateDir("demo")) {
		t.Error("--purge 要连私有状态一起删")
	}
	if !sysx.Exists(p.Desktop("other")) {
		t.Error("--purge 不该牵连别的实例")
	}
}

func wechatInspection() *Inspection {
	return &Inspection{
		SourceDesktop: "/usr/share/applications/wechat.desktop",
		Entry: &desk.Entry{
			Name:     "wechat",
			Exec:     "/bin/true",
			Names:    map[string]string{"zh_CN": "微信"},
			Comments: map[string]string{"zh_CN": "微信桌面版"},
		},
		Target: "/opt/wechat/wechat",
		// Plan refuses an empty icon source (a desktop entry without Icon= is
		// the gear placeholder this tool exists to remove), so every fixture
		// that reaches Plan must carry one.
		IconSource: "/usr/share/icons/hicolor/256x256/apps/wechat.png",
	}
}

func hasDerivation(der []Derivation, key string) bool {
	for _, d := range der {
		if d.Key == key && d.Source != "" {
			return true
		}
	}
	return false
}

// `twin new wechat` on a zh_CN desktop must expand to 微信2: every one of these
// values is derivable from the source entry, and each must be reported.
func TestPlanDerivesFromSource(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "wechat2" {
		t.Errorf("id = %q, want wechat2", it.ID)
	}
	if it.Name != "微信2" {
		t.Errorf("name = %q, want 微信2 (derived from Name[zh_CN])", it.Name)
	}
	if it.NameEn != "wechat2" {
		t.Errorf("name_en = %q, want wechat2", it.NameEn)
	}
	if it.Comment != "微信桌面版" {
		t.Errorf("comment = %q, want 微信桌面版 (from Comment[zh_CN])", it.Comment)
	}
	if it.Badge != "2" {
		t.Errorf("badge = %q, want 2 (trailing digit of the id)", it.Badge)
	}
	for _, key := range []string{"id", "name", "name_en", "comment", "badge"} {
		if !hasDerivation(der, key) {
			t.Errorf("no derivation recorded for %q: %+v", key, der)
		}
	}
}

// An unmatched locale must fall back to the ASCII Name rather than borrow a
// Name[zh_CN] it has no business showing.
func TestPlanFallsBackToASCIIName(t *testing.T) {
	clearLocale(t, "de_DE.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, _, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.Name != "wechat2" {
		t.Errorf("name = %q, want wechat2", it.Name)
	}
	// Name[en] would be identical to Name here, so it must be dropped.
	if it.NameEn != "" {
		t.Errorf("name_en = %q, want empty (identical to name)", it.NameEn)
	}
}

// The smallest free sequence number, skipping names already on disk.
func TestPlanSkipsTakenIDs(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	for _, id := range []string{"wechat2", "wechat3"} {
		f := p.Desktop(id)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("[Desktop Entry]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	it, _, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "wechat4" {
		t.Errorf("id = %q, want wechat4", it.ID)
	}
}

// An explicit option always wins, and nothing is then reported as derived.
func TestPlanExplicitOptionsWin(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{ID: "wx", Name: "我的微信", Badge: "A"}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "wx" || it.Name != "我的微信" || it.Badge != "A" {
		t.Fatalf("explicit options were overridden: id=%q name=%q badge=%q", it.ID, it.Name, it.Badge)
	}
	for _, d := range der {
		switch d.Key {
		case "id", "name", "badge":
			t.Errorf("%q must not be reported as derived when given explicitly: %+v", d.Key, d)
		}
	}
}

func TestSeqOf(t *testing.T) {
	cases := map[string]string{
		"wechat2": "2", "wechat12": "12", "myapp": "", "app2x": "", "2": "2",
	}
	for in, want := range cases {
		if got := seqOf(in); got != want {
			t.Errorf("seqOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The point of this round: wechat.desktop declares no Keywords, so without
// derivation every instance would make the user retype "微信;wechat" by hand.
func TestKeywordsDerivedWhenSourceHasNone(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "微信;wechat;" {
		t.Errorf("keywords = %q, want 微信;wechat;", it.Keywords)
	}
	var why string
	for _, d := range der {
		if d.Key == "keywords" {
			why = d.Source
		}
	}
	if !strings.Contains(why, "源无 Keywords") {
		t.Errorf("a synthesised list must say so: %q", why)
	}
}

// When the source does declare keywords they win outright: the source is the
// authority on what the program is called. A localised list beats the generic.
func TestKeywordsInheritedFromSource(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	insp := wechatInspection()
	insp.Entry.Keywords = "wechat;chat;"
	insp.Entry.KeywordLists = map[string]string{"zh_CN": "微信;聊天;"}
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{}, insp)
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "微信;聊天;" {
		t.Errorf("keywords = %q, want the source's Keywords[zh_CN] verbatim", it.Keywords)
	}
	if !hasDerivation(der, "keywords") {
		t.Errorf("inheritance should still be reported: %+v", der)
	}
}

// A declared-but-empty list (";") is not a keyword list.
func TestKeywordsTreatsBareSemicolonAsAbsent(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	insp := wechatInspection()
	insp.Entry.Keywords = ";"
	p := inst.Paths{Home: t.TempDir()}

	it, _, err := Plan(p, Options{}, insp)
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "微信;wechat;" {
		t.Errorf("keywords = %q, want the derived list", it.Keywords)
	}
}

func TestKeywordsExplicitWins(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{Keywords: "k1;k2"}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "k1;k2" {
		t.Errorf("keywords = %q, want the explicit value verbatim", it.Keywords)
	}
	for _, d := range der {
		if d.Key == "keywords" {
			t.Errorf("an explicit value must not be reported as derived: %+v", d)
		}
	}
}

// Derivation order (localised name, ASCII name, GenericName, program name) and
// case-insensitive de-duplication, with GenericName in the mix.
func TestDerivedKeywordsOrderAndDedup(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	insp := wechatInspection()
	insp.Entry.GenericName = "Instant Messenger"
	insp.Entry.GenericNames = map[string]string{"zh_CN": "即时通讯"}

	got, why := DerivedKeywords(insp)
	if want := "微信;wechat;即时通讯;"; got != want {
		t.Errorf("DerivedKeywords = %q, want %q", got, want)
	}
	if !strings.Contains(why, "Name[zh_CN]") || !strings.Contains(why, "GenericName[zh_CN]") {
		t.Errorf("the reason must name its sources: %q", why)
	}
	// The executable basename repeats Name here; it must be folded away.
	if n := strings.Count(got, "wechat"); n != 1 {
		t.Errorf("wechat appears %d times in %q", n, got)
	}
}

// Unmatched locale: no Name[...] may be borrowed, but the ASCII name still
// gives a usable keyword list.
func TestDerivedKeywordsFallsBackToASCII(t *testing.T) {
	clearLocale(t, "de_DE.UTF-8")
	got, _ := DerivedKeywords(wechatInspection())
	if got != "wechat;" {
		t.Errorf("DerivedKeywords = %q, want wechat;", got)
	}
}

// A keyword may not contain the list separator; such a candidate is dropped
// rather than allowed to corrupt the generated Keywords= value.
func TestDerivedKeywordsDropsSeparator(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	insp := wechatInspection()
	insp.Entry.Name = "a;b"
	insp.Entry.GenericName = "re;al"

	got, _ := DerivedKeywords(insp)
	if strings.Contains(got, "a;b") || strings.Contains(got, "re;al") {
		t.Errorf("a candidate containing ';' escaped into the list: %q", got)
	}
	if got != "微信;wechat;" {
		t.Errorf("DerivedKeywords = %q, want the surviving candidates only", got)
	}
}

// vlcInspection models the case this rule exists for: the source speaks English
// about its keywords while answering to a Chinese name.
func vlcInspection(keywords string) *Inspection {
	return &Inspection{
		SourceDesktop: "/usr/share/applications/vlc.desktop",
		Entry: &desk.Entry{
			Name:     "VLC media player",
			Exec:     "/bin/true",
			Names:    map[string]string{"zh_CN": "VLC 媒体播放器"},
			Comments: map[string]string{"zh_CN": "VLC 媒体播放器"},
			Keywords: keywords,
		},
		Target:     "/usr/bin/vlc",
		IconSource: "/usr/share/icons/hicolor/256x256/apps/vlc.png",
	}
}

// The source's list is inherited verbatim while the name is twin's own
// derivation: twin does not get to add a Chinese term the program never
// declared just because it happens to know one.
func TestKeywordsDerivedNameLeavesEnglishSourceAlone(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{}, vlcInspection("Player;Capture;DVD;"))
	if err != nil {
		t.Fatal(err)
	}
	// The display name is still derived and still Chinese — that is not in
	// question; only the keyword list stays as the source wrote it.
	if it.Name != "VLC 媒体播放器2" {
		t.Errorf("name = %q, want VLC 媒体播放器2", it.Name)
	}
	if it.Keywords != "Player;Capture;DVD;" {
		t.Errorf("keywords = %q, want the source list untouched", it.Keywords)
	}
	if why := derivationFor(der, "keywords"); strings.Contains(why, "前置") {
		t.Errorf("nothing was prepended, so nothing may claim it was: %q", why)
	}
}

// Once the user names the instance that name is a term they asked to be
// findable, so it goes to the head of the inherited list.
func TestKeywordsExplicitNamePrependedToEnglishSource(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{Name: "我的播放器"}, vlcInspection("Player;Capture;DVD;"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "我的播放器;Player;Capture;DVD;"; it.Keywords != want {
		t.Errorf("keywords = %q, want %q", it.Keywords, want)
	}
	why := derivationFor(der, "keywords")
	if !strings.Contains(why, "源 Keywords") || !strings.Contains(why, "我的播放器") {
		t.Errorf("the reason must name both halves of the result: %q", why)
	}
}

// A list that already speaks Chinese has nothing to gain from the prepend.
func TestKeywordsExplicitNameSkippedWhenSourceHasCJK(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, _, err := Plan(p, Options{Name: "我的播放器"}, vlcInspection("播放器;Player;"))
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "播放器;Player;" {
		t.Errorf("keywords = %q, want the source list untouched", it.Keywords)
	}
}

// Nor does the prepend fire when the source already lists that very keyword —
// case-insensitively, because keyword matching in the shell is.
func TestKeywordsExplicitNameSkippedWhenAlreadyListed(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, _, err := Plan(p, Options{Name: "vlc"}, vlcInspection("Player;VLC;"))
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "Player;VLC;" {
		t.Errorf("keywords = %q, want the source list untouched", it.Keywords)
	}
}

// A source that omits the trailing separator is left alone until we are the
// ones editing the value — then it is written in the recommended form.
func TestKeywordsPrependedListIsWellFormed(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, _, err := Plan(p, Options{Name: "我的播放器"}, vlcInspection("Player;Capture"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "我的播放器;Player;Capture;"; it.Keywords != want {
		t.Errorf("keywords = %q, want %q", it.Keywords, want)
	}
}

func TestHasCJK(t *testing.T) {
	yes := []string{"微信", "VLC 媒体播放器", "チャット", "한글", "聊天;chat;"}
	no := []string{"", "Player;Capture;", "Vidéo;Lecteur;", "lecteur;player;"}
	for _, s := range yes {
		if !hasCJK(s) {
			t.Errorf("hasCJK(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if hasCJK(s) {
			t.Errorf("hasCJK(%q) = true, want false", s)
		}
	}
}

// derivationFor returns the recorded source for key, or "".
func derivationFor(der []Derivation, key string) string {
	for _, d := range der {
		if d.Key == key {
			return d.Source
		}
	}
	return ""
}

// ---------------------------------------------------------------- config layers

// ruleInspection is wechatInspection plus an application rule, which is what
// Inspect attaches when it finds rules.d/<app>.toml or the built-in table.
func ruleInspection() *Inspection {
	insp := wechatInspection()
	insp.Presets = []inst.Bind{
		{Source: "~/.local/state/twin/<id>/xwechat", Target: "~/.xwechat"},
	}
	insp.Rule = &config.Rule{
		App:        "wechat",
		Keywords:   "wechat;微信;im",
		Categories: "Utility;Chat;",
		Tmpfs:      []string{"/tmp"},
		Bwrap:      []string{"--rule-flag"},
	}
	return insp
}

// Every layer must be able to supply a default, and the derivation log must
// name the layer that won — "which file set this?" is the first question a
// broken identity raises.
func TestPlanTakesValuesFromRule(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{}, ruleInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "wechat;微信;im" {
		t.Errorf("keywords = %q, want the rule's list verbatim", it.Keywords)
	}
	if got := derivationFor(der, "keywords"); got == "" {
		t.Error("规则提供的 keywords 必须出现在推导来源里")
	}
	if it.Categories != "Utility;Chat;" {
		t.Errorf("categories = %q, want the rule's", it.Categories)
	}
	if got := derivationFor(der, "categories"); got == "" {
		t.Error("规则提供的 categories 必须出现在推导来源里")
	}
	if len(it.Tmpfs) != 1 || it.Tmpfs[0] != "/tmp" {
		t.Errorf("tmpfs = %v, want the rule's", it.Tmpfs)
	}
	if got := derivationFor(der, "tmpfs"); got == "" {
		t.Error("规则提供的 tmpfs 必须出现在推导来源里")
	}
	// {id} / <id> are substituted with the concrete instance id.
	if it.Binds[0].Source != "~/.local/state/twin/wechat2/xwechat" {
		t.Errorf("bind source = %q", it.Binds[0].Source)
	}
}

// A CLI keyword list is literal: rules.d must not get a second word in.
func TestPlanExplicitKeywordsBeatRule(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, der, err := Plan(p, Options{Keywords: "only;this;"}, ruleInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "only;this;" {
		t.Errorf("keywords = %q, want the explicit list", it.Keywords)
	}
	if got := derivationFor(der, "keywords"); got != "" {
		t.Errorf("显式给了 --keywords 就不该有推导来源，得到 %q", got)
	}
}

func TestPlanLevelFromRuleThenConfig(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}

	// rules.d wins over config.toml.
	insp := ruleInspection()
	insp.Rule.Level = "none"
	it, _, err := Plan(p, Options{Cfg: &config.Config{Defaults: config.Defaults{Level: "alias", BadgeStyle: "corner", TmpfsUnlessX11: true}}}, insp)
	if err != nil {
		t.Fatal(err)
	}
	if it.Level != inst.LevelNone {
		t.Errorf("level = %s, want none (from the rule)", it.Level)
	}
	if it.WMClass != "" {
		t.Error("level=none 不该写 StartupWMClass")
	}

	// With no rule value, the environment layer decides.
	insp2 := ruleInspection()
	insp2.Rule.Level = ""
	it2, _, err := Plan(p, Options{Cfg: &config.Config{Defaults: config.Defaults{Level: "none", BadgeStyle: "corner"}}}, insp2)
	if err != nil {
		t.Fatal(err)
	}
	if it2.Level != inst.LevelNone {
		t.Errorf("level = %s, want none (from config.toml)", it2.Level)
	}
}

// The passthrough tail is concatenated low priority first, so the last writer
// shadows an earlier mount on the same destination (bwrap's own rule).
func TestPlanBwrapOrderIsConfigRuleCLI(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	cfg := config.Builtin()
	cfg.Bwrap = []string{"--from-config"}

	it, _, err := Plan(p, Options{Cfg: cfg, Bwrap: []string{"--from-cli"}}, ruleInspection())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--from-config", "--rule-flag", "--from-cli"}
	if len(it.Bwrap) != len(want) {
		t.Fatalf("bwrap = %v, want %v", it.Bwrap, want)
	}
	for i := range want {
		if it.Bwrap[i] != want[i] {
			t.Fatalf("bwrap = %v, want %v", it.Bwrap, want)
		}
	}
}

// A refused token must name the layer it came from, or the user has to audit
// three files by hand.
func TestPlanBlamesTheLayerForABadToken(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	cfg := config.Builtin()
	cfg.Path = "/home/x/.config/twin/config.toml"
	cfg.Bwrap = []string{"--argv0"}

	_, _, err := Plan(p, Options{Cfg: cfg}, ruleInspection())
	if err == nil {
		t.Fatal("配置里的 --argv0 应当被拒绝")
	}
	if !strings.Contains(err.Error(), cfg.Path) {
		t.Errorf("错误应指出是 config.toml 里的，得到 %v", err)
	}

	_, _, err = Plan(p, Options{Bwrap: []string{"--argv0"}}, ruleInspection())
	if err == nil || !strings.Contains(err.Error(), "命令行") {
		t.Errorf("命令行透传的错误应点名命令行，得到 %v", err)
	}
}

// Refresh fills the gaps and leaves every value the snapshot already holds
// alone — regen must not quietly rewrite a hand-edited instance.
func TestRefreshOnlyFillsEmptyFields(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "zh_CN.UTF-8")

	it := &inst.Instance{
		ID:       "wechat2",
		Keywords: "keep;me;", // hand-edited, must survive
		Target:   "/opt/wechat/wechat",
		Alias:    "wechat2",
	}
	it.Level = inst.LevelAlias
	cfg := config.Builtin()
	cfg.Bwrap = []string{"--from-config"}

	notes, err := Refresh(it, ruleInspection(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if it.Keywords != "keep;me;" {
		t.Errorf("快照里的 keywords 被改写了：%q", it.Keywords)
	}
	if it.Categories == "" {
		t.Error("空的 categories 应当被补上")
	}
	if len(it.Bwrap) != 2 {
		t.Fatalf("bwrap = %v, want config + rule", it.Bwrap)
	}
	if len(notes) == 0 {
		t.Error("补了东西就该说出来")
	}

	// Idempotent: a second refresh has nothing left to do.
	again, err := Refresh(it, ruleInspection(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("第二次 --refresh 不该再有改动，得到 %v", again)
	}
}

func TestSessionWarning(t *testing.T) {
	it := &inst.Instance{ID: "x", Tmpfs: []string{"/tmp"}, Target: "/bin/true"}

	t.Setenv("WAYLAND_DISPLAY", "")
	if SessionWarning(it) == "" {
		t.Error("有 tmpfs 但会话没有 WAYLAND_DISPLAY，应当警告")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if SessionWarning(it) != "" {
		t.Error("Wayland 会话下不该警告")
	}
	it.Tmpfs = nil
	t.Setenv("WAYLAND_DISPLAY", "")
	if SessionWarning(it) != "" {
		t.Error("没有 tmpfs 时不该警告")
	}
}

// ---------------------------------------------------------------- 托盘覆盖

// 两个开关：实例级 --no-tray 与机器级 defaults.tray_override。任一关掉都不碰
// 宿主那张表，并且要说清是谁关的 —— 否则用户会以为 twin 忘了写。
func TestPlanTrayOverrideSwitches(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	// 默认（config 内置 = 开）
	it, der, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if !it.TrayOverride {
		t.Error("默认应当覆盖托盘图标")
	}
	if got := derivationFor(der, "tray.override"); got != "" {
		t.Errorf("默认值不该出现在推导列表里，得到 %q", got)
	}

	// config.toml 关掉
	cfg := config.Builtin()
	cfg.Defaults.TrayOverride = false
	it, der, err = Plan(p, Options{Cfg: cfg}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.TrayOverride {
		t.Error("defaults.tray_override = false 之后不该再写宿主表")
	}
	if got := derivationFor(der, "tray.override"); !strings.Contains(got, "config.toml") {
		t.Errorf("要说清是 config.toml 关的，得到 %q", got)
	}

	// CLI 关掉（config 是开的）
	it, der, err = Plan(p, Options{NoTray: true}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.TrayOverride {
		t.Error("--no-tray 之后不该再写宿主表")
	}
	if got := derivationFor(der, "tray.override"); !strings.Contains(got, "--no-tray") {
		t.Errorf("要说清是命令行关的，得到 %q", got)
	}
}
