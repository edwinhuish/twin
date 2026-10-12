// Package tomlz implements a deliberately small TOML subset: exactly the shapes
// twin emits and a human is expected to hand-edit.
//
// Supported:
//
//	comments, blank lines, bare keys, [tables], [[arrays of tables]],
//	basic "strings" (with escapes), literal 'strings', booleans, integers,
//	floats, homogeneous arrays, and single-line inline tables {k = v}.
//
// Everything else is a hard error carrying a line number. That is on purpose:
// a silently mis-parsed config is far worse than a refused one — the whole
// point of this tool is to stop identity settings from failing quietly.
//
// Deliberately NOT supported (error out): dotted keys (a.b = v), multi-line
// strings, datetimes, and comments inside arrays.
package tomlz

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tree is an insertion-ordered map. Order matters: we both emit these files
// and round-trip them, and a stable diff is worth more than map iteration.
type Tree struct {
	keys []string
	vals map[string]any
	// implicit marks a table that exists only because something below it does,
	// e.g. `map` in [[map.bind]]. Its header is omitted: TOML does not need a
	// parent to be declared, and emitting `[map]` would be noise in a file a
	// human is meant to read.
	implicit bool
}

// New returns an empty Tree.
func New() *Tree { return &Tree{vals: make(map[string]any)} }

// hasSectionContent reports whether the table would print anything of its own
// (a scalar, or a nested table) — as opposed to holding only arrays of tables.
func (t *Tree) hasSectionContent() bool {
	for _, k := range t.keys {
		if _, isTree := t.vals[k].(*Tree); isTree {
			return true
		}
		if _, isArr := t.vals[k].([]*Tree); !isArr {
			return true
		}
	}
	return false
}

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

// Str returns key as a string ("" when absent or not a string).
func (t *Tree) Str(key string) string {
	v, _ := t.lookup(key)
	s, _ := v.(string)
	return s
}

// Bool returns key as a bool.
func (t *Tree) Bool(key string) bool {
	v, _ := t.lookup(key)
	b, _ := v.(bool)
	return b
}

// Int returns key as an int64.
func (t *Tree) Int(key string) int64 {
	v, _ := t.lookup(key)
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

// StrList returns key as a []string, skipping non-string elements.
func (t *Tree) StrList(key string) []string {
	v, _ := t.lookup(key)
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Tree returns the sub-table at key (an empty Tree when absent).
func (t *Tree) Tree(key string) *Tree {
	v, _ := t.lookup(key)
	switch x := v.(type) {
	case *Tree:
		return x
	case Inline:
		return x.T
	}
	return New()
}

// TableList returns the [[array of tables]] entries at a single key.
func (t *Tree) TableList(key string) []*Tree {
	v, _ := t.lookup(key)
	arr, _ := v.([]*Tree)
	return arr
}

// At walks a dotted path (e.g. "map.bind") and returns the sub-tree there,
// or an empty Tree when the path does not resolve.
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
		case Inline:
			node = x.T
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

// TablesAt walks a dotted path and returns the [[array of tables]] there.
func (t *Tree) TablesAt(path string) []*Tree {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return t.At(path[:i]).TableList(path[i+1:])
	}
	return t.TableList(path)
}

// AddTable appends an entry to the [[array of tables]] at path, creating the
// path if needed, and returns the new (empty) entry.
func (t *Tree) AddTable(path string) *Tree {
	parent, leaf := t, path
	if i := strings.LastIndex(path, "."); i >= 0 {
		parent, leaf = t.ensure(path[:i]), path[i+1:]
	}
	nt := New()
	parent.Set(leaf, append(parent.TableList(leaf), nt))
	return nt
}

func (t *Tree) ensure(path string) *Tree {
	node := t
	for _, p := range strings.Split(path, ".") {
		if v, ok := node.lookup(p); ok {
			if sub, isTree := v.(*Tree); isTree {
				node = sub
				continue
			}
		}
		nt := New()
		nt.implicit = true // created only to hold what comes next
		node.Set(p, nt)
		node = nt
	}
	return node
}

// Ensure walks a dotted path, creating intermediate tables, and returns the
// tree at the end of it. Used when building a document for Encode.
func (t *Tree) Ensure(path string) *Tree { return t.ensure(path) }

// Inline marks a table that must be written as a single-line {k = v} value.
// `env = {}` in instance.toml is the reason this exists.
type Inline struct{ T *Tree }

// NewInline returns an empty inline table.
func NewInline() Inline { return Inline{T: New()} }

// ---------------------------------------------------------------- encoding

// Encode renders t as TOML text. Scalars come first, then sub-tables, then
// arrays of tables — matching the hand-written shape in the design doc.
func Encode(t *Tree) []byte {
	var b strings.Builder
	if err := encodeTree(&b, t, nil); err != nil {
		// Encode only ever fails on types we never construct.
		panic(fmt.Sprintf("tomlz: encode: %v", err))
	}
	return []byte(b.String())
}

func encodeTree(b *strings.Builder, t *Tree, path []string) error {
	for _, k := range t.keys {
		if _, ok := t.vals[k].(*Tree); ok {
			continue
		}
		if _, ok := t.vals[k].([]*Tree); ok {
			continue
		}
		s, err := encodeValue(t.vals[k])
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		fmt.Fprintf(b, "%s = %s\n", k, s)
	}
	for _, k := range t.keys {
		sub, ok := t.vals[k].(*Tree)
		if !ok {
			continue
		}
		np := append(append([]string(nil), path...), k)
		if sub.implicit && !sub.hasSectionContent() {
			// e.g. `map` in [[map.bind]]: the fully qualified header below is
			// enough, and TOML creates the parent implicitly.
			if err := encodeTree(b, sub, np); err != nil {
				return err
			}
			continue
		}
		fmt.Fprintf(b, "\n[%s]\n", strings.Join(np, "."))
		if err := encodeTree(b, sub, np); err != nil {
			return err
		}
	}
	for _, k := range t.keys {
		arr, ok := t.vals[k].([]*Tree)
		if !ok {
			continue
		}
		np := append(append([]string(nil), path...), k)
		for _, sub := range arr {
			fmt.Fprintf(b, "\n[[%s]]\n", strings.Join(np, "."))
			if err := encodeTree(b, sub, np); err != nil {
				return err
			}
		}
	}
	return nil
}

func encodeValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return quoteBasic(x), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case Inline:
		return encodeInline(x.T)
	case []string:
		parts := make([]string, 0, len(x))
		for _, s := range x {
			parts = append(parts, quoteBasic(s))
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			s, err := encodeValue(e)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	return "", fmt.Errorf("unsupported value type %T", v)
}

func encodeInline(t *Tree) (string, error) {
	if t == nil || t.Len() == 0 {
		return "{}", nil
	}
	parts := make([]string, 0, t.Len())
	for _, k := range t.keys {
		v, err := encodeValue(t.vals[k])
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s = %s", k, v))
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}

func quoteBasic(s string) string {
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

// ---------------------------------------------------------------- parsing

// Parse reads a TOML subset document into a Tree.
func Parse(data []byte) (*Tree, error) {
	root := New()
	cur := root
	lines := strings.Split(string(data), "\n")
	for i, raw := range lines {
		lineno := i + 1
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "[["):
			name, err := headerName(line, "[[", "]]")
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineno, err)
			}
			parent, leaf, err := resolve(root, name)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineno, err)
			}
			nt := New()
			parent.Set(leaf, append(parent.TableList(leaf), nt))
			cur = nt
		case strings.HasPrefix(line, "["):
			name, err := headerName(line, "[", "]")
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineno, err)
			}
			sub, err := resolveTable(root, name)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineno, err)
			}
			cur = sub
		default:
			eq := indexUnquoted(line, '=')
			if eq < 0 {
				return nil, fmt.Errorf("line %d: expected `key = value`, got %q", lineno, line)
			}
			key := strings.TrimSpace(line[:eq])
			if key == "" {
				return nil, fmt.Errorf("line %d: empty key", lineno)
			}
			if strings.Contains(key, ".") {
				return nil, fmt.Errorf("line %d: dotted keys are not supported (use [table] sections)", lineno)
			}
			v, err := parseValue(strings.TrimSpace(line[eq+1:]))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineno, err)
			}
			cur.Set(key, v)
		}
	}
	return root, nil
}

func headerName(line, open, close string) (string, error) {
	if !strings.HasSuffix(line, close) {
		return "", fmt.Errorf("unterminated table header %q", line)
	}
	name := strings.TrimSpace(line[len(open) : len(line)-len(close)])
	if name == "" {
		return "", fmt.Errorf("empty table header %q", line)
	}
	for _, part := range strings.Split(name, ".") {
		if strings.TrimSpace(part) == "" {
			return "", fmt.Errorf("bad table header %q", line)
		}
	}
	return name, nil
}

// resolve returns the parent tree of a dotted path plus its leaf name.
func resolve(root *Tree, dotted string) (*Tree, string, error) {
	parts := strings.Split(dotted, ".")
	if len(parts) == 0 {
		return nil, "", fmt.Errorf("empty path")
	}
	node := root
	for _, p := range parts[:len(parts)-1] {
		v, ok := node.lookup(p)
		if !ok {
			nt := New()
			nt.implicit = true
			node.Set(p, nt)
			node = nt
			continue
		}
		switch x := v.(type) {
		case *Tree:
			node = x
		case []*Tree:
			if len(x) == 0 {
				return nil, "", fmt.Errorf("%s: array of tables is empty", p)
			}
			node = x[len(x)-1]
		default:
			return nil, "", fmt.Errorf("%s: already defined as a value", p)
		}
	}
	return node, parts[len(parts)-1], nil
}

func resolveTable(root *Tree, dotted string) (*Tree, error) {
	parent, leaf, err := resolve(root, dotted)
	if err != nil {
		return nil, err
	}
	if v, ok := parent.lookup(leaf); ok {
		if _, isTree := v.(*Tree); !isTree {
			return nil, fmt.Errorf("%s: already defined", dotted)
		}
	}
	nt := New()
	parent.Set(leaf, nt)
	return nt, nil
}

// stripComment removes a trailing # comment, ignoring # inside strings.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == '"' && c == '\\':
			i++ // skip the escaped byte
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && c == '#':
			return s[:i]
		}
	}
	return s
}

// indexUnquoted finds the first occurrence of want outside of strings.
func indexUnquoted(s string, want byte) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == '"' && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && c == want:
			return i
		}
	}
	return -1
}

func parseValue(s string) (any, error) {
	if s == "" {
		return nil, fmt.Errorf("empty value")
	}
	switch s[0] {
	case '"':
		str, rest, err := parseBasicString(s)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rest) != "" {
			return nil, fmt.Errorf("trailing content after string: %q", rest)
		}
		return str, nil
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return nil, fmt.Errorf("unterminated literal string")
		}
		rest := strings.TrimSpace(s[end+2:])
		if rest != "" {
			return nil, fmt.Errorf("trailing content after string: %q", rest)
		}
		return s[1 : end+1], nil
	case '[':
		return parseArray(s)
	case '{':
		return parseInline(s)
	}
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	plain := strings.ReplaceAll(s, "_", "")
	if i, err := strconv.ParseInt(plain, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(plain, 64); err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("unsupported value %q", s)
}

// parseBasicString parses a leading "..." and returns the rest of the input.
func parseBasicString(s string) (string, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("dangling escape in string")
			}
			i++
			switch s[i] {
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'u', 'U':
				n := 4
				if s[i] == 'U' {
					n = 8
				}
				if i+n >= len(s) {
					return "", "", fmt.Errorf("truncated \\%c escape", s[i])
				}
				hex := s[i+1 : i+1+n]
				cp, err := strconv.ParseUint(hex, 16, 32)
				if err != nil {
					return "", "", fmt.Errorf("bad \\%c escape %q", s[i], hex)
				}
				if !utf8.ValidRune(rune(cp)) {
					return "", "", fmt.Errorf("invalid code point U+%X", cp)
				}
				b.WriteRune(rune(cp))
				i += n
			default:
				return "", "", fmt.Errorf("unsupported escape \\%c", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("unterminated string")
}

// splitTop splits on want at nesting depth 0, ignoring strings and brackets.
func splitTop(s string, want byte) []string {
	var out []string
	var quote byte
	depth := 0
	start := 0
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
		case c == want && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func parseArray(s string) (any, error) {
	if !strings.HasSuffix(strings.TrimSpace(s), "]") {
		return nil, fmt.Errorf("unterminated array")
	}
	inner := strings.TrimSpace(s)
	inner = inner[1 : len(inner)-1]
	out := []any{}
	for _, part := range splitTop(inner, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := parseValue(part)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func parseInline(s string) (any, error) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasSuffix(trimmed, "}") {
		return nil, fmt.Errorf("unterminated inline table")
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	t := New()
	if inner == "" {
		return Inline{T: t}, nil
	}
	for _, part := range splitTop(inner, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := indexUnquoted(part, '=')
		if eq < 0 {
			return nil, fmt.Errorf("inline table entry %q is not `k = v`", part)
		}
		key := strings.TrimSpace(part[:eq])
		v, err := parseValue(strings.TrimSpace(part[eq+1:]))
		if err != nil {
			return nil, err
		}
		t.Set(key, v)
	}
	return Inline{T: t}, nil
}
