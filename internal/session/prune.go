package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

const snipMarker = "[snipped tool result — "

func isErrorResult(content string) bool {
	c := strings.TrimSpace(content)
	if len(c) == 0 {
		return false
	}
	return c[0] == '\x1b' ||
		strings.HasPrefix(c, "Error") ||
		strings.HasPrefix(c, "error") ||
		strings.HasPrefix(c, "Fatal") ||
		strings.HasPrefix(c, "panic")
}

func alreadySnipped(content string) bool {
	return strings.HasPrefix(content, snipMarker)
}

var readTools = map[string]bool{
	"read":   true,
	"grep":   true,
	"find":   true,
	"ls":     true,
	"glob":   true,
	"search": true,
}

// toolNamesByID maps tool_use IDs to their tool name so snip/prune can apply
// the per-tool retention policy to results, which carry only the ID.
func toolNamesByID(tx *Transcript) map[string]string {
	names := make(map[string]string)
	for i, n := 0, tx.FrozenLen(); i < n; i++ {
		fm := tx.FrozenAt(i)
		if fm.Msg.Role != protocol.RoleAssistant {
			continue
		}
		for _, block := range fm.Msg.Content {
			if block.Type == protocol.BlockToolUse && block.ToolUse != nil {
				names[block.ToolUse.ID] = block.ToolUse.Name
			}
		}
	}
	return names
}

func snipToolResult(res *protocol.ToolResult, toolName string) *protocol.ToolResult {
	if res == nil {
		return nil
	}
	if toolName == "" {
		toolName = "tool"
	}
	text := res.Content
	if len(text) < 1024 || isErrorResult(text) || alreadySnipped(text) {
		return nil
	}

	var headLen, tailLen int
	if readTools[toolName] {
		headLen = 4096
		tailLen = 256
	} else {
		headLen = 2048
		tailLen = 2048
	}

	origLen := len(text)
	head := text
	if len(head) > headLen {
		head = head[:headLen]
	}
	var tail string
	if origLen > headLen+tailLen+100 {
		tail = text[origLen-tailLen:]
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s%s, %d bytes; ", snipMarker, toolName, origLen))
	b.WriteString(head)
	if tail != "" {
		b.WriteString("\n\n...\n\n")
		b.WriteString(tail)
	}
	b.WriteString("]")

	return &protocol.ToolResult{
		ToolUseID: res.ToolUseID,
		Content:   b.String(),
		IsError:   false,
	}
}

func pruneToolResult(res *protocol.ToolResult, toolName string) *protocol.ToolResult {
	if res == nil {
		return nil
	}
	if toolName == "" {
		toolName = "tool"
	}
	text := res.Content
	if isErrorResult(text) {
		return nil
	}
	if !alreadySnipped(text) && len(text) < 4096 {
		return nil
	}
	return &protocol.ToolResult{
		ToolUseID: res.ToolUseID,
		Content:   fmt.Sprintf("[pruned tool result — %s; re-run the tool if you need the output]", toolName),
		IsError:   false,
	}
}

// SnipPrune runs the free-tier context reclamation pass on stale tool
// results. It snips at the SnipRatio threshold and prunes at the
// compaction threshold, always leaving tail messages untouched.
// Mutates the transcript's frozen messages in place.
func (c *Compactor) SnipPrune(tx *Transcript, promptTokens, ctxLimit int) (bool, error) {
	if ctxLimit == 0 {
		return false, nil
	}
	n := tx.FrozenLen()
	if n == 0 {
		return false, nil
	}
	ratio := float64(promptTokens) / float64(ctxLimit)
	snipRatio := c.SnipRatio
	if snipRatio <= 0 {
		snipRatio = 0.60
	}

	if ratio < snipRatio && ratio < c.Threshold {
		return false, nil
	}

	names := toolNamesByID(tx)

	keep := c.KeepTail
	if keep < 1 {
		keep = 1
	}
	extend := 4
	tailStart := n - keep - extend
	if tailStart < 0 {
		tailStart = 0
	}

	modified := false

	if ratio >= snipRatio {
		for i := 0; i < tailStart; i++ {
			fm := tx.FrozenAt(i)
			if fm.Msg.Role != protocol.RoleTool {
				continue
			}
			msgModified := false
			for j, block := range fm.Msg.Content {
				if block.Type != protocol.BlockToolResult || block.ToolResult == nil {
					continue
				}
				if snipped := snipToolResult(block.ToolResult, names[block.ToolResult.ToolUseID]); snipped != nil {
					fm.Msg.Content[j].ToolResult = snipped
					msgModified = true
				}
			}
			if msgModified {
				raw, _ := json.Marshal(fm.Msg)
				fm.Raw = raw
				tx.SnipAt(i, fm)
				modified = true
			}
		}
	}

	if ratio >= c.Threshold {
		for i := 0; i < tailStart; i++ {
			fm := tx.FrozenAt(i)
			if fm.Msg.Role != protocol.RoleTool {
				continue
			}
			msgModified := false
			for j, block := range fm.Msg.Content {
				if block.Type != protocol.BlockToolResult || block.ToolResult == nil {
					continue
				}
				if pruned := pruneToolResult(block.ToolResult, names[block.ToolResult.ToolUseID]); pruned != nil {
					fm.Msg.Content[j].ToolResult = pruned
					msgModified = true
				}
			}
			if msgModified {
				raw, _ := json.Marshal(fm.Msg)
				fm.Raw = raw
				tx.SnipAt(i, fm)
				modified = true
			}
		}
	}

	if modified {
		tx.BumpRewrite()
	}

	return modified, nil
}
