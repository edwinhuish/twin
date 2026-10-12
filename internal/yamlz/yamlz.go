// Package yamlz implements a deliberately small YAML subset: exactly the shapes
// twin emits in `profile.yml` and a human is expected to hand-edit.
//
// Supported:
//
//	comments (#), blank lines, space indentation, `key: value` mappings,
//	block sequences (`- value`, `- key: value`), empty flow collections
//	(`[]`, `{}`), one-line flow sequences (`[a, b]`), double-quoted and
//	single-quoted scalars, plain scalars, booleans (true/false), integers.
//
// Everything else is a hard error carrying a line number. That is on purpose: a
// silently mis-parsed identity file is far worse than a refused one — the whole
// point of this tool is to stop identity settings from failing quietly.
//
// Deliberately NOT supported (error out): tabs used for indentation, document
// markers (---), anchors / aliases / tags (& * !!), block scalars (| >),
// multi-line flow collections, non-empty flow mappings, duplicate keys, and
// YAML's ambiguous spellings of booleans and null (yes no on off y n null ~).
//
// That last rule is the one worth arguing about. `notify: yes` is *not* a
// boolean in YAML 1.2 — it is the string "yes", which the reader would then
// quietly turn into false. Refusing the ambiguous spellings, instead of
// guessing at them, keeps "the file says what twin will do" true; write `true`,
// or quote it (`"yes"`) when the text really is the value.
package yamlz

import (
	"fmt"
	"strconv"
	"strings"
)

// Tree is an insertion-ordered mapping. Order matters: we both emit these files
// and round-trip them, and a stable diff is worth more than map iteration.
type Tree struct {
	keys  []string
	vals  map[string]any
	notes map[string]string
}

// New returns an empty Tree.
func New() *Tree { return &Tree{vals: make(map[string]any)} }

// Set stores v under key, keeping first-insertion order.
func (t *Tree) Set(key string, v any) {
	if t.vals == nil {
		t.vals = make(map[string]any)
	}
	if _, ok := t.vals[key]; !ok {
		t.keys = append(t.keys, key)
	}
	t.vals[key] = v
}

// SetComment attaches a comment to key, written directly above it when the tree
// is encoded. text may contain newlines; each becomes its own `#` line.
//
// Comments are documentation for whoever reads the file, not data: Parse drops
// them. They hang off the Tree rather than travelling inside the values so that
// the only thing that can produce them is the code that builds the document —
// a hand-edited file cannot smuggle a line into a later render.
func (t *Tree) SetComment(key, text string) {
	if t.notes == nil {
		t.notes = make(map[string]string)
	}
	t.notes[key] = text
}

// Keys returns the keys in insertion order.
func (t *Tree) Keys() []string { return append([]string(nil), t.keys...) }

// Len reports how many keys the tree holds.
func (t *Tree) Len() int { return len(t.keys) }

func (t *Tree) lookup(key string) (any, bool) {
	if t == nil {
		return nil, false
	}
	v, ok := t.vals[key]
	return v, ok
}

// Has reports whether key is present.
func (t *Tree) Has(key string) bool { _, ok := t.lookup(key); return ok }

// Str returns key as a string ("" when absent).
//
// Numbers and booleans are rendered back to text rather than dropped: the
// document is untyped, so `badge: 2` is a perfectly reasonable thing to write,
// and the alternative — reading it as an integer and then returning "" — is the
// silent failure this package exists to avoid.
func (t *Tree) Str(key string) string {
	v, _ := t.lookup(key)
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return ""
}

// Bool returns key as a bool. The string forms are accepted so a quoted
// `"true"` — which is what hand-editing produces sooner or later — is not read
// as false.
func (t *Tree) Bool(key string) bool {
	v, _ := t.lookup(key)
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(x)))
		return err == nil && b
	}
	return false
}

// Int returns key as an int64.
func (t *Tree) Int(key string) int64 {
	v, _ := t.lookup(key)
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

// StrList returns key as a []string, skipping non-string elements.
//
// Both spellings of a sequence are accepted: []any is what parsing produces,
// []string is what a caller building a document writes. Answering "" for one of
// them would make a tree behave differently depending on which side of the
// round trip it came from.
func (t *Tree) StrList(key string) []string {
	v, _ := t.lookup(key)
	switch arr := v.(type) {
	case []string:
		// A copy, and never nil: an empty sequence and an absent one answer
		// with the same value here, so a caller cannot tell them apart by
		// accident (Has is how you ask that question).
		return append(make([]string, 0, len(arr)), arr...)
	case []any:
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// StrListStrict returns key as a list of strings and refuses every other shape.
//
// StrList reads a wrong shape as "no entries", which is the right answer for a
// knob whose absence changes nothing. It is the wrong reader for a list that
// *is* the answer: a `bind` written as a single string would come back as
// "nothing is isolated", and nothing on screen would say so. Absent stays
// (nil, nil); written-as-the-wrong-thing is an error.
func (t *Tree) StrListStrict(key string) ([]string, error) {
	v, ok := t.lookup(key)
	if !ok {
		return nil, nil
	}
	switch arr := v.(type) {
	case []string:
		return append(make([]string, 0, len(arr)), arr...), nil
	case []any:
		out := make([]string, 0, len(arr))
		for i, e := range arr {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d]: expected a string, got %T", key, i, e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: expected a list of strings, got %T", key, v)
}

// Tree returns the sub-tree at key (an empty Tree when absent).
func (t *Tree) Tree(key string) *Tree {
	v, _ := t.lookup(key)
	if x, ok := v.(*Tree); ok {
		return x
	}
	return New()
}

// TableList returns the sequence of mappings at a single key.
func (t *Tree) TableList(key string) []*Tree {
	v, _ := t.lookup(key)
	arr, _ := v.([]*Tree)
	return arr
}

// At walks a dotted path (e.g. "map.bind") and returns the sub-tree there, or an
// empty Tree when the path does not resolve.
func (t *Tree) At(path string) *Tree {
	node := t
	for _, p := range strings.Split(path, ".") {
		v, ok := node.lookup(p)
		if !ok {
			return New()
		}
		switch x := v.(type) {
		case *Tree:
			node = x
		case []*Tree:
			if len(x) == 0 {
				return New()
			}
			node = x[len(x)-1]
		default:
			return New()
		}
	}
	return node
}

// TablesAt walks a dotted path and returns the sequence of mappings there.
func (t *Tree) TablesAt(path string) []*Tree {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return t.At(path[:i]).TableList(path[i+1:])
	}
	return t.TableList(path)
}

// AddTable appends an entry to the sequence of mappings at path, creating the
// path if needed, and returns the new (empty) entry.
func (t *Tree) AddTable(path string) *Tree {
	parent, leaf := t, path
	if i := strings.LastIndex(path, "."); i >= 0 {
		parent, leaf = t.Ensure(path[:i]), path[i+1:]
	}
	nt := New()
	parent.Set(leaf, append(parent.TableList(leaf), nt))
	return nt
}

// Ensure walks a dotted path, creating intermediate mappings, and returns the
// tree at the end of it.
func (t *Tree) Ensure(path string) *Tree {
	node := t
	for _, p := range strings.Split(path, ".") {
		if sub, ok := node.lookup(p); ok {
			if t2, isTree := sub.(*Tree); isTree {
				node = t2
				continue
			}
		}
		nt := New()
		node.Set(p, nt)
		node = nt
	}
	return node
}

// ---------------------------------------------------------------- encoding

// Encode renders t as YAML text. Values come before nested blocks at every
// level, matching the shape a human would write by hand.
func Encode(t *Tree) []byte {
	var b strings.Builder
	writeMapping(&b, t, 0, "")
	return []byte(b.String())
}

// writeMapping writes t's entries with keys in column keyCol. firstPrefix is
// written in place of the first line's indentation — the "- " that makes a
// mapping an item of a sequence.
func writeMapping(b *strings.Builder, t *Tree, keyCol int, firstPrefix string) {
	pad := func(n int) string { return strings.Repeat(" ", n) }
	wrote := false
	head := func() string {
		if !wrote {
			wrote = true
			return firstPrefix
		}
		return pad(keyCol)
	}
	// A comment never goes through head(): a `- ` followed by `#` would turn the
	// item into a comment. It sits on its own line instead — at the dash's
	// column when this mapping opens a sequence item (an item's keys sit two
	// columns past its dash), and at its own otherwise.
	commentCol := keyCol
	if strings.HasSuffix(firstPrefix, "- ") {
		commentCol -= 2
	}
	note := func(k string) { t.writeComment(b, k, pad(commentCol)) }

	for _, k := range t.keys {
		switch x := t.vals[k].(type) {
		case *Tree, []*Tree:
			continue // nested blocks come last
		case []string:
			note(k)
			if len(x) == 0 {
				fmt.Fprintf(b, "%s%s: []\n", head(), k)
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", head(), k)
			for _, e := range x {
				fmt.Fprintf(b, "%s- %s\n", pad(keyCol+2), scalar(e))
			}
		case []any:
			note(k)
			if len(x) == 0 {
				fmt.Fprintf(b, "%s%s: []\n", head(), k)
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", head(), k)
			writeSeq(b, x, keyCol+2)
		default:
			note(k)
			fmt.Fprintf(b, "%s%s: %s\n", head(), k, scalar(t.vals[k]))
		}
	}

	for _, k := range t.keys {
		sub, ok := t.vals[k].(*Tree)
		if !ok {
			continue
		}
		note(k)
		if sub.Len() == 0 {
			fmt.Fprintf(b, "%s%s: {}\n", head(), k)
			continue
		}
		fmt.Fprintf(b, "%s%s:\n", head(), k)
		writeMapping(b, sub, keyCol+2, pad(keyCol+2))
	}

	for _, k := range t.keys {
		arr, ok := t.vals[k].([]*Tree)
		if !ok {
			continue
		}
		note(k)
		if len(arr) == 0 {
			fmt.Fprintf(b, "%s%s: []\n", head(), k)
			continue
		}
		fmt.Fprintf(b, "%s%s:\n", head(), k)
		for _, sub := range arr {
			// The item's keys sit two columns past its dash.
			writeMapping(b, sub, keyCol+4, pad(keyCol+2)+"- ")
		}
	}
}

// writeComment emits the comment attached to key, at the column given by pad.
func (t *Tree) writeComment(b *strings.Builder, key, pad string) {
	text := t.notes[key]
	if text == "" {
		return
	}
	for _, ln := range strings.Split(text, "\n") {
		if ln = strings.TrimRight(ln, " "); ln != "" {
			fmt.Fprintf(b, "%s# %s\n", pad, ln)
			continue
		}
		fmt.Fprintf(b, "%s#\n", pad)
	}
}

func writeSeq(b *strings.Builder, arr []any, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, e := range arr {
		if sub, ok := e.(*Tree); ok {
			writeMapping(b, sub, indent+4, pad+"- ")
			continue
		}
		fmt.Fprintf(b, "%s- %s\n", pad, scalar(e))
	}
}

// scalar renders a leaf value. Encode only ever fails on types we never
// construct, so an unknown one panics rather than producing a wrong file.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return quote(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case nil:
		return `""`
	}
	panic(fmt.Sprintf("yamlz: cannot encode %T", v))
}

// quote writes a scalar plain when that is unambiguous and quoted when it is
// not. Anything in doubt gets quotes: `#` and `: ` change what the line means,
// a leading `-` turns a value into a sequence item, and a bare digit would come
// back as a number.
func quote(s string) string {
	if !needsQuote(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var yamlWords = map[string]bool{
	"true": true, "false": true, "null": true, "~": true,
	"yes": true, "no": true, "on": true, "off": true, "y": true, "n": true,
	"none": true,
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	if strings.TrimSpace(s) != s {
		return true
	}
	if yamlWords[strings.ToLower(s)] {
		return true
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	switch s[0] {
	case '-', '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>',
		'\'', '"', '%', '@', '`':
		return true
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") ||
		strings.Contains(s, " #") || strings.ContainsAny(s, "\t\n\r") {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- parsing

// line is one significant line: comments and blanks are already gone, and the
// indentation is a column count.
type line struct {
	no     int
	indent int
	text   string
}

// Parse reads a YAML subset document into a Tree.
func Parse(data []byte) (*Tree, error) {
	lines, err := lex(string(data))
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return New(), nil
	}
	if isItem(lines[0].text) {
		return nil, fmt.Errorf("line %d: 顶层必须是映射（key: value）：序列只出现在某个键下面",
			lines[0].no)
	}
	t, next, err := parseMapping(lines, 0, 0)
	if err != nil {
		return nil, err
	}
	if next != len(lines) {
		return nil, fmt.Errorf("line %d: 这一行的缩进对不上：%q",
			lines[next].no, strings.TrimSpace(lines[next].text))
	}
	return t, nil
}

// lex splits the document into significant lines, refusing the constructs the
// subset does not cover before any structure is built.
func lex(s string) ([]line, error) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []line
	for i, raw := range strings.Split(s, "\n") {
		no := i + 1
		if strings.HasPrefix(raw, "---") || strings.HasPrefix(raw, "...") {
			return nil, fmt.Errorf("line %d: 不支持文档标记（--- / ...）：一个文件就是一份配置", no)
		}
		indent := 0
		for indent < len(raw) && raw[indent] == ' ' {
			indent++
		}
		if indent < len(raw) && raw[indent] == '\t' {
			return nil, fmt.Errorf("line %d: 缩进只能用空格，不能用 Tab（YAML 一致如此，也免得看不出层次）", no)
		}
		text := strings.TrimRight(stripComment(raw[indent:]), " ")
		if text == "" {
			continue
		}
		out = append(out, line{no: no, indent: indent, text: text})
	}
	return out, nil
}

// stripComment cuts a trailing comment. `#` only starts a comment at the start
// of a line or after a space — a path or a name may legitimately contain one.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == '"' && c == '\\':
			i++ // the escaped byte cannot close the literal
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && c == '#' && (i == 0 || s[i-1] == ' '):
			return s[:i]
		}
	}
	return s
}

// isItem reports whether a line opens a sequence item.
func isItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

// parseMapping consumes the mapping whose keys sit at column indent, starting
// at lines[i]. It returns the tree and the index of the first unconsumed line.
func parseMapping(lines []line, i, indent int) (*Tree, int, error) {
	t := New()
	for i < len(lines) {
		l := lines[i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, 0, fmt.Errorf("line %d: 缩进多了一级但没有对应的键：%q",
				l.no, strings.TrimSpace(l.text))
		}
		if isItem(l.text) {
			return nil, 0, fmt.Errorf("line %d: 这里期望 `key: value`，读到序列项 %q",
				l.no, strings.TrimSpace(l.text))
		}
		key, rest, ok := splitKey(l.text)
		if !ok {
			return nil, 0, fmt.Errorf("line %d: 期望 `key: value`，读到 %q", l.no, l.text)
		}
		if t.Has(key) {
			return nil, 0, fmt.Errorf("line %d: 键 %q 出现了两次 —— 后一个会静默盖掉前一个", l.no, key)
		}
		i++
		if rest != "" {
			v, err := parseScalar(rest, l.no)
			if err != nil {
				return nil, 0, err
			}
			t.Set(key, v)
			continue
		}
		// `key:` with nothing after it. Only a deeper block may follow: an
		// empty value would be ambiguous (null? empty string?), and guessing
		// there is how a config quietly loses a name.
		if i >= len(lines) || lines[i].indent <= indent {
			return nil, 0, fmt.Errorf("line %d: 键 %q 后面没有值。要空字符串写 \"\"，要走嵌套块就缩进",
				l.no, key)
		}
		childIndent := lines[i].indent
		if isItem(lines[i].text) {
			seq, next, err := parseSequence(lines, i, childIndent)
			if err != nil {
				return nil, 0, err
			}
			t.Set(key, seq)
			i = next
			continue
		}
		sub, next, err := parseMapping(lines, i, childIndent)
		if err != nil {
			return nil, 0, err
		}
		t.Set(key, sub)
		i = next
	}
	return t, i, nil
}

// parseSequence consumes the sequence whose dashes sit at column indent. The
// result is []*Tree for a sequence of mappings and []any for one of scalars —
// the same two shapes the encoder writes, which is what keeps a round trip
// exact. Mixing them in one sequence is a shape the schema never has, so it is
// refused rather than half-supported.
func parseSequence(lines []line, i, indent int) (any, int, error) {
	var (
		maps []*Tree
		scal []any
	)
	for i < len(lines) {
		l := lines[i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, 0, fmt.Errorf("line %d: 序列项的缩进对不上（这一项比上一项深）：%q",
				l.no, strings.TrimSpace(l.text))
		}
		if !isItem(l.text) {
			return nil, 0, fmt.Errorf("line %d: 这里期望序列项（- …），读到 %q", l.no, l.text)
		}
		body := strings.TrimSpace(strings.TrimPrefix(l.text, "-"))
		i++
		if body == "" {
			if i >= len(lines) || lines[i].indent <= indent {
				return nil, 0, fmt.Errorf("line %d: `-` 后面既没有值也没有嵌套块", l.no)
			}
			if isItem(lines[i].text) {
				return nil, 0, fmt.Errorf("line %d: 不支持嵌套序列（序列的项还是序列）", lines[i].no)
			}
			sub, next, err := parseMapping(lines, i, lines[i].indent)
			if err != nil {
				return nil, 0, err
			}
			maps = append(maps, sub)
			i = next
			continue
		}
		if _, _, isPair := splitKey(body); !isPair {
			v, err := parseScalar(body, l.no)
			if err != nil {
				return nil, 0, err
			}
			scal = append(scal, v)
			continue
		}
		// `- key: value` opens a mapping whose remaining keys are aligned two
		// columns past the dash. Rather than special-case that layout, the
		// inline pair is re-injected as the item's first line and the ordinary
		// mapping parser does the rest.
		item := []line{{no: l.no, indent: indent + 2, text: body}}
		next := i
		for next < len(lines) && lines[next].indent > indent {
			item = append(item, lines[next])
			next++
		}
		sub, consumed, err := parseMapping(item, 0, indent+2)
		if err != nil {
			return nil, 0, err
		}
		if consumed != len(item) {
			return nil, 0, fmt.Errorf("line %d: 这一项的缩进对不上：%q",
				item[consumed].no, strings.TrimSpace(item[consumed].text))
		}
		maps = append(maps, sub)
		i = next
	}
	if len(maps) > 0 && len(scal) > 0 {
		return nil, 0, fmt.Errorf("line %d: 同一个序列里不能混着放映射（- key: value）和标量（- value）",
			lines[i-1].no)
	}
	if len(maps) > 0 {
		return maps, i, nil
	}
	if scal == nil {
		scal = []any{}
	}
	return scal, i, nil
}

// splitKey splits `key: value` at the first colon that is followed by a space
// or ends the line, outside quotes. rest is "" for `key:`.
func splitKey(text string) (key, rest string, ok bool) {
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if quote == '"' && c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ':':
			if i+1 < len(text) && text[i+1] != ' ' {
				continue // a colon inside a value, e.g. a URL or a time
			}
			key = strings.TrimSpace(text[:i])
			if key == "" {
				return "", "", false
			}
			if strings.ContainsAny(key, "\"'") {
				// Quoted keys are legal YAML; we do not emit them, and
				// accepting them would mean two spellings for one key.
				return "", "", false
			}
			return key, strings.TrimSpace(text[i+1:]), true
		}
	}
	return "", "", false
}

// parseScalar reads one inline value.
func parseScalar(s string, no int) (any, error) {
	if s == "" {
		return nil, fmt.Errorf("line %d: 空值有歧义（null 还是空字符串？）—— 要空字符串写 \"\"", no)
	}
	switch s[0] {
	case '"', '\'':
		v, rest, err := parseQuoted(s)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", no, err)
		}
		if strings.TrimSpace(rest) != "" {
			return nil, fmt.Errorf("line %d: 引号后面还有东西：%q", no, strings.TrimSpace(rest))
		}
		return v, nil
	case '[':
		if !strings.HasSuffix(s, "]") {
			return nil, fmt.Errorf("line %d: flow 序列没有闭合（多行序列请用 `- ` 逐项写）", no)
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []any{}, nil
		}
		var out []any
		for _, part := range splitTop(inner, ',') {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			v, err := parseScalar(part, no)
			if err != nil {
				return nil, err
			}
			if _, isTree := v.(*Tree); isTree {
				return nil, fmt.Errorf("line %d: flow 序列里不支持映射，请用 `- key: value`", no)
			}
			out = append(out, v)
		}
		return out, nil
	case '{':
		if strings.TrimSpace(s) == "{}" {
			return New(), nil
		}
		return nil, fmt.Errorf("line %d: flow 映射（{…}）不支持，请用缩进块", no)
	case '&', '*', '!', '|', '>', '%', '@', '`':
		return nil, fmt.Errorf("line %d: 不支持 %q 开头的标量（锚点 / 别名 / 标签 / 块标量都不在这个子集里）",
			no, string(s[0]))
	}
	if yamlWords[strings.ToLower(s)] {
		switch strings.ToLower(s) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		// Guessing here is the one thing worse than refusing: `notify: yes`
		// would be a *string* and read back as false.
		return nil, fmt.Errorf("line %d: %q 在 YAML 里的含义有歧义 —— 要布尔值写 true/false，"+
			"要这段文字就加引号", no, s)
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	if strings.ContainsAny(s, "\t") {
		return nil, fmt.Errorf("line %d: 标量里有 Tab；要原样保留就加引号", no)
	}
	return s, nil
}

// parseQuoted reads a leading quoted scalar and returns the remainder.
func parseQuoted(s string) (string, string, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == q {
			if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'') // '' is the only escape in single quotes
				i++
				continue
			}
			return b.String(), s[i+1:], nil
		}
		if c != '\\' || q == '\'' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			return "", "", fmt.Errorf("字符串以单个反斜杠结尾")
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case '0':
			b.WriteByte(0)
		case 'u':
			if i+4 >= len(s) {
				return "", "", fmt.Errorf(`\u 转义不完整`)
			}
			cp, err := strconv.ParseUint(s[i+1:i+5], 16, 32)
			if err != nil {
				return "", "", fmt.Errorf(`坏的 \u 转义 %q`, s[i+1:i+5])
			}
			b.WriteRune(rune(cp))
			i += 4
		default:
			return "", "", fmt.Errorf("不支持的转义 \\%c", s[i])
		}
	}
	return "", "", fmt.Errorf("引号没有闭合")
}

// splitTop splits on sep at bracket depth 0 and outside quotes.
func splitTop(s string, sep byte) []string {
	var out []string
	var quote byte
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if quote == '"' && c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
