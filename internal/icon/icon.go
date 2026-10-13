// Package icon resolves a program's icon, composites a badge onto it and
// writes a 256×256 RGBA PNG.
//
// Pure Go for PNG/JPEG (stdlib only); SVG and the long tail (xpm, ico, …) are
// delegated to an external rasteriser. Keeping the common path dependency-free
// matters: twin must build offline and be packageable as a deb.
package icon

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/sysx"
)

// DefaultSize is the edge length of the generated PNG.
const DefaultSize = 256

// Styles for the badge.
const (
	StyleCorner = "corner" // rounded square, bottom right
	StyleNumber = "number" // circle, bottom right
	StyleDot    = "dot"    // small dot, no text
)

// Spec describes what to draw.
type Spec struct {
	Source string // absolute path or icon-theme name; may be empty
	Name   string // fallback: first letter of this is used when all else fails
	Badge  string
	Style  string
	Home   string // used to search theme directories
}

// Report records what actually happened — never leave the caller guessing.
type Report struct {
	SourceFile string
	Rasterized bool // an external rasteriser was involved
	SrcW       int
	SrcH       int
	OutPath    string
	Size       int
	Badge      string
	Style      string
	Notes      []string
}

// Validate rejects badge text the built-in font cannot draw, before we write
// anything. Style "dot" needs no text at all.
func Validate(spec Spec) error {
	if spec.Style == StyleDot {
		return nil
	}
	if bad := UnsupportedRunes(spec.Badge); len(bad) > 0 {
		return fmt.Errorf("badge %q contains characters the built-in 5×7 font cannot draw: %s\n"+
			"       use digits / A–Z (e.g. --badge 2), or switch to a geometric badge with --badge-style dot",
			spec.Badge, quoteRunes(bad))
	}
	return nil
}

// Compose builds the badge icon and writes it to outPath.
func Compose(spec Spec, outPath string, size int) (*Report, error) {
	if size <= 0 {
		size = DefaultSize
	}
	if err := Validate(spec); err != nil {
		return nil, err
	}
	rep := &Report{OutPath: outPath, Size: size, Badge: spec.Badge, Style: spec.Style}

	srcPath, tmp, notes, err := resolveAndLoad(spec)
	if err != nil {
		return nil, err
	}
	rep.Notes = append(rep.Notes, notes...)
	if tmp != "" {
		defer os.Remove(tmp)
		rep.Rasterized = true
	}

	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var src image.Image
	switch strings.ToLower(filepath.Ext(srcPath)) {
	case ".jpg", ".jpeg":
		src, err = jpeg.Decode(f)
	default:
		src, err = png.Decode(f)
	}
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", srcPath, err)
	}
	rep.SourceFile = srcPath
	sb := src.Bounds()
	rep.SrcW, rep.SrcH = sb.Dx(), sb.Dy()

	canvas := scaleTo(src, size, size)
	if spec.Style != StyleDot {
		drawBadge(canvas, spec.Badge, spec.Style)
	} else {
		drawDot(canvas)
	}

	if err := writePNG(canvas, outPath); err != nil {
		return nil, err
	}
	return rep, nil
}

// resolveAndLoad finds a usable file. It returns a temp PNG path when the
// source had to be rasterised externally.
func resolveAndLoad(spec Spec) (path, tmp string, notes []string, err error) {
	candidate := strings.TrimSpace(spec.Source)
	if candidate == "" || !filepath.IsAbs(candidate) {
		if found, note := findInTheme(spec.Home, candidate); found != "" {
			if note != "" {
				notes = append(notes, note)
			}
			candidate = found
		} else {
			candidate = ""
		}
	}
	if candidate == "" {
		return "", "", notes, fmt.Errorf("no usable icon source (looked for %q in the icon theme)", spec.Source)
	}

	kind := sniff(candidate)
	switch kind {
	case "png", "jpeg":
		return candidate, "", notes, nil
	}

	tmpFile := filepath.Join(os.TempDir(), "twin-raster-"+filepath.Base(candidate)+".png")
	if err := rasterizeExternal(candidate, tmpFile, 1024); err != nil {
		return "", "", notes, fmt.Errorf("could not rasterise %s (%s): %w", candidate, kind, err)
	}
	notes = append(notes, fmt.Sprintf("rasterised %s via an external tool", filepath.Base(candidate)))
	return tmpFile, tmpFile, notes, nil
}

// ResolveSource turns an icon reference (absolute path or theme name) into a
// file on disk. It returns a note when the reference was resolvable only
// indirectly, and an empty path when nothing was found.
func ResolveSource(home, spec string) (string, string) {
	if spec == "" {
		return "", ""
	}
	if filepath.IsAbs(spec) {
		if sysx.Exists(spec) {
			return spec, ""
		}
		return "", ""
	}
	found, note := findInTheme(home, spec)
	return found, note
}

// findInTheme looks an icon name up in the usual places.
func findInTheme(home, name string) (string, string) {
	if name == "" {
		return "", ""
	}
	if filepath.Ext(name) != "" && sysx.Exists(name) {
		return name, ""
	}
	sizes := []string{"512x512", "256x256", "128x128", "96x96", "64x64", "48x48", "scalable"}
	roots := []string{
		filepath.Join(home, ".local/share/icons"),
		"/usr/local/share/icons",
		"/usr/share/icons",
	}
	for _, root := range roots {
		for _, size := range sizes {
			for _, ext := range []string{".png", ".svg"} {
				p := filepath.Join(root, "hicolor", size, "apps", name+ext)
				if sysx.Exists(p) {
					return p, ""
				}
			}
		}
		// Flat layouts (~/.local/share/icons/foo.png) are common too.
		for _, ext := range []string{".png", ".svg"} {
			p := filepath.Join(root, name+ext)
			if sysx.Exists(p) {
				return p, ""
			}
		}
	}
	for _, ext := range []string{".png", ".svg", ".xpm"} {
		p := filepath.Join("/usr/share/pixmaps", name+ext)
		if sysx.Exists(p) {
			return p, ""
		}
	}
	_ = sizes
	return "", ""
}

// sniff classifies a file by magic bytes first, extension second.
func sniff(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]
	switch {
	case len(head) > 8 && string(head[1:4]) == "PNG":
		return "png"
	case len(head) > 3 && head[0] == 0xFF && head[1] == 0xD8:
		return "jpeg"
	}
	text := strings.ToLower(string(head))
	if strings.Contains(text, "<svg") || strings.Contains(text, "<?xml") {
		return "svg"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg":
		return "svg"
	case ".png":
		return "png"
	case ".jpg", ".jpeg":
		return "jpeg"
	}
	return "other"
}

// gdkPixbufScript uses GdkPixbuf, which is the one loader on this machine that
// understands SVG and the long tail of legacy formats.
const gdkPixbufScript = `import sys
import gi
gi.require_version("GdkPixbuf", "2.0")
from gi.repository import GdkPixbuf
src, dst, size = sys.argv[1], sys.argv[2], int(sys.argv[3])
pb = GdkPixbuf.Pixbuf.new_from_file_at_size(src, size, size)
pb.savev(dst, "png", [], [])
`

// rasterizeExternal converts any GdkPixbuf-readable file into a PNG.
func rasterizeExternal(src, dst string, size int) error {
	if py := sysx.PythonWithGI(); py != "" {
		res, err := sysx.Run(py, "-c", gdkPixbufScript, src, dst, fmt.Sprint(size))
		if err == nil && res.Code == 0 && sysx.Exists(dst) {
			return nil
		}
	}
	if rs := sysx.Which("rsvg-convert"); rs != "" {
		res, err := sysx.Run(rs, "-w", fmt.Sprint(size), "-h", fmt.Sprint(size), "-o", dst, src)
		if err == nil && res.Code == 0 && sysx.Exists(dst) {
			return nil
		}
	}
	if cv := sysx.FirstInstalled("magick", "convert"); cv != "" {
		res, err := sysx.Run(cv, src, "-resize", fmt.Sprintf("%dx%d", size, size), dst)
		if err == nil && res.Code == 0 && sysx.Exists(dst) {
			return nil
		}
	}
	return fmt.Errorf("no working rasteriser (tried python3-gi GdkPixbuf, rsvg-convert, ImageMagick)")
}

// ------------------------------------------------------------------ drawing

// writePNG says nothing on success. The path it wrote is already on the run's
// own 产物 list, so a note repeating it ("wrote 256x256 PNG to …") only put an
// English sentence between the paths and the next step. The part a caller
// cannot see — that an SVG had to go through an external rasteriser — is
// reported where it is decided, in resolveAndLoad.
func writePNG(img *image.RGBA, path string) error {
	// Convert to non-premultiplied so the encoder writes the colours we intend.
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)

	var buf bytes.Buffer
	enc := &png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, out); err != nil {
		return fmt.Errorf("encode png: %w", err)
	}
	if err := sysx.WriteFileAtomic(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return nil
}

// toRGBA converts any image to premultiplied RGBA, which is the space our
// blending maths assumes.
func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

func scaleTo(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return toRGBA(src)
	}
	if b.Dx() >= w && b.Dy() >= h {
		return boxDown(toRGBA(src), w, h)
	}
	return bilinear(toRGBA(src), w, h)
}

// boxDown area-averages down to w×h. Averaging premultiplied samples keeps
// edges from developing dark fringes.
func boxDown(src *image.RGBA, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	for y := 0; y < h; y++ {
		y0 := y * sh / h
		y1 := (y + 1) * sh / h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0 := x * sw / w
			x1 := (x + 1) * sw / w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb, sa, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					c := src.RGBAAt(sx, sy)
					sr += uint64(c.R)
					sg += uint64(c.G)
					sb += uint64(c.B)
					sa += uint64(c.A)
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				uint8(sr / n), uint8(sg / n), uint8(sb / n), uint8(sa / n),
			})
		}
	}
	return dst
}

// bilinear upsamples. Nearest-neighbour would look blocky on a 48px source
// blown up to 256px.
func bilinear(src *image.RGBA, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)*float64(sh)/float64(h) - 0.5
		y0 := int(fy)
		if fy < 0 {
			y0 = 0
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		dy := fy - float64(y0)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(w) - 0.5
			x0 := int(fx)
			if fx < 0 {
				x0 = 0
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			dx := fx - float64(x0)
			c00 := src.RGBAAt(x0, y0)
			c10 := src.RGBAAt(x1, y0)
			c01 := src.RGBAAt(x0, y1)
			c11 := src.RGBAAt(x1, y1)
			mix := func(a, b, c, d uint8) uint8 {
				top := float64(a)*(1-dx) + float64(b)*dx
				bot := float64(c)*(1-dx) + float64(d)*dx
				return uint8(top*(1-dy) + bot*dy + 0.5)
			}
			dst.SetRGBA(x, y, color.RGBA{
				mix(c00.R, c10.R, c01.R, c11.R),
				mix(c00.G, c10.G, c01.G, c11.G),
				mix(c00.B, c10.B, c01.B, c11.B),
				mix(c00.A, c10.A, c01.A, c11.A),
			})
		}
	}
	return dst
}

// mask is an 8-bit coverage buffer used to composite shapes with anti-aliasing.
type mask struct {
	w, h int
	cov  []uint8
}

func newMask(w, h int) *mask { return &mask{w: w, h: h, cov: make([]uint8, w*h)} }

func (m *mask) set(x, y int, v uint8) {
	if x < 0 || y < 0 || x >= m.w || y >= m.h {
		return
	}
	i := y*m.w + x
	if v > m.cov[i] {
		m.cov[i] = v
	}
}

func (m *mask) max(x, y int) uint8 {
	if x < 0 || y < 0 || x >= m.w || y >= m.h {
		return 0
	}
	return m.cov[y*m.w+x]
}

func (m *mask) fillRect(x, y, w, h int) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			m.set(xx, yy, 255)
		}
	}
}

// fillCircle rasterises a disc with analytic coverage.
func (m *mask) fillCircle(cx, cy, r float64) {
	x0 := int(cx - r - 1)
	x1 := int(cx + r + 1)
	y0 := int(cy - r - 1)
	y1 := int(cy + r + 1)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d := dx*dx + dy*dy
			if d > (r+1)*(r+1) {
				continue
			}
			cov := clamp01(r + 0.5 - math.Sqrt(d))
			m.set(x, y, uint8(cov*255+0.5))
		}
	}
}

// fillRoundRect rasterises a rounded rectangle from its signed distance field.
func (m *mask) fillRoundRect(x0, y0, w, h int, r float64) {
	hw, hh := float64(w)/2, float64(h)/2
	cx := float64(x0) + hw
	cy := float64(y0) + hh
	ix0 := int(float64(x0) - 1)
	ix1 := int(float64(x0+w) + 1)
	iy0 := int(float64(y0) - 1)
	iy1 := int(float64(y0+h) + 1)
	for y := iy0; y <= iy1; y++ {
		for x := ix0; x <= ix1; x++ {
			px := abs(float64(x)+0.5-cx) - (hw - r)
			py := abs(float64(y)+0.5-cy) - (hh - r)
			if px < 0 {
				px = 0
			}
			if py < 0 {
				py = 0
			}
			d := math.Sqrt(px*px+py*py) - r
			m.set(x, y, uint8(clamp01(0.5-d)*255+0.5))
		}
	}
}

// paint blends a solid colour over dst using the mask as coverage.
func (m *mask) paint(dst *image.RGBA, c color.RGBA) {
	for y := 0; y < m.h; y++ {
		for x := 0; x < m.w; x++ {
			cov := m.max(x, y)
			if cov == 0 {
				continue
			}
			blendPremul(dst, x, y, c, cov)
		}
	}
}

func blendPremul(dst *image.RGBA, x, y int, c color.RGBA, cov uint8) {
	if !(image.Point{x, y}).In(dst.Bounds()) {
		return
	}
	a := uint32(cov) * uint32(c.A) / 255
	ia := 255 - a
	old := dst.RGBAAt(x, y)
	dst.SetRGBA(x, y, color.RGBA{
		R: uint8((uint32(c.R)*a + uint32(old.R)*ia) / 255),
		G: uint8((uint32(c.G)*a + uint32(old.G)*ia) / 255),
		B: uint8((uint32(c.B)*a + uint32(old.B)*ia) / 255),
		A: uint8(minInt(255, int(uint32(old.A)+a))),
	})
}

var (
	badgeFill = color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	badgeRing = color.RGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xCC}
	badgeInk  = color.RGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xFF}
)

// drawBadge paints the badge in the bottom-right corner. Nothing touches the
// canvas edge: a badge that gets clipped reads as a rendering bug.
func drawBadge(img *image.RGBA, text, style string) {
	s := img.Bounds().Dx()
	margin := int(0.04 * float64(s))
	box := int(0.46 * float64(s))
	x1 := s - margin
	y1 := s - margin
	x0 := x1 - box
	y0 := y1 - box

	const ring = 2.0
	outer := newMask(s, s)
	inner := newMask(s, s)
	if style == StyleCorner {
		rOuter := 0.20 * float64(box)
		outer.fillRoundRect(x0, y0, box, box, rOuter)
		inner.fillRoundRect(x0+int(ring*2), y0+int(ring*2),
			box-int(ring*4), box-int(ring*4), rOuter-ring*2)
	} else {
		outer.fillCircle(float64(x0)+float64(box)/2, float64(y0)+float64(box)/2, float64(box)/2)
		inner.fillCircle(float64(x0)+float64(box)/2, float64(y0)+float64(box)/2, float64(box)/2-ring*2)
	}
	outer.paint(img, badgeRing)
	inner.paint(img, badgeFill)

	// Text: fit inside the inner disc/square with a little padding.
	pad := int(0.26 * float64(box))
	tw, th := box-2*pad, box-2*pad
	scale := fitScale(text, tw, th)
	if scale < 1 {
		return
	}
	tm := newMask(s, s)
	gw := textWidth(text)
	tx := x0 + (box-gw*scale)/2
	ty := y0 + (box-glyphH*scale)/2
	drawText(tm, text, tx, ty, scale)
	tm.paint(img, badgeInk)
}

// drawDot paints the geometric-only badge.
func drawDot(img *image.RGBA) {
	s := img.Bounds().Dx()
	margin := 0.045 * float64(s)
	d := 0.30 * float64(s)
	cx := float64(s) - margin - d/2
	cy := float64(s) - margin - d/2

	ring := newMask(s, s)
	ring.fillCircle(cx, cy, d/2)
	ring.paint(img, badgeRing)
	inner := newMask(s, s)
	inner.fillCircle(cx, cy, d/2-2.0)
	inner.paint(img, badgeFill)
}

func quoteRunes(rs []rune) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, fmt.Sprintf("%q", string(r)))
	}
	return strings.Join(parts, ", ")
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
