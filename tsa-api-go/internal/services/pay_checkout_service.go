package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// payOrders is the slice of *PaycrestOrderService a direct payment needs: a
// sell sized to an exact naira amount, and the deposit that amount takes.
type payOrders interface {
	CreateSellForFiat(ctx context.Context, req PaycrestSellRequest, converted bool, maxTokenBaseUnits *big.Int) (*models.PaycrestOrderRecord, error)
	SellDepositFor(ctx context.Context, fiatCode, fiatAmount, chainKey, token string, converted bool) (*big.Int, error)
}

// payQuoter prices a converted payment's swap. *RampSellQuoter fills it.
type payQuoter interface {
	Quote(ctx context.Context, in SellQuoteInput) (*SellSwapQuote, error)
}

// payRateBufferBps is added on top of the deposit a converted payment's swap
// must land, so the payout order still sizes inside the USDC that landed when
// Paycrest's rate drifts between the swap and the order. With the swap's own
// 0.5% slippage it is about 0.5% of rate headroom.
const payRateBufferBps = 100

// ErrPayWrongRoute is returned when a route is asked to pay a payment that
// settles the other way: the naira routes on a crypto merchant's payment, or the
// crypto routes on a naira merchant's. Its message says which (payWrongRoute).
var ErrPayWrongRoute = errors.New("this payment can't be paid this way")

// payWrongRoute is ErrPayWrongRoute for a route that pays `wanted`: the message
// names how the payment really settles, so a naira merchant hit on the crypto
// route is not told it settles in crypto.
func payWrongRoute(wanted string) error {
	if wanted == models.PaySettlementFiat {
		return fmt.Errorf("%w: it settles in crypto — pick a token to pay with", ErrPayWrongRoute)
	}
	return fmt.Errorf("%w: it settles in naira — pay with a stablecoin or another token", ErrPayWrongRoute)
}

// ErrPayInternal is a start that failed for a reason of ours — a database that
// would not answer — rather than a refusal of the payment or a provider's
// answer. Its message is safe to show; the cause is only logged.
var ErrPayInternal = errors.New("we couldn't start this payment — please try again")

// IsPayServiceError reports whether err is one PayPaymentService itself
// returns (or the KYC refusal its Lock does), as opposed to a provider's or an
// internal failure.
func IsPayServiceError(err error) bool {
	for _, target := range []error{
		ErrPayPaymentNotFound, ErrKYCVerificationRequired, ErrPayForbidden,
		ErrPayNotPayable, ErrPayInProgress, ErrPayTransition,
		ErrPayPaymentInvalid, ErrPayMerchantNotReady,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// payInternal turns an error the payment's own store raised into ErrPayInternal
// unless it is one of the service's own refusals; the cause is logged.
func payInternal(err error) error {
	if err == nil || IsPayServiceError(err) || errors.Is(err, ErrPayInternal) {
		return err
	}
	log.Printf("[tsa-pay] start failed internally: %v", err)
	return ErrPayInternal
}

// payBookkeepingTimeout bounds the writes that record what a provider call just
// did; sellCancelTimeout is the same 5 s the order service gives its own cancel.
const payBookkeepingTimeout = sellCancelTimeout

// payBookkeeping is the context for every write a start makes AFTER a provider
// call created something: linking the attempt, ending an intent, failing or
// freeing the payment. It outlives the request, the way cancelShortOrder and
// releaseClaim do — a customer who closes the app must not leave an order she
// can fund unlinked from the payment it pays.
func payBookkeeping(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), payBookkeepingTimeout)
}

// PayCheckoutService starts a naira payment for a signed-in customer — either
// directly (a Paycrest order she funds with a stablecoin she holds) or
// converted (a sell intent that swaps any token into the USDC the order
// needs) — and hands the payment to the reconciler, which settles it. It owns
// the one-attempt-per-payment rule at the start; PayPaymentService owns every
// state change.
type PayCheckoutService struct {
	payments *PayPaymentService
	orders   payOrders
	intents  payIntents // nil → converted payments unavailable
	quoter   payQuoter  // nil → no converted quote
	pricer   payPricer  // nil (with rate) → crypto payments unavailable; see SetCrypto
	rate     payRate
	live     bool
}

// NewPayCheckoutService wires the starter. intents and quoter may be nil (ramp
// sell off): direct payments still work, converted ones answer
// ErrSellUnavailable. Pass a nil interface, never a typed-nil pointer.
func NewPayCheckoutService(payments *PayPaymentService, orders payOrders, intents payIntents, quoter payQuoter, liveEnabled bool) *PayCheckoutService {
	return &PayCheckoutService{payments: payments, orders: orders, intents: intents, quoter: quoter, live: liveEnabled}
}

// PayDirectInput names the stablecoin the customer will fund the order with.
type PayDirectInput struct{ ChainKey, Token string }

// PayConvertedInput names the token the customer pays with and where it is.
type PayConvertedInput struct {
	SourceChainID int64
	SourceToken   string
	SourceAddress string
}

// PayQuote is what paying a payment with one token would cost: FromAmount of
// the source token (its base units) buys USDCTarget (Base, 6dp base units).
type PayQuote struct {
	FromAmount string `json:"fromAmount"`
	USDCTarget string `json:"usdcTarget"`
}

// Quote prices paying with one token. It locks nothing and creates nothing:
// the payment stays free for whoever starts it.
func (s *PayCheckoutService) Quote(ctx context.Context, user *models.User, id uuid.UUID, in PayConvertedInput) (*PayQuote, error) {
	if s.quoter == nil {
		return nil, ErrSellUnavailable
	}
	v, err := s.quotable(ctx, user, id, models.PaySettlementFiat)
	if err != nil {
		return nil, err
	}
	target, err := s.convertedTarget(ctx, payFiatAmount(v.MerchantNet))
	if err != nil {
		return nil, err
	}
	q, err := s.quoter.Quote(ctx, SellQuoteInput{
		FromChainID: in.SourceChainID, FromToken: in.SourceToken, FromAddress: in.SourceAddress,
		WalletAddress: user.WalletAddress, FiatCode: "NGN", ToAmount: target.String(),
	})
	if err != nil {
		return nil, err
	}
	if q == nil || q.Route == nil {
		return nil, ErrSellUnavailable
	}
	return &PayQuote{FromAmount: q.Route.FromAmount, USDCTarget: target.String()}, nil
}

// quotable is the check both quotes share: a live payment this user could start
// right now — not a test payment, not one already in progress (hers or
// anyone's), not an expired one — and one that settles the way the route pays.
func (s *PayCheckoutService) quotable(ctx context.Context, user *models.User, id uuid.UUID, settlement string) (*PayCheckoutView, error) {
	v, err := s.payments.CheckoutView(ctx, user, id, s.live)
	if err != nil {
		return nil, err
	}
	if v.Mode != models.PayModeLive || v.Status != models.PayStatusPending || !v.Payable {
		return nil, ErrPayNotPayable
	}
	if v.SettlementMode != settlement {
		return nil, payWrongRoute(settlement)
	}
	return v, nil
}

// PayDirect opens the payment's payout order for exactly the merchant's net,
// to the merchant's snapshotted bank, and links it as the payment's one
// attempt. The customer then funds it with the token she named.
func (s *PayCheckoutService) PayDirect(ctx context.Context, user *models.User, id uuid.UUID, in PayDirectInput) (*models.PaycrestOrderRecord, error) {
	p, err := s.begin(ctx, user, id, models.PaySettlementFiat)
	if err != nil {
		return nil, err
	}
	order, err := s.orders.CreateSellForFiat(ctx, PaycrestSellRequest{
		UserID:        user.ID,
		WalletAddress: user.WalletAddress,
		FiatCode:      "NGN",
		FiatAmount:    payFiatAmount(p.MerchantNetKobo),
		ChainKey:      strings.TrimSpace(in.ChainKey),
		Token:         strings.TrimSpace(in.Token),
		BankCode:      p.SettleBankCode,
		AccountNo:     p.SettleAccountNumber,
		AccountName:   p.SettleAccountName,
		PayPaymentID:  &p.ID,
	}, false, nil)
	if errors.Is(err, ErrSellAttemptMayExist) {
		// An order may be open that she could fund: the payment is failed for
		// support, never handed to another payer.
		s.failStart(ctx, p, "start_attempt_may_exist", "", uuid.Nil, err)
		return nil, err
	}
	if err != nil {
		// CreateSellForFiat cancels a short order itself, and says so when it
		// could not: here nothing fundable exists.
		s.release(ctx, p, "released: the payout order could not be opened")
		return nil, err
	}
	bctx, stop := payBookkeeping(ctx)
	defer stop()
	if err := s.payments.LinkOrder(bctx, p.ID, order.ID); err != nil {
		s.failStart(ctx, p, "link_failed", "order", order.ID, err)
		return nil, payInternal(err)
	}
	return order, nil
}

// PayConverted opens a sell intent for the payment: the customer's token is
// swapped into exactly the USDC the merchant's payout order needs (plus the
// rate buffer), and the payout order is opened once it lands. The payment
// links the intent as its one attempt; the reconciler follows it from there.
func (s *PayCheckoutService) PayConverted(ctx context.Context, user *models.User, id uuid.UUID, in PayConvertedInput) (*models.RampSellIntent, *SellSwapQuote, error) {
	if s.intents == nil {
		return nil, nil, ErrSellUnavailable
	}
	p, err := s.begin(ctx, user, id, models.PaySettlementFiat)
	if err != nil {
		return nil, nil, err
	}
	fiat := payFiatAmount(p.MerchantNetKobo)
	target, err := s.convertedTarget(ctx, fiat)
	if err != nil {
		s.release(ctx, p, "released: the swap could not be sized")
		return nil, nil, err
	}
	return s.startIntent(ctx, user, p, CreateSellIntentInput{
		SourceChainID: in.SourceChainID,
		SourceToken:   in.SourceToken,
		SourceAddress: in.SourceAddress,
		FiatCode:      "NGN",
		BankCode:      p.SettleBankCode,
		AccountNumber: p.SettleAccountNumber,
		AccountName:   p.SettleAccountName,
		PayoutFiat:    fiat,
		ToAmount:      target.String(),
	})
}

// startIntent opens the sell intent for p (the caller has already taken the
// lock) and links it as the payment's one attempt. Both routes end here.
func (s *PayCheckoutService) startIntent(ctx context.Context, user *models.User, p *models.PayPayment, in CreateSellIntentInput) (*models.RampSellIntent, *SellSwapQuote, error) {
	in.PayPaymentID = &p.ID
	intent, quote, err := s.intents.Create(ctx, user, in)
	if err != nil {
		// A failed Create inserts no intent: nothing is signable.
		s.release(ctx, p, "released: the swap could not be started")
		return nil, nil, err
	}
	bctx, stop := payBookkeeping(ctx)
	defer stop()
	if err := s.payments.LinkIntent(bctx, p.ID, intent.ID); err != nil {
		// She has signed nothing yet, so the intent can be ended safely — it
		// would otherwise hold her one open sale.
		if aerr := s.intents.AbandonForPay(bctx, intent.ID, nil); aerr != nil {
			log.Printf("[tsa-pay] could not end unlinked intent %s: %v", intent.ID, aerr)
		}
		s.failStart(ctx, p, "link_failed", "intent", intent.ID, err)
		return nil, nil, payInternal(err)
	}
	return intent, quote, nil
}

// ---- crypto settlement: the merchant is paid a token in their own wallet ----

// payPricer is LiFi's USD price for a token and its decimals.
// *LiFiQuoteClient fills it.
type payPricer interface {
	TokenPriceUSD(ctx context.Context, chainID int64, token string) (*big.Rat, int, error)
}

// payRate is the provider's naira-per-USDC sell rate, with no sender fee.
// *PaycrestQuoteService fills it.
type payRate interface {
	ProviderRate(ctx context.Context, fiat string) (*big.Rat, error)
}

// payCryptoSlippageBps is the swap slippage the request is padded for, so
// LiFi's toAmountMin (at least toAmount x (1 - slippage)) still covers what
// the merchant is owed.
const payCryptoSlippageBps = 50

// ErrPaySameToken is returned when the customer would pay the merchant's own
// token on the merchant's own chain: a plain transfer, which is not provable as
// hers (a transfer proof is not bound to its sender), so it is deferred.
var ErrPaySameToken = errors.New("paying with the store's own token on the same network isn't available yet — pick another token")

// SetCrypto wires the pricing a crypto payment needs. Until it is called,
// CryptoQuote and PayCrypto answer ErrSellUnavailable and naira payments are
// untouched. Pass nil interfaces, never typed-nil pointers.
func (s *PayCheckoutService) SetCrypto(p payPricer, rate payRate) { s.pricer, s.rate = p, rate }

// PayCryptoQuote is what paying a crypto merchant with one token would cost:
// FromAmount of the source token (its base units) delivers DestAmount of the
// merchant's token (its base units, DestDecimals decimals) to their wallet.
type PayCryptoQuote struct {
	FromAmount   string `json:"fromAmount"`
	DestAmount   string `json:"destAmount"`
	DestDecimals int    `json:"destDecimals"`
}

// payCryptoAmounts sizes a crypto payment, all in big.Rat / big.Int:
//
//	owed     = floor(kobo x 10^dec / (100 x rate x price))   what the merchant is credited
//	toAmount = ceil(owed x 10000 / (10000 - slippage))        what the swap is asked to deliver
//
// rate is naira per USDC and price is USD per merchant token, so the owed
// tokens are worth exactly the merchant's naira net at the provider's rate.
func payCryptoAmounts(kobo int64, dec int, price, rate *big.Rat) (owed, toAmount *big.Int) {
	num := new(big.Rat).SetInt(big.NewInt(kobo))
	num.Mul(num, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dec)), nil)))
	den := new(big.Rat).Mul(rate, price)
	den.Mul(den, big.NewRat(100, 1))
	q := num.Quo(num, den)
	owed = new(big.Int).Div(q.Num(), q.Denom())

	pad := new(big.Int).Mul(owed, big.NewInt(10_000))
	d := big.NewInt(10_000 - payCryptoSlippageBps)
	toAmount, rem := new(big.Int).DivMod(pad, d, new(big.Int))
	if rem.Sign() != 0 {
		toAmount.Add(toAmount, big.NewInt(1))
	}
	return owed, toAmount
}

// cryptoTarget prices p's merchant net in the merchant's token. A price or
// rate that cannot be read is ErrSellUnavailable: better to refuse than to
// owe the merchant a guess.
//
// toUSD is what the padded request is worth at the price it was sized from: the
// figure the route's own value for it is checked against, so a price nobody
// else agrees with cannot silently under-pay the merchant.
func (s *PayCheckoutService) cryptoTarget(ctx context.Context, p *models.PayPayment) (owed, toAmount *big.Int, dec int, toUSD float64, err error) {
	price, dec, err := s.pricer.TokenPriceUSD(ctx, p.SettleChainID, p.SettleTokenAddress)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("%w: pricing the merchant's token: %v", ErrSellUnavailable, err)
	}
	rate, err := s.rate.ProviderRate(ctx, "NGN")
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("%w: reading the provider rate: %v", ErrSellUnavailable, err)
	}
	owed, toAmount = payCryptoAmounts(p.MerchantNetKobo, dec, price, rate)
	if owed.Sign() <= 0 {
		return nil, nil, 0, 0, fmt.Errorf("%w: the amount is too small to deliver in this token", ErrSellUnavailable)
	}
	usd := new(big.Rat).Mul(new(big.Rat).SetInt(toAmount), price)
	usd.Quo(usd, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dec)), nil)))
	toUSD, _ = usd.Float64()
	return owed, toAmount, dec, toUSD, nil
}

// payCryptoFee is the whole platform fee the round trip may spend, snapshotted
// on the payment. A payment that snapshotted none must not be sold: a zero
// budget would fall back to the quoter's own B and charge a fee the payment
// never priced.
func payCryptoFee(p *models.PayPayment) error {
	if p.FeeBps <= 0 {
		return fmt.Errorf("%w: this payment snapshotted no fee", ErrSellUnavailable)
	}
	return nil
}

// paySameToken is the merchant's own token on the merchant's own chain.
func paySameToken(p *models.PayPayment, in PayConvertedInput) bool {
	return in.SourceChainID == p.SettleChainID && strings.EqualFold(strings.TrimSpace(in.SourceToken), p.SettleTokenAddress)
}

func (s *PayCheckoutService) cryptoReady() bool {
	return s.intents != nil && s.pricer != nil && s.rate != nil
}

// CryptoQuote prices paying a crypto merchant with one token. Like Quote it
// locks nothing and creates nothing.
func (s *PayCheckoutService) CryptoQuote(ctx context.Context, user *models.User, id uuid.UUID, in PayConvertedInput) (*PayCryptoQuote, error) {
	if s.quoter == nil || !s.cryptoReady() {
		return nil, ErrSellUnavailable
	}
	if _, err := s.quotable(ctx, user, id, models.PaySettlementCrypto); err != nil {
		return nil, err
	}
	p, err := s.payments.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if paySameToken(p, in) {
		return nil, ErrPaySameToken
	}
	if err := payCryptoFee(p); err != nil {
		return nil, err
	}
	owed, toAmount, dec, toUSD, err := s.cryptoTarget(ctx, p)
	if err != nil {
		return nil, err
	}
	q, err := s.quoter.Quote(ctx, SellQuoteInput{
		FromChainID: in.SourceChainID, FromToken: in.SourceToken, FromAddress: in.SourceAddress,
		WalletAddress: user.WalletAddress, FiatCode: "NGN", ToAmount: toAmount.String(),
		ToChainID: p.SettleChainID, ToToken: p.SettleTokenAddress, ToAddress: p.SettleWalletAddress, BudgetBps: p.FeeBps, Delivery: true,
	})
	if err != nil {
		return nil, err
	}
	if q == nil || q.Route == nil {
		return nil, ErrSellUnavailable
	}
	if !payValueAgrees(toUSD, q.Route.ToAmountUSD) {
		return nil, fmt.Errorf("%w: the token's price and the route's value disagree", ErrSellUnavailable)
	}
	return &PayCryptoQuote{FromAmount: q.Route.FromAmount, DestAmount: owed.String(), DestDecimals: dec}, nil
}

// PayCrypto opens a delivery sale for the payment: the customer's token is
// swapped, exact-output, into the merchant's token in the merchant's wallet.
// No Paycrest leg exists, so the whole platform fee is the round-trip budget.
// The reconciler proves the delivery on-chain and settles the payment.
func (s *PayCheckoutService) PayCrypto(ctx context.Context, user *models.User, id uuid.UUID, in PayConvertedInput) (*models.RampSellIntent, *SellSwapQuote, error) {
	if !s.cryptoReady() {
		return nil, nil, ErrSellUnavailable
	}
	p, err := s.begin(ctx, user, id, models.PaySettlementCrypto)
	if err != nil {
		return nil, nil, err
	}
	if paySameToken(p, in) {
		s.release(ctx, p, "released: the store's own token on its own network")
		return nil, nil, ErrPaySameToken
	}
	if err := payCryptoFee(p); err != nil {
		s.release(ctx, p, "released: the payment has no fee snapshot")
		return nil, nil, err
	}
	owed, toAmount, _, toUSD, err := s.cryptoTarget(ctx, p)
	if err != nil {
		s.release(ctx, p, "released: the transfer could not be sized")
		return nil, nil, err
	}
	return s.startIntent(ctx, user, p, CreateSellIntentInput{
		SourceChainID:    in.SourceChainID,
		SourceToken:      in.SourceToken,
		SourceAddress:    in.SourceAddress,
		ToAmount:         toAmount.String(),
		DestChainID:      p.SettleChainID,
		DestTokenAddress: p.SettleTokenAddress,
		DestAddress:      p.SettleWalletAddress,
		DestAmount:       owed.String(),
		BudgetBps:        p.FeeBps,
		ExpectedToUSD:    toUSD,
	})
}

// begin takes the payment's lock for user, on the route for how it settles
// (models.PaySettlement*). The payment is READ and validated first — a live
// payment on the route it settles by — so a call on the wrong route or a test
// payment never takes anyone's lock at all; only then is it locked. Every
// refusal after a fresh lock still hands it back: nothing has been asked of a
// provider, so no money can be moving.
func (s *PayCheckoutService) begin(ctx context.Context, user *models.User, id uuid.UUID, settlement string) (*models.PayPayment, error) {
	read, err := s.payments.Get(ctx, id)
	if err != nil {
		return nil, payInternal(err)
	}
	// The payer resuming her own attempt is told so whatever else is true.
	holds := read.Status == models.PayStatusProcessing && read.PayerUserID != nil && *read.PayerUserID == user.ID
	if !holds {
		if read.Mode != models.PayModeLive {
			return nil, ErrPayNotPayable
		}
		if read.SettlementMode != settlement {
			return nil, payWrongRoute(settlement)
		}
	}
	p, fresh, err := s.payments.Lock(ctx, user, id, s.live)
	if err != nil {
		return nil, payInternal(err)
	}
	if !fresh {
		// She already holds this payment: the app resumes her attempt, and a
		// second one is never started.
		return nil, ErrPayInProgress
	}
	// The snapshot cannot change under a lock, so this is a backstop only.
	if p.Mode != models.PayModeLive {
		s.release(ctx, p, "released: not a live payment")
		return nil, ErrPayNotPayable
	}
	if p.SettlementMode != settlement {
		s.release(ctx, p, "released: this payment settles another way")
		return nil, payWrongRoute(settlement)
	}
	return p, nil
}

// convertedTarget is the USDC (Base, 6dp base units) a converted payment's
// swap must land: the deposit the naira payout takes, plus payRateBufferBps,
// rounded up — in big.Int, never floats.
func (s *PayCheckoutService) convertedTarget(ctx context.Context, fiat string) (*big.Int, error) {
	dep, err := s.orders.SellDepositFor(ctx, "NGN", fiat, rampSettlementChainKey, rampSettlementToken, true)
	if err != nil {
		return nil, err
	}
	num := new(big.Int).Mul(dep, big.NewInt(10_000+payRateBufferBps))
	num.Add(num, big.NewInt(9_999))
	return num.Div(num, big.NewInt(10_000)), nil
}

// release hands a payment back after a start that created nothing. A release
// that itself fails is logged, not returned: the caller's own error is the
// one the customer needs, and the reconciler frees a lock with no attempt
// behind it after payLockNoAttemptGrace — and only once it has looked for an
// attempt that may exist.
func (s *PayCheckoutService) release(ctx context.Context, p *models.PayPayment, reason string) {
	bctx, stop := payBookkeeping(ctx)
	defer stop()
	// Guarded on p's reading — a locked payment with no attempt yet.
	if err := s.payments.releaseFrom(bctx, p, reason); err != nil {
		log.Printf("[tsa-pay] could not release payment %s after a refused start: %v", p.ID, err)
	}
}

// failStart is the one way a start can go wrong AFTER a provider may have
// created something: an attempt exists, or may, and it could not be linked or
// cleanly ruled out. The payment is never released — it fails, for support.
// kind and attemptID name the attempt when there is one.
func (s *PayCheckoutService) failStart(ctx context.Context, p *models.PayPayment, alert, kind string, attemptID uuid.UUID, cause error) {
	detail := ""
	if kind != "" {
		detail = fmt.Sprintf(" %s=%s", kind, attemptID)
	}
	log.Printf("[tsa-pay] ALERT pay_%s_total=1 payment=%s%s err=%v", alert, p.ID, detail, cause)
	bctx, stop := payBookkeeping(ctx)
	defer stop()
	// Not guarded on an attempt: when linking failed, whether the write landed
	// is exactly what is unknown, and either way the payment must fail.
	if err := s.payments.Fail(bctx, p.ID, "internal: could not start the attempt cleanly"); err != nil {
		log.Printf("[tsa-pay] ALERT pay_%s_stuck_total=1 payment=%s%s err=%v", alert, p.ID, detail, err)
	}
}

// payFiatAmount renders integer kobo as the naira decimal a Paycrest sell is
// sized in ("50000.00").
func payFiatAmount(kobo int64) string {
	return fmt.Sprintf("%d.%02d", kobo/100, kobo%100)
}
