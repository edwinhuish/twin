package inst

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RedirectRule is one L5 redirection rule. It is written to
// Paths.RuleFile(id) and read by the shell extension that rewrites
// Shell.WindowTracker.get_window_app() (设计 §5.4).
//
// The schema lives here, next to RuleFile, so the generator that writes it and
// the verifier that re-reads it cannot drift apart — a rule that says one thing
// and is validated as another is exactly the kind of silent failure this tool
// exists to prevent.
type RedirectRule struct {
	Comment  string        `json:"_comment,omitempty"`
	ID       string        `json:"id"`
	Enabled  bool          `json:"enabled"`
	Target   string        `json:"target"`
	TrayIcon string        `json:"trayIcon,omitempty"`
	Match    RedirectMatch `json:"match"`
}

// RedirectMatch is the process evidence a rule matches on.
//
// It is deliberately *process* evidence, never the window's app_id: the whole
// reason an instance needs L5 is that the program hard-codes its app_id, so
// that property carries nothing distinguishing. Both markers below are measured
// facts about an alias-level instance (2026-10-12):
//
//   - the rename leaves `<targetdir>/<id>` in the process's mountinfo, beside
//     every other bubblewrap bind, and it follows the process across re-exec and
//     reparenting;
//   - argv[0] of the running program *is* that same path, so it is in cmdline
//     too.
//
// Neither marker carries a private data-directory name — the older hand-written
// rule keyed on `.xwechat2`, which is the fingerprint 设计 §5.4 warns against.
type RedirectMatch struct {
	Mountinfo []string `json:"mountinfo,omitempty"`
	Cmdline   []string `json:"cmdline,omitempty"`
}

// NewRedirectRule builds the rule for an instance. iconPath and aliasPath are
// passed in rather than derived so the caller (which knows Paths) stays the
// only place that decides where artefacts live.
func NewRedirectRule(it *Instance, iconPath, aliasPath string) *RedirectRule {
	return &RedirectRule{
		Comment:  "twin 生成 —— 不要手改；改 instance.toml 后跑 `twin regen " + it.ID + "`。",
		ID:       it.ID,
		Enabled:  true,
		Target:   it.ID + ".desktop",
		TrayIcon: iconPath,
		Match: RedirectMatch{
			Mountinfo: []string{
				aliasPath,
				// The private state root without the home prefix, so the rule
				// survives a moved home. Present whenever the instance has at
				// least one bind.
				"/.local/state/twin/" + it.ID,
			},
			Cmdline: []string{aliasPath},
		},
	}
}

// Render serialises the rule deterministically, so `regen` reproduces it byte
// for byte.
func (r *RedirectRule) Render() ([]byte, error) {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ParseRedirectRule decodes a rule file.
func ParseRedirectRule(b []byte) (*RedirectRule, error) {
	var r RedirectRule
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate checks the rule actually names this instance and carries evidence.
// A rule with no match markers is silently ignored by the extension, so callers
// must not treat "the file exists" as success.
func (r *RedirectRule) Validate(id string) error {
	if r.ID != id {
		return fmt.Errorf("规则里的 id 是 %q，本实例是 %q", r.ID, id)
	}
	if want := id + ".desktop"; r.Target != want {
		return fmt.Errorf("规则的 target 是 %q，应当是 %q", r.Target, want)
	}
	if len(r.Match.Mountinfo) == 0 && len(r.Match.Cmdline) == 0 {
		return fmt.Errorf("规则没有任何 match 条件，扩展会直接忽略它")
	}
	if !r.Enabled {
		return fmt.Errorf("规则 enabled=false")
	}
	return nil
}

// Carries reports whether the rule's evidence mentions the given path. The
// verifier uses it to catch a stale rule left behind by an earlier alias path.
func (r *RedirectRule) Carries(path string) bool {
	for _, m := range append(append([]string{}, r.Match.Mountinfo...), r.Match.Cmdline...) {
		if m == path || strings.Contains(m, path) || strings.Contains(path, m) {
			return true
		}
	}
	return false
}
