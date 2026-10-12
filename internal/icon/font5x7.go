package icon

import (
	"fmt"
	"sort"
	"strings"
)

// glyphW / glyphH are the cell size of the built-in bitmap font.
const (
	glyphW = 5
	glyphH = 7
)

// glyphs maps a rune to glyphH rows of glyphW bits ('0' = off, '1' = on).
//
// A hand-drawn 5×7 font is deliberate: pulling in golang.org/x/image/font plus
// a TTF would give us CJK, but it would also make the binary depend on a font
// file being present and add two module dependencies. Badges are almost always
// a digit or a couple of capitals — so we cover exactly that, and refuse the
// rest loudly instead of drawing tofu.
var glyphs = map[rune][]string{
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11111", "00010", "00100", "00010", "00001", "10001", "01110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "01100"},

	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'D': {"11100", "10010", "10001", "10001", "10001", "10010", "11100"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01110"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'I': {"01110", "00100", "00100", "00100", "00100", "00100", "01110"},
	'J': {"00111", "00010", "00010", "00010", "00010", "10010", "01100"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "10001", "11001", "10101", "10011", "10001", "10001"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "11011", "10001"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},

	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
	'+': {"00000", "00100", "00100", "11111", "00100", "00100", "00000"},
	'.': {"00000", "00000", "00000", "00000", "00000", "01100", "01100"},
	'!': {"00100", "00100", "00100", "00100", "00100", "00000", "00100"},
	'?': {"01110", "10001", "00001", "00110", "00100", "00000", "00100"},
	'/': {"00001", "00010", "00010", "00100", "01000", "01000", "10000"},
	' ': {"00000", "00000", "00000", "00000", "00000", "00000", "00000"},
}

// UnsupportedRunes lists the characters of s the built-in font cannot draw.
// The caller must surface this: a badge that silently loses characters is
// exactly the kind of quiet failure this tool exists to prevent.
func UnsupportedRunes(s string) []rune {
	seen := map[rune]bool{}
	var out []rune
	for _, r := range strings.ToUpper(s) {
		if _, ok := glyphs[r]; ok || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// textWidth returns the glyph-space width of s (no letter spacing applied).
func textWidth(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return n*glyphW + (n - 1) // one spare column between glyphs
}

// drawText paints s into m, scaled by an integer factor, with its top-left
// corner at (x, y).
func drawText(m *mask, s string, x, y, scale int) {
	cx := x
	for _, r := range strings.ToUpper(s) {
		rows, ok := glyphs[r]
		if !ok {
			cx += (glyphW + 1) * scale
			continue
		}
		for ry, row := range rows {
			for rx := 0; rx < glyphW; rx++ {
				if row[rx] != '1' {
					continue
				}
				m.fillRect(cx+rx*scale, y+ry*scale, scale, scale)
			}
		}
		cx += (glyphW + 1) * scale
	}
}

// fitScale returns the largest integer scale at which s fits in w×h.
func fitScale(s string, w, h int) int {
	tw := textWidth(s)
	if tw == 0 {
		return 0
	}
	sw, sh := w/tw, h/glyphH
	if sw < sh {
		return sw
	}
	return sh
}

// describeGlyphCoverage is used by the font's own test.
func describeGlyphCoverage() string { return fmt.Sprintf("%d glyphs", len(glyphs)) }
