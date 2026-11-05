package codex

// ProviderName is the auth.json key and provider ID, shared with pi.
const ProviderName = "openai-codex"

// DefaultBaseURL is the ChatGPT backend; requests go to {base}/codex/responses.
const DefaultBaseURL = "https://chatgpt.com/backend-api"

// Model describes one model served through the ChatGPT subscription.
type Model struct {
	ID   string
	Name string
}

// Models is the static catalogue, mirroring pi's openai-codex model list.
// The backend has no list endpoint usable with subscription tokens.
// Context windows and prices live in the pricing table; thinking-level
// support lives in provider.SupportedLevels.
var Models = []Model{
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna"},
	{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol"},
	{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra"},
	{ID: "gpt-6-astra", Name: "GPT-6 Astra"},
	{ID: "gpt-6-luna", Name: "GPT-6 Luna"},
	{ID: "gpt-6-sol", Name: "GPT-6 Sol"},
	{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol"},
}

// ModelIDs returns the IDs from Models.
func ModelIDs() []string {
	ids := make([]string, len(Models))
	for i, m := range Models {
		ids[i] = m.ID
	}
	return ids
}
