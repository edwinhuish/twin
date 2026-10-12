package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edwinhuish/twin/internal/inst"
)

// StateHint is one candidate data directory for a program, with the evidence
// that produced it.
//
// Hints are never applied on their own. A wrong guess that gets bound away
// silently is exactly the failure this tool exists to prevent: the instance
// still starts, it just reads the other instance's data, or nobody's. So
// discovery only prints, and only an explicit rules.d file or --map acts
// (设计 §7).
type StateHint struct {
	// Path is `~/`-relative, spelled the way rules.d spells it.
	Path string
	// Evidence says why it is being proposed, phrased for the terminal.
	Evidence string
	// Strong marks "this directory exists on this machine": the program has
	// already written there, so it is a fact about the program rather than a
	// guess about it. `twin rules --init` writes only these as live [[bind]].
	Strong bool
}

// xdgRoots are the per-program homes we probe. They are ordered as a program is
// likely to keep something worth isolating: settings and profiles first, caches
// last (a cache is usually safe to share, and the least interesting to bind).
var xdgRoots = []string{".config", ".local/share", ".local/state", ".cache"}

// SuggestStateDirs proposes directories this program may keep its own state in,
// from the evidence that costs nothing: the directories that already exist on
// this machine. The program created them, so this is a fact about the program
// rather than a guess about it.
//
// `twin new` prints these on every run. The fuzzier probe — `~/…` literals
// inside the binary — is SuggestStateDirsDeep, because it costs a second full
// read of the binary and is worth paying for only when discovery is the point.
func SuggestStateDirs(p inst.Paths, insp *Inspection) []StateHint {
	return suggestStateDirs(p, insp, false)
}

// SuggestStateDirsDeep adds what the binary says about itself: every `~/…` path
// literal it builds at run time. That evidence is weaker — the literal may
// belong to an optional feature, to a platform this build is not running on, or
// to a library it merely links — so it is reduced, filtered against the
// program's own name, and only ever offered to the user (see RuleDraft).
func SuggestStateDirsDeep(p inst.Paths, insp *Inspection) []StateHint {
	return suggestStateDirs(p, insp, true)
}

func suggestStateDirs(p inst.Paths, insp *Inspection, deep bool) []StateHint {
	names := programNames(insp)
	seen := map[string]bool{}
	var hints []StateHint

	add := func(path, evidence string, strong bool) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		hints = append(hints, StateHint{Path: path, Evidence: evidence, Strong: strong})
	}

	for _, n := range names {
		for _, root := range xdgRoots {
			if dirExists(filepath.Join(p.Home, root, n)) {
				add("~/"+root+"/"+n, "目录存在", true)
			}
		}
		// The dotfile layout: some programs keep everything in one directory of
		// their own directly under the home (WeChat's ~/.xwechat). It sits
		// outside every XDG root, which is why it cannot be derived from
		// xdgRoots.
		if dirExists(filepath.Join(p.Home, "."+n)) {
			add("~/."+n, "目录存在", true)
		}
	}

	// The dotfile directory is often not spelled quite like the program
	// ("wechat" → ~/.xwechat), so it is matched by name rather than looked up.
	// Those matches are still facts — the directory exists — but they are
	// labelled as name matches, because "~/.xwechat for wechat" and "~/.ssh for
	// ssh" are not the same claim about what a private copy would cost.
	//
	// The XDG roots themselves are skipped: ~/.config is shared by every
	// program on the machine, so a name match there would be a false positive.
	for _, entry := range dotDirs(p.Home) {
		name := strings.TrimPrefix(entry, ".")
		if xdgRootNames[name] {
			continue
		}
		if !relatedTo(name, names) {
			continue
		}
		add("~/"+entry, "目录存在（名字相近）", true)
	}

	if deep {
		for _, lit := range binaryStatePaths(insp.Target, names) {
			add(lit, "二进制内有这个字符串", false)
		}
	}

	// Facts before guesses, then alphabetical: stable output matters because
	// this list is meant to be read and acted on, not skimmed.
	sort.SliceStable(hints, func(i, j int) bool {
		if hints[i].Strong != hints[j].Strong {
			return hints[i].Strong
		}
		return hints[i].Path < hints[j].Path
	})
	return hints
}

// programNames lists what the program may call its own directory: the desktop
// entry's basename (how a person refers to it) and the real binary's basename
// (what survives a repackaged entry). Both are tried because a rule file may be
// filed under either — the same reason config.RuleKeys tries two names.
func programNames(insp *Inspection) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\x00") {
			return
		}
		for _, seen := range out {
			if seen == s {
				return
			}
		}
		out = append(out, s)
	}
	add(strings.TrimSuffix(filepath.Base(insp.SourceDesktop), ".desktop"))
	add(filepath.Base(insp.Target))
	return out
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// xdgRootNames are the dotfile directories that belong to the machine rather
// than to one program: ~/.config holds every application's configuration, so a
// name match on it would mean nothing.
var xdgRootNames = map[string]bool{"config": true, "cache": true, "local": true}

// dotDirs lists the directories directly under the home whose name starts with
// a dot. os.ReadDir sorts, so the result is stable across runs.
func dotDirs(home string) []string {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, ".") || name == "." || name == ".." {
			continue
		}
		// os.Stat follows symlinks: a symlinked directory is still a directory
		// the program writes into.
		if st, err := os.Stat(filepath.Join(home, name)); err == nil && st.IsDir() {
			out = append(out, name)
		}
	}
	return out
}

// ---------------------------------------------------------------- binary scan

// homeMarkers are the spellings a program uses to build a path under $HOME.
// The XDG ones are folded onto their default locations so that a literal found
// this way dedupes against the existence probe instead of shadowing it.
var homeMarkers = []struct{ marker, repl string }{
	{"${HOME}/", "~/"},
	{"$HOME/", "~/"},
	{"$XDG_CONFIG_HOME/", "~/.config/"},
	{"$XDG_DATA_HOME/", "~/.local/share/"},
	{"$XDG_STATE_HOME/", "~/.local/state/"},
	{"$XDG_CACHE_HOME/", "~/.cache/"},
	{"~/", "~/"},
}

// binaryStatePaths reads `~/…` literals out of the binary and reduces each one
// to "XDG root + one segment".
//
// The reduction is what makes the list usable. A program builds
// ~/.config/Foo/BrowserMetrics/2026-10-12 at run time, and binding that leaf
// would be meaningless — the unit a person can decide about is ~/.config/Foo.
func binaryStatePaths(path string, names []string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		// The scan is advisory: a binary we cannot read is not an error, it is
		// just a program we have no hint about.
		return nil
	}
	defer f.Close()

	// A capped carry: a long printable blob would otherwise be copied forward
	// on every chunk, turning a linear scan into a quadratic one.
	const carryCap = 4096
	const maxPaths = 256

	seen := map[string]bool{}
	var out []string
	collect := func(data []byte) {
		forEachString(data, func(run string) {
			for _, cand := range reduceHomePaths(run, names) {
				if seen[cand] || len(out) >= maxPaths {
					continue
				}
				seen[cand] = true
				out = append(out, cand)
			}
		})
	}

	buf := make([]byte, 1<<20)
	var carry []byte
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			seg := make([]byte, 0, len(carry)+n)
			seg = append(seg, carry...)
			seg = append(seg, buf[:n]...)

			// Hold back the run that touches the end of seg: it may continue
			// into the next chunk, and a truncated path would be reported as a
			// different — wrong — path.
			cut := len(seg)
			for cut > 0 && isPrintableByte(seg[cut-1]) {
				cut--
			}
			collect(seg[:cut])
			if keep := len(seg) - cut; keep > carryCap {
				cut = len(seg) - carryCap
			}
			carry = append(carry[:0], seg[cut:]...)
		}
		if rerr != nil {
			// Nothing follows, so the run held back above is complete after all.
			// Dropping it would lose every literal that sits at the very end of
			// the binary — which is where a build tool tends to leave the
			// strings it embedded last.
			collect(carry)
			break
		}
	}
	return out
}

func isPrintableByte(c byte) bool { return c >= 0x20 && c < 0x7f }

// forEachString walks the maximal runs of printable bytes, which is the reading
// `strings` uses — and the reason a statically linked toolkit that ldd cannot
// see is visible here at all (see traits.go).
func forEachString(seg []byte, fn func(string)) {
	start := -1
	for i := 0; i <= len(seg); i++ {
		if i < len(seg) && isPrintableByte(seg[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if i-start >= 6 {
				fn(string(seg[start:i]))
			}
			start = -1
		}
	}
}

func reduceHomePaths(run string, names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range homeMarkers {
		for off := 0; off < len(run); {
			i := strings.Index(run[off:], m.marker)
			if i < 0 {
				break
			}
			i += off
			off = i + len(m.marker)
			cand, ok := reduceStatePath(m.repl+takePath(run[off:]), names)
			if !ok || seen[cand] {
				continue
			}
			seen[cand] = true
			out = append(out, cand)
		}
	}
	return out
}

// takePath reads the path-shaped run that follows a marker. It stops at the
// first character that cannot be in a path, which is also what drops format
// placeholders ("%s/.config/x") before they can be mistaken for a real path.
func takePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r > 0x7f:
			// Non-ASCII segments are real (CJK directory names exist); keep them.
			b.WriteRune(r)
		case r <= ' ' || r == 0x7f:
			return b.String()
		case strings.ContainsRune(`"'<>=,;:()[]{}|*?&%$!`, r):
			return b.String()
		}
		b.WriteRune(r)
	}
	return b.String()
}

// reduceStatePath normalises a literal and decides whether it is worth
// proposing at all. Only paths that live in a known state root — or that are a
// dotfile directory directly in the home — survive: "~/Documents/report.pdf" is
// a file the program opens, not a directory it owns.
func reduceStatePath(raw string, names []string) (string, bool) {
	rest, ok := strings.CutPrefix(raw, "~/")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, "/")
	for len(parts) > 0 && (parts[len(parts)-1] == "" || parts[len(parts)-1] == ".") {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return "", false
	}

	switch parts[0] {
	case ".config", ".cache":
		return rootAndSegment("~/"+parts[0], parts[1:], names)
	case ".local":
		if len(parts) < 2 || (parts[1] != "share" && parts[1] != "state") {
			return "", false
		}
		return rootAndSegment("~/"+parts[0]+"/"+parts[1], parts[2:], names)
	default:
		// A dotfile directory in the home (~/.xwechat). There is nothing below
		// it to reduce: the whole thing is the program's own namespace.
		if !strings.HasPrefix(parts[0], ".") || !relatedTo(parts[0], names) {
			return "", false
		}
		return "~/" + parts[0], true
	}
}

func rootAndSegment(root string, rest []string, names []string) (string, bool) {
	if len(rest) == 0 || rest[0] == "" || rest[0] == "." || !relatedTo(rest[0], names) {
		return "", false
	}
	return root + "/" + rest[0], true
}

// relatedTo reports whether a directory segment plausibly belongs to this
// program rather than to the toolkit it links.
//
// The bar exists because binaries are full of *shared* paths: Chromium writes
// ~/.config/autostart, Qt reads ~/.config/fontconfig, GTK touches
// ~/.config/dconf. Proposing one of those as "this program's private data
// directory" would talk a user into binding away state that half the desktop
// depends on — and the breakage would look like a twin bug.
func relatedTo(segment string, names []string) bool {
	seg := normalizeName(segment)
	if len(seg) < 3 {
		return false
	}
	for _, n := range names {
		name := normalizeName(n)
		if len(name) < 3 {
			continue
		}
		if strings.Contains(name, seg) || strings.Contains(seg, name) {
			return true
		}
	}
	return false
}

// normalizeName folds case and drops punctuation: vendors vary those freely
// ("google-chrome" the binary, "Google Chrome" the window title), so the name
// that has to survive the comparison is the letters and digits.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- rule draft

// RuleDraft renders the file `twin rules --init` writes.
//
// Only hints backed by a real directory become live [[bind]] entries; the rest
// stay commented out. That split is the point of the file: "this directory
// exists" is a fact about the program on this machine, "this string is in the
// binary" is a guess about it, and the user should not have to tell the two
// apart from prose before deciding what their data layout is.
func RuleDraft(key, source string, hints []StateHint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# twin —— %s 的应用规则草稿\n", key)
	fmt.Fprintf(&b, "# 由 `twin rules --init %s` 写出（源桌面项 %s）。\n", key, source)
	b.WriteString(`#
# 这是草稿，不是结论。请逐条确认：下面每一条 [[bind]] 都会：
#   · 整体**替换**内置规则里的映射（不是追加）；
#   · 出现在每个新建实例的 instance.toml 里，并变成启动器里的一个 --bind。
#
#   private 指向别处            ⇒ 私有副本，两个实例互不干扰
#   private 与 canonical 相同   ⇒ 与该目录共用，只把只读升回可写
#   private 留空                ⇒ 同上（原地升回可写）
#
# 生效范围：只影响**以后新建**的实例。已存在的实例请改它的 instance.toml
# 再跑 twin regen <id>，或者 twin new <source> --as <id> --force 重来一次。
`)
	fmt.Fprintf(&b, "# 写完验证：twin rules %s\n", key)
	fmt.Fprintf(&b, "\napp = %q\n", key)

	var live, pending []StateHint
	for _, h := range hints {
		if h.Strong {
			live = append(live, h)
			continue
		}
		pending = append(pending, h)
	}

	for _, h := range live {
		fmt.Fprintf(&b, "\n# %s\n[[bind]]\ncanonical = %q\nprivate   = %q\n",
			h.Evidence, h.Path, PrivateFor(h.Path))
	}

	if len(pending) > 0 {
		b.WriteString("\n# 下面这些只是二进制里出现的字符串，没有别的证据 —— 需要时再打开\n")
		for _, h := range pending {
			fmt.Fprintf(&b, "# [[bind]]\n# canonical = %q\n# private   = %q\n#\n", h.Path, PrivateFor(h.Path))
		}
	}

	if len(hints) == 0 {
		b.WriteString(`
# 没有发现任何证据：这台机器上没有 ~/.config/<程序名> 这类目录，二进制里也没有
# 相关的 ~/ 路径字面量。也就是说这个程序"把状态放在哪"只能由你来判断 ——
# 若它有单实例锁或共享配置（Electron / Qt / GTK 系很常见），两个实例会互相干扰。
#
# 形状示例（路径请换成它真正用的那个）：
# [[bind]]
# canonical = "~/.config/` + key + `"
# private   = "~/.local/state/twin/{id}/config"
`)
	}
	return b.String()
}

// PrivateFor proposes where a hint's data should live when it is isolated.
// Naming the private copy after the *purpose* of the directory it replaces
// (config / share / state / cache) rather than after the program keeps a
// program's several directories from colliding inside one private root.
func PrivateFor(canonical string) string {
	rest, ok := strings.CutPrefix(canonical, "~/")
	if !ok {
		return canonical
	}
	parts := strings.Split(rest, "/")
	for _, root := range [][2]string{
		{".config", "config"},
		{".local/share", "share"},
		{".local/state", "state"},
		{".cache", "cache"},
	} {
		if sub, ok := strings.CutPrefix(rest, root[0]+"/"); ok && sub != "" {
			return "~/.local/state/twin/{id}/" + root[1]
		}
	}
	// A dotfile directory: its own name, minus the dot, survives as the label.
	return "~/.local/state/twin/{id}/" + strings.TrimPrefix(parts[0], ".")
}
