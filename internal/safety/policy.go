package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mintoleda/talos/internal/protocol"
)

type Mode int

const (
	ModeAuto Mode = iota
	ModeAsk
	ModePanic
)

func ParseMode(s string) Mode {
	switch strings.ToLower(s) {
	case "ask":
		return ModeAsk
	case "panic":
		return ModePanic
	default:
		return ModeAuto
	}
}

func (m Mode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModeAsk:
		return "ask"
	case ModePanic:
		return "panic"
	default:
		return "auto"
	}
}

// NextMode returns the next mode in the auto → ask → panic cycle.
func NextMode(m Mode) Mode {
	switch m {
	case ModeAuto:
		return ModeAsk
	case ModeAsk:
		return ModePanic
	default:
		return ModeAuto
	}
}

type Policy struct {
	mu          sync.Mutex
	mode        Mode
	savedMode   Mode // last non-panic mode, restored on toggle-off
	workDir     string
	classifier  *Classifier
	interactive bool
	Envelope    interface{}
}

func NewPolicy(mode Mode, workDir string, classifier *Classifier, interactive bool) *Policy {
	return &Policy{mode: mode, savedMode: mode, workDir: workDir, classifier: classifier, interactive: interactive}
}

func (p *Policy) Check(tu protocol.ToolUse) (Decision, string) {
	p.mu.Lock()
	mode := p.mode
	p.mu.Unlock()

	switch tu.Name {
	case "bash", "bash_background":
		cmd, _ := tu.Args["command"].(string)
		d, reason := p.classifier.Classify(cmd)
		return p.resolve(d, reason, mode)
	case "write", "edit":
		path, _ := tu.Args["path"].(string)
		if !p.withinWorkDir(path) {
			return Block, fmt.Sprintf("path %s is outside the working directory", path)
		}
		return p.resolve(Allow, "", mode)
	default:
		return p.resolve(Allow, "", mode)
	}
}

// SetMode changes the permission mode atomically.
func (p *Policy) SetMode(m Mode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = m
}

// Mode returns the current permission mode.
func (p *Policy) Mode() Mode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode
}

// TogglePanic switches to panic if currently in a non-panic mode, or restores
// the previous mode if currently in panic. Returns the resulting mode.
func (p *Policy) TogglePanic() Mode {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == ModePanic {
		p.mode = p.savedMode
	} else {
		p.savedMode = p.mode
		p.mode = ModePanic
	}
	return p.mode
}

// resolve applies the permission mode and interactivity to a base decision from
// the classifier/path check. A classifier Block is catastrophic and always wins.
// A Prompt is the interesting case:
//   - auto + interactive -> Allow  (auto-allow, D11)
//   - auto + headless     -> Block  (no human present, fail safe)
//   - ask  + interactive  -> Prompt (ask the human)
//   - ask  + headless      -> Block
//   - panic (any)          -> Block
func (p *Policy) resolve(base Decision, reason string, mode Mode) (Decision, string) {
	if base == Block {
		return Block, reason
	}
	if mode == ModePanic {
		return Block, "permission mode is PANIC"
	}
	if base == Allow {
		return Allow, ""
	}
	// base == Prompt
	switch mode {
	case ModeAuto:
		if p.interactive {
			return Allow, ""
		}
		return Block, "headless mode: " + reason
	default: // ModeAsk
		if p.interactive {
			return Prompt, reason
		}
		return Block, "headless mode: " + reason
	}
}

func (p *Policy) withinWorkDir(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	root := filepath.Clean(p.workDir)
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
