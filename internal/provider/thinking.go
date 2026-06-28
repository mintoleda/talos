package provider

// Thinking levels follow pi's convention — an abstract spectrum that each
// provider translates to its own API parameters.
const (
	ThinkingOff     = "off"
	ThinkingMinimal = "minimal"
	ThinkingLow     = "low"
	ThinkingMedium  = "medium"
	ThinkingHigh    = "high"
	ThinkingXHigh   = "xhigh"
)

// allLevels is the full set for models with no restrictions.
var allLevels = []string{ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh}

// modelThinkingCaps maps model IDs to the set of thinking levels the model
// actually supports. Unknown models default to all levels. This mirrors pi's
// per-model thinkingLevelMap where null means unsupported.
//
// Auto-extracted from pi's model registry. Models not listed allow all 6 levels.
var modelThinkingCaps = map[string][]string{
	// ---- off + high only (minimal/low/medium/xhigh unsupported) ----
	"MiniMaxAI/MiniMax-M3":                     {ThinkingOff, ThinkingHigh},
	"Qwen/Qwen3.5-397B-A17B":                   {ThinkingOff, ThinkingHigh},
	"Qwen/Qwen3.5-9B":                          {ThinkingOff, ThinkingHigh},
	"Qwen/Qwen3.6-Plus":                        {ThinkingOff, ThinkingHigh},
	"deepseek-ai/DeepSeek-V4-Pro":              {ThinkingOff, ThinkingHigh},
	"glm-5.1":                                  {ThinkingOff, ThinkingHigh},
	"google/gemma-4-31B-it":                    {ThinkingOff, ThinkingHigh},
	"kimi-k2.6":                                {ThinkingOff, ThinkingHigh},
	"moonshotai/Kimi-K2.6":                     {ThinkingOff, ThinkingHigh},
	"nvidia/nemotron-3-ultra-550b-a55b":         {ThinkingOff, ThinkingHigh},
	"openai/gpt-oss-safeguard-20b":             {ThinkingOff, ThinkingHigh},
	"qwen/qwen3-32b":                           {ThinkingOff, ThinkingHigh},
	"zai-org/GLM-5":                            {ThinkingOff, ThinkingHigh},
	"zai-org/GLM-5.1":                          {ThinkingOff, ThinkingHigh},

	// ---- off + high + xhigh (minimal/low/medium unsupported) ----
	"deepseek-v4-flash":              {ThinkingOff, ThinkingHigh, ThinkingXHigh},
	"deepseek-v4-flash-free":         {ThinkingOff, ThinkingHigh, ThinkingXHigh},
	"deepseek-v4-pro":                {ThinkingOff, ThinkingHigh, ThinkingXHigh},
	"deepseek/deepseek-v3.2-exp":     {ThinkingOff, ThinkingHigh, ThinkingXHigh},
	"deepseek/deepseek-v4-flash":     {ThinkingOff, ThinkingHigh, ThinkingXHigh},
	"deepseek/deepseek-v4-pro":       {ThinkingOff, ThinkingHigh, ThinkingXHigh},

	// ---- minimal + high only ----
	"gemma-4-26b-a4b-it": {ThinkingMinimal, ThinkingHigh},
	"gemma-4-31b-it":     {ThinkingMinimal, ThinkingHigh},
	"gemini-flash-lite-latest": {ThinkingMinimal, ThinkingHigh},

	// ---- minimal + low + medium + high (no xhigh) ----
	"gemini-2.5-pro":               {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gemini-3-flash":               {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gemini-3-flash-preview":       {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gemini-3.1-flash-lite":        {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gemini-3.1-flash-lite-preview": {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gemini-3.5-flash":             {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-4o-mini":                  {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5":                        {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5-chat-latest":            {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5-codex":                  {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5-mini":                   {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5-nano":                   {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5-pro":                    {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5.1":                      {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5.1-chat-latest":          {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5.1-codex":                {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5.1-codex-max":            {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"gpt-5.1-codex-mini":           {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"ibm-granite/granite-4.1-8b":   {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
	"inception/mercury-2":          {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},

	// ---- low + high only ----
	"gemini-3-pro-preview":                {ThinkingLow, ThinkingHigh},
	"gemini-3.1-pro":                      {ThinkingLow, ThinkingHigh},
	"gemini-3.1-pro-preview":              {ThinkingLow, ThinkingHigh},
	"gemini-3.1-pro-preview-customtools":  {ThinkingLow, ThinkingHigh},

	// ---- low + medium + high (no off, no xhigh) ----
	"openai/gpt-oss-120b": {ThinkingLow, ThinkingMedium, ThinkingHigh},
	"openai/gpt-oss-20b":  {ThinkingLow, ThinkingMedium, ThinkingHigh},

	// ---- medium + high + xhigh (no off, no minimal, no low) ----
	"gpt-5.5-pro":          {ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"openai/gpt-5.5-pro":   {ThinkingMedium, ThinkingHigh, ThinkingXHigh},

	// ---- high only ----
	"MiniMaxAI/MiniMax-M2.7": {ThinkingHigh},
	"grok-build-0.1":         {ThinkingHigh},

	// ---- high + xhigh only ----
	"Ring-2.6-1T": {ThinkingHigh, ThinkingXHigh},

	// ---- minimal + low + medium + high + xhigh (no off) ----
	"claude-3-sonnet-20240229":           {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"claude-fable-5":                     {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"deepseek.v3.2":                      {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"eu.anthropic.claude-fable-5":        {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"eu.anthropic.claude-sonnet-4-6":      {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"global.anthropic.claude-fable-5":    {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.2":                            {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.2-chat-latest":                {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.2-codex":                      {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.2-pro":                        {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.3-chat-latest":                {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.3-codex":                      {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.4":                            {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.4-mini":                       {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.4-nano":                       {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.4-pro":                        {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"gpt-5.5":                            {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"qwen.qwen3-vl-235b-a22b":            {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
	"us.anthropic.claude-fable-5":        {ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh},
}

// ValidThinkingLevels returns all recognized thinking levels.
func ValidThinkingLevels() []string {
	return allLevels
}

// IsValidThinkingLevel returns true if the given level is one of the known values.
func IsValidThinkingLevel(level string) bool {
	for _, l := range allLevels {
		if l == level {
			return true
		}
	}
	return false
}

// SupportedLevels returns the thinking levels available for the given model.
// Unknown models get all levels. An exact match is tried first, then suffix
// matching so "deepseek-v4-flash" also catches "opencode-go/deepseek-v4-flash".
func SupportedLevels(model string) []string {
	if caps, ok := modelThinkingCaps[model]; ok {
		return caps
	}
	// Suffix match for provider-prefixed model IDs (e.g. opencode-go/deepseek-v4-flash).
	for pattern, caps := range modelThinkingCaps {
		if len(model) >= len(pattern) && model[len(model)-len(pattern):] == pattern {
			return caps
		}
	}
	return allLevels
}

// ClampThinkingLevel snaps the requested level to the nearest available level
// for the given model. This mirrors pi's clampThinkingLevel.
func ClampThinkingLevel(model, level string) string {
	caps := SupportedLevels(model)

	// Already valid.
	for _, l := range caps {
		if l == level {
			return level
		}
	}

	// Search upward from the requested index, then downward.
	reqIdx := indexOf(allLevels, level)
	if reqIdx == -1 {
		return caps[0]
	}
	for i := reqIdx; i < len(allLevels); i++ {
		for _, l := range caps {
			if l == allLevels[i] {
				return l
			}
		}
	}
	for i := reqIdx - 1; i >= 0; i-- {
		for _, l := range caps {
			if l == allLevels[i] {
				return l
			}
		}
	}
	return caps[0]
}

// MapThinkingToAnthropicBudget returns the anthropic thinking.budget_tokens for
// the given abstract level, or 0 to disable thinking.
func MapThinkingToAnthropicBudget(level string) int {
	switch level {
	case ThinkingOff:
		return 0
	case ThinkingMinimal:
		return 1024
	case ThinkingLow:
		return 2048
	case ThinkingMedium:
		return 4096
	case ThinkingHigh:
		return 8192
	case ThinkingXHigh:
		return 16384
	default:
		return 0
	}
}

// MapThinkingToOpenAIEffort returns the reasoning_effort value for the given
// abstract level, or "" to omit reasoning entirely. Standard OpenAI values are
// "low", "medium", "high". For xhigh we use "high" since standard OpenAI doesn't
// define a higher tier — some compatible providers may interpret it via their
// own thinkingLevelMap.
func MapThinkingToOpenAIEffort(level string) string {
	switch level {
	case ThinkingOff:
		return ""
	case ThinkingMinimal:
		return "low"
	case ThinkingLow:
		return "low"
	case ThinkingMedium:
		return "medium"
	case ThinkingHigh:
		return "high"
	case ThinkingXHigh:
		return "high"
	default:
		return ""
	}
}

// indexOf returns the index of s in slice, or -1.
func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}
