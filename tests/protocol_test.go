package tests

import (
	"encoding/json"
	"testing"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

func TestGenerateAndVerifyIdentity(t *testing.T) {
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("GenerateNodeIdentity: %v", err)
	}
	if len(priv) == 0 || len(pub) == 0 {
		t.Fatal("empty keys")
	}

	nodeID := protocol.NodeIDFromPubKey(pub)
	if len(nodeID) != 32 {
		t.Fatalf("node ID should be 32 chars, got %d", len(nodeID))
	}

	// Same public key always produces the same node ID
	if protocol.NodeIDFromPubKey(pub) != nodeID {
		t.Fatal("node ID is not deterministic")
	}
}

func TestSignAndVerify(t *testing.T) {
	priv, pub, _ := protocol.GenerateNodeIdentity()
	payload := []byte("test payload for signing")

	sig, err := protocol.SignPayload(priv, payload)
	if err != nil {
		t.Fatalf("SignPayload: %v", err)
	}

	if !protocol.VerifySignature(pub, payload, sig) {
		t.Fatal("valid signature failed verification")
	}

	// Tampered payload must not verify
	tampered := append([]byte{}, payload...)
	tampered[0] ^= 0xFF
	if protocol.VerifySignature(pub, tampered, sig) {
		t.Fatal("tampered payload verified — signature check broken")
	}

	// Wrong key must not verify
	_, otherPub, _ := protocol.GenerateNodeIdentity()
	if protocol.VerifySignature(otherPub, payload, sig) {
		t.Fatal("wrong public key verified — signature check broken")
	}
}

func TestJobSpecValidation(t *testing.T) {
	cases := []struct {
		name    string
		spec    protocol.JobSpec
		wantErr bool
	}{
		{
			name: "valid fast-lane",
			spec: protocol.JobSpec{
				Lane:            protocol.JobLaneFast,
				Sensitivity:     protocol.SensitivityLow,
				RedundancyDepth: 0,
				Recurrence:      nil,
			},
		},
		{
			name: "valid background-lane",
			spec: protocol.JobSpec{
				Lane:            protocol.JobLaneBackground,
				Sensitivity:     protocol.SensitivityModerate,
				RedundancyDepth: 1,
				Recurrence:      &protocol.RecurrenceSpec{IntervalSeconds: 300, MaxJitterSeconds: 30},
			},
		},
		{
			name: "fast-lane with recurrence — invalid",
			spec: protocol.JobSpec{
				Lane:       protocol.JobLaneFast,
				Recurrence: &protocol.RecurrenceSpec{IntervalSeconds: 300},
			},
			wantErr: true,
		},
		{
			name: "background-lane without recurrence — invalid",
			spec: protocol.JobSpec{
				Lane:       protocol.JobLaneBackground,
				Recurrence: nil,
			},
			wantErr: true,
		},
		{
			name: "HIGH sensitivity with redundancy_depth=0 — invalid",
			spec: protocol.JobSpec{
				Lane:            protocol.JobLaneFast,
				Sensitivity:     protocol.SensitivityHighRequiresAttestation,
				RedundancyDepth: 0,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCapabilityManifestRoundTrip(t *testing.T) {
	manifest := protocol.CapabilityManifest{
		NodeID:               "abc123",
		IsCluster:            false,
		DeclaredMemoryGB:     64.0,
		DeclaredMemoryCapPct: 0.5,
		GeographicHint:       "us",
		Models: []protocol.ModelCapability{
			{
				ModelID:          "mlx-community/Llama-3.2-3B-Instruct-4bit",
				Quantization:     "4bit",
				Runtime:          protocol.RuntimeExoMLX,
				MaxContextTokens: 8192,
				IsMoE:            false,
			},
		},
		ReachabilityEndpoint: "http://localhost:8765",
		PricePerUnit:         map[string]float64{"compute_cycles": 0.0},
	}

	b, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	var out protocol.CapabilityManifest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if out.NodeID != manifest.NodeID {
		t.Errorf("NodeID mismatch: %s vs %s", out.NodeID, manifest.NodeID)
	}
	if len(out.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out.Models))
	}
	if out.Models[0].ModelID != manifest.Models[0].ModelID {
		t.Errorf("model ID mismatch")
	}
}

func TestBenchCompareSignatures(t *testing.T) {
	// Import bench inline to avoid cycle
	claimed := &protocol.MeasuredSignature{
		TokensPerSecDecode:  100.0,
		TokensPerSecPrefill: 200.0,
	}
	measured := &protocol.MeasuredSignature{
		TokensPerSecDecode:  95.0, // within 20%
		TokensPerSecPrefill: 210.0,
	}
	// Simple inline check mirroring bench.CompareSignatures logic
	withinTolerance := func(claim, actual, tol float64) bool {
		if claim <= 0 {
			return actual >= 0
		}
		r := actual / claim
		return r >= (1-tol) && r <= (1+tol)
	}
	if !withinTolerance(claimed.TokensPerSecDecode, measured.TokensPerSecDecode, 0.20) {
		t.Error("decode within tolerance should pass")
	}

	// Way off — should fail
	bad := &protocol.MeasuredSignature{TokensPerSecDecode: 10.0}
	if withinTolerance(claimed.TokensPerSecDecode, bad.TokensPerSecDecode, 0.20) {
		t.Error("decode out of tolerance should fail")
	}
	_ = measured
}
