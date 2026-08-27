package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	ErrStableNoDepositAddress = errors.New("it stable: no Flutterwave deposit address configured")
	ErrStableQuoteExpired     = errors.New("it stable: quote has expired")
	ErrStableNoDestination    = errors.New("it stable: a payout destination is required")
)

// stableDepositChain is where every sell deposit is routed. Flutterwave accepts
// inbound stablecoins on Polygon and Ethereum only; Polygon is the cheaper and
// is Flutterwave's own documented default. Defined in terms of feeChainPolygon
// (fee_quote_service.go) rather than a second "polygon" literal, uppercased
// because the rest of the stable ramp (SourceChain, deliveryMatrix) keys chains
// upper-case.
var stableDepositChain = strings.ToUpper(feeChainPolygon)

// StableDepositInstructions tells the app where to route the user's funds.
//
// Address is FLUTTERWAVE'S address, never one of ours: the user's stablecoin
// goes straight from their wallet to the provider, so the platform is never in
// the custody path.
type StableDepositInstructions struct {
	Chain           string   `json:"chain"`
	Address         string   `json:"address"`
	Asset           string   `json:"asset"`
	AmountBaseUnits *big.Int `json:"-"`
	AmountDisplay   string   `json:"amount"`
}

// SetStableDepositAddress records Flutterwave's own deposit address for a
// chain. Flutterwave exposes it on the dashboard only — there is no API to
// fetch it — so it is copied in by configuration.
func (s *ITService) SetStableDepositAddress(chain, address string) {
	if s.stableDepositAddrs == nil {
		s.stableDepositAddrs = map[string]string{}
	}
	s.stableDepositAddrs[strings.ToUpper(strings.TrimSpace(chain))] = strings.TrimSpace(address)
}

func (s *ITService) stableDepositAddress(chain string) (string, error) {
	addr := s.stableDepositAddrs[strings.ToUpper(strings.TrimSpace(chain))]
	if addr == "" {
		return "", fmt.Errorf("%w: %s", ErrStableNoDepositAddress, chain)
	}
	if err := ValidateDeliveryAddress(chain, addr); err != nil {
		return "", fmt.Errorf("configured deposit address is invalid: %w", err)
	}
	return addr, nil
}

// CreateStableSellIntent records a priced sell and returns where the user must
// send their stablecoin.
//
// Nothing is paid out here and nothing is reserved: the payout is funded
// entirely by the user's own funds once they provably arrive at Flutterwave
// (see the watcher). The intent is the ledger of that promise, not a claim on
// any balance of ours.
func (s *ITService) CreateStableSellIntent(
	ctx context.Context,
	userID uuid.UUID,
	q *StableSellQuote,
	sourceChain string,
	bank *ITSellBank,
) (*models.ITIntent, *StableDepositInstructions, error) {
	if q == nil {
		return nil, nil, errors.New("it stable: nil quote")
	}
	if time.Now().After(q.ExpiresAt) {
		return nil, nil, ErrStableQuoteExpired
	}
	if bank == nil || (strings.TrimSpace(bank.Number) == "" && strings.TrimSpace(bank.MobileMsisdn) == "") {
		return nil, nil, ErrStableNoDestination
	}
	addr, err := s.stableDepositAddress(stableDepositChain)
	if err != nil {
		return nil, nil, err
	}

	expires := q.ExpiresAt
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        userID,
		Direction:     models.ITDirectionSell,
		Status:        models.ITStatusPendingDeposit,
		Asset:         q.Asset,
		AssetAmount:   q.AssetAmount.String(),
		Currency:      q.Currency,
		SourceChain:   strings.ToUpper(strings.TrimSpace(sourceChain)),
		NgnKobo:       q.GrossMinor.String(),
		NetNgnKobo:    q.NetMinor.String(),
		RateKobo:      q.RatePerUnitMinor.String(),
		LockedRate:    q.RatePerUnitMinor.String(),
		RateExpiresAt: &expires,

		BankCode:            strings.TrimSpace(bank.Code),
		AccountNumber:       strings.TrimSpace(bank.Number),
		AccountName:         strings.TrimSpace(bank.Name),
		DestinationCurrency: strings.TrimSpace(bank.DestinationCurrency),
		RailType:            strings.TrimSpace(bank.RailType),
		MobileNetwork:       strings.TrimSpace(bank.MobileNetwork),
		MobileCountry:       strings.TrimSpace(bank.MobileCountry),
		MobileMsisdn:        strings.TrimSpace(bank.MobileMsisdn),
	}
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, nil, err
	}

	return intent, &StableDepositInstructions{
		Chain:           stableDepositChain,
		Address:         addr,
		Asset:           q.Asset,
		AmountBaseUnits: q.AssetAmount,
		AmountDisplay:   fmt.Sprintf("%.6f", StableFromBaseUnits(q.AssetAmount)),
	}, nil
}

// SetStableVerifier wires the two independent sources used to confirm that a
// user's sell deposit reached Flutterwave: LiFi to FIND the destination
// transaction, and an on-chain verifier to PROVE it moved the funds.
// tokenAddrs maps asset symbol → the token contract on the deposit chain.
func (s *ITService) SetStableVerifier(lifi *LiFiStatusClient, chain ERC20TransferVerifier, tokenAddrs map[string]string) {
	s.stableLiFi = lifi
	s.stableChain = chain
	s.stableTokens = tokenAddrs
}

// ReportStableDeposit records the source transaction the user broadcast.
//
// Idempotent: the app may retry, and a repeat report of the same hash is a
// no-op rather than a state reset.
func (s *ITService) ReportStableDeposit(ctx context.Context, intentID uuid.UUID, srcTxHash string) error {
	srcTxHash = strings.TrimSpace(srcTxHash)
	if srcTxHash == "" {
		return errors.New("it stable: empty deposit transaction hash")
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.DepositTxHash == srcTxHash && intent.Status != models.ITStatusPendingDeposit {
		return nil // already reported
	}
	if intent.Status != models.ITStatusPendingDeposit && intent.Status != models.ITStatusDepositReported {
		return ErrITBadState
	}
	return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
		"deposit_tx_hash": srcTxHash,
		"status":          models.ITStatusDepositReported,
	}).Error
}

// ConfirmStableDeposit proves the user's funds reached Flutterwave and prices
// the payout from what actually arrived.
//
// Two independent sources must agree before anything advances. LiFi resolves
// the source transaction to a destination transaction; the chain receipt then
// proves that transaction credited Flutterwave's address with the right token.
// A provider's claim alone never moves money.
//
// The payout is computed at the LOCKED rate on the RECEIVED amount. Both halves
// matter: the rate is locked because the user has already irreversibly
// broadcast and cannot walk away from a reprice, and the amount is the received
// one because a bridge delivers less than it was given — quoting the payout off
// the original amount would disburse money the platform never received.
func (s *ITService) ConfirmStableDeposit(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == models.ITStatusDepositConfirmed ||
		intent.Status == models.ITStatusConverting ||
		intent.Status == models.ITStatusDebited ||
		intent.Status == models.ITStatusSettled {
		return nil // already confirmed; webhooks and ticks both redeliver
	}
	if intent.Status != models.ITStatusDepositReported {
		return ErrITBadState
	}
	if s.stableLiFi == nil || s.stableChain == nil {
		return errors.New("it stable: deposit verification is not configured")
	}

	status, err := s.stableLiFi.Status(ctx, intent.DepositTxHash)
	if err != nil {
		return err
	}
	switch status.Status {
	case LiFiStatusDone:
	case LiFiStatusFailed:
		return fmt.Errorf("%w: %s", ErrLiFiFailed, intent.DepositTxHash)
	default:
		return fmt.Errorf("%w: %s", ErrLiFiPending, intent.DepositTxHash)
	}

	asset := models.ITIntentAsset(intent)
	token := s.stableTokens[asset]
	if token == "" {
		return fmt.Errorf("it stable: no token address configured for %s", asset)
	}
	fwAddr, err := s.stableDepositAddress(stableDepositChain)
	if err != nil {
		return err
	}

	received, err := s.stableChain.VerifyTransfer(ctx, status.DestTxHash, token, fwAddr)
	if err != nil {
		return fmt.Errorf("it stable: could not verify %s on-chain: %w", status.DestTxHash, err)
	}
	if received.Sign() <= 0 {
		return fmt.Errorf("%w: zero credited", ErrTransferNotFound)
	}

	// Scale the quoted net by how much actually arrived.
	//
	// Scaling rather than recomputing from the locked rate is deliberate: the
	// quoted net already has the itemised fee line deducted, and recomputing
	// rate × received would silently hand that fee back. Scaling keeps every
	// component of the quote proportional to what the user actually delivered.
	quoted, ok := new(big.Int).SetString(intent.AssetAmount, 10)
	if !ok || quoted.Sign() <= 0 {
		return errors.New("it stable: intent has no usable quoted amount")
	}
	quotedNet, ok := new(big.Int).SetString(intent.NetNgnKobo, 10)
	if !ok || quotedNet.Sign() <= 0 {
		return errors.New("it stable: intent has no usable quoted net")
	}
	net := new(big.Int).Quo(new(big.Int).Mul(quotedNet, received), quoted)
	if net.Sign() <= 0 {
		return fmt.Errorf("it stable: received amount %s leaves nothing to pay out", received)
	}

	return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
		"settled_tx_hash": status.DestTxHash,
		"received_amount": received.String(),
		"net_ngn_kobo":    net.String(),
		"status":          models.ITStatusDepositConfirmed,
	}).Error
}
