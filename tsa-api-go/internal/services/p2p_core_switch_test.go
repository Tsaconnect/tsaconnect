package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Slices 8+9 of the contracts v2 migration: P2P on P2PTradeEscrow — keyed trades, the escrow a
// row lives on, the payment-merchant releaser, readiness from the core set, dispute alerts.

const coreSwitchBuyer = "0x000000000000000000000000000000000000bEEF"

// The escrow stores a trade under TradeKey(opener, tradeId): the open passes HashID(id), and the
// trade records the key and the escrow it opened on.
func TestOpenTrade_RecordsTheKeyAndTheEscrow(t *testing.T) {
	svc, esc, adID, pmID, db := setupSignedAd(t)
	trade, _, err := svc.OpenTrade(context.Background(), adID, seedVerifiedUser(t, db), coreSwitchBuyer,
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID})
	if err != nil {
		t.Fatal(err)
	}
	if esc.lastOpenTradeArgs.TradeID != HashID(trade.ID) {
		t.Fatal("openTrade must be passed HashID(trade.ID)")
	}
	var row models.P2PTrade
	db.First(&row, "id = ?", trade.ID)
	if row.OnChainTradeID != newTradeKeyHex(coreSwitchBuyer, trade.ID) {
		t.Fatalf("on_chain_trade_id = %s, want the buyer's trade key", row.OnChainTradeID)
	}
	if row.EscrowAddress != esc.contractAddress {
		t.Fatalf("escrow_address = %q, want %s", row.EscrowAddress, esc.contractAddress)
	}
}

// A buy-ad trade is keyed by the depositing user; only a PM buy-ad names the release key as the
// trade's releaser (M1).
func TestOpenTradeWithDeposit_KeyAndReleaser(t *testing.T) {
	for _, pm := range []bool{false, true} {
		svc, esc, _, _, adID, _, db := buyAdFixture(t)
		if pm {
			markAdAsPM(t, svc, adID)
		}
		userID := seedVerifiedUser(t, db)
		trade, _, err := svc.OpenTrade(context.Background(), adID, userID, coreSwitchBuyer, OpenTradeParams{
			TokenAmount: "50000000", PaymentMethodID: seedPaymentMethod(t, db, userID, "NGN"),
			PayoutAccountNumber: "0123456789", PayoutBank: "058", PayoutAccountName: "JOHN DOE",
		})
		if err != nil {
			t.Fatalf("pm=%v: %v", pm, err)
		}
		want := ""
		if pm {
			want = testReleaseKey.Hex()
		}
		if got := esc.lastOpenTradeDepositArgs.Releaser; got != want {
			t.Fatalf("pm=%v: releaser = %q, want %q", pm, got, want)
		}
		if trade.OnChainTradeID != newTradeKeyHex(coreSwitchBuyer, trade.ID) || trade.EscrowAddress != esc.contractAddress {
			t.Fatalf("pm=%v: key %s escrow %s", pm, trade.OnChainTradeID, trade.EscrowAddress)
		}
	}
}

// With no release key there is nobody to release a PM buy-ad trade: refuse the open.
func TestOpenTradeWithDeposit_PMBuyAdNeedsTheReleaseKey(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	markAdAsPM(t, svc, adID)
	svc.releaseAddress = ""
	userID := seedVerifiedUser(t, db)
	_, _, err := svc.OpenTrade(context.Background(), adID, userID, coreSwitchBuyer, OpenTradeParams{
		TokenAmount: "50000000", PaymentMethodID: seedPaymentMethod(t, db, userID, "NGN"),
		PayoutAccountNumber: "0123456789", PayoutBank: "058",
	})
	if !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// A PM's sell-ad trades are released by the release key only once the PM named it (setReleaser):
// until then neither its ads go live nor its trades open.
func TestPMSellAd_RefusedUntilTheReleaserIsSet(t *testing.T) {
	svc, esc, adID, _, db := setupSignedAd(t)
	markAdAsPM(t, svc, adID)
	esc.defaultReleaser = common.Address{}
	_, _, err := svc.OpenTrade(context.Background(), adID, seedVerifiedUser(t, db), coreSwitchBuyer, OpenTradeParams{FiatAmount: "1000"})
	if !errors.Is(err, ErrPMReleaserNotSet) {
		t.Fatalf("open: err = %v", err)
	}
	esc.readReleaserErr = errors.New("rpc down")
	_, _, err = svc.OpenTrade(context.Background(), adID, seedVerifiedUser(t, db), coreSwitchBuyer, OpenTradeParams{FiatAmount: "1000"})
	if !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("an unreadable releaser is retryable, not 'not set': %v", err)
	}
}

// A sell-ad whose voucher was signed for another escrow cannot be opened on this one.
func TestOpenTrade_AdSignedForAnotherEscrowIsRetired(t *testing.T) {
	svc, _, adID, pmID, db := setupSignedAd(t)
	db.Model(&models.P2PAd{}).Where("id = ?", adID).Update("escrow_address", "0x7dAdae104930da7B62EA185fa697fCFA7f9eC324")
	_, _, err := svc.OpenTrade(context.Background(), adID, seedVerifiedUser(t, db), coreSwitchBuyer,
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID})
	if !errors.Is(err, ErrRetiredEscrow) {
		t.Fatalf("err = %v", err)
	}
}

// A trade recorded on another escrow (or none: before the switch) is never acted on here.
func TestTradeEscrow_RefusesARetiredEscrow(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	tr := &models.P2PTrade{ID: uuid.New(), ChainID: 146, OnChainTradeID: HashIDHex(uuid.New())}
	for _, addr := range []string{"", "0x7dAdae104930da7B62EA185fa697fCFA7f9eC324"} {
		tr.EscrowAddress = addr
		if _, _, err := svc.tradeEscrow(tr); !errors.Is(err, ErrRetiredEscrow) {
			t.Fatalf("escrow %q: err = %v", addr, err)
		}
	}
	tr.EscrowAddress = esc.contractAddress
	if _, key, err := svc.tradeEscrow(tr); err != nil || "0x"+common.Bytes2Hex(key[:]) != tr.OnChainTradeID {
		t.Fatalf("key = %x, err = %v", key, err)
	}
}

// The system release is release(key) from the release key (no adminRelease any more).
func TestAutoRelease_ReleasesFromTheReleaseKey(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1"); err != nil {
		t.Fatal(err)
	}
	amount, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	esc.releasedEvent = &P2PTradeReleasedEvent{AmountToBuyer: amount, FeeToSystem: big.NewInt(0)}
	signer := &fakeReleaseSigner{addr: testReleaseKey.Hex(), hash: "0xrelease"}
	if _, err := svc.AutoRelease(context.Background(), trade.ID, signer); err != nil {
		t.Fatal(err)
	}
	if esc.lastReleaseCaller != testReleaseKey.Hex() {
		t.Fatalf("release prepared from %q, want the release key", esc.lastReleaseCaller)
	}
}

// seedPMUser is an active payment merchant whose maker wallet is wallet.
func seedPMUser(t *testing.T, svc *P2PTradeService, wallet string) uuid.UUID {
	t.Helper()
	createPaymentMerchantsTable(t, svc.db)
	userID := uuid.New()
	if err := svc.db.Exec(`INSERT INTO payment_merchants (id, user_id, name, status, kind, operator_wallet_address, available_ngn) VALUES (?,?,?,?,?,?,?)`,
		uuid.New().String(), userID.String(), "PM", models.PaymentMerchantStatusActive, models.PaymentMerchantKindMerchant, wallet, "0").Error; err != nil {
		t.Fatal(err)
	}
	svc.SetPMService(NewPMService(svc.db))
	return userID
}

func TestPMReleaser_StatusPrepareConfirm(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	wallet := common.HexToAddress("0x00000000000000000000000000000000000000Aa")
	userID := seedPMUser(t, svc, wallet.Hex())
	esc.defaultReleaser = common.Address{}
	ctx := context.Background()

	st, err := svc.PMReleaser(ctx, userID, 0)
	if err != nil || st.Set || st.Wallet != wallet.Hex() || st.Expected != testReleaseKey.Hex() || st.ChainID != 146 {
		t.Fatalf("status = %+v, err = %v", st, err)
	}
	if _, err := svc.PreparePMReleaser(ctx, userID, 0); err != nil {
		t.Fatal(err)
	}
	// A setReleaser that named anyone else is refused.
	esc.releaserSetEvent = &P2PReleaserSetEvent{Seller: wallet, Releaser: common.HexToAddress("0x01")}
	if _, err := svc.ConfirmPMReleaser(ctx, userID, 0, "0xtx"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("wrong releaser: err = %v", err)
	}
	esc.releaserSetEvent = &P2PReleaserSetEvent{Seller: wallet, Releaser: testReleaseKey}
	if st, err := svc.ConfirmPMReleaser(ctx, userID, 0, "0xtx"); err != nil || !st.Set {
		t.Fatalf("confirm = %+v, %v", st, err)
	}
	var audits int64
	db.Model(&models.P2PAuditLog{}).Where("action = ?", models.P2PAuditPMReleaserSet).Count(&audits)
	if audits != 1 {
		t.Fatalf("audits = %d", audits)
	}
	// Not a PM: forbidden.
	if _, err := svc.PMReleaser(ctx, uuid.New(), 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-PM: err = %v", err)
	}
}

func TestMissingPMReleasers_ListsPMsThatHaveNotNamedTheKey(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	set := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	unset := common.HexToAddress("0x00000000000000000000000000000000000000a2")
	seedPMUser(t, svc, set.Hex())
	seedPMUser(t, svc, unset.Hex())
	esc.defaultReleaser = common.Address{}
	esc.releasers = map[common.Address]common.Address{set: testReleaseKey}
	missing, err := svc.MissingPMReleasers(context.Background())
	if err != nil || len(missing) != 1 {
		t.Fatalf("missing = %v, err = %v", missing, err)
	}
}

// A chain takes new activity only while its core set passes its gates; exits are never asked.
func TestRegistry_UnverifiedFollowsTheCoreSet(t *testing.T) {
	core := &CoreRegistry{chains: map[int64]*coreChain{146: {set: config.CoreContracts{ChainID: 146}}}, now: time.Now}
	reg := NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: newMockEscrow()}, config.CoreP2PEIP712Version)
	reg.core = core
	if !reg.Unverified(146) {
		t.Fatal("a set that has not passed its gates is exits-only")
	}
	core.chains[146].ready = true
	if reg.Unverified(146) || reg.Unverified(56) {
		t.Fatal("a ready set takes new activity; an unregistered chain is not 'unverified'")
	}
}

// The voucher the seller signs carries P2PTradeEscrow's taker (zero: any buyer) and domain "3".
func TestCreateAd_VoucherHasTheTaker(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	svc.escrows = NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: esc}, config.CoreP2PEIP712Version)
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	res, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(seedPaymentMethod(t, db, sellerID, "NGN")))
	if err != nil {
		t.Fatal(err)
	}
	fields := res.TypedData.Types["AdVoucher"]
	if last := fields[len(fields)-1]; last.Name != "taker" || last.Type != "address" {
		t.Fatalf("last voucher field = %+v, want taker address", last)
	}
	if res.TypedData.Message.Taker != (common.Address{}).Hex() || res.TypedData.Domain.Version != "3" {
		t.Fatalf("taker %s version %s", res.TypedData.Message.Taker, res.TypedData.Domain.Version)
	}
	if res.Ad.EscrowAddress != esc.contractAddress {
		t.Fatalf("ad escrow = %q", res.Ad.EscrowAddress)
	}
}

// seedDispute is a disputed trade whose open dispute row was raised `ago`.
func seedDispute(t *testing.T, svc *P2PTradeService, ago time.Duration) uuid.UUID {
	t.Helper()
	tr := seedOpenedPMTrade(t, svc)
	svc.db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("status", models.P2PTradeStatusDisputed)
	if err := svc.db.Create(&models.P2PDispute{ID: uuid.New(), TradeID: tr.ID, RaisedBy: tr.BuyerID, RaisedRole: models.P2PDisputeRoleBuyer,
		Reason: "no crypto", Status: models.P2PDisputeStatusOpen, CreatedAt: svc.clock.Now().Add(-ago)}).Error; err != nil {
		t.Fatal(err)
	}
	return tr.ID
}

func TestAlertUnresolvedDisputes_StaleThenBackstopOncePerTrade(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	alerts := &recordingAlerter{}
	svc.SetAlerter(alerts)
	seedDispute(t, svc, time.Hour)                       // too fresh
	stale := seedDispute(t, svc, 4*24*time.Hour)         // stale
	nearBackstop := seedDispute(t, svc, 55*24*time.Hour) // inside the backstop warning
	n, err := svc.AlertUnresolvedDisputes(context.Background(), 50)
	if err != nil || n != 2 {
		t.Fatalf("first pass: n = %d err = %v, want 2", n, err)
	}
	if n, _ := svc.AlertUnresolvedDisputes(context.Background(), 50); n != 0 {
		t.Fatalf("second pass alerted %d again", n)
	}
	got := map[uuid.UUID]P2PAlert{}
	for _, a := range alerts.got {
		got[a.TradeID] = a
	}
	if a := got[nearBackstop]; a.Kind != P2PAlertDisputeUnresolved || a.BackstopIn != 5*24*time.Hour {
		t.Fatalf("backstop alert = %+v", a)
	}
	if _, ok := got[stale]; !ok {
		t.Fatal("the stale dispute was not alerted")
	}
}
