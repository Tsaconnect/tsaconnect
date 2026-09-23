package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// sell_intent_service.go — one sale in progress, from quote to offramp
// (design §5.1–§5.3).
//
// The shape of the flow: the server re-runs its OWN quote and stores the
// figures (so the client never states what it is owed), she signs that quote
// with her own key, she reports the hash, and only once LiFi AND the Base
// chain agree that USDC landed in HER wallet does a naira payout get opened —
// for exactly the amount the chain proves, never the amount anyone claims.
//
// Nothing here ever holds her funds. The whole value of this row is memory:
// which swap was hers, which quote it executed, and whether the payout has
// already been opened.

var (
	// ErrSellIntentOpen: she already has a sale in progress. One at a time,
	// so a second swap cannot be pointed at the same payout.
	ErrSellIntentOpen = errors.New("you already have a sale in progress")
	// ErrSellIntentState: the step doesn't apply to this sale — it is
	// finished, it already carries another swap, or the hash is malformed.
	ErrSellIntentState = errors.New("this sale can't take that step now")
	// ErrSwapNotLanded: LiFi hasn't reported the swap done yet. A wait, not a
	// failure.
	ErrSwapNotLanded = errors.New("your swap hasn't landed yet")
	// ErrSwapMismatch: the swap reported does not answer this intent's own
	// description of itself. Refused rather than investigated — §5.3.
	ErrSwapMismatch = errors.New("that swap doesn't match this sale")
	// ErrSellIntentNotFound: no such sale for this user. The same answer for
	// "does not exist" and "belongs to someone else", so an id cannot be
	// probed for existence.
	ErrSellIntentNotFound = errors.New("that sale can't be found")
	// ErrSellIntentOrdering: another request is opening this sale's payout
	// right now. A wait: retrying returns that order once it exists.
	ErrSellIntentOrdering = errors.New("your payout is being opened — try again in a moment")
	// ErrSwapFailed: LiFi reports the swap failed, was refunded or only
	// partly delivered (§5.5). Terminal for this sale; her funds are with her.
	ErrSwapFailed = errors.New("your swap didn't complete")
)

// sellIntentClaimStale is how long an ordering claim protects itself. Far
// beyond one Paycrest request (30s timeout); a claim older than this belongs
// to a caller that died between claiming and writing back.
const sellIntentClaimStale = 5 * time.Minute

// sellQuoter is the price this service re-runs for itself. *RampSellQuoter
// fills it; the interface exists so a quote can be faked without a live LiFi.
type sellQuoter interface {
	Quote(ctx context.Context, in SellQuoteInput) (*SellSwapQuote, error)
}

// lifiStatusReader is the "what became of that transaction" lookup.
// *LiFiStatusClient fills it.
type lifiStatusReader interface {
	Status(ctx context.Context, txHash string) (*LiFiTransferStatus, error)
}

// CreateSellIntentInput is what the app may say about a sale. Deliberately
// small: everything about the PRICE is the server's own, re-quoted here.
type CreateSellIntentInput struct {
	SourceChainID                                  int64
	SourceToken, SourceAmount, SourceAddress       string
	FiatCode, BankCode, AccountNumber, AccountName string
}

type SellIntentService struct {
	db     *gorm.DB
	quoter sellQuoter
	status lifiStatusReader
	// base proves on Base itself what landed. LiFi's status finds the
	// destination transaction; only the receipt says how much it moved.
	base   ERC20TransferVerifier
	orders *PaycrestOrderService
	// usdc is the settlement token's address on Base — config's, never the
	// client's.
	usdc string
	now  func() time.Time
	// fees books our integrator fee through the same engine All Swap settles
	// through. Optional: unwired, the fee is still collected by LiFi, it just
	// doesn't fund cashback/TP.
	fees ITFeeSink
}

func NewSellIntentService(db *gorm.DB, quoter sellQuoter, status lifiStatusReader, base ERC20TransferVerifier, orders *PaycrestOrderService, usdc string, now func() time.Time) *SellIntentService {
	if now == nil {
		now = time.Now
	}
	return &SellIntentService{db: db, quoter: quoter, status: status, base: base,
		orders: orders, usdc: strings.TrimSpace(usdc), now: now}
}

// SetFeeService wires the fee engine. ITFeeSink is reused as-is rather than
// redeclared: it is already the package's narrow "somewhere to book a settled
// fee" seam, and *FeeDistributionService satisfies it.
func (s *SellIntentService) SetFeeService(f ITFeeSink) { s.fees = f }

// rampSellFeeSourceType namespaces sell-intent fee distributions in the
// ledger, alongside "allswap", "it_intent", "p2p_release".
const rampSellFeeSourceType = "ramp_sell"

func (s *SellIntentService) ready() bool {
	return s != nil && s.db != nil && s.quoter != nil && s.status != nil &&
		s.base != nil && s.orders != nil && s.usdc != ""
}

// ─── Create ────────────────────────────────────────────────────────────────

// Create opens one sale: payout details first, then the server's own quote.
//
// The order matters and is the whole point of §5.1 — a bank we cannot pay is
// found BEFORE she signs anything away, because after the swap her tokens are
// gone and all we could offer is "finish this later, by hand".
func (s *SellIntentService) Create(ctx context.Context, user *models.User, in CreateSellIntentInput) (*models.RampSellIntent, *SellSwapQuote, error) {
	if !s.ready() || user == nil {
		return nil, nil, ErrSellUnavailable
	}
	open, err := s.Open(ctx, user.ID)
	if err != nil {
		return nil, nil, err
	}
	if open != nil {
		return nil, nil, ErrSellIntentOpen
	}

	fiat := strings.ToUpper(strings.TrimSpace(in.FiatCode))
	if fiat == "" {
		fiat = "NGN"
	}
	if err := s.orders.requirePaycrestEnabled(fiat); err != nil {
		return nil, nil, err
	}
	// Exactly the checks CreateSell runs, run here instead — see the doc
	// comment. The institution code they resolve to is not stored: the order
	// is opened later, from the bank code, through the same resolution.
	_, kind, err := s.orders.paycrestBankCode(ctx, fiat, in.BankCode)
	if err != nil {
		return nil, nil, err
	}
	if err := validatePayoutIdentifier(kind, fiat, in.AccountNumber); err != nil {
		return nil, nil, err
	}
	if err := s.orders.assertNoOpenOrder(ctx, user.ID, models.PaycrestDirectionSell); err != nil {
		return nil, nil, err
	}

	q, err := s.quoter.Quote(ctx, SellQuoteInput{
		FromChainID: in.SourceChainID, FromToken: in.SourceToken, FromAmount: in.SourceAmount,
		FromAddress: in.SourceAddress, WalletAddress: user.WalletAddress, FiatCode: fiat,
	})
	if err != nil {
		return nil, nil, err
	}
	if q == nil || q.Route == nil {
		return nil, nil, ErrSellUnavailable
	}

	row := &models.RampSellIntent{
		UserID:             user.ID,
		SourceAddress:      strings.TrimSpace(in.SourceAddress),
		WalletAddress:      user.WalletAddress,
		SourceChainID:      in.SourceChainID,
		SourceTokenAddress: strings.TrimSpace(in.SourceToken),
		SourceAmount:       strings.TrimSpace(in.SourceAmount),
		QuotedUSDC:         q.USDCAmount,
		QuotedFiat:         q.FiatEstimate,
		FiatCode:           fiat,
		FeeBps:             q.FeeBps,
		LiFiTransactionID:  q.Route.TransactionID,
		QuotedGasLimit:     q.Route.GasLimit,
		// Stored as the quote priced them, never re-derived from the route:
		// the top-up a sale may send is the one its admission paid for.
		QuotedTopUpWei: q.TopUpWei,
		TopUpBudgeted:  q.GasTopUp,
		QuotedFromUSD:  q.Route.FromAmountUSD,
		BankCode:       strings.TrimSpace(in.BankCode),
		AccountNumber:  strings.TrimSpace(in.AccountNumber),
		AccountName:    strings.TrimSpace(in.AccountName),
		Status:         models.SellIntentSwapping,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		// The partial unique index is the real one-open-intent rule: two
		// concurrent taps both read "none open" and only one insert survives.
		if isUniqueViolation(err) {
			return nil, nil, ErrSellIntentOpen
		}
		return nil, nil, err
	}
	return row, q, nil
}

// ─── The swap she signed ───────────────────────────────────────────────────

var (
	// evmTxHashRe is a 32-byte transaction hash, 0x-prefixed.
	evmTxHashRe = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	// solanaTxSigRe is a base58 transaction signature. Case-sensitive, and
	// deliberately never lower-cased anywhere in this file.
	solanaTxSigRe = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{64,88}$`)
)

// swapHashForChain normalises and checks a reported hash against the shape
// the source chain's family writes. ok is false for anything else — a
// malformed hash is a client bug, and looking it up would put an arbitrary
// string into an upstream request.
//
// Only the two families a sell can actually be QUOTED from have a shape here:
// EVM and Solana. Any other family (Bitcoin, Sui) is refused outright — the
// quote already refuses those sources, so no sale of theirs should exist.
func swapHashForChain(chainID int64, hash string) (string, bool) {
	hash = strings.TrimSpace(hash)
	switch addressFamily(chainID) {
	case "solana":
		return hash, solanaTxSigRe.MatchString(hash)
	case "evm":
		// Hex is case-insensitive but the unique index is not: one casing, so
		// the same swap cannot be recorded twice under two spellings.
		hash = strings.ToLower(hash)
		return hash, evmTxHashRe.MatchString(hash)
	}
	return hash, false
}

// RecordSwap attaches the transaction she broadcast to her sale.
//
// It is idempotent for the SAME hash (the app persists the hash locally and
// re-reports it after a kill) and refuses everything else: a second, different
// hash on this intent, and any hash already recorded on another one. The
// unique index is what actually enforces the second — one on-chain swap backs
// exactly one sale, or the same money could be sold twice.
func (s *SellIntentService) RecordSwap(ctx context.Context, userID, id uuid.UUID, txHash string) error {
	if !s.ready() {
		return ErrSellUnavailable
	}
	in, err := s.load(ctx, userID, id)
	if err != nil {
		return err
	}
	hash, ok := swapHashForChain(in.SourceChainID, txHash)
	if !ok {
		return fmt.Errorf("%w: that doesn't look like a transaction on that network", ErrSellIntentState)
	}
	if in.SwapTxHash != nil {
		if *in.SwapTxHash == hash {
			return nil
		}
		return fmt.Errorf("%w: this sale already has a swap", ErrSellIntentState)
	}
	if in.Status != models.SellIntentSwapping {
		return ErrSellIntentState
	}
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND user_id = ? AND status = ? AND swap_tx_hash IS NULL",
			id, userID, models.SellIntentSwapping).
		Updates(map[string]any{"swap_tx_hash": hash, "status": models.SellIntentSwapped, "updated_at": s.now()})
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			// That hash already backs a sale — this one's or someone else's.
			return fmt.Errorf("%w: that swap is already recorded", ErrSellIntentState)
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSellIntentState
	}
	return nil
}

// ─── Opening the offramp ───────────────────────────────────────────────────

// OpenOrder turns a landed swap into a naira payout, once.
//
// Three independent things have to agree before any order exists: the intent
// says which swap is its own, LiFi says that swap delivered USDC to her Base
// wallet as quoted (§5.3), and Base's own receipt says how much. The order is
// opened for the RECEIPT's amount — LiFi's figure is a lead, never the money.
//
// Idempotent under concurrency (§5.3): the row is claimed before Paycrest is
// called, so of any number of simultaneous calls exactly one opens an order;
// the others get that order back, or ErrSellIntentOrdering while it is still
// being opened.
func (s *SellIntentService) OpenOrder(ctx context.Context, user *models.User, id uuid.UUID) (*models.PaycrestOrderRecord, error) {
	if !s.ready() || user == nil {
		return nil, ErrSellUnavailable
	}
	in, err := s.load(ctx, user.ID, id)
	if err != nil {
		return nil, err
	}
	if in.PaycrestOrderID != nil {
		// Already sold. Hand back the order rather than opening a second one
		// against the same USDC.
		return s.orders.Get(ctx, user.ID, *in.PaycrestOrderID)
	}
	if in.SwapTxHash == nil || !in.Open() {
		return nil, ErrSellIntentState
	}
	if in.Status == models.SellIntentOrdering && !s.claimIsStale(in) {
		return nil, ErrSellIntentOrdering
	}

	st, err := s.status.Status(ctx, *in.SwapTxHash)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, ErrSwapNotLanded
	}
	// §5.5: a swap that ended without delivering what was quoted is a final
	// answer with LiFi's own reason, not a wait and not a mismatch alarm.
	// boundToIntent still demands COMPLETED; this only names the outcome.
	if st.Status == LiFiStatusFailed ||
		(st.Status == LiFiStatusDone && (st.Substatus == LiFiSubstatusRefunded || st.Substatus == LiFiSubstatusPartial)) {
		return nil, &SwapEndedShortError{Status: st.Status, Substatus: st.Substatus}
	}
	if st.Status != LiFiStatusDone {
		return nil, ErrSwapNotLanded
	}
	if !s.boundToIntent(in, st) {
		log.Printf("[ramp-sell] ALERT ramp_sell_swap_mismatch_total=1 intent=%s tx=%s status=%s/%s",
			in.ID, *in.SwapTxHash, st.Status, st.Substatus)
		return nil, ErrSwapMismatch
	}

	landed, err := s.base.VerifyTransfer(ctx, st.DestTxHash, s.usdc, in.WalletAddress)
	if err != nil {
		return nil, err
	}
	if landed == nil || landed.Sign() <= 0 {
		return nil, fmt.Errorf("%w: nothing was credited by %s", ErrSwapMismatch, st.DestTxHash)
	}

	// Claim the row BEFORE Paycrest is asked for anything. The conditional
	// write is the claim: of two concurrent callers exactly one moves the
	// status it read to ordering, and only that one opens an offramp. The
	// same shape as the IT payout's attempt claim.
	claimed, err := s.claim(ctx, in)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return s.lostClaim(ctx, user.ID, in.ID)
	}

	order, err := s.orders.CreateSellForToken(ctx, PaycrestSellRequest{
		UserID:        user.ID,
		WalletAddress: in.WalletAddress,
		FiatCode:      in.FiatCode,
		ChainKey:      rampSettlementChainKey,
		Token:         rampSettlementToken,
		BankCode:      in.BankCode,
		AccountNo:     in.AccountNumber,
		AccountName:   in.AccountName,
	}, BaseUnitsToDecimal(landed, 6), true)
	if err != nil {
		// No offramp exists: give the claim back so she can simply retry.
		s.releaseClaim(ctx, in.ID)
		return nil, err
	}

	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND status = ? AND paycrest_order_id IS NULL", in.ID, models.SellIntentOrdering).
		Updates(map[string]any{
			"landed_usdc":       BaseUnitsToDecimal(landed, 6),
			"paycrest_order_id": order.ID,
			"status":            models.SellIntentOrdered,
			"updated_at":        s.now(),
		})
	if res.Error != nil || res.RowsAffected == 0 {
		// We held the claim, so this is a DB failure (or a claim that sat past
		// the stale window). The offramp exists upstream either way; losing
		// the row is an operator problem, not a reason to fail her sale.
		log.Printf("[ramp-sell] ALARM opened offramp %s for intent %s but could not record it (rows=%d): %v",
			order.ID, in.ID, res.RowsAffected, res.Error)
		return order, nil
	}
	s.bookFee(in, user.ID)
	return order, nil
}

// claim moves the intent from the status this caller read to ordering. false
// means another caller changed it first. A stale ordering claim can be taken
// over; the one-live-sell check inside CreateSellForToken is what stops a
// takeover from opening a second offramp if the dead holder's did get opened.
func (s *SellIntentService) claim(ctx context.Context, in *models.RampSellIntent) (bool, error) {
	q := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND status = ? AND paycrest_order_id IS NULL", in.ID, in.Status)
	if in.Status == models.SellIntentOrdering {
		q = q.Where("updated_at < ?", s.now().Add(-sellIntentClaimStale))
	}
	res := q.Updates(map[string]any{"status": models.SellIntentOrdering, "updated_at": s.now()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// releaseClaim returns an ordering intent to swapped (it has a swap recorded,
// or it could not have been claimed) after its order failed to open. It runs
// even if the request was cancelled — a failure caused by her disconnecting
// must not leave the sale stuck in ordering for the stale window.
func (s *SellIntentService) releaseClaim(ctx context.Context, id uuid.UUID) {
	res := s.db.WithContext(context.WithoutCancel(ctx)).Model(&models.RampSellIntent{}).
		Where("id = ? AND status = ? AND paycrest_order_id IS NULL", id, models.SellIntentOrdering).
		Updates(map[string]any{"status": models.SellIntentSwapped, "updated_at": s.now()})
	if res.Error != nil {
		log.Printf("[ramp-sell] could not release the ordering claim on intent %s: %v — it frees itself after %s",
			id, res.Error, sellIntentClaimStale)
	}
}

// lostClaim answers the caller that lost the race: the winner's order if it
// is already recorded, otherwise "being opened, retry". A row back at swapped
// was claimed and released in between — nothing is open, so that is a retry
// too, not a dead sale.
func (s *SellIntentService) lostClaim(ctx context.Context, userID, id uuid.UUID) (*models.PaycrestOrderRecord, error) {
	now, err := s.load(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if now.PaycrestOrderID != nil {
		return s.orders.Get(ctx, userID, *now.PaycrestOrderID)
	}
	if now.Status == models.SellIntentOrdering || now.Status == models.SellIntentSwapped {
		return nil, ErrSellIntentOrdering
	}
	return nil, ErrSellIntentState
}

func (s *SellIntentService) claimIsStale(in *models.RampSellIntent) bool {
	return in.UpdatedAt.Before(s.now().Add(-sellIntentClaimStale))
}

// SwapEndedShortError is ErrSwapFailed with how LiFi says the swap ended
// (§5.5), so a caller can tell a refund from a partial fill without reading
// the message. errors.Is(err, ErrSwapFailed) still holds.
type SwapEndedShortError struct{ Status, Substatus string }

func (e *SwapEndedShortError) Error() string {
	return fmt.Sprintf("%s (%s)", ErrSwapFailed, lifiOutcome(&LiFiTransferStatus{Status: e.Status, Substatus: e.Substatus}))
}

func (e *SwapEndedShortError) Unwrap() error { return ErrSwapFailed }

// lifiOutcome is LiFi's own description of how a swap ended, for §5.5.
func lifiOutcome(st *LiFiTransferStatus) string {
	if st.Substatus != "" {
		return st.Status + "/" + st.Substatus
	}
	return st.Status
}

// boundToIntent is §5.3: what proves this on-chain swap is the one THIS user
// opened THIS sale for.
//
// Every clause answers a different way of being someone else's swap, and
// none of them is optional:
//
//   - COMPLETED, not merely DONE — a partial fill or a refund is also DONE,
//     and neither delivered what was quoted.
//   - The destination is USDC on Base, in HER wallet. Without this, any swap
//     to anywhere could fund a payout to her bank.
//   - The sending chain, token, amount and sender are the intent's own. This
//     is what stops a swap she made for some other purpose — or a swap of a
//     tenth the size — from being claimed against a quote for more.
//   - LiFi's transactionId is the one our quote carried, so she executed OUR
//     route, integrator fee included, not one she assembled herself.
//
// An unstated field never satisfies a clause: "" and nil are unknowns, and an
// unknown must not bind money.
func (s *SellIntentService) boundToIntent(in *models.RampSellIntent, st *LiFiTransferStatus) bool {
	if st.Substatus != LiFiSubstatusCompleted || strings.TrimSpace(st.DestTxHash) == "" {
		return false
	}
	if st.DestChainID != rampSettlementChainID || !sameTokenAddress(st.ReceivingToken, s.usdc) {
		return false
	}
	if !sameAddress(st.ToAddress, in.WalletAddress) {
		return false
	}
	if st.SendingChainID != in.SourceChainID || !sameTokenAddress(st.SendingToken, in.SourceTokenAddress) {
		return false
	}
	want, ok := new(big.Int).SetString(strings.TrimSpace(in.SourceAmount), 10)
	if !ok || st.SendingAmount == nil || st.SendingAmount.Cmp(want) != 0 {
		return false
	}
	if !sameAddress(st.FromAddress, in.SourceAddress) {
		return false
	}
	// A non-EVM route may carry no transactionId at all; the clauses above
	// are what bind those. When our quote HAD one, it must be the one.
	if in.LiFiTransactionID != "" && !strings.EqualFold(strings.TrimSpace(st.TransactionID), in.LiFiTransactionID) {
		return false
	}
	return true
}

// sameAddress compares two addresses where neither being stated is a
// mismatch, not a match. EqualFold alone would answer true for two empties.
func sameAddress(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

// sameTokenAddress is sameAddress plus the one equivalence that is real: a
// chain's native coin has two accepted spellings and LiFi picks either.
func sameTokenAddress(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	if isNativeTokenAddress(a) && isNativeTokenAddress(b) {
		return true
	}
	return sameAddress(a, b)
}

// bookFee books what we kept on this sale: the integrator fee LiFi collected
// for us, as bps of the route's own USD input. Through All Swap's engine call,
// keyed on the intent, so a replay is a guard-row no-op rather than a second
// accrual.
//
// Only when the swap was bound to our quote's transactionId. Without one (a
// Solana source today) boundToIntent cannot tell our fee-carrying route from
// an equal-amount route she assembled without it, so there is no evidence the
// fee was ever collected — and booking it would pay real cashback out of a
// fee we may never have received.
func (s *SellIntentService) bookFee(in *models.RampSellIntent, userID uuid.UUID) {
	if s.fees == nil || in.FeeBps <= 0 || in.QuotedFromUSD <= 0 {
		return
	}
	if strings.TrimSpace(in.LiFiTransactionID) == "" {
		log.Printf("[ramp-sell] fee not booked for intent %s: no LiFi transactionId bound the swap to our quote", in.ID)
		return
	}
	s.fees.EnqueueFeeSettled(FeeEvent{
		Feature:    models.FeatureAllswap,
		Token:      rampSettlementToken,
		FeeUSD:     in.QuotedFromUSD * float64(in.FeeBps) / 10_000,
		UserID:     userID,
		SourceType: rampSellFeeSourceType,
		SourceID:   in.ID,
	})
}

// ─── Reading and ending a sale ─────────────────────────────────────────────

// Abandon ends an open sale. She keeps whatever she holds — if the swap
// landed, the USDC is already hers on Base.
func (s *SellIntentService) Abandon(ctx context.Context, userID, id uuid.UUID) error {
	if !s.ready() {
		return ErrSellUnavailable
	}
	// Not while ordering: an offramp is being opened for it right now.
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND user_id = ? AND status IN ?", id, userID,
			[]string{models.SellIntentSwapping, models.SellIntentSwapped}).
		Updates(map[string]any{"status": models.SellIntentFailed, "updated_at": s.now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSellIntentState
	}
	return nil
}

// Open is the user's sale in progress, or nil. Newest first, because the
// partial unique index makes a second one impossible in production and a
// deterministic answer is still wanted if one ever exists.
func (s *SellIntentService) Open(ctx context.Context, userID uuid.UUID) (*models.RampSellIntent, error) {
	if s == nil || s.db == nil {
		return nil, ErrSellUnavailable
	}
	var in models.RampSellIntent
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND status IN ?", userID, models.SellIntentOpenStatuses).
		Order("created_at DESC").First(&in).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

// Resumable is the sale "Finish your sale" offers: her open sale if she has
// one, otherwise her newest ORDERED sale whose payout still awaits the USDC
// deposit — a sale killed between opening the payout and sending into it.
//
// A separate query rather than a widening of SellIntentOpenStatuses: ordered
// is deliberately not "open" (the one-open-sale index and Create's check are
// built on that), it is only still worth resuming while its order waits.
func (s *SellIntentService) Resumable(ctx context.Context, userID uuid.UUID) (*models.RampSellIntent, error) {
	open, err := s.Open(ctx, userID)
	if err != nil || open != nil {
		return open, err
	}
	waiting := s.db.Model(&models.PaycrestOrderRecord{}).Select("id").
		Where("user_id = ? AND status = ?", userID, models.PaycrestStatusAwaitingDeposit)
	var in models.RampSellIntent
	err = s.db.WithContext(ctx).
		Where("user_id = ? AND status = ? AND paycrest_order_id IN (?)", userID, models.SellIntentOrdered, waiting).
		Order("created_at DESC").First(&in).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

// Get is one of her sales, by id AND owner (see load).
func (s *SellIntentService) Get(ctx context.Context, userID, id uuid.UUID) (*models.RampSellIntent, error) {
	if s == nil || s.db == nil {
		return nil, ErrSellUnavailable
	}
	return s.load(ctx, userID, id)
}

// ─── Gas top-up bookkeeping (GasTopUp.TopUpForIntent's IntentTopUpStore) ────

// CountToppedUpSince is how many of her sales were topped up at or after
// since, used or not — the daily cap.
func (s *SellIntentService) CountToppedUpSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("user_id = ? AND topped_up_at IS NOT NULL AND topped_up_at >= ?", userID, since).
		Count(&n).Error
	return n, err
}

// CountUnusedSince is how many topped-up sales of hers ended without a swap
// since `since` — gas we paid for that nothing used, the farming signal.
func (s *SellIntentService) CountUnusedSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("user_id = ? AND status = ? AND swap_tx_hash IS NULL AND topped_up_at IS NOT NULL AND updated_at >= ?",
			userID, models.SellIntentFailed, since).
		Count(&n).Error
	return n, err
}

// MarkToppedUp is the once-per-intent claim: true iff this caller set
// topped_up_at, so two concurrent requests cannot both send gas for one sale.
// Eligibility is re-checked on the row itself: the caller's copy may be stale,
// and a sale swapped or abandoned since it was loaded must not win the claim.
func (s *SellIntentService) MarkToppedUp(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND topped_up_at IS NULL AND status = ? AND swap_tx_hash IS NULL", id, models.SellIntentSwapping).
		Update("topped_up_at", at)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ClearToppedUp gives the claim back, for a top-up that provably never left.
func (s *SellIntentService) ClearToppedUp(ctx context.Context, id uuid.UUID) error {
	return s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ?", id).
		Update("topped_up_at", nil).Error
}

// RecordTopUp writes down the top-up a claimed sale was actually sent.
func (s *SellIntentService) RecordTopUp(ctx context.Context, id uuid.UUID, txHash, amountWei string) error {
	return s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ?", id).
		Updates(map[string]any{"top_up_tx_hash": txHash, "top_up_wei": amountWei}).Error
}

// load reads one intent BY ID AND OWNER. Never by id alone: every step of a
// sale is driven by an id the client holds, and a row fetched without its
// owner would let anyone who learns an id drive someone else's sale.
func (s *SellIntentService) load(ctx context.Context, userID, id uuid.UUID) (*models.RampSellIntent, error) {
	var in models.RampSellIntent
	err := s.db.WithContext(ctx).First(&in, "id = ? AND user_id = ?", id, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSellIntentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}
