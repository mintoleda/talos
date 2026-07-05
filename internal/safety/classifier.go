package safety

import "regexp"

type Decision int

const (
	Allow Decision = iota
	Prompt
	Block
)

type Classifier struct {
	block  []*regexp.Regexp
	prompt []*regexp.Regexp
}

func NewClassifier() *Classifier {
	return &Classifier{
		block: compile(
			`rm\s+-rf?\s+/\s*$`,
			`rm\s+-rf?\s+(~|\.|\*)(\s|$)`,
			`\bmkfs\b`,
			`\bdd\b.*of=/dev/`,
			`\b(fdisk|diskpart)\b`,

			`sed\b.*-i[^>]*\.(go|py|js|ts|rs|java|rb|sh|toml|ya?ml|json|md|c|h|cpp|css|html|tex)`,
			`perl\b.*-i\b.*\.(go|py|js|ts|rs|java|rb|sh|toml|ya?ml|json|md|c|h|cpp|css|html|tex)`,

			`(python3?|node|ruby|perl|php)\s+(-[a-zA-Z]+\s+)*-[a-zA-Z]*[cer]\b`,

			`(cat|tee|dd)\b[^|]*>\s*\S+\.(go|py|js|ts|rs|java|rb|sh|toml|ya?ml|json|md|c|h|cpp|css|html|tex)`,
			`tee\b\s+\S+\.(go|py|js|ts|rs|java|rb|sh|toml|ya?ml|json|md|c|h|cpp|css|html|tex)`,
			`dd\b.*\bof=\S+\.(go|py|js|ts|rs|java|rb|sh|toml|ya?ml|json|md|c|h|cpp|css|html|tex)`,

			`<<\s*(EOF|END|DELIM|HEREDOC|DOC)\b.*>\s*\S+\.(go|py|js|ts|rs|java|rb|sh|toml|ya?ml|json|md)`,

			`>\s*\S+\.(go|py|js|ts|rs|java|rb|sh|toml|ya?ml|json|md|c|h|cpp|css|html|tex)\s*$`,
		),
		prompt: compile(
			`\bsudo\b`,
			`\bsu\b`,
			`(curl|wget)\b[^|]*\|\s*(sh|bash|zsh)`,
			`git\s+reset\s+--hard`,
			`git\s+clean\s+-[a-z]*f`,
			`git\s+checkout\s+--\s+\.`,
			`git\s+restore\s+\.`,
			`(chmod|chown)\s+-R`,
			`>\s*/dev/(sd|nvme|disk)`,
		),
	}
}

func compile(patterns ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err == nil {
			out = append(out, re)
		}
	}
	return out
}

func (c *Classifier) Classify(cmd string) (Decision, string) {
	for _, re := range c.block {
		if re.MatchString(cmd) {
			return Block, "matches a catastrophic pattern: " + re.String()
		}
	}
	for _, re := range c.prompt {
		if re.MatchString(cmd) {
			return Prompt, "matches a dangerous pattern: " + re.String()
		}
	}
	return Allow, ""
}
