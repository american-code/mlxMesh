package settlement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// settlementContent is the signable portion of a settlement record.
// Marshalling this struct (not the full map) produces a canonical byte sequence to sign.
type settlementContent struct {
	DivisionOrder      map[string]any `json:"division_order"`
	VerificationResult bool           `json:"verification_result"`
	SignedAt           string         `json:"signed_at"`
}

// CreateSettlementRecord signs the division order plus verification outcome.
// A record with verificationResult=false must still be created and published —
// it is evidence for dispute resolution. Silently dropping failed verifications
// erases the audit trail the open, no-custody settlement model depends on (proposal §10).
func CreateSettlementRecord(divisionOrder map[string]any, verificationResult bool, signerPrivateKey []byte) (map[string]any, error) {
	signedAt := time.Now().UTC().Format(time.RFC3339)
	content := settlementContent{
		DivisionOrder:      divisionOrder,
		VerificationResult: verificationResult,
		SignedAt:           signedAt,
	}
	contentBytes, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal record content: %w", err)
	}
	sig, err := protocol.SignPayload(signerPrivateKey, contentBytes)
	if err != nil {
		return nil, fmt.Errorf("sign record: %w", err)
	}
	sum := sha256.Sum256(contentBytes)
	recordID := hex.EncodeToString(sum[:16])

	return map[string]any{
		"record_id":           recordID,
		"division_order":      divisionOrder,
		"verification_result": verificationResult,
		"signed_at":           signedAt,
		"signature":           hex.EncodeToString(sig),
	}, nil
}

// PublishSettlementRecord POSTs the signed record to the pod coordinator.
// This makes the record available to both parties for their own off-protocol payment step.
// The protocol never custodies funds — publishing a record is not the same as moving money.
func PublishSettlementRecord(ctx context.Context, record map[string]any, podEndpoint string) error {
	b, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, podEndpoint+"/settlement/records", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST settlement/records: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("coordinator responded HTTP %d", resp.StatusCode)
	}
	return nil
}
