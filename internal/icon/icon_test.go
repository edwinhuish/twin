package icon

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// solidPNG writes an opaque red square. Red is what makes the assertions below
// readable: any pixel the badge touches stops being red.
func solidPNG(t *testing.T, path string, size int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{R: 0xFF, A: 0xFF}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func compose(t *testing.T, src, badge, style string) *image.RGBA {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.png")
	if _, err := Compose(Spec{Source: src, Badge: badge, Style: style}, out, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return toRGBA(img)
}

// The badge marks the icon's bottom-right corner, so it sits flush with the
// icon's right and bottom edges. It used to be inset 4% to stay clear of a
// rounded icon corner, which read as a badge floating inside the artwork — and
// the badge is the one part of a 16px tray icon that still has to be countable.
func TestBadgeSitsFlushWithTheIconEdges(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.png")
	solidPNG(t, src, 64)

	s := DefaultSize
	for _, tc := range []struct {
		style string
		badge string
		edge  int // 角标与右/下边缘相接的那一格（圆形只有切点，方块有整段，取中点）
	}{
		{StyleCorner, "2", s - int(0.46*float64(s))/2},
		{StyleNumber, "2", s - int(0.46*float64(s))/2},
		{StyleDot, "", s - int(0.30*float64(s))/2},
	} {
		got := compose(t, src, tc.badge, tc.style)
		for _, p := range []image.Point{{s - 1, tc.edge}, {tc.edge, s - 1}} {
			if c := got.RGBAAt(p.X, p.Y); c.R > 0x80 {
				t.Errorf("%s：%v 还是底图的颜色（%v），角标没贴到边缘", tc.style, p, c)
			}
		}
		// 反向判据：同一条边上离角标很远的那一段仍然是底图 —— 上面测到的不是"整条边被涂了"。
		if c := got.RGBAAt(s-1, 0); c.R < 0x80 {
			t.Errorf("%s：画布右边缘整条都被涂了（%v）", tc.style, c)
		}
	}
}
