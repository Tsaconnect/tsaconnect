package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// seedActiveAd inserts an active sell ad directly (no voucher flow needed to
// test visibility and renewal).
func seedActiveAd(t *testing.T, db *gorm.DB, sellerID uuid.UUID, chainID int64, pmID *uuid.UUID, rate, remaining string) *models.P2PAd {
	t.Helper()
	ad := &models.P2PAd{
		ID: uuid.New(), SellerID: sellerID, Side: models.P2PAdSideSell, Token: "USDT", ChainID: chainID,
		Amount: remaining, Remaining: remaining, Reserved: "0", FiatCurrency: "NGN", Rate: rate,
		RateType: models.P2PAdRateTypeFixed, FloatAnchor: models.P2PAdFloatAnchorBybitSell,
		PaymentMethodIDs: stringifyUUIDs(nil), Status: models.P2PAdStatusActive, PMID: pmID,
		VoucherSignature: "0xdeadbeef",
	}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed ad: %v", err)
	}
	return ad
}

// newLPWithKey: an active LP whose registered wallet is a fresh key's address.
func (r *poolRig) newLPWithKey(t *testing.T) (*models.PaymentMerchant, func(hash [32]byte) []byte) {
	t.Helper()
	key, addr := newSellerKey(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, addr)
	return lp, func(h [32]byte) []byte { return signVoucherForTest(t, h, key) }
}

func poolAdParams() CreateAdParams {
	// TokenAmount and FiatCurrency are what the app sends; the pool branch sets
	// its own voucher size and currency (plan B P8).
	return CreateAdParams{Token: "USDT", TokenAmount: "1", FiatCurrency: "NGN", ChainID: poolTestChain}
}

func (r *poolRig) publish(t *testing.T, lp *models.PaymentMerchant, sign func([32]byte) []byte) *models.P2PAd {
	t.Helper()
	ctx := context.Background()
	res, err := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, poolAdParams())
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	h, _ := hexToHash32(res.Ad.VoucherHash)
	ad, err := r.svc.SignAd(ctx, res.Ad.ID, lp.UserID, lp.OperatorWalletAddress, sign(h))
	if err != nil {
		t.Fatalf("SignAd: %v", err)
	}
	return ad
}

func hexToHash32(s string) ([32]byte, error) { return decodeHex32(s) }

func TestCreatePoolAd_VoucherMaxIs100xLPMax18dp(t *testing.T) {
	r := newPoolRig(t)
	lp, _ := r.newLPWithKey(t)
	res, err := r.svc.CreateAd(context.Background(), lp.UserID, lp.OperatorWalletAddress, poolAdParams())
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	want := new(big.Int).Mul(usdt18(5000), big.NewInt(100)) // 500,000 USDT at 18 dp
	if res.Ad.Amount != want.String() || res.Ad.Remaining != want.String() || res.TypedData.Message.MaxAmount != want.String() {
		t.Fatalf("maxAmount = %s / %s, want %s", res.Ad.Amount, res.TypedData.Message.MaxAmount, want)
	}
	if res.Ad.PMID == nil || *res.Ad.PMID != lp.ID || res.Ad.ChainID != poolTestChain || res.Ad.Rate != "0" ||
		len(res.Ad.PaymentMethodIDs) != 0 || res.Ad.Status != models.P2PAdStatusPending || res.Ad.FiatCurrency != "NGN" {
		t.Fatalf("ad = %+v", res.Ad)
	}
	if res.TypedData.Domain.ChainID != poolTestChain || res.TypedData.Domain.VerifyingContract != poolTestEscrow ||
		res.TypedData.Message.MinPerTrade != "0" || res.TypedData.Message.MaxPerTrade != "0" {
		t.Fatalf("typed data = %+v", res.TypedData)
	}
}

func TestCreatePoolAd_DeadlineIs90Days(t *testing.T) {
	r := newPoolRig(t)
	lp, _ := r.newLPWithKey(t)
	res, _ := r.svc.CreateAd(context.Background(), lp.UserID, lp.OperatorWalletAddress, poolAdParams())
	want := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC).Add(90 * 24 * time.Hour)
	if res.Ad.VoucherDeadline == nil || !res.Ad.VoucherDeadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", res.Ad.VoucherDeadline, want)
	}
}

func TestCreatePoolAd_OneLiveStockAdUnlessRenewDue(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	old := r.publish(t, lp, sign)
	if _, err := r.svc.CreateAd(context.Background(), lp.UserID, lp.OperatorWalletAddress, poolAdParams()); !errors.Is(err, ErrPoolStockAdExists) {
		t.Fatalf("second stock ad = %v, want ErrPoolStockAdExists", err)
	}
	// 10 days left: due (S28), so a renewal may be created.
	soon := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	r.db.Model(&models.P2PAd{}).Where("id = ?", old.ID).Update("voucher_deadline", soon)
	if _, err := r.svc.CreateAd(context.Background(), lp.UserID, lp.OperatorWalletAddress, poolAdParams()); err != nil {
		t.Fatalf("renewal create: %v", err)
	}
}

func TestCreatePoolAd_ReplacesUnsignedDraft(t *testing.T) {
	r := newPoolRig(t)
	lp, _ := r.newLPWithKey(t)
	ctx := context.Background()
	first, _ := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, poolAdParams())
	if _, err := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, poolAdParams()); err != nil {
		t.Fatalf("a never-signed draft must not block: %v", err)
	}
	var draft models.P2PAd
	r.db.First(&draft, "id = ?", first.Ad.ID)
	if draft.Status != models.P2PAdStatusCancelled {
		t.Fatalf("draft status = %s, want cancelled", draft.Status)
	}
}

func TestSignPoolAd_ClosesSupersededAd(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	old := r.publish(t, lp, sign)
	r.db.Model(&models.P2PAd{}).Where("id = ?", old.ID).Update("voucher_deadline", time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	fresh := r.publish(t, lp, sign)
	var reloaded models.P2PAd
	r.db.First(&reloaded, "id = ?", old.ID)
	if fresh.Status != models.P2PAdStatusActive || reloaded.Status != models.P2PAdStatusCancelled {
		t.Fatalf("fresh=%s old=%s", fresh.Status, reloaded.Status)
	}
	if got, _ := r.svc.LatestStockAd(context.Background(), lp, poolTestChain, "USDT"); got == nil || got.ID != fresh.ID {
		t.Fatalf("latest = %+v", got)
	}
}

// Review Focus 3.
func TestCreatePoolAd_RefusesBuySideNonLPAndWrongWallet(t *testing.T) {
	r := newPoolRig(t)
	lp, _ := r.newLPWithKey(t)
	ctx := context.Background()
	buy := poolAdParams()
	buy.Side = models.P2PAdSideBuy
	if _, err := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, buy); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("buy side: %v", err)
	}
	if _, err := r.svc.CreateAd(ctx, lp.UserID, "0x00000000000000000000000000000000000000ee", poolAdParams()); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("other wallet: %v", err)
	}
	merchant := seedPMRow(t, r.db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, lpWallet)
	if _, err := r.svc.CreateAd(ctx, merchant.UserID, lpWallet, poolAdParams()); !errors.Is(err, ErrAssetNotTradeable) {
		t.Errorf("a PM on the pool chain: %v", err)
	}
}

// Review I3: a paused stock ad can be resumed by its LP, and a paused one never
// blocks a fresh stock ad.
func TestPoolStockAd_PauseThenResume(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	ad := r.publish(t, lp, sign)
	ctx := context.Background()
	if err := r.svc.PauseAd(ctx, ad.ID, lp.UserID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := r.svc.ResumeAd(ctx, ad.ID, lp.UserID); err != nil {
		t.Fatalf("resume on the pool chain: %v", err)
	}
	other := seedPMRow(t, r.db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, lpWallet)
	r.db.Model(&models.P2PAd{}).Where("id = ?", ad.ID).Update("status", models.P2PAdStatusPaused)
	if err := r.svc.ResumeAd(ctx, ad.ID, other.UserID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("someone else resuming: %v", err)
	}
}

func TestCreatePoolAd_PausedIsReplaced(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	old := r.publish(t, lp, sign)
	_ = r.svc.PauseAd(context.Background(), old.ID, lp.UserID)
	fresh := r.publish(t, lp, sign)
	var reloaded models.P2PAd
	r.db.First(&reloaded, "id = ?", old.ID)
	if fresh.Status != models.P2PAdStatusActive || reloaded.Status != models.P2PAdStatusCancelled {
		t.Fatalf("fresh=%s paused-old=%s", fresh.Status, reloaded.Status)
	}
}

// Roadmap risk 6 (pure rule; Task 10 pins the sweep).
func TestPoolVoucherRenewDue_UsesTheVouchersOwnMaxAmount(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	far := now.Add(60 * 24 * time.Hour)
	ad := func(max, rem *big.Int) *models.P2PAd {
		return &models.P2PAd{Amount: max.String(), Remaining: rem.String(), VoucherDeadline: &far}
	}
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100)) // signed at a $5,000 max
	if poolVoucherRenewDue(ad(own, usdt18(10001)), now) {
		t.Fatal("remaining above 2 × own max ÷ 100 (10,000) is not due")
	}
	if !poolVoucherRenewDue(ad(own, usdt18(9999)), now) {
		t.Fatal("remaining below 2 × own max ÷ 100 is due")
	}
	// Signed when pool_lp_max was $1,000: its floor is 2,000, whatever today's
	// max ($5,000 here, floor 10,000) says.
	older := new(big.Int).Mul(usdt18(1000), big.NewInt(100))
	if poolVoucherRenewDue(ad(older, usdt18(2500)), now) {
		t.Fatal("an older voucher is measured against its own max, not today's")
	}
	if !poolVoucherRenewDue(ad(older, usdt18(1999)), now) {
		t.Fatal("below the older voucher's own floor is due")
	}
	near := now.Add(13 * 24 * time.Hour)
	if !poolVoucherRenewDue(&models.P2PAd{Amount: own.String(), Remaining: own.String(), VoucherDeadline: &near}, now) {
		t.Fatal("13 days left is due")
	}
}

// seedTradeOn inserts a trade on adID in the given status.
func seedTradeOn(t *testing.T, db *gorm.DB, adID uuid.UUID, status string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO p2p_trades (id, ad_id, buyer_id, seller_id, status, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		uuid.New(), adID, uuid.New(), uuid.New(), status, time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
}

func makeRenewDue(t *testing.T, db *gorm.DB, adID uuid.UUID) {
	t.Helper()
	if err := db.Model(&models.P2PAd{}).Where("id = ?", adID).
		Update("voucher_deadline", time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatalf("make due: %v", err)
	}
}

// A superseded stock ad still carrying a live trade stays live until the trade
// ends; one whose trades have all ended is closed.
func TestSignPoolAd_SupersededAdWithLiveTradeStays(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	busy := r.publish(t, lp, sign)
	makeRenewDue(t, r.db, busy.ID)
	seedTradeOn(t, r.db, busy.ID, models.P2PTradeStatusPaid)
	seedTradeOn(t, r.db, busy.ID, models.P2PTradeStatusReleased)
	fresh := r.publish(t, lp, sign)
	var reloaded models.P2PAd
	r.db.First(&reloaded, "id = ?", busy.ID)
	if reloaded.Status != models.P2PAdStatusActive {
		t.Fatalf("ad with a live trade = %s, want active", reloaded.Status)
	}
	if got, _ := r.svc.LatestStockAd(context.Background(), lp, poolTestChain, "USDT"); got == nil || got.ID != fresh.ID {
		t.Fatalf("latest = %+v, want the fresh ad", got)
	}

	// The fresh ad's trades have all ended (cancelled, resolved): the next
	// renewal closes it.
	makeRenewDue(t, r.db, fresh.ID)
	seedTradeOn(t, r.db, fresh.ID, models.P2PTradeStatusCancelled)
	seedTradeOn(t, r.db, fresh.ID, models.P2PTradeStatusResolved)
	r.publish(t, lp, sign)
	var ended models.P2PAd
	r.db.First(&ended, "id = ?", fresh.ID)
	if ended.Status != models.P2PAdStatusCancelled {
		t.Fatalf("ad whose trades ended = %s, want cancelled", ended.Status)
	}
}

// No over-commit: a renewal is a fresh 100 × max voucher, never old + new.
func TestCreatePoolAd_RenewalVoucherIsNotCumulative(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	old := r.publish(t, lp, sign)
	makeRenewDue(t, r.db, old.ID)
	fresh := r.publish(t, lp, sign)
	want := new(big.Int).Mul(usdt18(5000), big.NewInt(100)).String()
	if fresh.Amount != want || fresh.Remaining != want {
		t.Fatalf("renewal voucher = %s / %s, want %s", fresh.Amount, fresh.Remaining, want)
	}
}

// A refused create changes nothing: a pending draft is only replaced by a
// create that goes through.
func TestCreatePoolAd_RefusedCreateKeepsTheDraft(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	ctx := context.Background()
	old := r.publish(t, lp, sign)
	makeRenewDue(t, r.db, old.ID)
	draft, err := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, poolAdParams())
	if err != nil {
		t.Fatalf("renewal draft: %v", err)
	}
	r.db.Model(&models.P2PAd{}).Where("id = ?", old.ID).Update("voucher_deadline", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, poolAdParams()); !errors.Is(err, ErrPoolStockAdExists) {
		t.Fatalf("create while not due = %v", err)
	}
	var reloaded models.P2PAd
	r.db.First(&reloaded, "id = ?", draft.Ad.ID)
	if reloaded.Status != models.P2PAdStatusPending {
		t.Fatalf("draft = %s after a refused create, want pending", reloaded.Status)
	}
}

// SignAd goes through openableFor: the LP must still be active and the pool
// open, and only the registered wallet may sign.
func TestSignPoolAd_OnlyActiveLPRegisteredWalletPoolOpen(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	ctx := context.Background()
	draft := func() [32]byte {
		res, err := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, poolAdParams())
		if err != nil {
			t.Fatalf("CreateAd: %v", err)
		}
		h, _ := hexToHash32(res.Ad.VoucherHash)
		return h
	}
	latestDraft := func() uuid.UUID {
		var ad models.P2PAd
		r.db.Where("pm_id = ? AND status = ?", lp.ID, models.P2PAdStatusPending).First(&ad)
		return ad.ID
	}

	// A valid signature by another wallet over the voucher re-pointed at it.
	otherKey, otherAddr := newSellerKey(t)
	h := draft()
	var pending models.P2PAd
	r.db.First(&pending, "id = ?", latestDraft())
	v, err := r.svc.adVoucher(&pending, otherAddr)
	if err != nil {
		t.Fatalf("voucher: %v", err)
	}
	otherHash, _ := r.pool.HashVoucher(v)
	if _, err := r.svc.SignAd(ctx, pending.ID, lp.UserID, otherAddr, signVoucherForTest(t, otherHash, otherKey)); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("sign from another wallet = %v, want ErrInvalidParams", err)
	}

	r.svc.SetPoolGate(fakePoolOpen(false))
	if _, err := r.svc.SignAd(ctx, latestDraft(), lp.UserID, lp.OperatorWalletAddress, sign(h)); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("sign while the pool is closed = %v, want ErrPoolClosed", err)
	}
	if _, err := r.svc.CreateAd(ctx, lp.UserID, lp.OperatorWalletAddress, poolAdParams()); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("create while the pool is closed = %v, want ErrPoolClosed", err)
	}
	r.svc.SetPoolGate(fakePoolOpen(true))

	r.db.Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).Update("status", models.PaymentMerchantStatusPaused)
	if _, err := r.svc.SignAd(ctx, latestDraft(), lp.UserID, lp.OperatorWalletAddress, sign(h)); !errors.Is(err, ErrAssetNotTradeable) {
		t.Fatalf("sign by a paused LP = %v, want ErrAssetNotTradeable", err)
	}
}

// Review I3 with the pool's gates: a paused stock ad stays paused while the
// pool is closed or its LP is no longer active.
func TestPoolStockAd_ResumeNeedsActiveLPAndOpenPool(t *testing.T) {
	r := newPoolRig(t)
	lp, sign := r.newLPWithKey(t)
	ad := r.publish(t, lp, sign)
	ctx := context.Background()
	if err := r.svc.PauseAd(ctx, ad.ID, lp.UserID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	r.svc.SetPoolGate(fakePoolOpen(false))
	if err := r.svc.ResumeAd(ctx, ad.ID, lp.UserID); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("resume while closed = %v", err)
	}
	r.svc.SetPoolGate(fakePoolOpen(true))
	r.db.Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).Update("status", models.PaymentMerchantStatusPaused)
	if err := r.svc.ResumeAd(ctx, ad.ID, lp.UserID); !errors.Is(err, ErrAssetNotTradeable) {
		t.Fatalf("resume by a paused LP = %v", err)
	}
}
