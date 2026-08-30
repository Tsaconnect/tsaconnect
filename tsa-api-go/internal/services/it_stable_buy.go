package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	// ErrStableBuyAlreadyOpen enforces one purchase awaiting payment per user.
	// The standing VA's reference identifies the USER, not the purchase, so a
	// second intent in pending_payment would make an incoming charge ambiguous.
	ErrStableBuyAlreadyOpen = errors.New("it stable buy: this user already has a purchase awaiting payment")
	// ErrStableBuyNoVA means the user has no standing virtual account yet.
	ErrStableBuyNoVA = errors.New("it stable buy: user has no virtual account; activate one first")
)

// StableBuyInstructions is where the user sends their naira: their own
// standing virtual account, unchanged between purchases.
type StableBuyInstructions struct {
	AccountNumber string `json:"accountNumber"`
	BankName      string `json:"bankName"`
	AccountName   string `json:"accountName"`
	Reference     string `json:"reference"`
}

// CreateStableBuyIntent opens a purchase and tells the user where to pay.
//
// The delivery target is validated HERE rather than at delivery time, because
// delivery is the one irreversible step: Flutterwave rejects an unsupported
// (chain, asset) pair and a rejected transfer to a live address cannot be
// recovered. Nothing is written if the target is bad.
func (s *ITService) CreateStableBuyIntent(
	ctx context.Context,
	userID uuid.UUID,
	q *StableBuyQuote,
	destChain, destAddress string,
) (*models.ITIntent, *StableBuyInstructions, error) {
	if q == nil {
		return nil, nil, errors.New("it stable buy: nil quote")
	}
	destChain = strings.ToUpper(strings.TrimSpace(destChain))
	destAddress = strings.TrimSpace(destAddress)
	if err := ValidateBuyDelivery(destChain, q.Asset, destAddress); err != nil {
		return nil, nil, err
	}
	if q.AssetUnits == nil || q.AssetUnits.Cmp(s.stableMinSell()) < 0 {
		return nil, nil, fmt.Errorf("%w: minimum delivery is %g %s",
			ErrStableBuyBelowMinimum, StableFromBaseUnits(s.stableMinSell()), q.Asset)
	}

	var va models.FiatVirtualAccount
	err := s.db.WithContext(ctx).
		// Status filter matters: handing out an inactive VA's details tells the
		// user to pay into an account that will not credit them.
		Where("user_id = ? AND currency = ? AND status = ?",
			userID, q.Currency, models.FiatVAStatusActive).
		First(&va).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrStableBuyNoVA
	} else if err != nil {
		return nil, nil, err
	}

	var open int64
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status = ? AND asset IN ?",
			userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Count(&open).Error; err != nil {
		return nil, nil, err
	}
	if open > 0 {
		return nil, nil, ErrStableBuyAlreadyOpen
	}
	// NOTE: this Count-then-Create is not atomic. Two concurrent requests can
	// both see zero and both insert. Add the backing partial unique index in
	// internal/config/database.go alongside the existing uq_it_intents_* ones:
	//
	//	CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_open_stable_buy
	//	  ON it_intents (user_id)
	//	  WHERE direction = 'buy' AND status = 'pending_payment'
	//	    AND asset IN ('USDC','USDT');
	//
	// and translate its violation below to ErrStableBuyAlreadyOpen, so the race
	// loses at the database rather than producing a second claimable intent.
	// SQLite has no such index, which is why the check above stays as well.

	intent := &models.ITIntent{
		ID:          uuid.New(),
		UserID:      userID,
		Direction:   models.ITDirectionBuy,
		Status:      models.ITStatusPendingPayment,
		Asset:       q.Asset,
		AssetAmount: q.AssetUnits.String(),
		Currency:    q.Currency,
		DestChain:   destChain,
		DestAddress: destAddress,
		LockedRate:  q.RatePerUnitMinor.String(),

		// Every numeric column must carry a value, not "". PostgreSQL rejects
		// ''::numeric outright and GORM sends unset fields in the INSERT.
		// SQLite's type affinity accepts them silently, which is how this bit
		// the sell leg. The values below are correct, not filler:
		//
		//	NgnKobo/AmountMinor  what the user pays, as quoted
		//	McgpAmount           a stablecoin buy has no MCGP leg
		//	FeeNgnKobo/NetNgnKobo  B1 charges the spread only; no fee line
		//	ReceivedAmount       unknown until the naira actually lands
		NgnKobo:     q.FiatMinor.String(),
		AmountMinor: q.FiatMinor.String(),
		RateKobo:    q.RatePerUnitMinor.String(),
		RateMinor:   q.RatePerUnitMinor.String(),
		McgpAmount:  "0",
		FeeNgnKobo:  "0",
		NetNgnKobo:  q.FiatMinor.String(),
		// PlatformKeptKobo has no `default:` tag, so GORM puts it in the INSERT
		// as '' and BeforeSave mirrors that into PlatformKeptMinor. Postgres
		// rejects ''::numeric (SQLSTATE 22P02). This is the EXACT defect the
		// sell leg hit — it sets the same field explicitly at
		// it_stable_sell.go:191. SQLite's type affinity hides it, so the test
		// suite stays green while production INSERTs fail.
		PlatformKeptKobo: "0",
		ReceivedAmount:   "0",
	}
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, nil, err
	}

	return intent, &StableBuyInstructions{
		AccountNumber: va.AccountNumber,
		BankName:      va.BankName,
		AccountName:   va.AccountName,
		Reference:     va.Reference,
	}, nil
}
