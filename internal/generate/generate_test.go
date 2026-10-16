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

// 定义就在实例目录里的时候，install 不重写它：那一次往返会让手写的注释与排版消失，
// 而"gen 写一份、install 读同一份"正是要保住它们。定义在别处时才写一份存档记录，
// 因为 `twin reinstall` 要有东西可重放。
func TestADefinitionInItsOwnDirectoryIsNotRewritten(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}
	if !definitionIsHere(p, "wechat2", p.Profile("wechat2")) {
		t.Error("实例目录里那份定义就是它自己，install 应当原样留着")
	}
	if definitionIsHere(p, "wechat2", "/home/u/wraps/x.yml") {
		t.Error("-p 指的别处那份不是实例目录里那份，应当写出存档记录")
	}
	// 另一个实例的定义也不能当成这一个：认的是路径，不是文件名。
	if definitionIsHere(p, "wechat2", p.Profile("wechat3")) {
		t.Error("同名不同 id 的两份定义不能相认")
	}
	// 没给 -p、也没给 <id> 时路径是空的：那不是"就在这儿"，否则 install 会
	// 以为定义已经写好，于是什么都不写。
	if definitionIsHere(p, "wechat2", "") {
		t.Error("空路径不能当成定义已在原处")
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

	it, body, der, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "wechat2" {
		t.Errorf("id = %q, want wechat2", it.ID)
	}
	if body.Name != "微信2" {
		t.Errorf("name = %q, want 微信2 (derived from Name[zh_CN])", body.Name)
	}
	if body.NameEn != "wechat2" {
		t.Errorf("name_en = %q, want wechat2", body.NameEn)
	}
	if body.Comment != "微信桌面版" {
		t.Errorf("comment = %q, want 微信桌面版 (from Comment[zh_CN])", body.Comment)
	}
	if it.Badge != "2" {
		t.Errorf("badge = %q, want 2 (trailing digit of the id)", it.Badge)
	}
	for _, key := range []string{"id", "override.desktop Name", "override.desktop Name[en]",
		"override.desktop Comment", "badge"} {
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

	_, body, _, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if body.Name != "wechat2" {
		t.Errorf("name = %q, want wechat2", body.Name)
	}
	// Name[en] would be identical to Name here, so it must be dropped.
	if body.NameEn != "" {
		t.Errorf("name_en = %q, want empty (identical to name)", body.NameEn)
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
	it, _, _, err := Plan(p, Options{}, wechatInspection())
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

	it, body, der, err := Plan(p, Options{ID: "wx", Name: "我的微信", Badge: "A"}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "wx" || body.Name != "我的微信" || it.Badge != "A" {
		t.Fatalf("explicit options were overridden: id=%q name=%q badge=%q", it.ID, body.Name, it.Badge)
	}
	// The name is not in the table — the other lines of the entry still are,
	// because they are derived, so the check is on the value, not on the key.
	for _, d := range der {
		switch {
		case d.Key == "id" || d.Key == "badge":
			t.Errorf("%q must not be reported as derived when given explicitly: %+v", d.Key, d)
		case d.Value == "我的微信":
			t.Errorf("an explicit value must not be reported as derived: %+v", d)
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

	_, body, der, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "微信;wechat;" {
		t.Errorf("keywords = %q, want 微信;wechat;", body.Keywords)
	}
	why := derivationFor(der, "override.desktop Keywords")
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

	_, body, der, err := Plan(p, Options{}, insp)
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "微信;聊天;" {
		t.Errorf("keywords = %q, want the source's Keywords[zh_CN] verbatim", body.Keywords)
	}
	if !hasDerivation(der, "override.desktop Keywords") {
		t.Errorf("inheritance should still be reported: %+v", der)
	}
}

// A declared-but-empty list (";") is not a keyword list.
func TestKeywordsTreatsBareSemicolonAsAbsent(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	insp := wechatInspection()
	insp.Entry.Keywords = ";"
	p := inst.Paths{Home: t.TempDir()}

	_, body, _, err := Plan(p, Options{}, insp)
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "微信;wechat;" {
		t.Errorf("keywords = %q, want the derived list", body.Keywords)
	}
}

func TestKeywordsExplicitWins(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	_, body, der, err := Plan(p, Options{Keywords: "k1;k2"}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "k1;k2" {
		t.Errorf("keywords = %q, want the explicit value verbatim", body.Keywords)
	}
	if why := derivationFor(der, "override.desktop Keywords"); why != "" {
		t.Errorf("an explicit value must not be reported as derived: %q", why)
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

	_, body, der, err := Plan(p, Options{}, vlcInspection("Player;Capture;DVD;"))
	if err != nil {
		t.Fatal(err)
	}
	// The display name is still derived and still Chinese — that is not in
	// question; only the keyword list stays as the source wrote it.
	if body.Name != "VLC 媒体播放器2" {
		t.Errorf("name = %q, want VLC 媒体播放器2", body.Name)
	}
	if body.Keywords != "Player;Capture;DVD;" {
		t.Errorf("keywords = %q, want the source list untouched", body.Keywords)
	}
	if why := derivationFor(der, "override.desktop Keywords"); strings.Contains(why, "前置") {
		t.Errorf("nothing was prepended, so nothing may claim it was: %q", why)
	}
}

// Once the user names the instance that name is a term they asked to be
// findable, so it goes to the head of the inherited list.
func TestKeywordsExplicitNamePrependedToEnglishSource(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	_, body, der, err := Plan(p, Options{Name: "我的播放器"}, vlcInspection("Player;Capture;DVD;"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "我的播放器;Player;Capture;DVD;"; body.Keywords != want {
		t.Errorf("keywords = %q, want %q", body.Keywords, want)
	}
	why := derivationFor(der, "override.desktop Keywords")
	if !strings.Contains(why, "源 Keywords") || !strings.Contains(why, "我的播放器") {
		t.Errorf("the reason must name both halves of the result: %q", why)
	}
}

// A list that already speaks Chinese has nothing to gain from the prepend.
func TestKeywordsExplicitNameSkippedWhenSourceHasCJK(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	_, body, _, err := Plan(p, Options{Name: "我的播放器"}, vlcInspection("播放器;Player;"))
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "播放器;Player;" {
		t.Errorf("keywords = %q, want the source list untouched", body.Keywords)
	}
}

// Nor does the prepend fire when the source already lists that very keyword —
// case-insensitively, because keyword matching in the shell is.
func TestKeywordsExplicitNameSkippedWhenAlreadyListed(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	_, body, _, err := Plan(p, Options{Name: "vlc"}, vlcInspection("Player;VLC;"))
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "Player;VLC;" {
		t.Errorf("keywords = %q, want the source list untouched", body.Keywords)
	}
}

// A source that omits the trailing separator is left alone until we are the
// ones editing the value — then it is written in the recommended form.
func TestKeywordsPrependedListIsWellFormed(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	_, body, _, err := Plan(p, Options{Name: "我的播放器"}, vlcInspection("Player;Capture"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "我的播放器;Player;Capture;"; body.Keywords != want {
		t.Errorf("keywords = %q, want %q", body.Keywords, want)
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
		Args:       []string{"--rule-flag"},
	}
	return insp
}

// Every layer must be able to supply a default, and the derivation log must
// name the layer that won — "which file set this?" is the first question a
// broken identity raises.
func TestPlanTakesValuesFromRule(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, body, der, err := Plan(p, Options{}, ruleInspection())
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "wechat;微信;im" {
		t.Errorf("keywords = %q, want the rule's list verbatim", body.Keywords)
	}
	if got := derivationFor(der, "override.desktop Keywords"); got == "" {
		t.Error("规则提供的 keywords 必须出现在推导来源里")
	}
	if body.Categories != "Utility;Chat;" {
		t.Errorf("categories = %q, want the rule's", body.Categories)
	}
	if got := derivationFor(der, "override.desktop Categories"); got == "" {
		t.Error("规则提供的 categories 必须出现在推导来源里")
	}
	// The rule's /tmp becomes the line that hides it, written beside the base's
	// own /dev/shm: one list, where the order itself is readable.
	if got := strings.Join(it.Extras, " / "); !strings.Contains(got, "--tmpfs /tmp") {
		t.Errorf("bwrap.extras = %q，规则那条 --tmpfs /tmp 应当在里面", got)
	}
	if got := derivationFor(der, "bwrap.extras"); got == "" {
		t.Error("规则提供的 tmpfs 必须出现在推导来源里")
	}
	// The base's root mount is written first in this node, then the rule's own
	// mappings — {id} / <id> substituted with the concrete instance id.
	if !it.Binds[0].IsRootMount() {
		t.Errorf("bwrap.bind[0] = %q，应当是基座那条根挂载", it.Binds[0])
	}
	if it.Binds[1].Source != "~/.local/state/twin/wechat2/xwechat" {
		t.Errorf("bind source = %q", it.Binds[1].Source)
	}
}

// A CLI keyword list is literal: rules.d must not get a second word in.
func TestPlanExplicitKeywordsBeatRule(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	_, body, der, err := Plan(p, Options{Keywords: "only;this;"}, ruleInspection())
	if err != nil {
		t.Fatal(err)
	}
	if body.Keywords != "only;this;" {
		t.Errorf("keywords = %q, want the explicit list", body.Keywords)
	}
	if got := derivationFor(der, "override.desktop Keywords"); got != "" {
		t.Errorf("显式给了 --keywords 就不该有推导来源，得到 %q", got)
	}
}

func TestPlanLevelFromRuleThenConfig(t *testing.T) {
	p := inst.Paths{Home: t.TempDir()}

	// rules.d wins over config.toml.
	insp := ruleInspection()
	insp.Rule.Level = "none"
	it, _, _, err := Plan(p, Options{Cfg: &config.Config{Defaults: config.Defaults{Level: "alias", BadgeStyle: "corner", TmpfsUnlessX11: true}}}, insp)
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
	it2, _, _, err := Plan(p, Options{Cfg: &config.Config{Defaults: config.Defaults{Level: "none", BadgeStyle: "corner"}}}, insp2)
	if err != nil {
		t.Fatal(err)
	}
	if it2.Level != inst.LevelNone {
		t.Errorf("level = %s, want none (from config.toml)", it2.Level)
	}
}

// The option list is concatenated low priority first — the base twin writes,
// then config, then rules.d, then the CLI — so the last writer shadows an
// earlier mount on the same destination (bwrap's own rule), while the base
// stays first, where the mappings emitted after the whole list can overrule it.
func TestPlanBwrapOrderIsBaseConfigRuleCLI(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	cfg := config.Builtin()
	cfg.Args = []string{"--from-config"}

	it, _, der, err := Plan(p, Options{Cfg: cfg, Args: []string{"--from-cli"}}, ruleInspection())
	if err != nil {
		t.Fatal(err)
	}
	// Written out rather than rebuilt from sandbox.BaseFor: this is the command
	// line, and the base is the first half of it — the root mount is in `bind`,
	// and this fixture's rule asks for a private /tmp, so no host-/tmp line.
	want := []string{
		"--proc /proc", "--tmpfs /dev/shm", "--tmpfs /tmp", "--die-with-parent",
		"--from-config", "--rule-flag", "--from-cli",
	}
	if strings.Join(it.Extras, " ") != strings.Join(want, " ") {
		t.Fatalf("bwrap.extras = %v, want %v", it.Extras, want)
	}
	// The base's provenance has to be in the derivation log: it is the one part
	// of this list the user did not write, and the whole point is that they can
	// see it and change it.
	if derivationFor(der, "bwrap.extras") == "" {
		t.Error("基座必须出现在推导来源里")
	}
}

// Every piece of the base goes where it can be read as what it is: the root
// mount into `bind` (it is a mount), and the rest as the very option lines the
// command gets — `--proc /proc` next to `--tmpfs /dev/shm` next to the host's
// `/tmp` line, in the order they are applied. One list is also what makes
// "which line wins" a question the file itself answers.
func TestPlanWritesTheBaseByNode(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	it, _, der, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	// The root is a mount, so it is written in the node for mounts — as the
	// first entry there, because the layers above (the rule's mappings) are
	// appended after it.
	if len(it.Binds) == 0 || !it.Binds[0].IsRootMount() {
		t.Errorf("bwrap.bind = %v，want 第一条是基座那条根挂载", it.Binds)
	}
	// Everything else the base contributes is an option line, in command order:
	// this fixture has no Wayland trait and no rule, so the host's /tmp is the
	// branch — a line of its own, *after* every tmpfs line, which is what lets
	// it replace a `--tmpfs /tmp` without deleting that line as well.
	want := []string{"--proc /proc", "--tmpfs /dev/shm", "--bind /tmp /tmp", "--die-with-parent"}
	if got := strings.Join(it.Extras, " / "); got != strings.Join(want, " / ") {
		t.Errorf("bwrap.extras = %q, want %q", got, strings.Join(want, " / "))
	}
	if len(it.Env) != 1 || it.Env[0].Key != "TWIN_INSTANCE" || it.Env[0].Value != "wechat2" {
		t.Errorf("bwrap.env = %+v", it.Env)
	}
	// Every node the base filled has to name its source: this is the part the
	// user did not write.
	for _, key := range []string{"bwrap.bind", "bwrap.env", "bwrap.extras"} {
		if !hasDerivation(der, key) {
			t.Errorf("%s 的推导来源没记下来", key)
		}
	}
}

// No base, no sandbox options: `level none` runs the program untouched, so a
// /tmp entry there would describe a mount that never happens — and
// SessionWarning reads that list when an instance is replayed in an X11 session.
func TestPlanNoneLevelTakesNoTmpDecision(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	insp := wechatInspection()
	insp.Traits.Wayland = true

	it, _, _, err := Plan(p, Options{Cfg: config.Builtin(), Level: inst.LevelNone}, insp)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Extras) != 0 || len(it.Env) != 0 || len(it.Binds) != 0 {
		t.Errorf("level none 没有基座：bind=%v extras=%v env=%v", it.Binds, it.Extras, it.Env)
	}
}

// A refused token must name the layer it came from, or the user has to audit
// three files by hand.
func TestPlanBlamesTheLayerForABadToken(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}
	cfg := config.Builtin()
	cfg.Path = "/home/x/.config/twin/config.toml"
	cfg.Args = []string{"--argv0"}

	_, _, _, err := Plan(p, Options{Cfg: cfg}, ruleInspection())
	if err == nil {
		t.Fatal("配置里的 --argv0 应当被拒绝")
	}
	if !strings.Contains(err.Error(), cfg.Path) {
		t.Errorf("错误应指出是 config.toml 里的，得到 %v", err)
	}

	_, _, _, err = Plan(p, Options{Args: []string{"--argv0"}}, ruleInspection())
	if err == nil || !strings.Contains(err.Error(), "命令行") {
		t.Errorf("命令行透传的错误应点名命令行，得到 %v", err)
	}
}

// Refresh only ever *adds*: the option lines the environment layers contribute
// and the snapshot does not carry yet. Everything already in the file is left
// byte for byte — regen must not quietly rewrite a hand-edited instance.
//
// The entry's prose is not refreshed at all any more, and cannot be: it lives in
// the file `override.desktop` points at, which from the first byte on is the
// user's document (see Materialize).
func TestRefreshAppendsOnlyWhatIsMissing(t *testing.T) {
	it := &inst.Instance{
		ID:              "wechat2",
		Target:          "/opt/wechat/wechat",
		DesktopOverride: "./wechat2.desktop",
		Level:           inst.LevelAlias,
		// Hand-written, and config.toml wants the same line: appending it a
		// second time would be the "helpful" rewrite that makes the list grow
		// on every run.
		Extras: []string{"--from-config"},
	}
	cfg := config.Builtin()
	cfg.Args = []string{"--from-config"}

	notes, err := Refresh(it, ruleInspection(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(it.Extras, " ") != "--from-config --rule-flag" {
		t.Fatalf("bwrap.extras = %v, want config + rule（基座属于定义本身，--refresh 不碰它）", it.Extras)
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

// The warning has to find the line that hides /tmp — there is exactly one place
// it can be said (a `--tmpfs /tmp` line in bwrap.extras), and missing it would
// let the warning go quiet exactly where it matters: a Wayland instance
// replayed in an X11 session.
func TestSessionWarning(t *testing.T) {
	hidesTmp := &inst.Instance{ID: "x", Target: "/bin/true",
		Extras: []string{"--proc /proc", "--tmpfs /tmp"}}
	// The other branch of the same decision: the host's /tmp, bound onto
	// itself. No warning — /tmp/.X11-unix is exactly what it keeps reachable.
	hostTmp := &inst.Instance{ID: "x", Target: "/bin/true",
		Extras: []string{"--proc /proc", "--bind /tmp /tmp"}}

	t.Setenv("WAYLAND_DISPLAY", "")
	if SessionWarning(hidesTmp) == "" {
		t.Error("盖住了 /tmp 而会话没有 WAYLAND_DISPLAY，应当警告")
	}
	if SessionWarning(hostTmp) != "" {
		t.Error("没有盖住 /tmp 时不该警告")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if SessionWarning(hidesTmp) != "" {
		t.Error("Wayland 会话下不该警告")
	}
}

// ---------------------------------------------------------------- 托盘覆盖

// 两个开关：实例级 --no-tray 与机器级 defaults.tray_override。任一关掉都不碰
// 宿主那张表，并且要说清是谁关的 —— 否则用户会以为 twin 忘了写。
func TestPlanTrayOverrideSwitches(t *testing.T) {
	clearLocale(t, "zh_CN.UTF-8")
	p := inst.Paths{Home: t.TempDir()}

	// 默认（config 内置 = 开）
	it, _, der, err := Plan(p, Options{}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if !it.TrayOverride {
		t.Error("默认应当覆盖托盘图标")
	}
	if got := derivationFor(der, "override.tray"); got != "" {
		t.Errorf("默认值不该出现在推导列表里，得到 %q", got)
	}

	// config.toml 关掉
	cfg := config.Builtin()
	cfg.Defaults.TrayOverride = false
	it, _, der, err = Plan(p, Options{Cfg: cfg}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.TrayOverride {
		t.Error("defaults.tray_override = false 之后不该再写宿主表")
	}
	if got := derivationFor(der, "override.tray"); !strings.Contains(got, "config.toml") {
		t.Errorf("要说清是 config.toml 关的，得到 %q", got)
	}

	// CLI 关掉（config 是开的）
	it, _, der, err = Plan(p, Options{NoTray: true}, wechatInspection())
	if err != nil {
		t.Fatal(err)
	}
	if it.TrayOverride {
		t.Error("--no-tray 之后不该再写宿主表")
	}
	if got := derivationFor(der, "override.tray"); !strings.Contains(got, "--no-tray") {
		t.Errorf("要说清是命令行关的，得到 %q", got)
	}
}
