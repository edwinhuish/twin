// Package desk reads and writes freedesktop.org Desktop Entry files.
//
// The parsing here is deliberately partial: twin only needs the main
// [Desktop Entry] group, and only the keys it either copies forward or checks.
// Anything else is preserved verbatim in Extra so `inspect` can show it.
package desk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/sysx"
)

// Entry is a parsed desktop file.
type Entry struct {
	Path    string
	Name    string
	Comment string
	// Names and Comments keep every localised variant (Name[zh_CN], Comment[en])
	// so a display name can be derived from the user's own locale instead of
	// falling back to the ASCII Name. See LocalizedName.
	Names        map[string]string
	Comments     map[string]string
	GenericNames map[string]string
	KeywordLists map[string]string
	Type         string
	Exec         string // raw Exec= value
	TryExec      string
	Icon         string
	Categories   string
	Keywords     string
	// GenericName answers "what kind of program is this" (e.g. "Web Browser").
	// It is the best free source of search keywords when an entry declares no
	// Keywords — which is exactly the case for wechat.desktop.
	GenericName string
	Terminal    bool
	Notify      bool
	WMClass     string
	Extra       map[string]string
}

// FieldCodes are the known %-codes of the Exec key.
var fieldCodes = map[byte]bool{
	'f': true, 'F': true, 'u': true, 'U': true, 'i': true, 'c': true,
	'k': true, 'd': true, 'D': true, 'n': true, 'N': true, 'v': true, 'm': true,
}

// Parse reads a desktop entry. Unknown keys land in Extra.
func Parse(path string) (*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	e := &Entry{
		Path:         path,
		Extra:        map[string]string{},
		Names:        map[string]string{},
		Comments:     map[string]string{},
		GenericNames: map[string]string{},
		KeywordLists: map[string]string{},
	}
	group := ""
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			group = line[1 : len(line)-1]
			continue
		}
		if group != "Desktop Entry" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%s:%d: line is not key=value: %q", path, n+1, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		switch key {
		case "Name":
			e.Name = val
		case "Comment":
			e.Comment = val
		case "Type":
			e.Type = val
		case "Exec":
			e.Exec = val
		case "TryExec":
			e.TryExec = val
		case "Icon":
			e.Icon = val
		case "Categories":
			e.Categories = val
		case "Keywords":
			e.Keywords = val
		case "GenericName":
			e.GenericName = val
		case "Terminal":
			e.Terminal = val == "true"
		case "StartupNotify":
			e.Notify = val == "true"
		case "StartupWMClass":
			e.WMClass = val
		default:
			if loc, ok := bracketKey(key, "Name"); ok {
				e.Names[loc] = val
				continue
			}
			if loc, ok := bracketKey(key, "Comment"); ok {
				e.Comments[loc] = val
				continue
			}
			// Check the longer base first: "Name[" is not a prefix of
			// "GenericName[zh_CN]", but keeping the order explicit avoids a
			// future reordering quietly swallowing the longer key.
			if loc, ok := bracketKey(key, "GenericName"); ok {
				e.GenericNames[loc] = val
				continue
			}
			if loc, ok := bracketKey(key, "Keywords"); ok {
				e.KeywordLists[loc] = val
				continue
			}
			e.Extra[key] = val
		}
	}
	if e.Name == "" && e.Exec == "" {
		return nil, fmt.Errorf("%s: no [Desktop Entry] group with Name/Exec", path)
	}
	return e, nil
}

// bracketKey extracts the locale from a localised key: `Name[zh_CN]` → "zh_CN".
func bracketKey(key, base string) (string, bool) {
	prefix := base + "["
	if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, "]") {
		return key[len(prefix) : len(key)-1], true
	}
	return "", false
}

// LocaleTags returns the locale tags to try, most specific first, read from the
// environment the way GLib does — LC_ALL, then LC_MESSAGES, then LANG — with
// the codeset and modifier stripped, so `zh_CN.UTF-8` yields ["zh_CN", "zh"].
//
// Locale matching is deliberately not a heuristic over which Name[...] exists:
// picking `Name[zh_CN]` on a German desktop would be exactly the kind of silent
// wrong guess this tool refuses to make.
func LocaleTags() []string {
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if tags := localeVariants(os.Getenv(env)); len(tags) > 0 {
			return tags
		}
	}
	return nil
}

func localeVariants(v string) []string {
	if i := strings.IndexByte(v, '@'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}
	switch v {
	case "", "C", "POSIX":
		return nil
	}
	out := []string{v}
	if i := strings.IndexByte(v, '_'); i > 0 {
		out = append(out, v[:i])
	}
	return out
}

// LocalizedName returns the localised display name for the first of tags that
// has one, together with the tag that matched. An empty value means the entry
// carries no name for any of these locales — the caller should fall back to
// Name. Returning the tag too is what makes a derived default *explainable*:
// "微信2 ← Name[zh_CN]" can only be printed if we know which tag won.
func (e *Entry) LocalizedName(tags ...string) (string, string) {
	for _, t := range tags {
		if v := e.Names[t]; v != "" {
			return v, t
		}
	}
	return "", ""
}

// LocalizedComment is LocalizedName for Comment[<locale>].
func (e *Entry) LocalizedComment(tags ...string) (string, string) {
	for _, t := range tags {
		if v := e.Comments[t]; v != "" {
			return v, t
		}
	}
	return "", ""
}

// LocalizedKeywords is LocalizedName for Keywords[<locale>]. The value is a
// ';'-separated list, returned verbatim because the separator style is the
// source's own choice.
func (e *Entry) LocalizedKeywords(tags ...string) (string, string) {
	for _, t := range tags {
		if v := e.KeywordLists[t]; v != "" {
			return v, t
		}
	}
	return "", ""
}

// LocalizedGenericName is LocalizedName for GenericName[<locale>].
func (e *Entry) LocalizedGenericName(tags ...string) (string, string) {
	for _, t := range tags {
		if v := e.GenericNames[t]; v != "" {
			return v, t
		}
	}
	return "", ""
}

// Command tokenises Exec and strips field codes, giving the real argv prefix.
//
// A desktop Exec value is not a shell line: the spec defines its own quoting.
// Getting this wrong would silently produce a wrong source.exec, which is the
// first link in the identity chain.
func (e *Entry) Command() ([]string, error) {
	args, err := Tokenize(e.Exec)
	if err != nil {
		return nil, fmt.Errorf("%s: Exec=%q: %w", e.Path, e.Exec, err)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("%s: Exec is empty", e.Path)
	}
	return args, nil
}

// Tokenize splits an Exec value into arguments, resolving %-codes.
func Tokenize(exec string) ([]string, error) {
	var args []string
	var cur strings.Builder
	haveArg := false
	flush := func() {
		if haveArg {
			args = append(args, cur.String())
			cur.Reset()
			haveArg = false
		}
	}
	for i := 0; i < len(exec); i++ {
		c := exec[i]
		switch c {
		case ' ', '\t':
			flush()
		case '\\':
			if i+1 < len(exec) {
				i++
				cur.WriteByte(exec[i])
				haveArg = true
			}
		case '"':
			haveArg = true
			i++
			for ; i < len(exec); i++ {
				if exec[i] == '\\' && i+1 < len(exec) {
					n := exec[i+1]
					if n == '"' || n == '`' || n == '$' || n == '\\' {
						cur.WriteByte(n)
						i++
						continue
					}
					cur.WriteByte('\\')
					continue
				}
				if exec[i] == '"' {
					break
				}
				cur.WriteByte(exec[i])
			}
			if i >= len(exec) {
				return nil, fmt.Errorf("unbalanced double quote")
			}
		case '%':
			if i+1 >= len(exec) {
				return nil, fmt.Errorf("dangling %% at end of Exec")
			}
			code := exec[i+1]
			i++
			if code == '%' {
				cur.WriteByte('%')
				haveArg = true
				continue
			}
			if !fieldCodes[code] {
				return nil, fmt.Errorf("unknown field code %%%c", code)
			}
			// Field codes expand to zero or more arguments; for our purposes
			// (finding the program and its static args) dropping them is right.
		default:
			cur.WriteByte(c)
			haveArg = true
		}
	}
	flush()
	return args, nil
}

// ExecLine builds an Exec= value from argv, quoting exactly as the Desktop
// Entry specification requires. A home directory containing a space would
// otherwise silently produce an entry that never starts.
func ExecLine(argv ...string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, quoteExecArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteExecArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Render describes the desktop entry we generate.
type Render struct {
	Name      string
	NameEn    string
	Comment   string
	CommentEn string
	Exec      string
	Icon      string
	Terminal  bool
	Categ     string
	Keywords  string
	WMClass   string
	Notify    bool
}

// Bytes renders a desktop entry that desktop-file-validate accepts cleanly.
func (r Render) Bytes() []byte {
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Version=1.0\n")
	writeKV(&b, "Name", r.Name)
	if r.NameEn != "" {
		writeKV(&b, "Name[en]", r.NameEn)
	}
	if r.Comment != "" {
		writeKV(&b, "Comment", r.Comment)
	}
	if r.CommentEn != "" {
		writeKV(&b, "Comment[en]", r.CommentEn)
	}
	writeKV(&b, "Exec", r.Exec)
	writeKV(&b, "Icon", r.Icon)
	b.WriteString("Terminal=false\n")
	if r.Categ != "" {
		writeKV(&b, "Categories", r.Categ)
	}
	if r.Keywords != "" {
		writeKV(&b, "Keywords", r.Keywords)
	}
	// Wayland: the basename is what actually claims the window (2026-10-11
	// measurement). StartupWMClass is written because it is harmless and does
	// matter on X11/XWayland — but nothing should *depend* on it.
	if r.WMClass != "" {
		writeKV(&b, "StartupWMClass", r.WMClass)
	}
	if r.Notify {
		b.WriteString("StartupNotify=true\n")
	} else {
		b.WriteString("StartupNotify=false\n")
	}
	return []byte(b.String())
}

func writeKV(b *strings.Builder, key, val string) {
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(escapeValue(val, key == "Exec"))
	b.WriteByte('\n')
}

// escapeValue escapes a value for the Desktop Entry format. Exec additionally
// needs shell-metacharacter escaping inside its argument quoting.
func escapeValue(s string, isExec bool) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if !isExec {
		return out
	}
	// Quote the command line as a whole is wrong (it must stay tokenisable),
	// so only guard the characters the spec reserves.
	return out
}

// Issue is one finding from Validate.
type Issue struct {
	Fatal bool
	Msg   string
}

// Validate checks the conditions that make GLib accept or silently discard a
// desktop entry. The Exec check is the one that bit us in practice: GLib
// returns NULL and the entry disappears without a sound.
func Validate(path, home string) []Issue {
	var issues []Issue
	e, err := Parse(path)
	if err != nil {
		return []Issue{{Fatal: true, Msg: err.Error()}}
	}
	if e.Name == "" {
		issues = append(issues, Issue{Fatal: true, Msg: "missing Name="})
	}
	if e.Exec == "" {
		issues = append(issues, Issue{Fatal: true, Msg: "missing Exec="})
		return issues
	}
	args, err := e.Command()
	if err != nil {
		return append(issues, Issue{Fatal: true, Msg: err.Error()})
	}
	prog := args[0]
	if !filepath.IsAbs(prog) {
		if sysx.Which(prog) == "" {
			issues = append(issues, Issue{Fatal: true,
				Msg: fmt.Sprintf("Exec program %q not found on PATH", prog)})
		}
	} else if !sysx.Exists(prog) {
		issues = append(issues, Issue{Fatal: true,
			Msg: fmt.Sprintf("Exec target %s does not exist — GLib discards the whole entry "+
				"(the panel then shows a gear, which looks like a matching problem)", prog)})
	} else if !sysx.IsExecutableFile(prog) {
		issues = append(issues, Issue{Fatal: true,
			Msg: fmt.Sprintf("Exec target %s exists but has no execute bit — GLib discards the whole "+
				"entry and the panel shows a gear; `desktop-file-validate` does NOT catch this", prog)})
	}
	if e.Icon == "" {
		issues = append(issues, Issue{Msg: "no Icon= — the panel will fall back to a placeholder"})
	} else if filepath.IsAbs(e.Icon) && !sysx.Exists(e.Icon) {
		issues = append(issues, Issue{Msg: fmt.Sprintf("Icon=%s does not exist", e.Icon)})
	}
	_ = home
	return issues
}
