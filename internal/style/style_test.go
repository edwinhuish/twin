package style

import "testing"

// envOf turns a map into the getenv function the policy takes.
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// The interesting cases are the ones where the terminal and the environment
// disagree. Getting NO_COLOR wrong is the one a scripted user notices, because
// they asked for it explicitly and there is no second chance.
func TestAllowed(t *testing.T) {
	cases := []struct {
		name string
		tty  bool
		env  map[string]string
		want bool
	}{
		{"终端，无意见", true, nil, true},
		{"重定向到文件", false, nil, false},
		{"NO_COLOR 关掉终端着色", true, map[string]string{"NO_COLOR": "1"}, false},
		{"NO_COLOR 连强制也要压住", true, map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"}, false},
		{"CLICOLOR_FORCE 救回管道", false, map[string]string{"CLICOLOR_FORCE": "1"}, true},
		{"CLICOLOR_FORCE=0 不算强制", false, map[string]string{"CLICOLOR_FORCE": "0"}, false},
		{"TERM=dumb 是终端但不懂颜色", true, map[string]string{"TERM": "dumb"}, false},
	}
	for _, c := range cases {
		if got := allowed(c.tty, envOf(c.env)); got != c.want {
			t.Errorf("%s：allowed = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// A disabled style must be a no-op, not a different rendering: the plain output
// is what lands in logs and in test expectations.
func TestDisabledStyleEmitsNothing(t *testing.T) {
	off := Style{}
	for _, got := range []string{
		off.Bold("x"), off.Dim("x"), off.Green("x"), off.Yellow("x"), off.Red("x"), off.Cyan("x"),
		off.Mark(LevelOK), off.Mark(LevelWarn), off.Mark(LevelFail), off.Mark(LevelSkip),
	} {
		if got != "x" && got != Glyph(LevelOK) && got != Glyph(LevelWarn) &&
			got != Glyph(LevelFail) && got != Glyph(LevelSkip) {
			t.Errorf("关掉颜色后仍带转义序列：%q", got)
		}
	}
}

// The glyph is the fallback channel: when the escapes are stripped, this is all
// the reader has left to tell a pass from a failure.
func TestGlyphsAreDistinct(t *testing.T) {
	seen := map[string]Level{}
	for _, l := range []Level{LevelOK, LevelWarn, LevelFail, LevelSkip} {
		g := Glyph(l)
		if g == "" {
			t.Fatalf("level %d 没有字形", l)
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("level %d 与 %d 共用一个字形 %q", l, prev, g)
		}
		seen[g] = l
	}
}
