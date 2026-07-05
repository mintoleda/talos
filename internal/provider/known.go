package provider

type Known struct {
	Name    string
	BaseURL string // BaseURL without /v1 suffix
	EnvVar  string
	Label   string
}

// All is the list of providers talos supports out of the box.
// Anthropic is included for completeness but its /v1/models endpoint is not
// standard; model listing falls back to a hardcoded list.
var All = []Known{
	{Name: "opencode-go", BaseURL: "https://opencode.ai/zen/go", EnvVar: "OPENCODE_API_KEY", Label: "opencode.ai/go"},
	{Name: "opencode-zen", BaseURL: "https://opencode.ai/zen", EnvVar: "OPENCODE_API_KEY", Label: "opencode.ai/zen"},
	{Name: "deepseek", BaseURL: "https://api.deepseek.com", EnvVar: "DEEPSEEK_API_KEY", Label: "deepseek.com"},
	{Name: "openrouter", BaseURL: "https://openrouter.ai/api", EnvVar: "OPENROUTER_API_KEY", Label: "openrouter.ai"},
	{Name: "openai", BaseURL: "https://api.openai.com", EnvVar: "OPENAI_API_KEY", Label: "openai.com"},
	{Name: "anthropic", BaseURL: "https://api.anthropic.com", EnvVar: "ANTHROPIC_API_KEY", Label: "anthropic.com"},
	{Name: "cloudflare", BaseURL: "", EnvVar: "CLOUDFLARE_API_KEY", Label: "Cloudflare Workers AI"},
}

func ByName(name string) (Known, bool) {
	for _, k := range All {
		if k.Name == name {
			return k, true
		}
	}
	return Known{}, false
}
