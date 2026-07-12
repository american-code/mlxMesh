package jobrunner

import (
	"testing"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// draftExtra is pure and needs no live Exo — the fast/streaming/background
// lanes all funnel through it, so these cases guard the actual request-body
// keys sent once Exo's API supports them (see protocol.DraftModelConfig).

func TestDraftExtra_UnconfiguredModelReturnsNil(t *testing.T) {
	r := New("http://localhost:52415", map[string]protocol.DraftModelConfig{
		"llama-3.1-70b": {DraftModelID: "llama-3.2-3b"},
	})
	if got := r.draftExtra("mixtral-8x7b"); got != nil {
		t.Errorf("expected nil for an unconfigured model, got %+v", got)
	}
}

func TestDraftExtra_NilDraftModelsReturnsNil(t *testing.T) {
	r := New("http://localhost:52415", nil)
	if got := r.draftExtra("llama-3.1-70b"); got != nil {
		t.Errorf("expected nil when no draft models are configured at all, got %+v", got)
	}
}

func TestDraftExtra_ConfiguredModelIncludesBothFields(t *testing.T) {
	r := New("http://localhost:52415", map[string]protocol.DraftModelConfig{
		"llama-3.1-70b": {DraftModelID: "llama-3.2-3b", NumDraftTokens: 4},
	})
	got := r.draftExtra("llama-3.1-70b")
	if got["draft_model"] != "llama-3.2-3b" {
		t.Errorf("draft_model = %v, want llama-3.2-3b", got["draft_model"])
	}
	if got["num_draft_tokens"] != 4 {
		t.Errorf("num_draft_tokens = %v, want 4", got["num_draft_tokens"])
	}
}

func TestDraftExtra_ZeroNumDraftTokensOmitted(t *testing.T) {
	// 0 means "let Exo pick its own default" — the key should be absent
	// entirely, not sent as an explicit 0 (which could mean something
	// different to a future Exo API, e.g. "disable speculative decoding").
	r := New("http://localhost:52415", map[string]protocol.DraftModelConfig{
		"llama-3.1-70b": {DraftModelID: "llama-3.2-3b"},
	})
	got := r.draftExtra("llama-3.1-70b")
	if _, present := got["num_draft_tokens"]; present {
		t.Errorf("expected num_draft_tokens to be omitted when 0, got %+v", got)
	}
	if got["draft_model"] != "llama-3.2-3b" {
		t.Errorf("draft_model = %v, want llama-3.2-3b", got["draft_model"])
	}
}

func TestDraftExtra_EmptyDraftModelIDTreatedAsUnconfigured(t *testing.T) {
	r := New("http://localhost:52415", map[string]protocol.DraftModelConfig{
		"llama-3.1-70b": {NumDraftTokens: 4}, // DraftModelID left empty
	})
	if got := r.draftExtra("llama-3.1-70b"); got != nil {
		t.Errorf("expected nil when DraftModelID is empty, got %+v", got)
	}
}
