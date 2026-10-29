package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// p2p_pm_releaser.go — the payment-merchant release path on P2PTradeEscrow (slice 9, decision M1).
// The escrow has no global release power: a PM's sell-ad trades are released by the platform's
// release key only because the PM named it with setReleaser, once, from its maker wallet. Until it
// has, its sell-ads cannot be signed or opened.

// ErrPMReleaserNotSet: the PM's maker wallet has not named the platform release key as its
// releaser on the chain's escrow.
var ErrPMReleaserNotSet = errors.New("p2p: the payment merchant has not let the platform release its trades")

// SetReleaseAddress is the platform release key's address (PM_RELEASE_PRIVATE_KEY or its signer):
// the releaser PMs name and the trade releaser PM buy-ad opens pass. Empty: PM settlement is not
// running, so PM ads cannot be opened.
func (s *P2PTradeService) SetReleaseAddress(addr string) {
	if common.IsHexAddress(addr) {
		s.releaseAddress = common.HexToAddress(addr).Hex()
	}
}

// requirePMReleaser checks the PM maker wallet named the release key as its releaser on escrow.
func (s *P2PTradeService) requirePMReleaser(escrow P2PEscrowAdapter, pmWallet string) error {
	if s.releaseAddress == "" {
		return fmt.Errorf("%w: payment-merchant settlement is not running", ErrChainUnavailable)
	}
	got, err := escrow.ReadReleaser(pmWallet)
	if err != nil {
		return fmt.Errorf("%w: read releaser: %v", ErrChainUnavailable, err)
	}
	if got != common.HexToAddress(s.releaseAddress) {
		return ErrPMReleaserNotSet
	}
	return nil
}

// PMReleaserStatus is a PM's releaser on one chain's escrow.
type PMReleaserStatus struct {
	ChainID  int64  `json:"chainId"`
	Wallet   string `json:"wallet"`
	Releaser string `json:"releaser"` // what the escrow holds now (zero address: none)
	Expected string `json:"expected"` // the platform release key
	Set      bool   `json:"set"`
}

// pmReleaserTarget resolves the caller's active PM, its maker wallet and the chain's escrow.
func (s *P2PTradeService) pmReleaserTarget(ctx context.Context, userID uuid.UUID, chainID int64) (*models.PaymentMerchant, P2PEscrowAdapter, int64, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	pm, err := s.lookupActivePM(ctx, userID)
	if err != nil {
		return nil, nil, 0, err
	}
	if pm == nil {
		return nil, nil, 0, ErrForbidden
	}
	if common.HexToAddress(pm.OperatorWalletAddress) == (common.Address{}) {
		return nil, nil, 0, fmt.Errorf("%w: PM has no registered maker wallet", ErrInvalidParams)
	}
	if s.releaseAddress == "" {
		return nil, nil, 0, fmt.Errorf("%w: payment-merchant settlement is not running", ErrChainUnavailable)
	}
	escrow, _, _, err := s.resolve(chainID)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%w: chain %d not available for P2P", ErrInvalidParams, chainID)
	}
	return pm, escrow, chainID, nil
}

// PMReleaser reads the caller's releaser on chainID's escrow (0: the default chain).
func (s *P2PTradeService) PMReleaser(ctx context.Context, userID uuid.UUID, chainID int64) (*PMReleaserStatus, error) {
	pm, escrow, chainID, err := s.pmReleaserTarget(ctx, userID, chainID)
	if err != nil {
		return nil, err
	}
	wallet := common.HexToAddress(pm.OperatorWalletAddress)
	got, err := escrow.ReadReleaser(wallet.Hex())
	if err != nil {
		return nil, fmt.Errorf("%w: read releaser: %v", ErrChainUnavailable, err)
	}
	want := common.HexToAddress(s.releaseAddress)
	return &PMReleaserStatus{ChainID: chainID, Wallet: wallet.Hex(), Releaser: got.Hex(), Expected: want.Hex(), Set: got == want}, nil
}

// PreparePMReleaser builds the PM maker wallet's setReleaser(release key). It is the PM's own tx:
// the key it names may release every trade of that wallet, and only the wallet can grant that.
func (s *P2PTradeService) PreparePMReleaser(ctx context.Context, userID uuid.UUID, chainID int64) ([]byte, error) {
	pm, escrow, _, err := s.pmReleaserTarget(ctx, userID, chainID)
	if err != nil {
		return nil, err
	}
	tx, err := escrow.PrepareSetReleaser(common.HexToAddress(pm.OperatorWalletAddress).Hex(), s.releaseAddress)
	if err != nil {
		if isChainRevert(err) {
			return nil, fmt.Errorf("%w: %v", ErrTradePrepareReverted, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrChainUnavailable, err)
	}
	return tx, nil
}

// ConfirmPMReleaser proves the PM's setReleaser named the release key and returns the status.
func (s *P2PTradeService) ConfirmPMReleaser(ctx context.Context, userID uuid.UUID, chainID int64, txHash string) (*PMReleaserStatus, error) {
	pm, escrow, chainID, err := s.pmReleaserTarget(ctx, userID, chainID)
	if err != nil {
		return nil, err
	}
	wallet := common.HexToAddress(pm.OperatorWalletAddress)
	ev, err := escrow.VerifyReleaserSet(txHash, wallet)
	switch {
	case err == nil:
	case strings.Contains(err.Error(), "not found in receipt"), strings.Contains(err.Error(), "reverted on chain"):
		// A mined receipt that is not this wallet's setReleaser.
		return nil, fmt.Errorf("%w: %v", ErrInvalidTransition, err)
	default:
		// No receipt yet, or the node could not be asked: retry.
		return nil, fmt.Errorf("%w: %v", ErrChainUnavailable, err)
	}
	want := common.HexToAddress(s.releaseAddress)
	if ev.Releaser != want {
		return nil, fmt.Errorf("%w: the transaction named %s, not the platform release key", ErrInvalidTransition, ev.Releaser.Hex())
	}
	s.audit(ctx, models.P2PAuditPMReleaserSet, &userID, nil, nil, map[string]any{
		"chainId": chainID, "wallet": wallet.Hex(), "releaser": want.Hex(), "tx": txHash,
	})
	log.Printf("[p2p] pm=%s wallet=%s named the release key %s on chain %d (tx %s)", pm.ID, wallet.Hex(), want.Hex(), chainID, txHash)
	return &PMReleaserStatus{ChainID: chainID, Wallet: wallet.Hex(), Releaser: want.Hex(), Expected: want.Hex(), Set: true}, nil
}

// AlertMissingPMReleasers logs `ALERT pm_releaser_not_set` for each entry of MissingPMReleasers, at
// most hourly per entry, and returns how many there are.
func (s *P2PTradeService) AlertMissingPMReleasers(ctx context.Context) (int, error) {
	missing, err := s.MissingPMReleasers(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range missing {
		if reconcileAlerts.allow("pm_releaser:"+m, time.Hour) {
			log.Printf("[pm] ALERT pm_releaser_not_set %s — the PM must name the release key (setReleaser): its sell-ads cannot trade and paid trades cannot be auto-released", m)
		}
	}
	return len(missing), nil
}

// MissingPMReleasers lists each active payment merchant whose maker wallet has not named the
// release key on a chain PMs trade on (audience pm or all) — their sell-ads cannot be signed or
// opened until they do. A chain read that fails is reported as such, never as "set".
func (s *P2PTradeService) MissingPMReleasers(ctx context.Context) ([]string, error) {
	if s.releaseAddress == "" || s.escrows == nil {
		return nil, nil
	}
	var pms []models.PaymentMerchant
	if err := s.db.WithContext(ctx).
		Where("kind = ? AND status = ? AND COALESCE(operator_wallet_address, '') <> ''",
			models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive).
		Find(&pms).Error; err != nil {
		return nil, err
	}
	want := common.HexToAddress(s.releaseAddress)
	var missing []string
	for _, chainID := range s.escrows.ChainIDs() {
		if s.escrows.Audience(chainID) == config.P2PAudienceDirect {
			continue
		}
		escrow, err := s.escrows.For(chainID)
		if err != nil {
			continue
		}
		for _, pm := range pms {
			got, err := escrow.ReadReleaser(pm.OperatorWalletAddress)
			switch {
			case err != nil:
				missing = append(missing, fmt.Sprintf("pm=%s chain=%d unreadable: %s", pm.ID, chainID, RedactAlertText(err.Error())))
			case got != want:
				missing = append(missing, fmt.Sprintf("pm=%s wallet=%s chain=%d releaser=%s", pm.ID, pm.OperatorWalletAddress, chainID, got.Hex()))
			}
		}
	}
	return missing, nil
}

// payoutReleaseMargin is the least time a trade's payment window must still have when the naira
// is sent: past expiresAt anyone may cancel it by timeout, returning the crypto to the user.
const payoutReleaseMargin = 10 * time.Minute

// ReleaseKeyCanRelease reads t from its escrow and answers whether the release key is sure to be
// able to release it after the platform pays the user out (a PM sell payout): the trade must still
// be Pending with at least payoutReleaseMargin of its window left, and name the release key as its
// own releaser. Only that per-trade releaser counts — it is fixed at open — never the seller's
// releaserOf, which the seller (here the user being paid) can revoke after the payout.
func (s *P2PTradeService) ReleaseKeyCanRelease(t *models.P2PTrade) error {
	if s.releaseAddress == "" {
		return fmt.Errorf("%w: payment-merchant settlement is not running", ErrChainUnavailable)
	}
	escrow, key, err := s.tradeEscrow(t)
	if err != nil {
		return err
	}
	onchain, err := escrow.ReadTrade(key)
	if err != nil {
		return fmt.Errorf("%w: read trade: %v", ErrChainUnavailable, err)
	}
	if !onchain.Exists || onchain.Status != OnChainP2PStatusPending {
		return fmt.Errorf("%w: trade %s is not pending on-chain", ErrInvalidTransition, t.ID)
	}
	if onchain.ExpiresAt == nil || time.Unix(onchain.ExpiresAt.Int64(), 0).Sub(s.clock.Now()) < payoutReleaseMargin {
		return fmt.Errorf("%w: trade %s is too close to its payment deadline to pay out", ErrInvalidTransition, t.ID)
	}
	if onchain.TradeReleaser != common.HexToAddress(s.releaseAddress) {
		return fmt.Errorf("%w: the release key cannot release trade %s", ErrPMReleaserNotSet, t.ID)
	}
	return nil
}
