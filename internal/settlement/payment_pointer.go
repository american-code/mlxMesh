package settlement

import "fmt"

// PaymentPointer declares where a node operator wants to be paid.
// The protocol never custodies funds — this carries WHERE, not HOW (proposal §10).
type PaymentPointer struct {
	RailType           string `json:"rail_type"` // "stablecoin" | "fiat_invoice" | "other"
	AddressOrReference string `json:"address_or_reference"`
}

// ValidatePaymentPointer does format and sanity validation only.
// Must NOT initiate any transaction — doing so would mean this module touches money,
// which directly contradicts the no-custody design (proposal §10).
func ValidatePaymentPointer(p PaymentPointer) (bool, error) {
	switch p.RailType {
	case "stablecoin", "fiat_invoice", "other":
		if p.AddressOrReference == "" {
			return false, fmt.Errorf("address_or_reference is required for rail_type %q", p.RailType)
		}
		return true, nil
	case "":
		return false, fmt.Errorf("rail_type is required")
	default:
		return false, fmt.Errorf("unknown rail_type %q: must be stablecoin, fiat_invoice, or other", p.RailType)
	}
}
