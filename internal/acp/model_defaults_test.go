package acp

import (
	"context"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/models"
)

func TestUnconfiguredSessionAdvertisesOnlyDiscoveredModels(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{}, {provider: "openai-codex"}, {model: "gpt-6.1-sol"},
	} {
		s := &acpSession{provider: tc.provider, model: tc.model}
		_, state, _ := s.state([]models.Entry{{Provider: "openai-codex", ID: "gpt-6.1-sol"}})
		if state.CurrentModelID != "" || len(state.AvailableModels) != 1 ||
			state.AvailableModels[0].ModelID != "openai-codex/gpt-6.1-sol" {
			t.Fatalf("unconfigured state contains an invented current model: %+v", state)
		}
		if _, err := s.ensureBuilt(context.Background()); err == nil || !strings.Contains(err.Error(), "no model selected") {
			t.Fatalf("unconfigured prompt error = %v", err)
		}
	}
}

func TestUnconfiguredEmptyCatalogueHasNoLegacyDefault(t *testing.T) {
	s := &acpSession{}
	_, state, _ := s.state(nil)
	if state.CurrentModelID != "" || state.AvailableModels == nil || len(state.AvailableModels) != 0 {
		t.Fatalf("empty catalogue must not manufacture a default: %+v", state)
	}
}
