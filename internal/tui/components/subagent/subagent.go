package subagent

import (
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// Block is the live state of one sub-agent row in the dedicated Subagents
// pane. Terminal blocks are dropped by the pane, so the block only needs the
// fields the compact pane renders.
type Block struct {
	id           string
	role         string
	displayName  string
	task         string
	world        string
	state        State
	startTime    time.Time
	spinnerFrame int
}

type State int

const (
	Active State = iota
	Done
	Error
)

func New(id, role, task string) *Block {
	name := subagent.RoleDisplayName(subagent.SubAgentRole(role))
	label := name
	if name != role {
		label = fmt.Sprintf("%s (%s)", name, role)
	}
	return &Block{
		id:          id,
		role:        role,
		displayName: label,
		task:        task,
		state:       Active,
		startTime:   time.Now(),
	}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (b *Block) ID() string             { return b.id }
func (b *Block) Role() string           { return b.role }
func (b *Block) S() State               { return b.state }
func (b *Block) Task() string           { return b.task }
func (b *Block) World() string          { return b.world }
func (b *Block) DisplayName() string    { return b.displayName }
func (b *Block) Elapsed() time.Duration { return time.Since(b.startTime) }

// SetWorld records the dispatch world label shown beside the role
// ("worktree", "container", or a degraded label). Empty renders nothing.
func (b *Block) SetWorld(w string) { b.world = w }

func (b *Block) AdvanceSpinner() {
	if b.state == Active {
		b.spinnerFrame = (b.spinnerFrame + 1) % len(spinnerFrames)
	}
}

// SpinnerFrame returns the current spinner glyph. Completed and failed
// blocks keep their last frame; renderers that only animate active blocks
// should check S() first.
func (b *Block) SpinnerFrame() string { return spinnerFrames[b.spinnerFrame] }

func (b *Block) Complete() {
	b.state = Done
}

func (b *Block) Fail() {
	b.state = Error
}
