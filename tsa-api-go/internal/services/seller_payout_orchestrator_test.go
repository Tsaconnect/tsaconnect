package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// fakePayoutSeller stands in for PaycrestOrderService: it records the request
// and writes the order row the real one would.
type fakePayoutSeller struct {
	db    *gorm.DB
	err   error
	calls int
	last  PaycrestSellRequest
	max   *big.Int
	// deposit is the order's TokenAmount (decimal USDC).
	deposit string
}

func (f *fakePayoutSeller) CreateSellForFiat(_ context.Context, req PaycrestSellRequest, _ bool, max *big.Int) (*models.PaycrestOrderRecord, error) {
	f.calls++
	f.last, f.max = req, max
	if f.err != nil {
		return nil, f.err
	}
	until := sponsorTestNow.Add(time.Hour)
	row := &models.PaycrestOrderRecord{
		ID: uuid.New(), UserID: req.UserID, Direction: models.PaycrestDirectionSell,
		Status: models.PaycrestStatusAwaitingDeposit, Reference: "PCS-" + uuid.NewString(),
		FiatCode: req.FiatCode, FiatAmount: req.FiatAmount, Token: req.Token, ChainKey: req.ChainKey,
		TokenAmount: f.deposit, DepositAddress: sponsorTestDeposit, ValidUntil: &until,
		WalletAddress: req.WalletAddress, SellerPayoutID: req.SellerPayoutID,
		CreatedAt: sponsorTestNow,
	}
	return row, f.db.Create(row).Error
}

// fakeReleaseProver answers the escrow's OrderReleased lookup.
type fakeReleaseProver struct {
	log *types.Log
	err error
	// asked records what was looked up.
	event, addr string
	orderID     [32]byte
}

func (f *fakeReleaseProver) VerifyOrderEvent(_, escrowAddr, event string, orderID [32]byte) (*types.Log, error) {
	f.event, f.addr, f.orderID = event, escrowAddr, orderID
	return f.log, f.err
}

// releaseLog is the OrderReleased the escrow emits: paid to seller, amount.
func releaseLog(t *testing.T, orderID [32]byte, seller string, amount *big.Int) *types.Log {
	t.Helper()
	data, err := parsedEscrowABI.Events["OrderReleased"].Inputs.NonIndexed().Pack(amount, big.NewInt(2_000000), big.NewInt(0), big.NewInt(0))
	if err != nil {
		t.Fatal(err)
	}
	return &types.Log{
		Topics: []common.Hash{parsedEscrowABI.Events["OrderReleased"].ID, common.Hash(orderID), common.BytesToHash(common.HexToAddress(seller).Bytes())},
		Data:   data,
	}
}

// payoutRig is one confirmed marketplace order whose release went to the
// treasury: sonic holds the release, base the bridge landing and the deposit.
type payoutRig struct {
	t        *testing.T
	db       *gorm.DB
	svc      *SellerPayoutOrchestrator
	treasury *Treasury
	lifi     *fakeLiFiQuoter
	status   *fakeLiFiStatus
	sonicTx  *fakeTransferVerifier
	baseTx   *fakeTransferVerifier
	sonic    *sponsorRig
	base     *sponsorRig
	seller   *fakePayoutSeller
	escrow   *fakeReleaseProver
	payout   *models.SellerPayoutOrder
	order    models.Order
	clock    time.Time
	legStore *GormRampLegStore
}

func newPayoutRig(t *testing.T) *payoutRig {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}, &models.RampLeg{}, &models.SellerPayoutOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Raw DDL for the marketplace order: the model carries Postgres-only defaults.
	if err := db.Exec(`CREATE TABLE orders (id TEXT PRIMARY KEY, status TEXT, release_tx_hash TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	sonic, _ := newSponsorRig("sonic", "USDC")
	base, _ := newSponsorRig("base", "USDC")
	r := &payoutRig{t: t, db: db, sonic: sonic, base: base, clock: sponsorTestNow, legStore: &GormRampLegStore{DB: db}}
	r.treasury = NewTreasury(map[string]*TreasuryLane{
		"sonic": {SponsorLane: sonic.lane, ChainID: 146, Routers: treasuryTestRouters},
		"base":  {SponsorLane: base.lane, ChainID: 8453, Routers: treasuryTestRouters},
	}, TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000}, func() time.Time { return r.clock })

	r.lifi = &fakeLiFiQuoter{route: &LiFiRoute{
		To: treasuryTestRouter, Data: testBridgeCalldata(t, r.treasury.Address(), 8453, false),
		ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across",
	}}
	r.status = newFakeLiFiStatus(t, r.treasury.Address())
	r.sonicTx = &fakeTransferVerifier{amount: big.NewInt(100_000000)}
	r.baseTx = &fakeTransferVerifier{amount: new(big.Int)}
	r.seller = &fakePayoutSeller{db: db, deposit: "99.600000"}
	r.escrow = &fakeReleaseProver{}

	r.svc = NewSellerPayoutOrchestrator(db, r.treasury, r.legStore, r.lifi,
		NewLiFiStatusClient(r.status.srv.URL, ""),
		map[string]ERC20TransferVerifier{"sonic": r.sonicTx, "base": r.baseTx},
		r.seller, r.escrow, "0xescrow", 30*time.Minute, 0, func() time.Time { return r.clock })

	r.order = models.Order{ID: uuid.New(), Status: models.OrderStatusCompleted, ReleaseTxHash: "0xrelease"}
	if err := db.Exec(`INSERT INTO orders (id, status, release_tx_hash) VALUES (?, ?, ?)`,
		r.order.ID.String(), r.order.Status, r.order.ReleaseTxHash).Error; err != nil {
		t.Fatal(err)
	}
	r.payout = &models.SellerPayoutOrder{
		ID: uuid.New(), MarketplaceOrderID: r.order.ID, SellerUserID: uuid.New(),
		Currency: "NGN", InstitutionCode: "058", InstitutionName: "GTBank",
		AccountNumber: "0123456789", AccountName: "ADA OBI",
		ChainKey: "sonic", Token: "USDC", ExpectedAmount: "100000000", FiatTargetKobo: 15_000_000,
		Status: models.SellerPayoutAwaitingRelease, CreatedAt: r.clock, UpdatedAt: r.clock,
	}
	if err := db.Create(r.payout).Error; err != nil {
		t.Fatal(err)
	}
	r.escrow.log = releaseLog(t, GenerateOrderID(r.order.ID), r.treasury.Address(), big.NewInt(100_000000))
	return r
}

func (r *payoutRig) reload() *models.SellerPayoutOrder {
	r.t.Helper()
	var out models.SellerPayoutOrder
	if err := r.db.First(&out, "id = ?", r.payout.ID).Error; err != nil {
		r.t.Fatal(err)
	}
	return &out
}

// step runs one Advance against the current row.
func (r *payoutRig) step() error {
	return r.svc.Advance(context.Background(), r.reload())
}

func (r *payoutRig) mustStep() {
	r.t.Helper()
	if err := r.step(); err != nil {
		r.t.Fatalf("Advance: %v", err)
	}
}

func (r *payoutRig) wantStatus(want string) *models.SellerPayoutOrder {
	r.t.Helper()
	got := r.reload()
	if got.Status != want {
		r.t.Fatalf("status = %s (%s); want %s", got.Status, got.FailureReason, want)
	}
	return got
}

func (r *payoutRig) legs(kind string) []models.RampLeg {
	r.t.Helper()
	all, err := r.legStore.ForOrder(context.Background(), r.payout.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []models.RampLeg
	for _, l := range all {
		if l.Kind == kind {
			out = append(out, l)
		}
	}
	return out
}

func (r *payoutRig) broadcasts() int { return r.sonic.signer.calls() + r.base.signer.calls() }

// toBridged takes the payout through release and the bridge landing.
func (r *payoutRig) toBridged() {
	r.t.Helper()
	r.mustStep() // awaiting_release -> received
	r.wantStatus(models.SellerPayoutReceived)
	r.mustStep() // bridge sent
	r.wantStatus(models.SellerPayoutBridging)
	r.status.set(LiFiStatusDone, "99800000")
	r.status.setSubstatus(LiFiSubstatusCompleted)
	r.baseTx.mu.Lock()
	r.baseTx.amount = big.NewInt(99_800000)
	r.baseTx.mu.Unlock()
	r.mustStep()
	r.wantStatus(models.SellerPayoutBridged)
}

func TestSellerPayout_PaysTheSellerEndToEnd(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()

	r.mustStep() // opens the sale
	got := r.wantStatus(models.SellerPayoutOrdered)
	if got.PaycrestOrderID == nil || r.seller.calls != 1 {
		t.Fatalf("payout = %+v, sell calls = %d; want one order linked", got, r.seller.calls)
	}
	// The request pays the snapshot destination exactly, refunds to the
	// treasury, and is marked as the payout's.
	req := r.seller.last
	if req.FiatAmount != "150000.00" || req.BankCode != "058" || req.AccountNo != "0123456789" ||
		req.WalletAddress != r.treasury.Address() || req.SellerPayoutID == nil || *req.SellerPayoutID != r.payout.ID ||
		req.ChainKey != "base" || req.Token != "USDC" || !req.KeepSurplus {
		t.Fatalf("sell request = %+v", req)
	}
	// Drift is capped at 0.5% of what landed: 99.8 -> 100.2989.
	if r.seller.max.String() != "100299000" {
		t.Fatalf("deposit ceiling = %s; want landing + 50 bps", r.seller.max)
	}

	sent := r.broadcasts()
	r.mustStep() // deposit sent
	r.wantStatus(models.SellerPayoutDepositing)
	if r.broadcasts() != sent+1 {
		t.Fatalf("broadcasts = %d; want one deposit transfer", r.broadcasts()-sent)
	}
	_, _, to, amount := r.base.native.transferCall()
	if to != sponsorTestDeposit || amount.String() != "99600000" {
		t.Fatalf("deposit = %s %s; want 99.6 USDC to the Paycrest address", amount, to)
	}

	r.baseTx.mu.Lock()
	r.baseTx.amount = big.NewInt(99_600000)
	r.baseTx.mu.Unlock()
	r.mustStep()
	r.wantStatus(models.SellerPayoutDeposited)
	var order models.PaycrestOrderRecord
	r.db.First(&order, "id = ?", *got.PaycrestOrderID)
	if order.PaymentClaimedAt == nil {
		t.Fatal("a proven treasury deposit must be claimed or Paycrest reads it as unmatched")
	}

	r.mustStep() // still awaiting Paycrest
	r.wantStatus(models.SellerPayoutDeposited)
	r.db.Model(&order).Update("status", models.PaycrestStatusSettled)
	r.mustStep()
	r.wantStatus(models.SellerPayoutPaid)
	if r.seller.calls != 1 || len(r.legs(models.RampLegKindDeposit)) != 1 || len(r.legs(models.RampLegKindBridge)) != 1 {
		t.Fatalf("calls=%d deposits=%d bridges=%d; want exactly one of each", r.seller.calls,
			len(r.legs(models.RampLegKindDeposit)), len(r.legs(models.RampLegKindBridge)))
	}
}

func TestSellerPayout_MovesNothingOnAnOrderThatIsNotConfirmed(t *testing.T) {
	r := newPayoutRig(t)
	r.db.Exec(`UPDATE orders SET status = ? WHERE id = ?`, models.OrderStatusDelivered, r.order.ID.String())
	r.mustStep()
	r.wantStatus(models.SellerPayoutAwaitingRelease)
	if r.sonicTx.calls != 0 || r.broadcasts() != 0 {
		t.Fatal("an unconfirmed order must not even be looked up on chain")
	}
}

func TestSellerPayout_ProvesTheReleaseBeforeBelievingIt(t *testing.T) {
	other := uuid.New()
	cases := map[string]struct {
		setup func(*payoutRig)
		want  string
	}{
		"receipt not mined yet":     {func(r *payoutRig) { r.escrow.log, r.escrow.err = nil, ErrReceiptPending }, models.SellerPayoutAwaitingRelease},
		"node unreadable":           {func(r *payoutRig) { r.escrow.log, r.escrow.err = nil, errors.New("rpc down") }, models.SellerPayoutAwaitingRelease},
		"no release for this order": {func(r *payoutRig) { r.escrow.log, r.escrow.err = nil, ErrOrderEventNotFound }, models.SellerPayoutNeedsReview},
		"released to someone else": {func(r *payoutRig) {
			r.escrow.log = releaseLog(t, GenerateOrderID(r.order.ID), sponsorTestUser, big.NewInt(100_000000))
		}, models.SellerPayoutNeedsReview},
		"released a different amount": {func(r *payoutRig) {
			r.escrow.log = releaseLog(t, GenerateOrderID(r.order.ID), r.treasury.Address(), big.NewInt(150_000000))
		}, models.SellerPayoutNeedsReview},
		"transfer not mined yet":               {func(r *payoutRig) { r.sonicTx.err = errors.New("not found") }, models.SellerPayoutAwaitingRelease},
		"transfer paid someone else":           {func(r *payoutRig) { r.sonicTx.err = ErrTransferWrongRecipient }, models.SellerPayoutNeedsReview},
		"treasury credited less than reported": {func(r *payoutRig) { r.sonicTx.amount = big.NewInt(50_000000) }, models.SellerPayoutNeedsReview},
	}
	_ = other
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newPayoutRig(t)
			c.setup(r)
			r.mustStep()
			r.wantStatus(c.want)
			if r.broadcasts() != 0 {
				t.Fatal("nothing may move on an unproven release")
			}
		})
	}
}

// What is sold is what the escrow reported, however much more the transaction
// happened to credit the treasury.
func TestSellerPayout_AnOverCreditedTransactionDoesNotEnlargeThePayout(t *testing.T) {
	r := newPayoutRig(t)
	r.sonicTx.amount = big.NewInt(900_000000)
	r.mustStep()
	if got := r.wantStatus(models.SellerPayoutReceived); got.ReceivedAmount != "100000000" {
		t.Fatalf("received = %s; want the escrow's reported 100 USDC", got.ReceivedAmount)
	}
}

func TestSellerPayout_LooksTheReleaseUpOnTheOrdersOwnEscrow(t *testing.T) {
	r := newPayoutRig(t)
	r.mustStep()
	if r.escrow.event != "OrderReleased" || r.escrow.addr != "0xescrow" || r.escrow.orderID != GenerateOrderID(r.order.ID) {
		t.Fatalf("lookup = %s %s %x", r.escrow.event, r.escrow.addr, r.escrow.orderID)
	}
}

func TestSellerPayout_AnOrderThatWentBackToTheBuyerIsCancelled(t *testing.T) {
	r := newPayoutRig(t)
	r.db.Exec(`UPDATE orders SET status = ? WHERE id = ?`, models.OrderStatusRefunded, r.order.ID.String())
	r.mustStep()
	r.wantStatus(models.SellerPayoutCancelled)
}

func TestSellerPayout_NeverBridgesTwiceForOneAttempt(t *testing.T) {
	r := newPayoutRig(t)
	r.mustStep() // received
	r.mustStep() // bridge broadcast
	sent := r.broadcasts()
	r.mustStep() // still pending: observe only
	r.mustStep()
	if r.broadcasts() != sent || len(r.legs(models.RampLegKindBridge)) != 1 {
		t.Fatalf("broadcasts %d->%d, legs %d; a pending bridge must only be observed", sent, r.broadcasts(), len(r.legs(models.RampLegKindBridge)))
	}
}

func TestSellerPayout_NeverRetriesABridgeThatAlreadyBroadcast(t *testing.T) {
	r := newPayoutRig(t)
	r.mustStep()
	r.mustStep() // attempt 1 broadcasts
	sent := r.broadcasts()
	r.status.set(LiFiStatusFailed, "")
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
	r.clock = r.clock.Add(5 * time.Minute)
	r.mustStep()
	if len(r.legs(models.RampLegKindBridge)) != 1 || r.broadcasts() != sent {
		t.Fatal("a broadcast bridge that LiFi calls failed needs a person, never a second attempt")
	}
}

func TestSellerPayout_RefusesABridgeRouteThatCostsTooMuch(t *testing.T) {
	r := newPayoutRig(t)
	r.lifi.route.ToAmountUSD = 98 // 200 bps
	r.mustStep()
	r.mustStep()
	if r.broadcasts() != 0 {
		t.Fatal("a route over the budget must not be sent")
	}
	if legs := r.legs(models.RampLegKindBridge); len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("bridge legs = %+v; want the attempt freed", legs)
	}
}

func TestSellerPayout_RefusesARoutePayingSomeoneElse(t *testing.T) {
	r := newPayoutRig(t)
	r.lifi.route.Data = testBridgeCalldata(t, sponsorTestUser, 8453, false)
	r.mustStep()
	r.mustStep()
	if r.broadcasts() != 0 {
		t.Fatal("the route's calldata must pay the treasury or nothing is sent")
	}
}

func TestSellerPayout_NeverSettlesAPartialBridge(t *testing.T) {
	r := newPayoutRig(t)
	r.mustStep()
	r.mustStep()
	r.status.set(LiFiStatusDone, "99800000")
	r.status.setSubstatus(LiFiSubstatusPartial)
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
}

func TestSellerPayout_ADriftBeyondWhatTheyAbsorbParksForAPerson(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()
	r.seller.err = &SellShortfallError{MissingBaseUnits: big.NewInt(900_000)}
	r.mustStep()
	got := r.wantStatus(models.SellerPayoutNeedsReview)
	if got.PaycrestOrderID != nil || r.broadcasts() != 1 {
		t.Fatalf("payout = %+v, broadcasts = %d; want parked with nothing deposited", got, r.broadcasts())
	}
}

func TestSellerPayout_NeverAsksPaycrestAgainWhenAnOrderMayExist(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()
	r.seller.err = ErrSellAttemptMayExist
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
	r.seller.err = nil
	r.mustStep()
	r.mustStep()
	if r.seller.calls != 1 {
		t.Fatalf("sell calls = %d; an uncertain attempt is never repeated", r.seller.calls)
	}
}

func TestSellerPayout_ARefusedSaleIsRetriedThenParked(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()
	r.seller.err = ErrSellRateMoved
	r.mustStep()
	r.wantStatus(models.SellerPayoutBridged) // nothing was opened: try again
	r.mustStep()
	r.wantStatus(models.SellerPayoutBridged)
	r.clock = r.clock.Add(31 * time.Minute)
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
}

func TestSellerPayout_AdoptsASaleItOpenedBeforeItStopped(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()
	// Paycrest opened the order but the payout never recorded it.
	r.mustStep()
	order := r.reload().PaycrestOrderID
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).
		Updates(map[string]any{"status": models.SellerPayoutSelling, "paycrest_order_id": nil})
	r.mustStep()
	got := r.wantStatus(models.SellerPayoutOrdered)
	if got.PaycrestOrderID == nil || *got.PaycrestOrderID != *order || r.seller.calls != 1 {
		t.Fatalf("payout = %+v, calls = %d; want the existing order adopted, not a second opened", got, r.seller.calls)
	}
}

func TestSellerPayout_NeverDepositsTwice(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()
	r.mustStep() // ordered
	r.mustStep() // deposit sent
	sent := r.broadcasts()
	r.mustStep() // pending: observe only
	r.mustStep()
	if r.broadcasts() != sent || len(r.legs(models.RampLegKindDeposit)) != 1 {
		t.Fatalf("deposit legs = %d, extra broadcasts = %d", len(r.legs(models.RampLegKindDeposit)), r.broadcasts()-sent)
	}
}

func TestSellerPayout_DoesNotFundAnOrderAboutToExpire(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()
	r.mustStep() // ordered
	r.clock = sponsorTestNow.Add(time.Hour - time.Minute)
	sent := r.broadcasts()
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
	if r.broadcasts() != sent {
		t.Fatal("a deposit that would land after the order lapsed must not be sent")
	}
}

func TestSellerPayout_ADepositThatDidNotReachPaycrestParks(t *testing.T) {
	r := newPayoutRig(t)
	r.toBridged()
	r.mustStep()
	r.mustStep()
	r.baseTx.mu.Lock()
	r.baseTx.err = ErrTransferWrongRecipient
	r.baseTx.mu.Unlock()
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
}

// Money that came back from Paycrest is a person's call. The machine never
// pays the seller's wallet with it.
func TestSellerPayout_ARefundedOrderParksAndPaysNoOne(t *testing.T) {
	for _, status := range []string{models.PaycrestStatusRefunded, models.PaycrestStatusExpired, models.PaycrestStatusUnmatched,
		models.PaycrestStatusFailed, models.PaycrestStatusCancelled} {
		t.Run(status, func(t *testing.T) {
			r := newPayoutRig(t)
			r.toBridged()
			r.mustStep() // ordered
			r.mustStep() // deposit sent
			r.baseTx.mu.Lock()
			r.baseTx.amount = big.NewInt(99_600000)
			r.baseTx.mu.Unlock()
			r.mustStep() // deposited
			got := r.wantStatus(models.SellerPayoutDeposited)
			r.db.Model(&models.PaycrestOrderRecord{}).Where("id = ?", *got.PaycrestOrderID).Update("status", status)
			sent := r.broadcasts()
			r.mustStep()
			r.wantStatus(models.SellerPayoutNeedsReview)
			r.mustStep()
			if r.broadcasts() != sent {
				t.Fatal("nothing may be sent once a sale ended without paying")
			}
		})
	}
}

func TestSellerPayout_AKilledTreasurySendsNothing(t *testing.T) {
	r := newPayoutRig(t)
	r.mustStep() // received
	r.treasury.SetKillSwitch(true)
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
	if r.broadcasts() != 0 {
		t.Fatal("the kill switch must stop the bridge")
	}
}

func TestSellerPayout_TickAdvancesEveryPayout(t *testing.T) {
	r := newPayoutRig(t)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	r.wantStatus(models.SellerPayoutReceived)
}

var _ = config.Config{}
