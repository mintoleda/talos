package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

type Mode int

const (
	ModeAuto Mode = iota
	ModeAsk
	ModeBlock
)

func ParseMode(s string) Mode {
	switch strings.ToLower(s) {
	case "ask":
		return ModeAsk
	case "block":
		return ModeBlock
	default:
		return ModeAuto
	}
}

type Policy struct {
	mode        Mode
	workDir     string
	classifier  *Classifier
	interactive bool
	Envelope    interface{}
}

func NewPolicy(mode Mode, workDir string, classifier *Classifier, interactive bool) *Policy {
	return &Policy{mode: mode, workDir: workDir, classifier: classifier, interactive: interactive}
}

func (p *Policy) Check(tu protocol.ToolUse) (Decision, string) {
	switch tu.Name {
	case "bash":
		cmd, _ := tu.Args["command"].(string)
		d, reason := p.classifier.Classify(cmd)
		return p.resolve(d, reason)
	case "write", "edit":
		path, _ := tu.Args["path"].(string)
		if !p.withinWorkDir(path) {
			return Block, fmt.Sprintf("path %s is outside the working directory", path)
		}
		return p.resolve(Allow, "")
	default:
		return p.resolve(Allow, "")
	}
}

// resolve applies the permission mode and interactivity to a base decision from
// the classifier/path check. A classifier Block is catastrophic and always wins.
// A Prompt is the interesting case:
//   - auto + interactive -> Allow  (auto-allow, D11)
//   - auto + headless     -> Block  (no human present, fail safe)
//   - ask  + interactive  -> Prompt (ask the human)
//   - ask  + headless      -> Block
//   - block (any)          -> Block
func (p *Policy) resolve(base Decision, reason string) (Decision, string) {
	if base == Block {
		return Block, reason
	}
	if p.mode == ModeBlock {
		return Block, "permission mode is block"
	}
	if base == Allow {
		return Allow, ""
	}
	// base == Prompt
	switch p.mode {
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
