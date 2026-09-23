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

// ErrPayWrongRoute is returned when a naira route is asked to pay a payment
// that settles in crypto.
var ErrPayWrongRoute = errors.New("this payment settles in crypto — pick a token to pay with")

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
	v, err := s.payments.CheckoutView(ctx, user, id, s.live)
	if err != nil {
		return nil, err
	}
	// Only a live payment this user could start right now: not a test payment,
	// not one already in progress (hers or anyone's), not an expired one.
	if v.Mode != models.PayModeLive || v.Status != models.PayStatusPending || !v.Payable {
		return nil, ErrPayNotPayable
	}
	if v.SettlementMode != models.PaySettlementFiat {
		return nil, ErrPayWrongRoute
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

// PayDirect opens the payment's payout order for exactly the merchant's net,
// to the merchant's snapshotted bank, and links it as the payment's one
// attempt. The customer then funds it with the token she named.
func (s *PayCheckoutService) PayDirect(ctx context.Context, user *models.User, id uuid.UUID, in PayDirectInput) (*models.PaycrestOrderRecord, error) {
	p, err := s.begin(ctx, user, id)
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
	}, false, nil)
	if err != nil {
		// CreateSellForFiat cancels a short order itself: nothing fundable exists.
		s.release(ctx, p.ID, "released: the payout order could not be opened")
		return nil, err
	}
	if err := s.payments.LinkOrder(ctx, p.ID, order.ID); err != nil {
		s.linkFailed(ctx, p.ID, "order", order.ID, err)
		return nil, err
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
	p, err := s.begin(ctx, user, id)
	if err != nil {
		return nil, nil, err
	}
	fiat := payFiatAmount(p.MerchantNetKobo)
	target, err := s.convertedTarget(ctx, fiat)
	if err != nil {
		s.release(ctx, p.ID, "released: the swap could not be sized")
		return nil, nil, err
	}
	intent, quote, err := s.intents.Create(ctx, user, CreateSellIntentInput{
		SourceChainID: in.SourceChainID,
		SourceToken:   in.SourceToken,
		SourceAddress: in.SourceAddress,
		FiatCode:      "NGN",
		BankCode:      p.SettleBankCode,
		AccountNumber: p.SettleAccountNumber,
		AccountName:   p.SettleAccountName,
		PayPaymentID:  &p.ID,
		PayoutFiat:    fiat,
		ToAmount:      target.String(),
	})
	if err != nil {
		// A failed Create inserts no intent: nothing is signable.
		s.release(ctx, p.ID, "released: the swap could not be started")
		return nil, nil, err
	}
	if err := s.payments.LinkIntent(ctx, p.ID, intent.ID); err != nil {
		// She has signed nothing yet, so the intent can be ended safely — it
		// would otherwise hold her one open sale.
		if aerr := s.intents.AbandonForPay(ctx, intent.ID); aerr != nil {
			log.Printf("[tsa-pay] could not end unlinked intent %s: %v", intent.ID, aerr)
		}
		s.linkFailed(ctx, p.ID, "intent", intent.ID, err)
		return nil, nil, err
	}
	return intent, quote, nil
}

// begin takes the payment's lock for user and checks the payment is one this
// service may start. Every refusal after a fresh lock hands it back: nothing
// has been asked of a provider, so no money can be moving.
func (s *PayCheckoutService) begin(ctx context.Context, user *models.User, id uuid.UUID) (*models.PayPayment, error) {
	p, fresh, err := s.payments.Lock(ctx, user, id, s.live)
	if err != nil {
		return nil, err
	}
	if !fresh {
		// She already holds this payment: the app resumes her attempt, and a
		// second one is never started.
		return nil, ErrPayInProgress
	}
	if p.Mode != models.PayModeLive {
		s.release(ctx, p.ID, "released: not a live payment")
		return nil, ErrPayNotPayable
	}
	if p.SettlementMode != models.PaySettlementFiat {
		s.release(ctx, p.ID, "released: this payment settles in crypto")
		return nil, ErrPayWrongRoute
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
// behind it after payLockNoAttemptGrace.
func (s *PayCheckoutService) release(ctx context.Context, id uuid.UUID, reason string) {
	if err := s.payments.Release(ctx, id, reason); err != nil {
		log.Printf("[tsa-pay] could not release payment %s after a refused start: %v", id, err)
	}
}

// linkFailed is the one way a start can go wrong AFTER a provider created
// something: the attempt exists but the payment refused to link it. It is
// never released — an order or intent exists — so it fails, for support.
func (s *PayCheckoutService) linkFailed(ctx context.Context, id uuid.UUID, kind string, attemptID uuid.UUID, cause error) {
	log.Printf("[tsa-pay] ALERT pay_link_failed_total=1 payment=%s %s=%s err=%v", id, kind, attemptID, cause)
	if err := s.payments.Fail(ctx, id, "internal: could not link the attempt"); err != nil {
		log.Printf("[tsa-pay] ALERT pay_link_fail_stuck_total=1 payment=%s %s=%s err=%v", id, kind, attemptID, err)
	}
}

// payFiatAmount renders integer kobo as the naira decimal a Paycrest sell is
// sized in ("50000.00").
func payFiatAmount(kobo int64) string {
	return fmt.Sprintf("%d.%02d", kobo/100, kobo%100)
}
