package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
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

// deliveryReceiptGrace is how long, once a delivery's swap first reads
// COMPLETED, the destination receipt may show no credit before that is taken as
// the answer: a node lags its chain, and "no matching Transfer" a few seconds
// after LiFi says done is usually just that. Past it, the merchant was not paid.
const deliveryReceiptGrace = 10 * time.Minute

// deliveryValueBand is how far our price for the merchant's token may sit from
// the route's own value for the amount it delivers (5%, either way) before the
// sale is refused: a bogus price would size an amount that under-pays the
// merchant, and nothing after it would notice.
const deliveryValueBand = 0.05

// payValueAgrees reports whether our USD value for an amount and the route's
// agree to within deliveryValueBand. A route that names no value cannot be
// cross-checked, which is a disagreement.
func payValueAgrees(expectedUSD, routeUSD float64) bool {
	if expectedUSD <= 0 || routeUSD <= 0 {
		return false
	}
	return math.Abs(routeUSD-expectedUSD)/routeUSD <= deliveryValueBand
}

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
	// with); zero keeps the quoter's own B.
	DestChainID                               int64
	DestTokenAddress, DestAddress, DestAmount string
	BudgetBps                                 int
	// ExpectedToUSD is what ToAmount is worth at the price the caller sized it
	// from; when set, a delivery is refused unless the route's own value for
	// what it delivers agrees (deliveryValueBand).
	ExpectedToUSD float64
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
// own is tsaPayFeeSourceType.
const (
	rampSellFeeSourceType = "ramp_sell"
	tsaPayFeeSourceType   = "tsa_pay"
)

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
	asked, err := payFieldsOf(in)
	if err != nil {
		return nil, nil, err
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
	delivery := strings.TrimSpace(in.DestAddress) != ""
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

	row := &models.RampSellIntent{
		UserID:             user.ID,
		SourceAddress:      strings.TrimSpace(in.SourceAddress),
		WalletAddress:      user.WalletAddress,
		SourceChainID:      in.SourceChainID,
		SourceTokenAddress: strings.TrimSpace(in.SourceToken),
		SourceAmount:       sourceAmount,
		PayPaymentID:       in.PayPaymentID,
		PayoutFiat:         payoutFiat,
		DestChainID:        in.DestChainID,
		DestTokenAddress:   strings.TrimSpace(in.DestTokenAddress),
		DestAddress:        strings.TrimSpace(in.DestAddress),
		DestAmount:         strings.TrimSpace(in.DestAmount),
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

// checkDeliveryRoute is what a delivery needs of its route beyond an ordinary
// exact-output one, all refused before she signs — a delivery has no naira
// backstop, so a route that falls short ends with her already charged:
//
//   - its floor (toAmountMin) covers what the merchant is owed;
//   - it carries the transactionId that binds the swap she signs to THIS quote
//     (checkSwapBinding tolerates an empty one; for a delivery that clause is
//     the only thing tying the swap to us);
//   - our price for the token agrees with the route's own value for it.
func checkDeliveryRoute(in CreateSellIntentInput, route *LiFiRoute) error {
	owed, _ := new(big.Int).SetString(strings.TrimSpace(in.DestAmount), 10)
	switch {
	case route.ToAmountMin.Cmp(owed) < 0:
		return fmt.Errorf("%w: the route's floor is under what the merchant is owed", ErrSellUnavailable)
	case strings.TrimSpace(route.TransactionID) == "":
		return fmt.Errorf("%w: the route carries no transaction id to bind the swap to", ErrSellUnavailable)
	case in.ExpectedToUSD > 0 && !payValueAgrees(in.ExpectedToUSD, route.ToAmountUSD):
		return fmt.Errorf("%w: the token's price and the route's value disagree", ErrSellUnavailable)
	}
	return nil
}

// payFieldsOf validates the pay-checkout fields as a set and returns the exact
// output asked of the swap (nil for an ordinary sale). A naira pay sale needs
// PayoutFiat, ToAmount and PayPaymentID together; a delivery sale (DestAddress
// set) pays no naira, so it needs ToAmount and PayPaymentID and must carry no
// PayoutFiat. Partial sets are refused: a PayoutFiat without a PayPaymentID
// would take the pay path in OpenOrder but escape the Abandon guard, and a
// ToAmount alone would change the quote and nothing else.
func payFieldsOf(in CreateSellIntentInput) (*big.Int, error) {
	fiat, toAmount := strings.TrimSpace(in.PayoutFiat), strings.TrimSpace(in.ToAmount)
	delivery := strings.TrimSpace(in.DestAddress) != ""
	if fiat == "" && toAmount == "" && in.PayPaymentID == nil && !delivery {
		return nil, nil
	}
	switch {
	case delivery && fiat != "":
		return nil, fmt.Errorf("%w: a delivery sale pays no naira", ErrSellIntentState)
	case toAmount == "" || in.PayPaymentID == nil || (!delivery && fiat == ""):
		return nil, fmt.Errorf("%w: a pay sale needs its payment, its amount to deliver and, for naira, its payout together", ErrSellIntentState)
	}
	asked, ok := new(big.Int).SetString(toAmount, 10)
	if !ok || asked.Sign() <= 0 {
		return nil, fmt.Errorf("%w: %q is not an amount to swap for", ErrSellIntentState, toAmount)
	}
	return asked, nil
}

// checkDelivery is a delivery sale's own precondition: everything
// ConfirmDelivery will read must be there before a route is quoted, or the
// sale could be paid for and never proven.
func (in CreateSellIntentInput) checkDelivery() error {
	owed, ok := new(big.Int).SetString(strings.TrimSpace(in.DestAmount), 10)
	switch {
	case in.DestChainID <= 0 || strings.TrimSpace(in.DestTokenAddress) == "":
		return fmt.Errorf("%w: a delivery names its chain and token", ErrSellIntentState)
	case strings.EqualFold(strings.TrimSpace(in.DestTokenAddress), evmNativeAddress) ||
		strings.EqualFold(strings.TrimSpace(in.DestTokenAddress), evmNativeAliasAddress):
		// The proof reads ERC-20 Transfer logs; a native coin leaves none.
		return fmt.Errorf("%w: a delivery is an ERC-20 token, not a native coin", ErrSellIntentState)
	case !ok || owed.Sign() <= 0:
		return fmt.Errorf("%w: %q is not an amount to deliver", ErrSellIntentState, in.DestAmount)
	case strings.TrimSpace(in.ToAmount) == "":
		return fmt.Errorf("%w: a delivery is an exact-output swap", ErrSellIntentState)
	}
	return nil
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
//
// A pay intent (PayoutFiat set) is opened for the exact naira it owes, not for
// what landed: the binding and the landing proof are the same, but the deposit
// is sized from the naira and capped at the proven landed USDC — or, when she
// consents with useWalletBalance, at her whole Base USDC balance. A cap the
// deposit exceeds is a SellShortfallError. An ordinary sale ignores the flag.
func (s *SellIntentService) OpenOrder(ctx context.Context, user *models.User, id uuid.UUID, useWalletBalance bool) (*models.PaycrestOrderRecord, error) {
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
	if in.DestAddress != "" {
		// A delivery sale has no offramp. The landing proof below reads HER
		// wallet's USDC; letting it run here could open a naira order the sale
		// never owed. ConfirmDelivery is its only way forward.
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
	if in.PayoutFiat != "" && useWalletBalance {
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
	if in.PayoutFiat != "" {
		sellReq.FiatAmount = in.PayoutFiat
		order, err = s.orders.CreateSellForFiat(ctx, sellReq, true, payCap)
	} else {
		order, err = s.orders.CreateSellForToken(ctx, sellReq, BaseUnitsToDecimal(landed, 6), true)
	}
	if err != nil {
		if errors.Is(err, ErrSellAttemptMayExist) {
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

// ConfirmDelivery is OpenOrder's twin for a delivery sale (DestAddress set): it
// answers "did the merchant's wallet get what it is owed?" and, only when it did,
// moves the sale to delivered and books our fee. It returns the destination
// transaction that credited the merchant.
//
// This decides whether a merchant was paid, so nothing here takes LiFi's word:
// LiFi's status finds the swap and binds it to the sale (checkSwapBinding, the
// receiving side being the merchant's own token, chain and wallet), and the
// destination chain's own receipt says how much that wallet was credited. Less
// than DestAmount — by a single base unit — is ErrSwapMismatch: the money may
// be on its way, elsewhere, or short, and a person has to look.
//
// Idempotent: a sale already delivered is re-proven and answers with the same
// transaction, booking nothing more. The fee is booked before the guarded
// update so a crash between the two is healed by the next call (the engine's
// guard makes a repeat a no-op), never lost.
func (s *SellIntentService) ConfirmDelivery(ctx context.Context, id uuid.UUID) (string, error) {
	if !s.ready() {
		return "", ErrSellUnavailable
	}
	var in models.RampSellIntent
	if err := s.db.WithContext(ctx).First(&in, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSellIntentNotFound
		}
		return "", err
	}
	if in.DestAddress == "" || in.SwapTxHash == nil ||
		(in.Status != models.SellIntentSwapped && in.Status != models.SellIntentDelivered) {
		return "", ErrSellIntentState
	}
	owed, ok := new(big.Int).SetString(strings.TrimSpace(in.DestAmount), 10)
	if !ok || owed.Sign() <= 0 {
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_unreadable_total=1 intent=%s dest_amount=%q", in.ID, in.DestAmount)
		return "", ErrSellIntentState
	}

	st, err := s.status.Status(ctx, *in.SwapTxHash)
	if err != nil {
		return "", err
	}
	if st == nil || st.Status != LiFiStatusDone || st.Substatus != LiFiSubstatusCompleted {
		return "", &SwapNotLandedError{Status: st}
	}
	if err := checkSwapBinding(st, &in, rampSettlementChainID, s.usdc); err != nil {
		log.Printf("[ramp-sell] ALERT ramp_sell_swap_mismatch_total=1 intent=%s tx=%s status=%s/%s: %v",
			in.ID, *in.SwapTxHash, st.Status, st.Substatus, err)
		return "", err
	}

	verifier, ok := s.destVerifier(in.DestChainID)
	if !ok {
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_unverifiable_total=1 intent=%s chain=%d", in.ID, in.DestChainID)
		return "", fmt.Errorf("%w: no way to prove a delivery on chain %d", ErrSellUnavailable, in.DestChainID)
	}
	// The first time the swap reads COMPLETED starts the wait for its receipt to
	// be readable; it is written once and survives a restart.
	seen, err := s.deliverySeen(ctx, &in)
	if err != nil {
		return "", err
	}
	got, err := verifier.VerifyTransfer(ctx, st.DestTxHash, in.DestTokenAddress, in.DestAddress)
	switch {
	case errors.Is(err, ErrTransferReverted):
		// The chain says the destination transaction did nothing.
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_reverted_total=1 intent=%s tx=%s: %v", in.ID, st.DestTxHash, err)
		return "", fmt.Errorf("%w: %v", ErrSwapMismatch, err)
	case errors.Is(err, ErrTransferNotFound):
		// The real verifier says "no Transfer log credits the merchant" this way,
		// never as a zero. Right after LiFi says done it may be a lagging node: a
		// wait, carrying the status read so the caller need not read it again.
		if s.now().Sub(seen) < deliveryReceiptGrace {
			return "", &SwapNotLandedError{Status: st}
		}
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_no_credit_total=1 intent=%s tx=%s seen_for=%s: %v",
			in.ID, st.DestTxHash, s.now().Sub(seen).Round(time.Second), err)
		return "", fmt.Errorf("%w: %v", ErrSwapMismatch, err)
	case err != nil:
		return "", err
	}
	if got == nil || got.Cmp(owed) < 0 {
		log.Printf("[ramp-sell] ALERT ramp_sell_delivery_short_total=1 intent=%s tx=%s credited=%v owed=%s",
			in.ID, st.DestTxHash, got, owed)
		return "", fmt.Errorf("%w: %s credited less than the %s owed", ErrSwapMismatch, st.DestTxHash, owed)
	}

	if in.Status == models.SellIntentDelivered {
		return st.DestTxHash, nil
	}
	destTx := strings.ToLower(st.DestTxHash)
	// One destination transaction proves one sale (see RampSellIntent.DestTxHash).
	// Checked before the fee is booked; the unique index below is the backstop.
	var reused int64
	if err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("dest_tx_hash = ? AND id <> ?", destTx, in.ID).Count(&reused).Error; err != nil {
		return "", err
	}
	if reused > 0 {
		return "", s.deliveryTxReused(&in, st.DestTxHash)
	}
	s.bookFee(&in, in.UserID, tsaPayFeeSourceType)
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND status = ?", in.ID, models.SellIntentSwapped).
		Updates(models.WithBankDetailsCleared(map[string]any{"status": models.SellIntentDelivered, "dest_tx_hash": destTx, "updated_at": s.now()}))
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return "", s.deliveryTxReused(&in, st.DestTxHash)
		}
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		// Another caller got there first. Fine if it delivered; anything else
		// (a reconciler failed the sale in between) is not a delivery we made.
		var now models.RampSellIntent
		if err := s.db.WithContext(ctx).First(&now, "id = ?", in.ID).Error; err != nil {
			return "", err
		}
		if now.Status != models.SellIntentDelivered {
			return "", ErrSellIntentState
		}
	}
	return st.DestTxHash, nil
}

// deliverySeen is when in's swap was first seen COMPLETED, writing now if it is
// the first time. Guarded, so of two concurrent first sights one time wins.
func (s *SellIntentService) deliverySeen(ctx context.Context, in *models.RampSellIntent) (time.Time, error) {
	if in.DeliverySeenAt != nil {
		return *in.DeliverySeenAt, nil
	}
	now := s.now()
	res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
		Where("id = ? AND delivery_seen_at IS NULL", in.ID).
		Update("delivery_seen_at", now)
	if res.Error != nil {
		return time.Time{}, res.Error
	}
	if res.RowsAffected == 0 {
		var cur models.RampSellIntent
		if err := s.db.WithContext(ctx).First(&cur, "id = ?", in.ID).Error; err != nil {
			return time.Time{}, err
		}
		if cur.DeliverySeenAt != nil {
			return *cur.DeliverySeenAt, nil
		}
	}
	return now, nil
}

// deliveryTxReused is the answer for a destination transaction another sale
// already used to prove its delivery: a mismatch — one credit cannot pay two.
func (s *SellIntentService) deliveryTxReused(in *models.RampSellIntent, tx string) error {
	log.Printf("[ramp-sell] ALERT ramp_sell_delivery_tx_reused_total=1 intent=%s tx=%s", in.ID, tx)
	return fmt.Errorf("%w: %s already proved another sale's delivery", ErrSwapMismatch, tx)
}

// destVerifier is the on-chain proof for a chain id, by way of the chain key the
// ramp's verifiers are stored under.
func (s *SellIntentService) destVerifier(chainID int64) (ERC20TransferVerifier, bool) {
	if s.chainName == nil || s.destVerifiers == nil {
		return nil, false
	}
	key, ok := s.chainName(chainID)
	if !ok {
		return nil, false
	}
	v, ok := s.destVerifiers[key]
	return v, ok && v != nil
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
	return sellClaimIsStale(in, s.now())
}

// sellClaimIsStale reports an ordering claim nobody has touched for
// sellIntentClaimStale as of now: its holder is presumed dead. The pay
// reconciler asks the same question of a payment's intent.
func sellClaimIsStale(in *models.RampSellIntent, now time.Time) bool {
	return in.UpdatedAt.Before(now.Add(-sellIntentClaimStale))
}

// SwapNotLandedError is ErrSwapNotLanded carrying the LiFi status the answer
// was read from (nil when LiFi has no record), so the pay reconciler decides
// what the wait means from it instead of asking LiFi again.
type SwapNotLandedError struct{ Status *LiFiTransferStatus }

func (e *SwapNotLandedError) Error() string { return ErrSwapNotLanded.Error() }
func (e *SwapNotLandedError) Unwrap() error { return ErrSwapNotLanded }

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
// reconciler's AbandonForPay ends one of those.
func (s *SellIntentService) Abandon(ctx context.Context, userID, id uuid.UUID) error {
	if !s.ready() {
		return ErrSellUnavailable
	}
	return s.failOpenSale(ctx, "id = ? AND user_id = ? AND (pay_payment_id IS NULL OR swap_tx_hash IS NULL)", id, userID)
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
