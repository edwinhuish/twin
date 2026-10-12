package sandbox

import (
	"fmt"
	"sort"
	"strings"
)

// knownOptions is the membership set for `--` passthrough lint. It carries
// names only — no arities — and that is deliberate: the moment twin knows
// how many values an option takes, someone will start rewriting argv, and the
// porter becomes a translator with a table that rots on every bwrap release
// (设计 §2.6). A name missing from here produces a warning, never an error, so
// staying one bwrap version behind costs a spurious line, not a broken run.
//
// Source: `bwrap --help` on Ubuntu 26.04 (bubblewrap 0.11).
var knownOptions = map[string]bool{
	"--help": true, "--version": true,
	"--args": true, "--argv0": true, "--level-prefix": true,
	"--unshare-all": true, "--share-net": true,
	"--unshare-user": true, "--unshare-user-try": true,
	"--unshare-ipc": true, "--unshare-pid": true, "--unshare-net": true,
	"--unshare-uts": true, "--unshare-cgroup": true, "--unshare-cgroup-try": true,
	"--userns": true, "--userns2": true,
	"--disable-userns": true, "--assert-userns-disabled": true,
	"--pidns": true, "--uid": true, "--gid": true, "--hostname": true,
	"--chdir": true, "--clearenv": true, "--setenv": true, "--unsetenv": true,
	"--lock-file": true, "--sync-fd": true,
	"--bind": true, "--bind-try": true, "--dev-bind": true, "--dev-bind-try": true,
	"--ro-bind": true, "--ro-bind-try": true,
	"--bind-fd": true, "--ro-bind-fd": true, "--remount-ro": true,
	"--overlay-src": true, "--overlay": true, "--tmp-overlay": true, "--ro-overlay": true,
	"--exec-label": true, "--file-label": true,
	"--proc": true, "--dev": true, "--tmpfs": true, "--mqueue": true, "--dir": true,
	"--file": true, "--bind-data": true, "--ro-bind-data": true, "--symlink": true,
	"--seccomp": true, "--add-seccomp-fd": true,
	"--block-fd": true, "--userns-block-fd": true,
	"--info-fd": true, "--json-status-fd": true,
	"--new-session": true, "--die-with-parent": true, "--as-pid-1": true,
	"--cap-add": true, "--cap-drop": true,
	"--perms": true, "--size": true, "--chmod": true,
}

// bindish are the options whose *destination* twin would normally pre-create
// with mkdir. In the passthrough region nothing is pre-created, so a directory
// destination under the read-only `/` fails at launch ("Can't create file:
// Read-only file system") — while a file destination quietly succeeds, which
// makes the channel look more reliable than it is (设计 §2.5).
var bindish = map[string]bool{
	"--bind": true, "--bind-try": true, "--ro-bind": true, "--ro-bind-try": true,
	"--dev-bind": true, "--dev-bind-try": true,
	"--bind-fd": true, "--ro-bind-fd": true,
	"--bind-data": true, "--ro-bind-data": true,
	"--tmpfs": true, "--mqueue": true, "--overlay": true, "--tmp-overlay": true,
}

// LintBwrap returns human-readable warnings about a passthrough token list. It
// is read-only by construction: it looks at each token's option name and never
// touches the slice.
func LintBwrap(tokens []string) []string {
	var warns []string
	var seenBind []string
	for _, tok := range tokens {
		name := tok
		if idx := strings.IndexByte(tok, '='); idx >= 0 {
			name = tok[:idx]
		}
		if !strings.HasPrefix(name, "-") {
			continue // a value; without arities we cannot tell whose
		}
		if !knownOptions[name] {
			msg := fmt.Sprintf("%s 不是已知的 bwrap 选项", name)
			if hint := nearestOption(name); hint != "" {
				msg += "（最接近的是 " + hint + "）"
			} else {
				msg += "（若它是某个选项的值，忽略此条）"
			}
			warns = append(warns, msg)
		}
		if bindish[name] && !contains(seenBind, name) {
			seenBind = append(seenBind, name)
		}
	}
	if len(seenBind) > 0 {
		sort.Strings(seenBind)
		warns = append(warns, strings.Join(seenBind, " ")+
			" 出现在透传区：两侧目录 twin 不会预建，目录目标会因只读的 / 而失败"+
			"（文件目标反而会成功，所以别被假象骗了）—— 数据路径建议写进 rules.d 或实例快照的 bind")
	}
	return warns
}

// nearestOption suggests a known option within edit distance 2, which catches
// the realistic typo (`--unshere-pid` → `--unshare-pid`) without inventing
// matches for genuinely custom-looking tokens.
func nearestOption(name string) string {
	best, bestD := "", 3
	for k := range knownOptions {
		if d := editDistance(name, k); d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
