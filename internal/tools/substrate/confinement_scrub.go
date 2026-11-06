package substrate

import (
	"os"
	"path/filepath"
)

// ConfinementScratchName is the in-worktree directory the confinement
// redirect points at. The value lives here so the tools layer (which creates
// the scratch) and the dispatch lane (which scrubs it before sealing a
// proposal) share one contract.
const ConfinementScratchName = ".yaah-scratch"

// ScrubConfinementScratch removes the redirect scratch so the sealed proposal
// excludes housekeeping writes. Best-effort: a leftover scratch shows up in the
// delta, which is cosmetic, not a hazard.
func ScrubConfinementScratch(workdir string) {
	if workdir == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(workdir, ConfinementScratchName))
}
