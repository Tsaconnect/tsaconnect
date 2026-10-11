package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	openTestUSDT     = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894" // newService's USDT
	openTestMerchant = "0x000000000000000000000000000000000000d00d" // seedBuyAdWithTrade's ad.SellerAddress
	openTestUser     = "0x000000000000000000000000000000000000beef"
	openTestAttacker = "0x000000000000000000000000000000000000bad1"
	openTestCheap    = "0x0000000000000000000000000000000000000c4e"
)

// lostOpen seeds a JIT buy-ad trade whose openTradeWithDeposit landed on chain
// but whose confirm-open never reached the API: open_tx_hash is empty while the
// escrow reports the trade Pending exactly as prepared — the DB's amount,
// funding and token, the user as on-chain seller and the merchant as buyer.
func lostOpen(t *testing.T, amount int64) (*P2PTradeService, *mockEscrow, models.P2PTrade, *gorm.DB, time.Time) {
	t.Helper()
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPendingPayment) // open_tx_hash empty
	created := time.Date(2026, 5, 23, 11, 50, 0, 0, time.UTC)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPending, Funding: models.P2PTradeFundingDepositedByTaker,
		Seller: common.HexToAddress(openTestUser), Buyer: common.HexToAddress(openTestMerchant),
		Token:  common.HexToAddress(openTestUSDT),
		Amount: big.NewInt(amount), CreatedAt: big.NewInt(created.Unix()), Exists: true}
	esc.openedDepositEvent = &P2PTradeOpenedWithDepositEvent{TradeID: HashID(tradeID),
		Seller: common.HexToAddress(openTestUser), Buyer: common.HexToAddress(openTestMerchant),
		Token: common.HexToAddress(openTestUSDT), Amount: big.NewInt(amount)}
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	return svc, esc, tr, db, created
}

// lostPooledOpen is lostOpen for a sell-ad: the taker opened against the
// merchant's voucher, so the merchant is the on-chain seller.
func lostPooledOpen(t *testing.T) (*P2PTradeService, *mockEscrow, models.P2PTrade, *gorm.DB) {
	t.Helper()
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	merchantID, takerID := uuid.New(), uuid.New()
	now := time.Date(2026, 5, 23, 11, 50, 0, 0, time.UTC)
	ad := &models.P2PAd{
		ID: uuid.New(), SellerID: merchantID, Side: models.P2PAdSideSell,
		Token: "USDT", ChainID: 146, Amount: "100", Remaining: "70", Reserved: "30",
		FiatCurrency: "NGN", Rate: "1300", RateType: models.P2PAdRateTypeFixed,
		Status: models.P2PAdStatusActive, SellerAddress: openTestMerchant,
	}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed sell-ad: %v", err)
	}
	tr := models.P2PTrade{
		ID: uuid.New(), AdID: ad.ID, SellerID: merchantID, BuyerID: takerID, OpenerID: &takerID,
		OpenerAddress: common.HexToAddress(openTestUser).Hex(),
		Token:         "USDT", ChainID: 146, Funding: models.P2PTradeFundingPooled,
		TokenAmount: "30", FiatAmount: "39000", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPendingPayment,
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	tr.OnChainTradeID = HashIDHex(tr.ID)
	if err := db.Create(&tr).Error; err != nil {
		t.Fatalf("seed pooled trade: %v", err)
	}
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPending, Funding: models.P2PTradeFundingPooled,
		Seller: common.HexToAddress(openTestMerchant), Buyer: common.HexToAddress(openTestUser),
		Token:  common.HexToAddress(openTestUSDT),
		Amount: big.NewInt(30), CreatedAt: big.NewInt(now.Unix()), Exists: true}
	esc.openedEvent = &P2PTradeOpenedEvent{TradeID: HashID(tr.ID),
		Seller: common.HexToAddress(openTestMerchant), Buyer: common.HexToAddress(openTestUser),
		Token: common.HexToAddress(openTestUSDT), Amount: big.NewInt(30)}
	return svc, esc, tr, db
}

func openTxHash(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var tr models.P2PTrade
	if err := db.First(&tr, "id = ?", id).Error; err != nil {
		t.Fatalf("load trade: %v", err)
	}
	return tr.OpenTxHash
}

// openTamper is one way an opener can make the on-chain trade disagree with
// the one we prepared while keeping the amount right.
type openTamper struct {
	name   string
	pooled bool
	tamper func(esc *mockEscrow, db *gorm.DB, tr models.P2PTrade)
}

var openTampers = []openTamper{
	{"JIT wrong token", false, func(esc *mockEscrow, _ *gorm.DB, _ models.P2PTrade) {
		esc.onChainTrade.Token = common.HexToAddress(openTestCheap)
	}},
	{"JIT buyer is not the merchant", false, func(esc *mockEscrow, _ *gorm.DB, _ models.P2PTrade) {
		esc.onChainTrade.Buyer = common.HexToAddress(openTestAttacker)
	}},
	{"JIT ad has no merchant wallet", false, func(_ *mockEscrow, db *gorm.DB, tr models.P2PTrade) {
		db.Model(&models.P2PAd{}).Where("id = ?", tr.AdID).Update("seller_address", "")
	}},
	{"pooled wrong token", true, func(esc *mockEscrow, _ *gorm.DB, _ models.P2PTrade) {
		esc.onChainTrade.Token = common.HexToAddress(openTestCheap)
	}},
	{"pooled seller is not the merchant", true, func(esc *mockEscrow, _ *gorm.DB, _ models.P2PTrade) {
		esc.onChainTrade.Seller = common.HexToAddress(openTestAttacker)
	}},
	{"pooled funding on a JIT-shaped open", true, func(esc *mockEscrow, _ *gorm.DB, _ models.P2PTrade) {
		esc.onChainTrade.Funding = models.P2PTradeFundingDepositedByTaker
	}},
}

func tamperedOpen(t *testing.T, c openTamper) (*P2PTradeService, models.P2PTrade, *gorm.DB) {
	t.Helper()
	var (
		svc *P2PTradeService
		esc *mockEscrow
		tr  models.P2PTrade
		db  *gorm.DB
	)
	if c.pooled {
		svc, esc, tr, db = lostPooledOpen(t)
	} else {
		svc, esc, tr, db, _ = lostOpen(t, 30)
	}
	c.tamper(esc, db, tr)
	return svc, tr, db
}

func confirmAsOpener(svc *P2PTradeService, tr models.P2PTrade) (*models.P2PTrade, error) {
	opener := tr.BuyerID
	if tr.Funding == models.P2PTradeFundingDepositedByTaker {
		opener = tr.SellerID
	}
	return svc.ConfirmOpenTrade(context.Background(), tr.ID, opener, "0xopen")
}

func TestConfirmOpenTrade_RefusesOnChainTradeThatIsNotOurs(t *testing.T) {
	for _, c := range openTampers {
		t.Run(c.name, func(t *testing.T) {
			svc, tr, db := tamperedOpen(t, c)
			if _, err := confirmAsOpener(svc, tr); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("err = %v, want ErrInvalidTransition", err)
			}
			if got := openTxHash(t, db, tr.ID); got != "" {
				t.Fatalf("open_tx_hash = %q, a refused open must write nothing", got)
			}
		})
	}
}

func TestReconcile_RefusesToBackfillOnChainTradeThatIsNotOurs(t *testing.T) {
	for _, c := range openTampers {
		t.Run(c.name, func(t *testing.T) {
			svc, tr, db := tamperedOpen(t, c)
			_, _ = svc.ReconcileTrade(context.Background(), tr.ID)
			if got := openTxHash(t, db, tr.ID); got != "" {
				t.Fatalf("open_tx_hash = %q, a mismatched open must not be backfilled", got)
			}
		})
	}
}

func TestConfirmOpenTrade_AcceptsMatchingOpen(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		var (
			svc *P2PTradeService
			tr  models.P2PTrade
			db  *gorm.DB
		)
		if pooled {
			svc, _, tr, db = lostPooledOpen(t)
		} else {
			svc, _, tr, db, _ = lostOpen(t, 30)
		}
		if _, err := confirmAsOpener(svc, tr); err != nil {
			t.Fatalf("pooled=%v: matching open refused: %v", pooled, err)
		}
		if got := openTxHash(t, db, tr.ID); got != "0xopen" {
			t.Fatalf("pooled=%v: open_tx_hash = %q", pooled, got)
		}
	}
}

func TestReconcile_BackfillsMatchingPooledOpen(t *testing.T) {
	svc, _, tr, db := lostPooledOpen(t)
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil || out.Reason != "open backfilled" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if got := openTxHash(t, db, tr.ID); got != "reconciled" {
		t.Fatalf("open_tx_hash = %q", got)
	}
}

// The token address is compared case-insensitively: config may hold a
// lowercase address while the chain read is checksummed.
func TestOnChainOpenMatches_TokenCaseInsensitive(t *testing.T) {
	_, esc, tr, db := lostPooledOpen(t)
	var ad models.P2PAd
	db.First(&ad, "id = ?", tr.AdID)
	if err := onChainOpenMatches(&tr, &ad, strings.ToLower(openTestUSDT), esc.onChainTrade); err != nil {
		t.Fatalf("lowercase token address refused: %v", err)
	}
}

func TestReconcile_BackfillsLostOpenConfirm(t *testing.T) {
	svc, _, tr, db, created := lostOpen(t, 30)
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil || !out.Changed || out.Reason != "open backfilled" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.Status != models.P2PTradeStatusPendingPayment || after.OpenTxHash != "reconciled" ||
		!after.ExpiresAt.Equal(created.Add(30*time.Minute)) {
		t.Fatalf("after = %+v", after)
	}
	var audits int64
	db.Model(&models.P2PAuditLog{}).Where("trade_id = ? AND action = ?", tr.ID, models.P2PAuditReconcilerDrift).Count(&audits)
	if audits != 1 {
		t.Fatalf("reconciler drift audits = %d, want 1", audits)
	}
	if out2, _ := svc.ReconcileTrade(context.Background(), tr.ID); out2.Changed {
		t.Fatal("second pass must be a no-op")
	}
}

func TestReconcile_LostOpenWithWrongAmountIsNotBackfilled(t *testing.T) {
	svc, _, tr, db, _ := lostOpen(t, 31)
	_, _ = svc.ReconcileTrade(context.Background(), tr.ID)
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "" {
		t.Fatal("an amount mismatch must not be recorded as our open")
	}
}

func TestReconcile_LostOpenWithWrongFundingIsNotBackfilled(t *testing.T) {
	svc, esc, tr, db, _ := lostOpen(t, 30)
	esc.onChainTrade.Funding = models.P2PTradeFundingPooled
	_, _ = svc.ReconcileTrade(context.Background(), tr.ID)
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "" {
		t.Fatal("a funding mismatch must not be recorded as our open")
	}
}

// A lost open-confirm on a trade whose markPaid also landed: the backfill
// records the open and the same pass still applies the chain-Paid transition.
func TestReconcile_LostOpenThenChainPaidStillFlipsToPaid(t *testing.T) {
	svc, esc, tr, db, _ := lostOpen(t, 30)
	esc.onChainTrade.Status = OnChainP2PStatusPaid
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil || !out.Changed || out.ToStatus != models.P2PTradeStatusPaid {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "reconciled" || after.Status != models.P2PTradeStatusPaid {
		t.Fatalf("after = %+v", after)
	}
}

func TestPrepareMarkPaid_AcceptsOpenConfirmedOnChain(t *testing.T) {
	svc, _, tr, db, _ := lostOpen(t, 30)
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://res.cloudinary.com/tsa/image/upload/p.png"); err != nil {
		t.Fatalf("open is on chain, mark-paid must be allowed: %v", err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "reconciled" {
		t.Fatal("gate should backfill")
	}
}

func TestPrepareMarkPaid_RefusesWhenOpenNotOnChain(t *testing.T) {
	svc, esc, tr, _, _ := lostOpen(t, 30)
	esc.onChainTrade = nil
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://res.cloudinary.com/tsa/image/upload/p.png"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRaiseDispute_AcceptsOpenConfirmedOnChain(t *testing.T) {
	svc, _, tr, _, _ := lostOpen(t, 30)
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xa", "no fiat", nil); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRaiseDispute_RefusesWhenOpenNotOnChain(t *testing.T) {
	svc, esc, tr, db, _ := lostOpen(t, 30)
	esc.onChainTrade = nil
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xa", "no fiat", nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
	var disputes int64
	db.Model(&models.P2PDispute{}).Where("trade_id = ?", tr.ID).Count(&disputes)
	if disputes != 0 {
		t.Fatal("a refused dispute must not leave a dispute row")
	}
}

func TestConfirmOpenTrade_IdempotentOnceOpenRecordedAndMovedOn(t *testing.T) {
	svc, _, tr, db, _ := lostOpen(t, 30)
	db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Updates(map[string]any{"open_tx_hash": "reconciled", "status": models.P2PTradeStatusPaid})
	got, err := svc.ConfirmOpenTrade(context.Background(), tr.ID, tr.SellerID, "0xrealopen")
	if err != nil || got.Status != models.P2PTradeStatusPaid {
		t.Fatalf("retry-confirm after backfill must succeed: %+v %v", got, err)
	}
}

// foreignOpen is lostOpen where the on-chain trade under our id names the
// opener's own wallet as merchant: not the open we prepared.
func foreignOpen(t *testing.T, status OnChainP2PTradeStatus) (*P2PTradeService, *mockBus, models.P2PTrade, *gorm.DB) {
	t.Helper()
	svc, esc, tr, db, _ := lostOpen(t, 30)
	esc.onChainTrade.Buyer = common.HexToAddress(openTestAttacker)
	esc.onChainTrade.Status = status
	return svc, svc.bus.(*mockBus), tr, db
}

func adCapacity(t *testing.T, db *gorm.DB, adID uuid.UUID) (remaining, reserved string) {
	t.Helper()
	var ad models.P2PAd
	if err := db.First(&ad, "id = ?", adID).Error; err != nil {
		t.Fatalf("load ad: %v", err)
	}
	return ad.Remaining, ad.Reserved
}

// A refused backfill must not let a foreign trade's Paid/Released/Resolved/
// Disputed reach our row: no status flip, no events, no audit, no capacity use.
func TestReconcile_RefusedBackfillIgnoresForeignChainProgress(t *testing.T) {
	for _, st := range []OnChainP2PTradeStatus{OnChainP2PStatusPending, OnChainP2PStatusPaid,
		OnChainP2PStatusReleased, OnChainP2PStatusResolved, OnChainP2PStatusDisputed} {
		svc, bus, tr, db := foreignOpen(t, st)
		out, err := svc.ReconcileTrade(context.Background(), tr.ID)
		if err != nil || out.Changed {
			t.Fatalf("chain status %d: out=%+v err=%v, want a no-op", st, out, err)
		}
		var after models.P2PTrade
		db.First(&after, "id = ?", tr.ID)
		if after.Status != models.P2PTradeStatusPendingPayment || after.OpenTxHash != "" {
			t.Fatalf("chain status %d: trade = %s/%q, want pending_payment untouched", st, after.Status, after.OpenTxHash)
		}
		if len(bus.published) != 0 {
			t.Fatalf("chain status %d: %d events published, want none", st, len(bus.published))
		}
		var audits int64
		db.Model(&models.P2PAuditLog{}).Where("trade_id = ?", tr.ID).Count(&audits)
		if audits != 0 {
			t.Fatalf("chain status %d: %d audit rows, want none", st, audits)
		}
		if rem, res := adCapacity(t, db, tr.AdID); rem != "70" || res != "30" {
			t.Fatalf("chain status %d: ad remaining/reserved = %s/%s, want 70/30", st, rem, res)
		}
	}
}

// Chain Cancelled is the one status still applied after a refusal: it only
// restores the capacity our row reserved.
func TestReconcile_RefusedBackfillStillAppliesChainCancelled(t *testing.T) {
	svc, _, tr, db := foreignOpen(t, OnChainP2PStatusCancelled)
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil || out.ToStatus != models.P2PTradeStatusCancelled {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.Status != models.P2PTradeStatusCancelled || after.OpenTxHash != "" {
		t.Fatalf("trade = %s/%q, want cancelled with no open recorded", after.Status, after.OpenTxHash)
	}
	if rem, res := adCapacity(t, db, tr.AdID); rem != "100" || res != "0" {
		t.Fatalf("ad remaining/reserved = %s/%s, want 100/0", rem, res)
	}
}

// After a mined open receipt the trade exists, so a failed or lagging read is
// a retryable 503 — never a refusal and never a write.
func TestConfirmOpenTrade_UnreadableEscrowIsRetryable(t *testing.T) {
	cases := map[string]func(esc *mockEscrow){
		"read error":      func(esc *mockEscrow) { esc.readTradeErr = errors.New("rpc down") },
		"not visible yet": func(esc *mockEscrow) { esc.onChainTrade = nil },
	}
	for name, breakRead := range cases {
		t.Run(name, func(t *testing.T) {
			svc, esc, tr, db, _ := lostOpen(t, 30)
			breakRead(esc)
			if _, err := confirmAsOpener(svc, tr); !errors.Is(err, ErrChainUnavailable) {
				t.Fatalf("err = %v, want ErrChainUnavailable", err)
			}
			if got := openTxHash(t, db, tr.ID); got != "" {
				t.Fatalf("open_tx_hash = %q, want nothing written", got)
			}
		})
	}
}

func feeBpsOf(t *testing.T, db *gorm.DB, id uuid.UUID) *int {
	t.Helper()
	var row models.P2PTrade
	if err := db.First(&row, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return row.FeeBps
}

// ConfirmOpenTrade and the backfill both record the escrow's fee snapshot
// (platform + gas), through the one openRecordUpdates.
func TestOpenRecord_StoresSnapshottedFeeBps(t *testing.T) {
	t.Run("confirm", func(t *testing.T) {
		svc, esc, tr, db, _ := lostOpen(t, 30)
		esc.feeSnapshotPlatform, esc.feeSnapshotGas, esc.feeSnapshotFound = 150, 40, true
		if _, err := confirmAsOpener(svc, tr); err != nil {
			t.Fatal(err)
		}
		if got := feeBpsOf(t, db, tr.ID); got == nil || *got != 190 {
			t.Fatalf("fee_bps = %v, want 190", got)
		}
	})
	t.Run("backfill", func(t *testing.T) {
		svc, esc, tr, db, _ := lostOpen(t, 30)
		esc.feeSnapshotPlatform, esc.feeSnapshotFound = 50, true
		if _, err := svc.ReconcileTrade(context.Background(), tr.ID); err != nil {
			t.Fatal(err)
		}
		if got := feeBpsOf(t, db, tr.ID); got == nil || *got != 50 {
			t.Fatalf("fee_bps = %v, want 50", got)
		}
	})
	t.Run("a zero snapshot is a real zero", func(t *testing.T) {
		svc, esc, tr, db, _ := lostOpen(t, 30)
		esc.feeSnapshotFound = true
		if _, err := confirmAsOpener(svc, tr); err != nil {
			t.Fatal(err)
		}
		if got := feeBpsOf(t, db, tr.ID); got == nil || *got != 0 {
			t.Fatalf("fee_bps = %v, want 0", got)
		}
	})
	t.Run("unreadable snapshot leaves it unset", func(t *testing.T) {
		svc, _, tr, db, _ := lostOpen(t, 30)
		if _, err := confirmAsOpener(svc, tr); err != nil {
			t.Fatal(err)
		}
		if got := feeBpsOf(t, db, tr.ID); got != nil {
			t.Fatalf("fee_bps = %d, want unset", *got)
		}
	})
}

// A trade with a recorded snapshot carries the fee and the receiver's net; the
// ad carries the live rate and drops it when the chain cannot be read.
func TestTradeAndAdFeeFields(t *testing.T) {
	svc, _, tr, db, _ := lostOpen(t, 30)
	bps := 50
	db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Updates(map[string]any{"fee_bps": bps, "token_amount": "100000000"})
	got, err := svc.GetTrade(context.Background(), tr.ID, tr.BuyerID)
	if err != nil || got.FeeAmount != "500000" || got.BuyerNetAmount != "99500000" {
		t.Fatalf("trade = %+v err=%v", got, err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", tr.AdID)

	svc.SetFeeView(newFeeViewOn(&feeViewChain{legacy: 50}))
	live, err := svc.GetAd(context.Background(), ad.ID)
	svc.FillAdFees(context.Background(), live)
	if err != nil || live.FeeBps == nil || *live.FeeBps != 50 {
		t.Fatalf("ad fee = %v err=%v", live.FeeBps, err)
	}
	svc.SetFeeView(newFeeViewFailing(t))
	down, err := svc.GetAd(context.Background(), ad.ID)
	svc.FillAdFees(context.Background(), down)
	if err != nil || down.FeeBps != nil {
		t.Fatalf("ad fee must be omitted when the chain is unreadable, got %v err=%v", down.FeeBps, err)
	}
}

func TestChainsCarryLiveFeeBpsPerAsset(t *testing.T) {
	svc := &P2PTradeService{feeView: newFeeViewOn(&feeViewChain{legacy: 50})}
	out := svc.withLiveFees(context.Background(), []P2PChainInfo{{ChainID: 146, Assets: []string{"USDT", "USDC"}}})
	if out[0].FeeBps["USDT"] != 50 || out[0].FeeBps["USDC"] != 50 {
		t.Fatalf("feeBps = %v", out[0].FeeBps)
	}
	svc.feeView = newFeeViewFailing(t)
	out = svc.withLiveFees(context.Background(), []P2PChainInfo{{ChainID: 146, Assets: []string{"USDT"}}})
	if out[0].FeeBps != nil {
		t.Fatalf("unreadable chain must yield no feeBps, got %v", out[0].FeeBps)
	}
}

// ── Opener binding (final review A1) ───────────────────────────────────────
//
// openTrade sets buyer = msg.sender and openTradeWithDeposit sets seller =
// msg.sender, so anyone can open under our trade id before the user's own tx
// lands. Confirm and backfill must accept only the wallet the open was
// prepared for, and refuse (with the ALERT) a row that has none recorded.

var openerBindingCases = []openTamper{
	{"pooled front-run: on-chain buyer is not the prepared opener", true, func(esc *mockEscrow, _ *gorm.DB, _ models.P2PTrade) {
		esc.onChainTrade.Buyer = common.HexToAddress(openTestAttacker)
		esc.openedEvent.Buyer = common.HexToAddress(openTestAttacker)
	}},
	{"JIT front-run: on-chain seller is not the prepared opener", false, func(esc *mockEscrow, _ *gorm.DB, _ models.P2PTrade) {
		esc.onChainTrade.Seller = common.HexToAddress(openTestAttacker)
		esc.openedDepositEvent.Seller = common.HexToAddress(openTestAttacker)
	}},
	{"pooled legacy row: no opener recorded", true, func(_ *mockEscrow, db *gorm.DB, tr models.P2PTrade) {
		db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("opener_address", gorm.Expr("NULL"))
	}},
	{"JIT legacy row: no opener recorded", false, func(_ *mockEscrow, db *gorm.DB, tr models.P2PTrade) {
		db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("opener_address", gorm.Expr("NULL"))
	}},
}

func TestConfirmOpenTrade_RefusesOpenerItWasNotPreparedFor(t *testing.T) {
	for _, c := range openerBindingCases {
		t.Run(c.name, func(t *testing.T) {
			svc, tr, db := tamperedOpen(t, c)
			logs := captureLog(t)
			if _, err := confirmAsOpener(svc, tr); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("err = %v, want ErrInvalidTransition", err)
			}
			if got := openTxHash(t, db, tr.ID); got != "" {
				t.Fatalf("open_tx_hash = %q, a refused open must write nothing", got)
			}
			if !strings.Contains(logs.String(), "ALERT p2p_open_backfill_mismatch_total=1 path=confirm") {
				t.Fatalf("no confirm ALERT logged:\n%s", logs)
			}
		})
	}
}

func TestReconcile_RefusesToBackfillOpenerItWasNotPreparedFor(t *testing.T) {
	for _, c := range openerBindingCases {
		t.Run(c.name, func(t *testing.T) {
			svc, tr, db := tamperedOpen(t, c)
			logs := captureLog(t)
			_, _ = svc.ReconcileTrade(context.Background(), tr.ID)
			if got := openTxHash(t, db, tr.ID); got != "" {
				t.Fatalf("open_tx_hash = %q, an open by another wallet must not be backfilled", got)
			}
			if !strings.Contains(logs.String(), "ALERT p2p_open_backfill_mismatch_total=1 path=backfill") {
				t.Fatalf("no backfill ALERT logged:\n%s", logs)
			}
		})
	}
}

// The stored opener is compared case-insensitively and the client's
// callerAddress plays no part: a matching on-chain opener is accepted.
func TestOnChainOpenMatches_OpenerCaseInsensitive(t *testing.T) {
	for _, pooled := range []bool{true, false} {
		var (
			esc *mockEscrow
			tr  models.P2PTrade
			db  *gorm.DB
		)
		if pooled {
			_, esc, tr, db = lostPooledOpen(t)
		} else {
			_, esc, tr, db, _ = lostOpen(t, 30)
		}
		var ad models.P2PAd
		db.First(&ad, "id = ?", tr.AdID)
		tr.OpenerAddress = "0x" + strings.ToUpper(openTestUser[2:])
		if err := onChainOpenMatches(&tr, &ad, openTestUSDT, esc.onChainTrade); err != nil {
			t.Fatalf("pooled=%v: matching opener refused: %v", pooled, err)
		}
	}
}
