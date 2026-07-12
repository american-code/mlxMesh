package nodeconfig

import "testing"

// baseValidConfig returns a Config that passes Validate on its own, so each
// test below only needs to override the field it's actually exercising.
func baseValidConfig() Config {
	return Config{MemoryCapPct: 0.5}
}

func TestValidate_DraftModels_Valid(t *testing.T) {
	cfg := baseValidConfig()
	cfg.DraftModels = map[string]DraftModelConfig{
		"llama-3.1-70b": {DraftModelID: "llama-3.2-3b", NumDraftTokens: 4},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected a valid draft_models entry to pass, got %v", err)
	}
}

func TestValidate_DraftModels_ZeroNumDraftTokensAllowed(t *testing.T) {
	// 0 means "let Exo pick its own default" — not an error.
	cfg := baseValidConfig()
	cfg.DraftModels = map[string]DraftModelConfig{
		"llama-3.1-70b": {DraftModelID: "llama-3.2-3b"},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected num_draft_tokens=0 to be valid, got %v", err)
	}
}

func TestValidate_DraftModels_MissingDraftModelIDRejected(t *testing.T) {
	cfg := baseValidConfig()
	cfg.DraftModels = map[string]DraftModelConfig{
		"llama-3.1-70b": {NumDraftTokens: 4},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for an empty draft_model_id")
	}
}

func TestValidate_DraftModels_NegativeNumDraftTokensRejected(t *testing.T) {
	cfg := baseValidConfig()
	cfg.DraftModels = map[string]DraftModelConfig{
		"llama-3.1-70b": {DraftModelID: "llama-3.2-3b", NumDraftTokens: -1},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for a negative num_draft_tokens")
	}
}

func TestValidate_DraftModels_EmptyModelIDKeyRejected(t *testing.T) {
	cfg := baseValidConfig()
	cfg.DraftModels = map[string]DraftModelConfig{
		"": {DraftModelID: "llama-3.2-3b"},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for an empty model_id key")
	}
}

func TestValidate_DraftModels_NilIsValid(t *testing.T) {
	// The overwhelming common case: no speculative decoding configured at all.
	cfg := baseValidConfig()
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected nil DraftModels to be valid, got %v", err)
	}
}
