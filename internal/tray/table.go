// This file is the neutral half of the tray override: the file table the
// shell-side helper reads (ext/extension.js).
//
// Why a second way to say the same thing. The host's own table (see tray.go)
// is one GSettings key, and writing it needs the session bus — the single step
// of a wrap that can honestly end in "not done yet, exit 3". A file write
// always succeeds. So when the helper is in place *and* the shell has loaded
// it, the override travels through this file and the host's table is left
// alone (its row, if an earlier install wrote one, is deleted); when the
// helper is missing, not enabled or blocked, twin falls back to writing that
// table itself, which is what it has always done.
//
// Two properties decide the shape:
//
//   - one file per instance, not one shared table. A shared file would need a
//     read-modify-write, and the host's table already shows where that leads:
//     a merge that can lose somebody else's row if two runs interleave.
//     Per-instance files also make `twin remove` a single unlink.
//   - JSON, because the reader is JavaScript. JSON.parse is in the language,
//     where the TOML subset twin ships (internal/tomlz) would be a second
//     parser in the helper, kept in step by hand. rules.d made the same call.

package tray

import (
	"encoding/json"
	"os"

	"github.com/edwinhuish/twin/internal/inst"
	"github.com/edwinhuish/twin/internal/sysx"
)

// Override is one instance's row in the neutral table.
//
// ID is the StatusNotifierItem id, because that is the only handle the shell
// has on a tray item. For an instance that renamed the program it is the
// instance id itself (the alias is argv[0], and the id comes from there); for
// level=none nothing was renamed, so the program announced its own id and the
// row is a guess — the same caveat the host's table carries, written into the
// file so that whoever opens it sees it without the terminal.
type Override struct {
	Comment string `json:"_comment,omitempty"`
	ID      string `json:"id"`
	Icon    string `json:"icon"`
}

// NewOverride builds the row for an instance. The icon path is passed in
// rather than derived: Paths stays the only place that decides where artefacts
// live.
func NewOverride(id, icon string) *Override {
	return &Override{
		Comment: "twin 生成 —— 不要手改；改 profile.yml 后跑 `twin reinstall " + id + "`。",
		ID:      id,
		Icon:    icon,
	}
}

// Render serialises the row deterministically, so an unchanged install
// reproduces it byte for byte (and therefore does not touch the file).
func (o *Override) Render() ([]byte, error) {
	out, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ParseOverride decodes a row.
func ParseOverride(b []byte) (*Override, error) {
	var o Override
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// TableOutcome says whether a write touched the file.
type TableOutcome struct {
	// Written is true when the file was written or rewritten.
	Written bool
}

// EnsureOverride puts this instance's row in the neutral table, and reports
// whether anything changed.
//
// The comparison before writing is not an optimisation: the helper watches
// this directory, so rewriting identical bytes hands it a reload event for
// nothing — and the shell reloads the whole table on every event.
func EnsureOverride(p inst.Paths, o *Override) (TableOutcome, error) {
	body, err := o.Render()
	if err != nil {
		return TableOutcome{}, err
	}
	path := p.TrayTable(o.ID)
	if have, err := sysx.ReadFile(path); err == nil && have == string(body) {
		return TableOutcome{}, nil
	}
	if err := sysx.EnsureDir(p.TrayTableDir()); err != nil {
		return TableOutcome{}, err
	}
	if err := sysx.WriteFileAtomic(path, body, 0o644); err != nil {
		return TableOutcome{}, err
	}
	return TableOutcome{Written: true}, nil
}

// ReadOverride reads an instance's row. A missing file is (nil, false, nil):
// "this instance has no row" is a state, not an error — that is what the
// verifier and the rehearsal both need to tell apart from "the file is there
// and unreadable".
func ReadOverride(p inst.Paths, id string) (*Override, bool, error) {
	b, err := sysx.ReadFile(p.TrayTable(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	o, err := ParseOverride([]byte(b))
	if err != nil {
		return nil, false, err
	}
	return o, true, nil
}

// RemoveOverride deletes an instance's row. A missing file is not worth
// failing an unwrap over: the artefact the user asked to be rid of is gone
// either way.
func RemoveOverride(p inst.Paths, id string) (bool, error) {
	path := p.TrayTable(id)
	if !sysx.Exists(path) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
