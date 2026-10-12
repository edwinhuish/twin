// Package tray owns the one piece of host state twin writes outside the
// user's home: the tray host's icon override table.
//
// The table belongs to the Ubuntu appindicator extension (ubuntu-appindicators)
// and is a GSettings key of type a(sss):
//
//	[(indicator-id, icon, attention-icon), ...]
//
// A row only ever matches one indicator, and the match is a strict string
// comparison against the id the indicator announces over StatusNotifierItem —
// which, for a wrapped program, is the alias twin exec'd it through. That is
// why the override can be written by the generator instead of being arranged by
// hand beforehand: the identity is already known at wrap time.
//
// Reading uses dconf's mmap path and works anywhere. Writing needs the session
// bus, so twin writes when it runs inside the desktop session (the normal
// case, and it takes effect immediately — the host hot-reloads the key) and
// degrades to "print the command, exit 3" when it does not (§5.3 of the design).
//
// Package tray deliberately knows nothing about instances: it takes an id and
// two paths. Everything else is the caller's business.
package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/edwinhuish/twin/internal/sysx"
)

// Schema and Key identify the host's override table. This is the Ubuntu tray
// host (ubuntu-appindicators@ubuntu.com), which is the only one on this system;
// see 应用包裹生成器_twin设计.md §7.
const (
	Schema = "org.gnome.shell.extensions.appindicator"
	Key    = "custom-icons"
)

// Entry is one row: the StatusNotifierItem id plus the two icons the host
// should draw instead of whatever the program sends.
//
// Attention is usually the same image: the host falls back from the requested
// icon type to NORMAL when a lookup misses, but leaving ATTENTION unset means
// an "attention" badge would silently lose the override.
type Entry struct {
	ID        string
	Normal    string
	Attention string
}

// Action says what a write actually did, so the caller can report "nothing to
// do" instead of claiming a change every time.
type Action int

const (
	// ActionUnchanged means the table already held exactly this row.
	ActionUnchanged Action = iota
	// ActionWritten means the table was rewritten.
	ActionWritten
	// ActionRemoved means a row was deleted.
	ActionRemoved
	// ActionNoHost means there is no override table on this machine — no
	// gsettings, or no appindicator extension. Normal, not an error.
	ActionNoHost
)

// Outcome is the result of Ensure or Remove.
type Outcome struct {
	Action Action
	// ID is the row that was acted on.
	ID string
	// Detail is a human-readable reason, always filled in for ActionNoHost.
	Detail string
}

// HostState reports whether this machine has an override table at all.
type HostState struct {
	OK  bool
	Why string // why not, when !OK
}

// Host probes for the table. A machine without the appindicator extension is a
// legitimate target, not a failure: twin should say "no tray host here" and
// carry on rather than print a fix that can never be run.
func Host() HostState {
	if sysx.Which("gsettings") == "" {
		return HostState{Why: "本机没有 gsettings（没有 dconf 环境）"}
	}
	if _, err := ReadRaw(); err != nil {
		return HostState{Why: firstLine(err.Error())}
	}
	return HostState{OK: true}
}

// ReadRaw returns the current table exactly as the host prints it.
func ReadRaw() (string, error) {
	gs := sysx.Which("gsettings")
	if gs == "" {
		return "", fmt.Errorf("gsettings 不在 PATH")
	}
	res, err := sysx.Run(gs, "get", Schema, Key)
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("%s", firstLine(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

// Table is a parsed snapshot.
type Table struct {
	Raw     string
	Entries []Entry
}

// Snapshot reads and parses the table.
func Snapshot() (*Table, error) {
	raw, err := ReadRaw()
	if err != nil {
		return nil, err
	}
	entries, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("宿主表读出来是 %q，解析不了：%w", raw, err)
	}
	return &Table{Raw: raw, Entries: entries}, nil
}

// Find returns the row for id, if the host is already overriding it. When the
// table (incorrectly) holds several rows for one id the last one wins, which is
// what the host does too — it iterates and overwrites.
func (t *Table) Find(id string) (Entry, bool) {
	var found Entry
	ok := false
	for _, e := range t.Entries {
		if e.ID == id {
			found, ok = e, true
		}
	}
	return found, ok
}

// With returns the rows with e in place of any row for the same id. An existing
// row keeps its position so a hand-ordered table does not reshuffle, and
// duplicates collapse into that one row.
func (t *Table) With(e Entry) []Entry {
	out := make([]Entry, 0, len(t.Entries)+1)
	placed := false
	for _, have := range t.Entries {
		if have.ID != e.ID {
			out = append(out, have)
			continue
		}
		if !placed {
			out = append(out, e)
			placed = true
		}
	}
	if !placed {
		out = append(out, e)
	}
	return out
}

// Without returns the rows with every row for id removed.
func (t *Table) Without(id string) []Entry {
	out := make([]Entry, 0, len(t.Entries))
	for _, e := range t.Entries {
		if e.ID != id {
			out = append(out, e)
		}
	}
	return out
}

// Ensure makes the table carry the override for id. It is idempotent: a second
// call on an unchanged table writes nothing.
func Ensure(id, icon string) (Outcome, error) {
	return apply(id, func(t *Table) []Entry {
		return t.With(Entry{ID: id, Normal: icon, Attention: icon})
	}, ActionWritten, func(entries []Entry, id string) bool {
		e, ok := (&Table{Entries: entries}).Find(id)
		return ok && e.Normal == icon && e.Attention == icon
	})
}

// Remove deletes the override for id, leaving every other row alone.
func Remove(id string) (Outcome, error) {
	return apply(id, func(t *Table) []Entry {
		return t.Without(id)
	}, ActionRemoved, func(entries []Entry, id string) bool {
		_, ok := (&Table{Entries: entries}).Find(id)
		return !ok
	})
}

// apply is the read-modify-write every mutation shares.
//
// Two things here are not optional. The table is a single global key, so the
// write has to be a merge — clobbering it would delete overrides belonging to
// programs twin has never heard of. And the result has to be read back,
// because `gsettings set` without a session bus exits 0, prints a warning to
// stderr, and changes nothing: the one lie this tool must never believe.
func apply(id string, mutate func(*Table) []Entry, wrote Action, ok func([]Entry, string) bool) (Outcome, error) {
	cur, err := Snapshot()
	if err != nil {
		// An unreadable table means there is no tray host here — no gsettings,
		// or no appindicator extension. That is a normal state on a machine
		// that is not this one, so it is a skip with a reason, not a failure.
		return Outcome{Action: ActionNoHost, ID: id, Detail: firstLine(err.Error())}, nil
	}
	next := mutate(cur)
	if sameEntries(next, cur.Entries) {
		return Outcome{Action: ActionUnchanged, ID: id, Detail: Render(cur.Entries)}, nil
	}

	literal := Render(next)
	gs := sysx.Which("gsettings")
	res, err := sysx.Run(gs, "set", Schema, Key, literal)
	if err != nil {
		return Outcome{}, err
	}
	if res.Code != 0 {
		return Outcome{}, fmt.Errorf("gsettings set 失败（%d）：%s", res.Code, firstLine(res.Stderr))
	}
	if msg := dconfComplaint(res.Stderr); msg != "" {
		return Outcome{}, fmt.Errorf("dconf 拒绝落盘：%s%s", msg, busHint())
	}

	back, err := Snapshot()
	if err != nil {
		return Outcome{}, fmt.Errorf("写完了却读不回来：%w", err)
	}
	if !ok(back.Entries, id) {
		return Outcome{}, fmt.Errorf(
			"写入没有落盘：%s 现在还是 %s%s（%s）",
			Key, oneLine(back.Raw), busHint(), "gsettings set 在无会话总线时会「假成功」")
	}
	return Outcome{Action: wrote, ID: id, Detail: literal}, nil
}

// dconfComplaint pulls the one stderr line that means "I did not write
// anything". dconf reports a failed commit there and still exits 0.
func dconfComplaint(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "failed to commit") || strings.Contains(line, "无法提交") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// busHint explains a failure that is almost always "you are not in a desktop
// session", so the user does not go looking for a broken instance.
func busHint() string {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return ""
	}
	if p := sessionBusPath(); p != "" && sysx.Exists(p) {
		return ""
	}
	return "；当前没有可用的会话总线（" + sessionBusPath() + " 不存在）"
}

func sessionBusPath() string {
	if uid := os.Getenv("XDG_RUNTIME_DIR"); uid != "" {
		return filepath.Join(uid, "bus")
	}
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "bus")
}

// ------------------------------------------------------------------ GVariant
//
// The table is written and read as GVariant text (`[('a', '/b', '/c')]`). The
// alternative — binding to GLib through cgo — would cost the zero-dependency
// build for one key, and the grammar involved is three string literals in an
// array of triples.

// Render turns rows into the GVariant text `gsettings set` expects.
func Render(entries []Entry) string {
	if len(entries) == 0 {
		// The annotated empty form: `[]` alone would be ambiguous, and this is
		// what the host itself prints for an unset key.
		return "@a(sss) []"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("('")
		b.WriteString(escape(e.ID))
		b.WriteString("', '")
		b.WriteString(escape(e.Normal))
		b.WriteString("', '")
		b.WriteString(escape(e.Attention))
		b.WriteString("')")
	}
	b.WriteByte(']')
	return b.String()
}

// Parse reads the host's output back into rows.
func Parse(lit string) ([]Entry, error) {
	s := strings.TrimSpace(lit)
	if s == "" {
		return nil, nil
	}
	// `gsettings get` prints the type annotation only sometimes (it is omitted
	// when the value is the schema's declared type, present in `range`
	// output), so accept either.
	if strings.HasPrefix(s, "@") {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			return nil, fmt.Errorf("只有类型标注没有值：%q", s)
		}
		s = strings.TrimSpace(s[i+1:])
	}
	if s == "[]" {
		return nil, nil
	}
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("不是数组：%q", s)
	}
	var out []Entry
	for _, row := range splitTop(s[1:len(s)-1], ',') {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		if len(row) < 2 || row[0] != '(' || row[len(row)-1] != ')' {
			return nil, fmt.Errorf("不是元组：%q", row)
		}
		fields := splitTop(row[1:len(row)-1], ',')
		if len(fields) != 3 {
			return nil, fmt.Errorf("元组有 %d 个字段，期望 3 个：%q", len(fields), row)
		}
		var e Entry
		dst := []*string{&e.ID, &e.Normal, &e.Attention}
		for i, f := range fields {
			v, err := unquote(strings.TrimSpace(f))
			if err != nil {
				return nil, err
			}
			*dst[i] = v
		}
		out = append(out, e)
	}
	return out, nil
}

// splitTop splits on sep at bracket depth zero, outside string literals.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++ // the escaped byte cannot close the literal
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// unquote removes the quotes around a GVariant string literal and resolves its
// escapes. An unrecognised escape keeps its backslash rather than dropping a
// character: a path that survives mangled is worse than one that looks odd.
func unquote(s string) (string, error) {
	if len(s) < 2 || (s[0] != '\'' && s[0] != '"') {
		return s, nil // a bare token: nothing to unquote
	}
	q := s[0]
	if s[len(s)-1] != q {
		return "", fmt.Errorf("字符串字面量没有闭合：%q", s)
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'a':
			b.WriteByte(0x07)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '\\', '\'', '"':
			b.WriteByte(body[i])
		case 'u':
			r, size, ok := decodeU4(body[i+1:])
			if !ok {
				b.WriteString("\\u")
				continue
			}
			b.WriteRune(r)
			i += size
		default:
			b.WriteByte('\\')
			b.WriteByte(body[i])
		}
	}
	return b.String(), nil
}

func decodeU4(s string) (rune, int, bool) {
	if len(s) < 4 {
		return 0, 0, false
	}
	n := 0
	for i := 0; i < 4; i++ {
		d := hexDigit(s[i])
		if d < 0 {
			return 0, 0, false
		}
		n = n*16 + d
	}
	r := rune(n)
	if !utf8.ValidRune(r) {
		return 0, 0, false
	}
	return r, 4, true
}

func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func escape(s string) string {
	if !strings.ContainsAny(s, `'\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\'' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func sameEntries(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
