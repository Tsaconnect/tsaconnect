package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// paycrest_order_conversion_test.go — Task 9: creating and syncing a
// converted buy.
//
// Task 8 offers a converted venue (settle through a token Paycrest CAN fill,
// deliver what the user actually asked for via LiFi). This file pins the
// order-creation side of that offer:
//
//   - a converted order tells Paycrest to deliver to the TREASURY, never the
//     user — the user's own wallet only ever appears as the eventual
//     destination recorded on the row;
//   - the promise/route metadata Task 8 computed is copied onto the row
//     verbatim, never recomputed;
//   - Holder starts at HolderPaycrest explicitly, because the orchestrator's
//     custody gate (Task 7) treats anything else as already in custody;
//   - with the conversion flag off, a same-asset buy is untouched — direct,
//     delivered to the user, no new statuses;
//   - Sync must not let the Paycrest poller re-settle an order the
//     orchestrator has already taken custody of.

// fakePaycrestConversionClient implements PaycrestOrderer, capturing the
// onramp request so a test can assert who Paycrest was told to pay, and
// answering GetOrder with a controllable remote status for the Sync tests.
type fakePaycrestConversionClient struct {
	lastCreate capturedOnramp
	createErr  error
	order      *PaycrestOrder // returned by CreateOnrampOrder when set

	remoteStatus string // GetOrder's Status
	amountPaid   string // GetOrder's AmountPaid
	getOrderErr  error
}

// capturedOnramp is a smaller, test-only view of PaycrestOnrampRequest named
// for what the assertion cares about (WalletAddress), not the wire field
// (RecipientAddress) — the two are the same delivery target either way.
type capturedOnramp struct {
	WalletAddress    string
	Token            string
	Network          string
	SenderFeePercent string
}

func (f *fakePaycrestConversionClient) CreateOnrampOrder(_ context.Context, req PaycrestOnrampRequest) (*PaycrestOrder, error) {
	f.lastCreate = capturedOnramp{
		WalletAddress:    req.RecipientAddress,
		Token:            req.Token,
		Network:          req.Network,
		SenderFeePercent: req.SenderFeePercent,
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.order != nil {
		return f.order, nil
	}
	return &PaycrestOrder{
		ID: "pc-conv-" + req.Reference, Status: PaycrestStatusPending, Rate: "1500", Amount: "100",
		SenderFeePercent: req.SenderFeePercent,
		ProviderAccount: &PaycrestProviderAccount{
			Institution: "Fake Bank", AccountIdentifier: "0000000000", AccountName: "Fake",
			AmountToTransfer: req.AmountNGN,
		},
	}, nil
}

func (f *fakePaycrestConversionClient) CreateOfframpOrder(context.Context, PaycrestOfframpRequest) (*PaycrestOrder, error) {
	panic("CreateOfframpOrder not used by the conversion buy tests")
}

func (f *fakePaycrestConversionClient) GetOrder(_ context.Context, id string) (*PaycrestOrder, error) {
	if f.getOrderErr != nil {
		return nil, f.getOrderErr
	}
	status := f.remoteStatus
	if status == "" {
		status = PaycrestStatusPending
	}
	paid := f.amountPaid
	if paid == "" {
		paid = "150000"
	}
	return &PaycrestOrder{ID: id, Status: status, AmountPaid: paid}, nil
}

// orderConversionRig bundles a PaycrestOrderService wired for converted-buy
// creation: a Paycrest book that can only fill base/USDC directly, and a
// LiFi quoter that prices base/USDC -> optimism/USDT within the promise cap
// (same fixture Task 8's own tests use).
type orderConversionRig struct {
	t      *testing.T
	db     *gorm.DB
	svc    *PaycrestOrderService
	client *fakePaycrestConversionClient
	lifi   *fakeLiFiQuoter
}

func newOrderServiceWithFakes(t *testing.T) *orderConversionRig {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}
	createTestUsersTable(t, db)

	pc := &stubQuoter{market: 1500, rates: map[string]*PaycrestRates{
		"base|USDC": buy("1500"),
	}}
	quotes := NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"base"})
	lifi := &fakeLiFiQuoter{route: &LiFiRoute{
		ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), ETASeconds: 30, Tool: "across",
		// Priced in USD and executable: without a cost the venue pricer
		// cannot size the bridge, and without decodable calldata delivering
		// on the requested chain no venue is offered at all.
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000,
		Data: testBridgeCalldata(t, orderTestUserWallet, 10, false),
	}}
	quotes.SetLiFiQuoter(lifi)
	quotes.SetTreasuryAddress("0xTreasury")
	quotes.SetChainRegistry(testChainRegistry())
	quotes.SetTreasuryLanes(&fakeTreasuryLaneChecker{laned: map[string]bool{"base": true, "optimism": true}})

	client := &fakePaycrestConversionClient{}
	svc := NewPaycrestOrderService(db, client, quotes, nil, "", "")
	if svc == nil {
		t.Fatal("NewPaycrestOrderService returned nil")
	}
	// A converted order needs an explicit F to split into F − B — default it
	// here (config's own default B=50bps against a plausible F=1%) so tests
	// that only care about conversion routing don't each have to set it; a
	// test that cares about the split itself overrides these directly.
	svc.senderFeePercent = "1"
	svc.SetRoundTripBudgetBps(50)
	svc.dir = NewPaycrestDirectory(
		&stubInstitutions{rows: []PaycrestInstitution{{Name: "Access Bank", Code: "ABNGNGLA"}}},
		&stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}},
	)

	return &orderConversionRig{t: t, db: db, svc: svc, client: client, lifi: lifi}
}

// orderTestUserWallet is the buyer's own wallet: a real hex address, because
// a converted route is now priced to the address it will deliver to.
const orderTestUserWallet = "0x00000000000000000000000000000000000a11ce"

// buyRequest fills in a valid refund destination on top of the caller's
// fields, so each test only has to state what it is actually varying.
func (r *orderConversionRig) buyRequest(userID uuid.UUID, chainKey, token, requestedChain, requestedToken string) PaycrestBuyRequest {
	return PaycrestBuyRequest{
		UserID: userID, WalletAddress: orderTestUserWallet, FiatCode: "NGN", FiatAmount: "150000",
		ChainKey: chainKey, Token: token,
		RequestedChain: requestedChain, RequestedToken: requestedToken,
		RefundBankCode: "044", RefundAccount: "0123456789", RefundAccountName: "Test User",
	}
}

// seedConvertedBuy inserts a converted order row directly — status pending,
// holder paycrest — as if CreateBuy had already run and Paycrest has not yet
// settled it.
func (r *orderConversionRig) seedConvertedBuy(t *testing.T) *models.PaycrestOrderRecord {
	t.Helper()
	row := &models.PaycrestOrderRecord{
		ID: uuid.New(), UserID: uuid.New(),
		Direction: models.PaycrestDirectionBuy, Status: models.PaycrestStatusPending,
		Reference: "conv-" + uuid.NewString(), PaycrestID: "pc-" + uuid.NewString(),
		FiatCode: "NGN", FiatAmount: "150000",
		Token: "USDC", ChainKey: "base", Network: "base",
		RouteKind: models.RouteKindConverted, RequestedChain: "optimism", RequestedToken: "USDT",
		MarkupBps: 50, PromisedAmount: "99500000",
		Holder: models.HolderPaycrest, WalletAddress: "0xUser",
	}
	if err := r.db.Create(row).Error; err != nil {
		t.Fatalf("seed converted order: %v", err)
	}
	return row
}

// ─── Creation ───────────────────────────────────────────────────────────────

func TestCreateBuy_ConvertedOrderDeliversToTheTreasuryAndRecordsThePromise(t *testing.T) {
	s := newOrderServiceWithFakes(t)
	s.svc.SetConversionEnabled(true)
	s.svc.SetTreasury("0xTreasury")
	buyer := seedVerifiedUser(t, s.db)

	row, err := s.svc.CreateBuy(context.Background(), s.buyRequest(buyer, "base", "USDC", "optimism", "USDT"))
	if err != nil {
		t.Fatal(err)
	}
	if s.client.lastCreate.WalletAddress != "0xTreasury" {
		t.Fatalf("Paycrest must deliver to the treasury, got %s", s.client.lastCreate.WalletAddress)
	}
	// ConvertedVenues must carry the settlement leg's Network through, and
	// CreateBuy must submit that same network to Paycrest — an empty value
	// here means the real onramp request would have named no network at all.
	if s.client.lastCreate.Network == "" || s.client.lastCreate.Network != row.Network {
		t.Fatalf("submitted network = %q, row network = %q; the settlement leg's network must reach Paycrest",
			s.client.lastCreate.Network, row.Network)
	}
	if row.RouteKind != models.RouteKindConverted || row.RequestedChain != "optimism" || row.PromisedAmount == "" {
		t.Fatalf("row = %+v", row)
	}
	if row.Holder != models.HolderPaycrest {
		t.Fatalf("holder = %s, want paycrest until they deliver", row.Holder)
	}
	// The user's own wallet is still recorded — it is where the money ends
	// up, even though Paycrest itself never sees that address.
	if row.WalletAddress != orderTestUserWallet {
		t.Fatalf("row.WalletAddress = %s, want the user's own wallet recorded regardless of custody", row.WalletAddress)
	}
}

func TestCreateBuy_DirectOrderIsUnchangedWhenConversionIsOff(t *testing.T) {
	s := newOrderServiceWithFakes(t)
	buyer := seedVerifiedUser(t, s.db)

	row, err := s.svc.CreateBuy(context.Background(), s.buyRequest(buyer, "base", "USDC", "base", "USDC"))
	if err != nil {
		t.Fatal(err)
	}
	if s.client.lastCreate.WalletAddress != orderTestUserWallet || row.RouteKind == models.RouteKindConverted {
		t.Fatalf("a direct order must still deliver to the user: %+v", row)
	}
	if row.Holder != "" {
		t.Fatalf("a direct order must not carry a custody holder, got %q", row.Holder)
	}
}

// TestCreateBuy_ConvertedOrderRefusesWhenConversionDisabled pins binding
// ruling 5: the feature being off must refuse with a user-facing sentinel,
// not silently fall back to delivering the wrong asset to the user.
func TestCreateBuy_ConvertedOrderRefusesWhenConversionDisabled(t *testing.T) {
	s := newOrderServiceWithFakes(t)
	s.svc.SetTreasury("0xTreasury") // treasury configured, but the flag is off
	buyer := seedVerifiedUser(t, s.db)

	_, err := s.svc.CreateBuy(context.Background(), s.buyRequest(buyer, "base", "USDC", "optimism", "USDT"))
	if !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("err = %v, want ErrConversionUnavailable", err)
	}
}

// TestCreateBuy_ConvertedOrderRefusesWhenTreasuryAddressEmpty pins binding
// ruling 3: Treasury.Address() == "" (lanes disagree, or none configured) is
// a hard refusal — never hand Paycrest an empty delivery address.
func TestCreateBuy_ConvertedOrderRefusesWhenTreasuryAddressEmpty(t *testing.T) {
	s := newOrderServiceWithFakes(t)
	s.svc.SetConversionEnabled(true) // flag on, but no treasury wired
	buyer := seedVerifiedUser(t, s.db)

	_, err := s.svc.CreateBuy(context.Background(), s.buyRequest(buyer, "base", "USDC", "optimism", "USDT"))
	if !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("err = %v, want ErrConversionUnavailable", err)
	}
	if s.client.lastCreate.WalletAddress != "" {
		t.Fatalf("no order should have reached the provider: %+v", s.client.lastCreate)
	}
}

// TestCreateBuy_ConvertedOrderRefusesWhenCapsExceeded pins binding ruling 5's
// other half: an optional treasury-caps gate, when wired, refuses an order
// the background orchestrator would refuse anyway, up front rather than
// after the user has already paid in.
func TestCreateBuy_ConvertedOrderRefusesWhenCapsExceeded(t *testing.T) {
	s := newOrderServiceWithFakes(t)
	s.svc.SetConversionEnabled(true)
	s.svc.SetTreasury("0xTreasury")
	s.svc.SetTreasuryCaps(capsAlwaysRefuse{})
	buyer := seedVerifiedUser(t, s.db)

	_, err := s.svc.CreateBuy(context.Background(), s.buyRequest(buyer, "base", "USDC", "optimism", "USDT"))
	if !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("err = %v, want ErrConversionUnavailable", err)
	}
	if s.client.lastCreate.WalletAddress != "" {
		t.Fatalf("no order should have reached the provider: %+v", s.client.lastCreate)
	}
}

type capsAlwaysRefuse struct{}

func (capsAlwaysRefuse) CheckCaps(string, float64, float64) error {
	return ErrTreasuryCapExceeded
}

// ─── Sync ───────────────────────────────────────────────────────────────────

func TestSync_ConvertedBuyGoesToConvertingNotSettled(t *testing.T) {
	s := newOrderServiceWithFakes(t)
	row := s.seedConvertedBuy(t) // status pending, holder paycrest
	s.client.remoteStatus = "settled"
	out, err := s.svc.Sync(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; a converted buy is not settled when Paycrest pays the treasury", out.Status)
	}
}

// TestSync_ConvertedBuyOnceTreasuryHoldsFundsDoesNotOverwriteStatus is the
// other half of binding ruling 4: once custody has moved off Paycrest, the
// orchestrator owns the order's status, and the Paycrest poller (still
// selecting this row via DueForSync, since "converting" is non-terminal)
// must not resurrect Paycrest's own "settled" over whatever the orchestrator
// has since written.
func TestSync_ConvertedBuyOnceTreasuryHoldsFundsDoesNotOverwriteStatus(t *testing.T) {
	s := newOrderServiceWithFakes(t)
	row := s.seedConvertedBuy(t)
	// The orchestrator has already taken custody and moved the order into
	// its own state machine.
	if err := s.db.Model(row).Updates(map[string]any{
		"holder": models.HolderTreasury,
		"status": models.PaycrestStatusRefunding,
	}).Error; err != nil {
		t.Fatalf("seed custody transition: %v", err)
	}
	row.Holder = models.HolderTreasury
	row.Status = models.PaycrestStatusRefunding

	s.client.remoteStatus = "settled" // Paycrest's own (now stale) view of this order
	out, err := s.svc.Sync(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != models.PaycrestStatusRefunding {
		t.Fatalf("status = %s, want refunding untouched; Sync must not overwrite a converted order once holder != paycrest", out.Status)
	}
}

// ── Part 2: buying an arbitrary token, and recording what she is owed ───────

func TestCreateBuy_StoresTheRequestedAssetAndWhatSheIsOwed(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.svc.SetConversionEnabled(true)
	r.svc.SetTreasury("0xTreasury")
	r.lifi.route.Data = testBridgeCalldata(t, orderTestUserWallet, 56, false)
	r.lifi.route.ToTokenDecimals = 18
	user := seedVerifiedUser(t, r.db)
	req := r.buyRequest(user, "base", "USDC", "", "BNB")
	req.RequestedChainID, req.RequestedTokenAddress = 56, evmNativeAddress
	row, err := r.svc.CreateBuy(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateBuy: %v", err)
	}
	if row.RequestedChain != "bsc" || row.RequestedChainID != 56 || row.RequestedTokenAddress != evmNativeAddress ||
		row.RequestedTokenDecimals != 18 || row.DestinationAddress != req.WalletAddress {
		t.Fatalf("row = %+v", row)
	}
	orderAmount, _ := DecimalToBaseUnits(row.TokenAmount, 6)
	if want := QuotedFromOrder(orderAmount, 100, 50).String(); row.QuotedAmount != want {
		t.Fatalf("quoted = %q, want %q (Paycrest's amount at F−B, restated at F)", row.QuotedAmount, want)
	}
	if row.EstimatedAmount == "" || row.PromisedAmount != "" {
		t.Fatalf("estimate/promise = %q/%q", row.EstimatedAmount, row.PromisedAmount)
	}
}

func TestCreateBuy_RefusesAnUnknownChainID(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.svc.SetConversionEnabled(true)
	r.svc.SetTreasury("0xTreasury")
	user := seedVerifiedUser(t, r.db)
	req := r.buyRequest(user, "base", "USDC", "", "X")
	req.RequestedChainID, req.RequestedTokenAddress = 424242, evmNativeAddress
	if _, err := r.svc.CreateBuy(context.Background(), req); !errors.Is(err, ErrBadRequestedAsset) {
		t.Fatalf("err = %v, want ErrBadRequestedAsset", err)
	}
}

// recovery_pending is operator-owned. It is non-terminal, so DueForSync keeps
// selecting the row — without an explicit guard the very next poll would
// write Paycrest's own view over a status only a human may resolve.
func TestSync_RecoveryPendingIsNotOverwrittenByThePoller(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	row := r.seedConvertedBuy(t)
	if err := r.db.Model(row).Update("status", models.PaycrestStatusRecoveryPending).Error; err != nil {
		t.Fatalf("seed recovery_pending: %v", err)
	}
	row.Status = models.PaycrestStatusRecoveryPending

	r.client.remoteStatus = "settled"
	out, err := r.svc.Sync(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending untouched", out.Status)
	}
	if !out.NeedsOperator() {
		t.Fatal("a recovery_pending order must surface to an operator")
	}
}
