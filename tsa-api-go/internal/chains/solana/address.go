package solana

import (
	"fmt"

	solanago "github.com/gagliardetto/solana-go"
)

// ValidateAddress accepts any base58 string that decodes to a 32-byte Solana
// account key. Curve membership is intentionally NOT required: Solana accepts
// off-curve system addresses (PDAs, some exchange deposit addresses), so
// rejecting off-curve keys would block legitimate recipients.
func ValidateAddress(addr string) error {
	if _, err := solanago.PublicKeyFromBase58(addr); err != nil {
		return fmt.Errorf("invalid solana address: %s", addr)
	}
	return nil
}
