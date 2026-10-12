package generate

import "github.com/edwinhuish/twin/internal/inst"

// RuleJSON renders the L5 redirection rule for an instance. The schema itself
// lives in inst, next to the path the rule is written to, so the generator and
// the verifier read the same definition.
func RuleJSON(it *inst.Instance, p inst.Paths) ([]byte, error) {
	return inst.NewRedirectRule(it, p.Icon(it.ID), it.AliasPath()).Render()
}
