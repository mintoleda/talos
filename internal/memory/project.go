package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"
)

func ProjectID(root string) string {
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "HEAD")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		fields := strings.Fields(string(out))
		if len(fields) > 0 {
			return fields[0]
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha1.Sum([]byte(abs))
	return hex.EncodeToString(sum[:8])
}
