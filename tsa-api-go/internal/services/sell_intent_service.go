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
	// The rest are set only by the pay checkout, never from the app's own
	// request: a sale that pays SOMEONE ELSE an exact amount.
	//
	// PayPaymentID names the payment it settles. PayoutFiat is the exact naira
	// the recipient is owed. ToAmount is the exact amount the swap must land
	// (USDC on Base, 6dp base units, unless a Dest* target says otherwise);
	// with it the quote is exact-output, SourceAmount is ignored, and the
	// intent stores what the route says it costs instead.
	PayPaymentID         *uuid.UUID
	PayoutFiat, ToAmount string
	// A DELIVERY sale (TSA Pay's crypto route) hands a token to a merchant's
	// wallet instead of opening an offramp: DestAddress set means no bank
	// details are needed and no Paycrest order is ever made. DestAmount is the
	// least the merchant must be credited, in the token's base units — what
	// ConfirmDelivery proves on-chain; ToAmount is that owed amount padded for
	// slippage, so the route's own minimum still covers it. BudgetBps is the
	// whole fee the round trip may spend (there is no Paycrest leg to share it
	// with); an offramp leaves it zero and is priced by the quoter.
	DestChainID                               int64
	DestTokenAddress, DestAddress, DestAmount string
	BudgetBps                                 int
	// ExpectedToUSD is what ToAmount is worth at the price the caller sized it
	// from; when set, a delivery is refused unless the route's own value for
	// what it delivers agrees (deliveryValueBand).
	ExpectedToUSD float64
	// Purpose and ReferenceID mark a delivery that answers to something other
	// than a TSA Pay payment (models.RampSellIntent.Purpose): a marketplace
	// checkout funds its group of orders this way. A delivery needs one of
	// PayPaymentID or ReferenceID — something it is FOR.
	Purpose     string
	ReferenceID *uuid.UUID
	// PassThrough prices the route at cost (SellQuoteInput.PassThrough); the
	// fee it carries is stored as gas recovery, never as our fee.
	PassThrough bool
	// DestGasUSD / DestGasNeedWei are a destination-chain gas top-up the
	// caller priced: DestGasUSD goes into the quote's g, DestGasNeedWei (what
	// her wallet must hold there) is stored for the top-up to send against.
	DestGasUSD     float64
	DestGasNeedWei string
	// DestTopUpMaxWei is the shortfall that top-up was priced at: the most it
	// may ever send (models.RampSellIntent.DestTopUpMaxWei).
	DestTopUpMaxWei string
}

// delivery reports a request for a sale that hands a token to someone else's
// wallet (see RampSellIntent.IsDelivery for the stored form).
func (in CreateSellIntentInput) delivery() bool { return strings.TrimSpace(in.DestAddress) != "" }

type SellIntentService struct {
	db     *gorm.DB
	quoter sellQuoter
	status lifiStatusReader
	// base proves on Base itself what landed. LiFi's status finds the
	// destination transaction; only the receipt says how much it moved. Only an
	// offramp reads it: nil (with orders) leaves the service delivery-only —
	// the shape the marketplace's funding runs in when Paycrest is off.
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
	// usdcBalance reads her USDC on Base — only a pay intent's "use my wallet
	// balance too" consent ever asks for it.
	usdcBalance func(ctx context.Context, wallet string) (*big.Int, error)
	// destVerifiers prove a delivery sale's landing on ITS chain, keyed by
	// chain key, with chainName resolving LiFi's numeric id to that key. Both
	// are the ramp's own maps, never the client's word for a chain.
	destVerifiers map[string]ERC20TransferVerifier
	chainName     func(int64) (string, bool)
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

// SetUSDCBalanceReader wires the Base USDC balance read a pay intent's wallet
// consent needs. Unset, that consent is refused rather than assumed.
func (s *SellIntentService) SetUSDCBalanceReader(f func(ctx context.Context, wallet string) (*big.Int, error)) {
	s.usdcBalance = f
}

// SetDestVerifiers wires the per-chain on-chain proofs a delivery sale's
// landing needs (the ramp's rampTransferVerifiers map) and the resolver from a
// LiFi chain id to that map's key. Unset, no delivery can ever be confirmed:
// ConfirmDelivery refuses rather than take LiFi's word that a merchant was paid.
func (s *SellIntentService) SetDestVerifiers(v map[string]ERC20TransferVerifier, chainName func(int64) (string, bool)) {
	s.destVerifiers, s.chainName = v, chainName
}

// rampSellFeeSourceType namespaces sell-intent fee distributions in the
// ledger, alongside "allswap", "it_intent", "p2p_release". A delivery sale's
// own is tsaPayFeeSourceType, a marketplace checkout's marketplaceFeeSourceType.
const (
	rampSellFeeSourceType    = "ramp_sell"
	tsaPayFeeSourceType      = "tsa_pay"
	marketplaceFeeSourceType = "marketplace"
)

// deliveryFeeSourceType is the ledger source a delivery's fee is booked under.
func deliveryFeeSourceType(in *models.RampSellIntent) string {
	if in.IsMarketplace() {
		return marketplaceFeeSourceType
	}
	return tsaPayFeeSourceType
}

// ready: every step of a delivery can run. An offramp also needs offrampReady.
func (s *SellIntentService) ready() bool {
	return s != nil && s.db != nil && s.quoter != nil && s.status != nil
}

// offrampReady: the Paycrest leg and the Base landing proof are wired.
func (s *SellIntentService) offrampReady() bool {
	return s.ready() && s.base != nil && s.orders != nil && s.usdc != ""
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
	asked, err := exactOutputAsked(in)
	if err != nil {
		return nil, nil, err
	}
	purpose := strings.TrimSpace(in.Purpose)
	open, err := s.OpenFor(ctx, user.ID, purpose)
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
	delivery := in.delivery()
	if delivery {
		// No bank and no Paycrest order: what has to be described in full is
		// what ConfirmDelivery will later prove on-chain.
		if err := in.checkDelivery(); err != nil {
			return nil, nil, err
		}
		// And it must be provable where it lands: a swap that pays the merchant
		// on a chain nothing here can read a receipt for is confirmed never.
		if _, ok := s.destVerifier(in.DestChainID); !ok {
			return nil, nil, fmt.Errorf("%w: no way to prove a delivery on chain %d", ErrSellUnavailable, in.DestChainID)
		}
	} else {
		if !s.offrampReady() {
			return nil, nil, ErrSellUnavailable
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
	}

	payoutFiat := strings.TrimSpace(in.PayoutFiat)
	if payoutFiat != "" {
		if _, ok := parsePositiveDecimal(payoutFiat); !ok {
			return nil, nil, fmt.Errorf("%w: %q is not a naira amount", ErrSellIntentState, payoutFiat)
		}
	}
	quoteIn := SellQuoteInput{
		FromChainID: in.SourceChainID, FromToken: in.SourceToken, FromAmount: in.SourceAmount,
		FromAddress: in.SourceAddress, WalletAddress: user.WalletAddress, FiatCode: fiat,
		ToChainID: in.DestChainID, ToToken: strings.TrimSpace(in.DestTokenAddress),
		ToAddress: strings.TrimSpace(in.DestAddress), BudgetBps: in.BudgetBps, Delivery: delivery,
		PassThrough: in.PassThrough, DestGasUSD: in.DestGasUSD,
		// A TSA Pay payment's payout keeps the payment's own split; every
		// other sale is a ramp sale, priced at the take plus cost.
		TSAPay: in.PayPaymentID != nil,
	}
	sourceAmount := strings.TrimSpace(in.SourceAmount)
	exactOutput := asked != nil
	if exactOutput {
		quoteIn.FromAmount, quoteIn.ToAmount = "", strings.TrimSpace(in.ToAmount)
	}

	q, err := s.quoter.Quote(ctx, quoteIn)
	if err != nil {
		return nil, nil, err
	}
	if q == nil || q.Route == nil {
		return nil, nil, ErrSellUnavailable
	}
	if exactOutput {
		// What she signs is the route's own fromAmount, and boundToIntent
		// later demands the swap sent exactly that: an exact-output route that
		// does not say what it spends leaves nothing to bind the swap to.
		if amt, ok := new(big.Int).SetString(q.Route.FromAmount, 10); !ok || amt.Sign() <= 0 {
			return nil, nil, fmt.Errorf("%w: the route did not say what it spends", ErrSellUnavailable)
		}
		sourceAmount = q.Route.FromAmount
		// And it must actually deliver what the payment needs. LiFi's
		// toAmountMin below the ask is expected — the caller pads the ask so
		// the floor still covers what is owed, and the consented-shortfall
		// path backstops the rest — but a route that promises LESS than was
		// asked, or promises nothing, is refused before she signs anything.
		if q.Route.ToAmount == nil || q.Route.ToAmount.Cmp(asked) < 0 ||
			q.Route.ToAmountMin == nil || q.Route.ToAmountMin.Sign() <= 0 {
			return nil, nil, fmt.Errorf("%w: the route does not deliver the amount asked", ErrSellUnavailable)
		}
		if delivery {
			if err := checkDeliveryRoute(in, q.Route); err != nil {
				return nil, nil, err
			}
		}
	}

	feeBps, gasRecoveryBps := q.FeeBps, 0
	if in.PassThrough || q.PassThrough {
		// At cost: what the route carries recovers gas we pay, and is not ours.
		feeBps, gasRecoveryBps = 0, q.FeeBps
	}
	destNeed := strings.TrimSpace(in.DestGasNeedWei)
	row := &models.RampSellIntent{
		UserID:             user.ID,
		SourceAddress:      strings.TrimSpace(in.SourceAddress),
		WalletAddress:      user.WalletAddress,
		SourceChainID:      in.SourceChainID,
		SourceTokenAddress: strings.TrimSpace(in.SourceToken),
		SourceAmount:       sourceAmount,
		PayPaymentID:       in.PayPaymentID,
		PayoutFiat:         payoutFiat,
		Purpose:            purpose,
		ReferenceID:        in.ReferenceID,
		GasRecoveryBps:     gasRecoveryBps,
		DestTopUpBudgeted:  destNeed != "" && in.DestGasUSD > 0,
		DestGasNeedWei:     destNeed,
		DestTopUpMaxWei:    strings.TrimSpace(in.DestTopUpMaxWei),
		DestChainID:        in.DestChainID,
		DestTokenAddress:   strings.TrimSpace(in.DestTokenAddress),
		DestAddress:        strings.TrimSpace(in.DestAddress),
		DestAmount:         strings.TrimSpace(in.DestAmount),
		QuotedUSDC:         q.USDCAmount,
		QuotedFiat:         q.FiatEstimate,
		FiatCode:           fiat,
		FeeBps:             feeBps,
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

// exactOutputAsked validates the pay-checkout fields as a set and returns the exact
// output asked of the swap (nil for an ordinary sale). A naira pay sale needs
// PayoutFiat, ToAmount and PayPaymentID together; a delivery sale (DestAddress
// set) pays no naira, so it needs ToAmount and what it is for — a PayPaymentID,
// or a Purpose with its ReferenceID — and must carry no PayoutFiat. Partial
// sets are refused: a PayoutFiat without a PayPaymentID would take the pay path
// in OpenOrder but escape the Abandon guard, and a ToAmount alone would change
// the quote and nothing else. A Purpose is a delivery's alone.
func exactOutputAsked(in CreateSellIntentInput) (*big.Int, error) {
	fiat, toAmount := strings.TrimSpace(in.PayoutFiat), strings.TrimSpace(in.ToAmount)
	delivery := in.delivery()
	purpose := strings.TrimSpace(in.Purpose)
	if fiat == "" && toAmount == "" && in.PayPaymentID == nil && in.ReferenceID == nil && purpose == "" && !delivery {
		return nil, nil
	}
	referenced := purpose != "" && in.ReferenceID != nil && in.PayPaymentID == nil
	switch {
	case delivery && fiat != "":
		return nil, fmt.Errorf("%w: a delivery sale pays no naira", ErrSellIntentState)
	case (purpose != "" || in.ReferenceID != nil) && (!delivery || !referenced):
		return nil, fmt.Errorf("%w: a purpose is a delivery's, with its reference and no payment", ErrSellIntentState)
	case toAmount == "" || (in.PayPaymentID == nil && !referenced) || (!delivery && fiat == ""):
		return nil, fmt.Errorf("%w: a pay sale needs its payment, its amount to deliver and, for naira, its payout together", ErrSellIntentState)
	}
	asked, ok := new(big.Int).SetString(toAmount, 10)
	if !ok || asked.Sign() <= 0 {
		return nil, fmt.Errorf("%w: %q is not an amount to swap for", ErrSellIntentState, toAmount)
	}
	return asked, nil
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
		Updates(map[string]any{"swap_tx_hash": hash, "status": models.SellIntentSwapped, "swap_recorded_at": s.now(), "updated_at": s.now()})
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
//
// A pay intent (PayoutFiat set) is opened for the exact naira it owes, not for
// what landed: the binding and the landing proof are the same, but the deposit
// is sized from the naira and capped at the proven landed USDC — or, when she
// consents with useWalletBalance, at her whole Base USDC balance. A cap the
// deposit exceeds is a SellShortfallError. An ordinary sale ignores the flag.
func (s *SellIntentService) OpenOrder(ctx context.Context, user *models.User, id uuid.UUID, useWalletBalance bool) (*models.PaycrestOrderRecord, error) {
	if !s.offrampReady() || user == nil {
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
	if in.IsDelivery() {
		// A delivery sale has no offramp. The landing proof below reads HER
		// wallet's USDC; letting it run here could open a naira order the sale
		// never owed. ConfirmDelivery is its only way forward.
		return nil, ErrSellIntentState
	}
	if in.Status == models.SellIntentOrdering && (in.PayPaymentID != nil || !s.claimIsStale(in)) {
		// A pay intent's dead claim is never taken over here: an order that pays
		// the merchant may exist behind it, and only the pay reconciler (which
		// looks for that order, and ends the intent before failing the payment)
		// may decide what became of it.
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
	if err := checkSwapBinding(st, in, rampSettlementChainID, s.usdc); err != nil {
		log.Printf("[ramp-sell] ALERT ramp_sell_swap_mismatch_total=1 intent=%s tx=%s status=%s/%s: %v",
			in.ID, *in.SwapTxHash, st.Status, st.Substatus, err)
		return nil, err
	}

	landed, err := s.base.VerifyTransfer(ctx, st.DestTxHash, s.usdc, in.WalletAddress)
	if err != nil {
		return nil, err
	}
	if landed == nil || landed.Sign() <= 0 {
		return nil, fmt.Errorf("%w: nothing was credited by %s", ErrSwapMismatch, st.DestTxHash)
	}
	// Read before the claim, so a balance that cannot be read leaves nothing
	// to give back.
	payCap := landed
	if in.IsNairaPay() && useWalletBalance {
		if s.usdcBalance == nil {
			return nil, ErrSellUnavailable
		}
		balance, err := s.usdcBalance(ctx, in.WalletAddress)
		if err != nil {
			return nil, err
		}
		// Consent widens the cap, never narrows it: what landed is already in
		// her wallet, so a balance under it is a stale read, not a shortfall.
		if balance.Cmp(payCap) > 0 {
			payCap = balance
		}
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

	sellReq := PaycrestSellRequest{
		UserID:        user.ID,
		WalletAddress: in.WalletAddress,
		FiatCode:      in.FiatCode,
		ChainKey:      rampSettlementChainKey,
		Token:         rampSettlementToken,
		BankCode:      in.BankCode,
		AccountNo:     in.AccountNumber,
		AccountName:   in.AccountName,
		PayPaymentID:  in.PayPaymentID,
	}
	var order *models.PaycrestOrderRecord
	if in.IsNairaPay() {
		sellReq.FiatAmount = in.PayoutFiat
		order, err = s.orders.CreateSellForFiat(ctx, sellReq, true, payCap)
	} else {
		order, err = s.orders.CreateSellForToken(ctx, sellReq, BaseUnitsToDecimal(landed, 6))
	}
	if err != nil {
		if errors.Is(err, ErrSellAttemptMayExist) || s.payOrderIsOpen(ctx, in, err) {
			// An order may be open — a short one that could not be cancelled, or
			// one Paycrest opened that could not be recorded. Giving the claim back
			// would invite a retry against it; leave it `ordering`, so the intent
			// stays out of any release path until the claim goes stale and the pay
			// reconciler (which looks for that order) takes it from there.
			log.Printf("[ramp-sell] ALERT ramp_sell_attempt_may_exist_total=1 intent=%s: %v", in.ID, err)
			return nil, err
		}
		// No offramp exists: give the claim back so she can simply retry.
		s.releaseClaim(ctx, in.ID)
		return nil, err
	}

	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND status = ? AND paycrest_order_id IS NULL", in.ID, models.SellIntentOrdering).
		// The order now carries its own copy of her bank details; the
		// intent's goes in this same write.
		Updates(models.WithBankDetailsCleared(map[string]any{
			"landed_usdc":       BaseUnitsToDecimal(landed, 6),
			"paycrest_order_id": order.ID,
			"status":            models.SellIntentOrdered,
			"updated_at":        s.now(),
		}))
	if res.Error != nil || res.RowsAffected == 0 {
		// We held the claim, so this is a DB failure (or a claim that sat past
		// the stale window). The offramp exists upstream either way; losing
		// the row is an operator problem, not a reason to fail her sale.
		log.Printf("[ramp-sell] ALARM opened offramp %s for intent %s but could not record it (rows=%d): %v",
			order.ID, in.ID, res.RowsAffected, res.Error)
		return order, nil
	}
	s.bookFee(in, user.ID, rampSellFeeSourceType)
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
		if in.PayPaymentID != nil {
			return false, nil // never taken over: see OpenOrder
		}
		q = q.Where("updated_at < ?", s.now().Add(-sellIntentClaimStale))
	}
	res := q.Updates(map[string]any{"status": models.SellIntentOrdering, "updated_at": s.now()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// payOrderIsOpen reports that a pay intent's order failed to open BECAUSE one of
// this payment's own orders is already open (the answer to a retry after an
// earlier attempt that may have left one): giving the claim back would hand the
// intent to the swapped rules while that order is still fundable. The order is
// found by the payment marker on the order record itself — the ramp's own table.
func (s *SellIntentService) payOrderIsOpen(ctx context.Context, in *models.RampSellIntent, cause error) bool {
	if in.PayPaymentID == nil || !errors.Is(cause, ErrPaycrestOrderOpen) {
		return false
	}
	var n int64
	err := s.db.WithContext(context.WithoutCancel(ctx)).Model(&models.PaycrestOrderRecord{}).
		Where("pay_payment_id = ? AND user_id = ? AND status IN ?", *in.PayPaymentID, in.UserID,
			[]string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusAwaitingDeposit,
				models.PaycrestStatusPending, models.PaycrestStatusSettling}).
		Count(&n).Error
	return err != nil || n > 0 // an unreadable answer keeps the claim: the safe side
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
	return sellClaimIsStale(in, s.now())
}

// sellClaimIsStale reports an ordering claim nobody has touched for
// sellIntentClaimStale as of now: its holder is presumed dead. The pay
// reconciler asks the same question of a payment's intent.
func sellClaimIsStale(in *models.RampSellIntent, now time.Time) bool {
	return in.UpdatedAt.Before(now.Add(-sellIntentClaimStale))
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

// checkSwapBinding is §5.3: what proves this on-chain swap is the one THIS user
// opened THIS sale for. A nil error means it is; every other answer is
// ErrSwapMismatch, wrapped with the clause that failed (for the alert, never
// for the client).
//
// Every clause answers a different way of being someone else's swap, and
// none of them is optional:
//
//   - COMPLETED, not merely DONE — a partial fill or a refund is also DONE,
//     and neither delivered what was quoted.
//   - The destination is where the sale said it delivers — in.Dest: USDC on
//     Base in HER wallet for an ordinary sale, the merchant's token, chain and
//     wallet for a delivery. Without this, any swap to anywhere could fund a
//     payout to her bank, or be reported as having paid a merchant.
//   - The sending chain, token, amount and sender are the intent's own. This
//     is what stops a swap she made for some other purpose — or a swap of a
//     tenth the size — from being claimed against a quote for more.
//   - LiFi's transactionId is the one our quote carried, so she executed OUR
//     route, integrator fee included, not one she assembled herself.
//
// An unstated field never satisfies a clause: "" and nil are unknowns, and an
// unknown must not bind money.
//
// baseChainID and baseUSDC are the ordinary sale's destination, the defaults
// in.Dest falls back to.
func checkSwapBinding(st *LiFiTransferStatus, in *models.RampSellIntent, baseChainID int64, baseUSDC string) error {
	mismatch := func(clause string) error { return fmt.Errorf("%w: %s", ErrSwapMismatch, clause) }
	if st.Substatus != LiFiSubstatusCompleted || strings.TrimSpace(st.DestTxHash) == "" {
		return mismatch("not completed, or no destination transaction")
	}
	destChain, destToken, destAddress := in.Dest(baseChainID, baseUSDC)
	if st.DestChainID != destChain || !sameTokenAddress(st.ReceivingToken, destToken) {
		return mismatch("delivered another token or chain")
	}
	if !sameAddress(st.ToAddress, destAddress) {
		return mismatch("delivered to another recipient")
	}
	if st.SendingChainID != in.SourceChainID || !sameTokenAddress(st.SendingToken, in.SourceTokenAddress) {
		return mismatch("sent from another chain or token")
	}
	want, ok := new(big.Int).SetString(strings.TrimSpace(in.SourceAmount), 10)
	if !ok || st.SendingAmount == nil || st.SendingAmount.Cmp(want) != 0 {
		return mismatch("sent another amount")
	}
	if !sameAddress(st.FromAddress, in.SourceAddress) {
		return mismatch("sent by another address")
	}
	// A non-EVM route may carry no transactionId at all; the clauses above
	// are what bind those. When our quote HAD one, it must be the one.
	if in.LiFiTransactionID != "" && !strings.EqualFold(strings.TrimSpace(st.TransactionID), in.LiFiTransactionID) {
		return mismatch("not our quote")
	}
	return nil
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
// accrual. sourceType says which flow the sale belongs to: ramp_sell for an
// offramp, tsa_pay for a delivery.
//
// Only when the swap was bound to our quote's transactionId. Without one (a
// Solana source today) checkSwapBinding cannot tell our fee-carrying route from
// an equal-amount route she assembled without it, so there is no evidence the
// fee was ever collected — and booking it would pay real cashback out of a
// fee we may never have received.
func (s *SellIntentService) bookFee(in *models.RampSellIntent, userID uuid.UUID, sourceType string) {
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
		SourceType: sourceType,
		SourceID:   in.ID,
	})
}

// ─── Reading and ending a sale ─────────────────────────────────────────────

// Abandon ends an open sale. She keeps whatever she holds — if the swap
// landed, the USDC is already hers on Base.
//
// A pay intent whose swap was sent cannot be abandoned by her: the swap may
// still deliver, and a freed payment could then be paid twice. Only the
// reconciler's AbandonForPay ends one of those. A marketplace funding swap is
// held the same way: once sent, only its reconciler (AbandonForReference) ends
// it, so she is never told to pay again while it may still land.
func (s *SellIntentService) Abandon(ctx context.Context, userID, id uuid.UUID) error {
	if !s.ready() {
		return ErrSellUnavailable
	}
	return s.failOpenSale(ctx, "id = ? AND user_id = ? AND ((pay_payment_id IS NULL AND reference_id IS NULL) OR swap_tx_hash IS NULL)", id, userID)
}

// AbandonForPay is the reconciler's path: it moves a pay intent that will
// never settle from open to failed, which frees the customer's one-open-sale
// slot. No owner check and no swap-sent guard on WHO may end it — but the swap
// the caller decided on is part of the write: swapTxHash is the hash the caller
// READ (nil = it saw none), and the sale is only ended if it still has exactly
// that one. A swap she signs between the caller's read and this write is
// therefore never abandoned out from under the payment; ErrSellIntentState
// tells the caller to look again.
func (s *SellIntentService) AbandonForPay(ctx context.Context, id uuid.UUID, swapTxHash *string) error {
	if !s.ready() {
		return ErrSellUnavailable
	}
	// pay_payment_id: this path is for pay intents only. Nothing else may end
	// an ordinary sale without its owner.
	if swapTxHash == nil {
		return s.failOpenSale(ctx, "id = ? AND pay_payment_id IS NOT NULL AND swap_tx_hash IS NULL", id)
	}
	return s.failOpenSale(ctx, "id = ? AND pay_payment_id IS NOT NULL AND swap_tx_hash = ?", id, *swapTxHash)
}

// AbandonForReference is the marketplace reconciler's AbandonForPay: it ends a
// funding intent that will never deliver, guarded on the swap the caller read
// (nil = none), so a swap reported since is never written off under it.
func (s *SellIntentService) AbandonForReference(ctx context.Context, id uuid.UUID, swapTxHash *string) error {
	if !s.ready() {
		return ErrSellUnavailable
	}
	if swapTxHash == nil {
		return s.failOpenSale(ctx, "id = ? AND reference_id IS NOT NULL AND swap_tx_hash IS NULL", id)
	}
	return s.failOpenSale(ctx, "id = ? AND reference_id IS NOT NULL AND swap_tx_hash = ?", id, *swapTxHash)
}

// failOpenSale fails the sale matched by cond, only while it is swapping or
// swapped. Not while ordering: an offramp is being opened for it right now.
func (s *SellIntentService) failOpenSale(ctx context.Context, cond string, args ...any) error {
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where(cond, args...).
		Where("status IN ?", []string{models.SellIntentSwapping, models.SellIntentSwapped}).
		// A dead sale pays nobody: her bank details go in the same write.
		Updates(models.WithBankDetailsCleared(map[string]any{"status": models.SellIntentFailed, "updated_at": s.now()}))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSellIntentState
	}
	return nil
}

// SwapStatus is LiFi's answer for the swap recorded on this intent — the
// reconciler's read, so it takes no owner. nil, nil when no swap was recorded.
func (s *SellIntentService) SwapStatus(ctx context.Context, id uuid.UUID) (*LiFiTransferStatus, error) {
	if !s.ready() {
		return nil, ErrSellUnavailable
	}
	var in models.RampSellIntent
	if err := s.db.WithContext(ctx).First(&in, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSellIntentNotFound
		}
		return nil, err
	}
	if in.SwapTxHash == nil {
		return nil, nil
	}
	return s.status.Status(ctx, *in.SwapTxHash)
}

// Open is the user's sale in progress, or nil — a sale or TSA Pay payment,
// never a marketplace checkout's funding (OpenFor names a purpose). Newest
// first, because the partial unique index makes a second one impossible in
// production and a deterministic answer is still wanted if one ever exists.
func (s *SellIntentService) Open(ctx context.Context, userID uuid.UUID) (*models.RampSellIntent, error) {
	return s.OpenFor(ctx, userID, "")
}

// OpenFor is the user's open intent of one purpose ("" = a sale or a TSA Pay
// payment), or nil: the one-open rule is per purpose.
func (s *SellIntentService) OpenFor(ctx context.Context, userID uuid.UUID, purpose string) (*models.RampSellIntent, error) {
	if s == nil || s.db == nil {
		return nil, ErrSellUnavailable
	}
	var in models.RampSellIntent
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND purpose = ? AND status IN ?", userID, purpose, models.SellIntentOpenStatuses).
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
		Where("user_id = ? AND purpose = '' AND status = ? AND paycrest_order_id IN (?)", userID, models.SellIntentOrdered, waiting).
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
// since, used or not — the daily cap. One budget across every purpose: a
// sale's, a TSA Pay payment's and a marketplace funding's source-chain top-ups
// all come from the same sponsor lanes, and farming is per person, not per
// feature. (A marketplace funding's DESTINATION top-up — S on Sonic — is its
// own lane and has its own cap: CountDestToppedUpSince.)
func (s *SellIntentService) CountToppedUpSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("user_id = ? AND topped_up_at IS NOT NULL AND topped_up_at >= ?", userID, since).
		Count(&n).Error
	return n, err
}

// CountUnusedSince is how many topped-up intents of hers failed since `since`
// with nothing to show for the gas we paid — the farming signal:
//   - no swap hash at all (every purpose), or
//   - a marketplace funding whose hash LiFi never saw from her (swap_seen_at is
//     tracked only there): a well-formed hash nobody broadcast must not make
//     sponsored gas look used.
//
// A swap LiFi did see that then failed or was refunded is an honest attempt
// that went wrong outside her control, and never counts: this budget is shared
// by sales, TSA Pay and marketplace funding, and two unlucky refunds must not
// throttle a real user's next top-up.
func (s *SellIntentService) CountUnusedSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("user_id = ? AND status = ? AND topped_up_at IS NOT NULL AND updated_at >= ?",
			userID, models.SellIntentFailed, since).
		Where("swap_tx_hash IS NULL OR (purpose = ? AND swap_seen_at IS NULL)", models.SellIntentPurposeMarketplace).
		Count(&n).Error
	return n, err
}

// MarkToppedUp is the once-per-intent claim: true iff this caller set
// topped_up_at, so two concurrent requests cannot both send gas for one sale.
// Eligibility is re-checked on the row itself: the caller's copy may be stale,
// and a sale swapped or abandoned since it was loaded must not win the claim.
//
// The UTC-day cap (intentTopUpDailyCap) is enforced HERE, in the same
// transaction as the claim, under a lock on her user row — the way the
// wallet-proof challenge cap is: GasTopUp's own count is an early refusal, but
// two concurrent top-ups for two of her sales could both pass it. Past the cap
// this answers ErrTopUpDailyCap and claims nothing.
func (s *SellIntentService) MarkToppedUp(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	return s.claimTopUp(ctx, id, at, "topped_up_at",
		"id = ? AND topped_up_at IS NULL AND status = ? AND swap_tx_hash IS NULL", id, models.SellIntentSwapping)
}

// claimTopUp is the capped once-per-intent claim both top-up kinds share: lock
// the owner's user row, count the column's claims since the start of `at`'s
// UTC day, refuse at the cap, else set the column on the row cond matches.
func (s *SellIntentService) claimTopUp(ctx context.Context, id uuid.UUID, at time.Time, column, cond string, args ...any) (bool, error) {
	won := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner struct{ UserID uuid.UUID }
		if err := tx.Model(&models.RampSellIntent{}).Select("user_id").Where("id = ?", id).Take(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := lockUserForUpdate(tx, owner.UserID); err != nil {
			return err
		}
		y, m, d := at.UTC().Date()
		var today int64
		if err := tx.Model(&models.RampSellIntent{}).
			Where("user_id = ? AND "+column+" IS NOT NULL AND "+column+" >= ?", owner.UserID, time.Date(y, m, d, 0, 0, 0, 0, time.UTC)).
			Count(&today).Error; err != nil {
			return err
		}
		if today >= intentTopUpDailyCap {
			return ErrTopUpDailyCap
		}
		res := tx.Model(&models.RampSellIntent{}).Where(cond, args...).Update(column, at)
		if res.Error != nil {
			return res.Error
		}
		won = res.RowsAffected == 1
		return nil
	})
	return won, err
}

// lockUserForUpdate serialises per-user claims on Postgres (SQLite, the test
// rig, serialises writes anyway and has no FOR UPDATE).
func lockUserForUpdate(tx *gorm.DB, userID uuid.UUID) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", userID).Error
}

// ClearToppedUp gives the claim back, for a top-up that provably never left.
func (s *SellIntentService) ClearToppedUp(ctx context.Context, id uuid.UUID) error {
	return s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ?", id).
		Update("topped_up_at", nil).Error
}

// RecordTopUp writes down the top-up a claimed sale was actually sent.
// Inside a key lane hook, LaneDB is the lane's allocation transaction.
func (s *SellIntentService) RecordTopUp(ctx context.Context, id uuid.UUID, txHash, amountWei string) error {
	return LaneDB(ctx, s.db).Model(&models.RampSellIntent{}).
		Where("id = ?", id).
		Updates(map[string]any{"top_up_tx_hash": txHash, "top_up_wei": amountWei}).Error
}

// ─── Destination gas top-up bookkeeping (GasTopUp.TopUpDestForIntent) ──────

// CountDestToppedUpSince is how many of her intents had destination gas sent
// at or after since — the destination top-up's own daily cap.
func (s *SellIntentService) CountDestToppedUpSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("user_id = ? AND dest_topped_up_at IS NOT NULL AND dest_topped_up_at >= ?", userID, since).
		Count(&n).Error
	return n, err
}

// MarkDestToppedUp is the once-per-intent claim on destination gas: true iff
// this caller set dest_topped_up_at. Re-checked on the row: only a delivered
// intent whose quote budgeted the top-up can win it.
// Like MarkToppedUp, the destination top-up's own UTC-day cap is enforced in
// the claim's transaction (ErrTopUpDailyCap past it).
func (s *SellIntentService) MarkDestToppedUp(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	return s.claimTopUp(ctx, id, at, "dest_topped_up_at",
		"id = ? AND dest_topped_up_at IS NULL AND dest_top_up_budgeted = ? AND status = ?", id, true, models.SellIntentDelivered)
}

// ClearDestToppedUp gives the claim back, for a top-up that provably never left.
func (s *SellIntentService) ClearDestToppedUp(ctx context.Context, id uuid.UUID) error {
	return s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ?", id).
		Update("dest_topped_up_at", nil).Error
}

// RecordDestTopUp writes down the destination top-up actually sent.
// Inside a key lane hook, LaneDB is the lane's allocation transaction.
func (s *SellIntentService) RecordDestTopUp(ctx context.Context, id uuid.UUID, txHash, amountWei string) error {
	return LaneDB(ctx, s.db).Model(&models.RampSellIntent{}).
		Where("id = ?", id).
		Updates(map[string]any{"dest_top_up_tx_hash": txHash, "dest_top_up_wei": amountWei}).Error
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
