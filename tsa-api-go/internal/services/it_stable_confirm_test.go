package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type stubVerifier struct {
	amount *big.Int
	err    error
}

func (s *stubVerifier) VerifyTransfer(_ context.Context, _, _, _ string) (*big.Int, error) {
	return s.amount, s.err
}

func newStubLiFi(t *testing.T, status, destTx, amount string) *LiFiStatusClient {
	t.Helper()
	body := `{"status":"` + status + `"`
	if destTx != "" {
		body += `,"receiving":{"txHash":"` + destTx + `","chainId":137,"amount":"` + amount + `","toAddress":"` + testFWAddr + `"}`
	}
	body += `}`
	srv := lifiServer(t, 200, body)
	t.Cleanup(srv.Close)
	return NewLiFiStatusClient(srv.URL, "")
}

func TestReportStableDeposit_RecordsTheHashAndAdvances(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource"); err != nil {
		t.Fatalf("ReportStableDeposit: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.DepositTxHash != "0xsource" {
		t.Errorf("depositTxHash = %q", got.DepositTxHash)
	}
	if got.Status != models.ITStatusDepositReported {
		t.Errorf("status = %q, want %q", got.Status, models.ITStatusDepositReported)
	}
}

func TestReportStableDeposit_IsIdempotent(t *testing.T) {
	// The app can retry the report; a second call must not corrupt state.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	intent, _, _ := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())

	if err := s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource"); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if err := s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource"); err != nil {
		t.Fatalf("second report: %v", err)
	}
	// A no-op error return isn't proof of anything by itself — an
	// implementation that silently reset the hash or status on re-entry would
	// also return nil here. Re-fetch and check the row itself didn't move.
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.DepositTxHash != "0xsource" {
		t.Fatalf("depositTxHash changed on re-report: %q", got.DepositTxHash)
	}
	if got.Status != models.ITStatusDepositReported {
		t.Fatalf("status changed on re-report: %q, want %q", got.Status, models.ITStatusDepositReported)
	}
}

func TestReportStableDeposit_RejectsAnotherUsersIntent(t *testing.T) {
	// IDOR: a caller must not be able to attach a deposit report to a sell
	// intent they don't own, even though intent ids are UUIDs and therefore
	// unguessable in practice — the endpoint must not rely on that alone.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	victim, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	attacker := uuid.New()

	err = s.ReportStableDeposit(context.Background(), attacker, victim.ID, "0xattacker")
	if !errors.Is(err, ErrStableIntentNotFound) {
		t.Fatalf("err = %v, want ErrStableIntentNotFound", err)
	}

	// The victim's intent must be left completely unchanged by the attempt.
	got, gErr := s.GetIntent(context.Background(), victim.ID)
	if gErr != nil {
		t.Fatalf("reload victim: %v", gErr)
	}
	if got.DepositTxHash != "" {
		t.Fatalf("victim depositTxHash = %q, want empty", got.DepositTxHash)
	}
	if got.Status != models.ITStatusPendingDeposit {
		t.Fatalf("victim status = %q, want %q", got.Status, models.ITStatusPendingDeposit)
	}
}

func TestReportStableDeposit_OwnerStillSucceeds(t *testing.T) {
	// The ownership scoping added to fix the IDOR must not break the
	// legitimate path: the actual owner reporting against their own intent
	// still works.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	owner := uuid.New()
	intent, _, err := s.CreateStableSellIntent(context.Background(), owner, testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.ReportStableDeposit(context.Background(), owner, intent.ID, "0xowner"); err != nil {
		t.Fatalf("owner report: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.DepositTxHash != "0xowner" {
		t.Errorf("depositTxHash = %q", got.DepositTxHash)
	}
	if got.Status != models.ITStatusDepositReported {
		t.Errorf("status = %q, want %q", got.Status, models.ITStatusDepositReported)
	}
}

func TestReportStableDeposit_RejectsADepositAlreadyClaimedByAnotherIntent(t *testing.T) {
	// The same on-chain deposit tx must not be able to fund two intents — the
	// chain proves a transfer happened, not who sent it, since Flutterwave's
	// deposit address is shared across every user. Enforced by the DB's
	// partial unique index on deposit_tx_hash (config/database.go); this test
	// creates that same index directly because the sqlite test DB is built
	// with AutoMigrate rather than by running the Postgres-only migration in
	// database.go.
	s := newStableSellDBService(t)
	if err := s.db.Exec(`CREATE UNIQUE INDEX uq_it_intents_deposit_tx_hash
	    ON it_intents (deposit_tx_hash) WHERE deposit_tx_hash <> ''`).Error; err != nil {
		t.Fatalf("create unique index: %v", err)
	}
	s.SetStableDepositAddress("POLYGON", testFWAddr)

	first, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	const sharedHash = "0xshared"
	if err := s.ReportStableDeposit(context.Background(), first.UserID, first.ID, sharedHash); err != nil {
		t.Fatalf("first report: %v", err)
	}

	// The second attempt must fail cleanly — not a 500-worthy raw DB error,
	// and definitely not a panic — and must be recognizable by the caller.
	err = s.ReportStableDeposit(context.Background(), second.UserID, second.ID, sharedHash)
	if !errors.Is(err, ErrStableDepositAlreadyClaimed) {
		t.Fatalf("second report err = %v, want ErrStableDepositAlreadyClaimed", err)
	}

	// The second intent must be left in its original state, not partially
	// written by the rejected attempt.
	got, gErr := s.GetIntent(context.Background(), second.ID)
	if gErr != nil {
		t.Fatalf("reload second: %v", gErr)
	}
	if got.DepositTxHash != "" {
		t.Fatalf("second intent depositTxHash = %q, want empty", got.DepositTxHash)
	}
	if got.Status != models.ITStatusPendingDeposit {
		t.Fatalf("second intent status = %q, want %q", got.Status, models.ITStatusPendingDeposit)
	}
}

func TestConfirmStableDeposit_UsesTheVerifiedAmountNotTheQuotedOne(t *testing.T) {
	// The bridge delivered 99.884217 USDC against a 100 USDC quote. The payout
	// must follow what ARRIVED — paying the quoted amount would disburse money
	// the platform never received.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(
		newStubLiFi(t, "DONE", "0xdest", "99884217"),
		&stubVerifier{amount: big.NewInt(99884217)},
		map[string]string{"USDC": testToken},
	)
	intent, _, _ := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	_ = s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource")

	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err != nil {
		t.Fatalf("ConfirmStableDeposit: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.ReceivedAmount != "99884217" {
		t.Fatalf("receivedAmount = %q, want 99884217", got.ReceivedAmount)
	}
	if got.SettledTxHash != "0xdest" {
		t.Fatalf("settledTxHash = %q, want 0xdest", got.SettledTxHash)
	}
	if got.Status != models.ITStatusDepositConfirmed {
		t.Fatalf("status = %q, want %q", got.Status, models.ITStatusDepositConfirmed)
	}
}

func TestConfirmStableDeposit_ScalesTheNetByWhatActuallyArrived(t *testing.T) {
	// The bridge delivered 99.884217 of the quoted 100 USDC, so the payout is
	// the quoted net scaled by that ratio — NOT recomputed from the rate, which
	// would hand back the fee line that was already deducted.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(
		newStubLiFi(t, "DONE", "0xdest", "99884217"),
		&stubVerifier{amount: big.NewInt(99884217)},
		map[string]string{"USDC": testToken},
	)
	intent, _, _ := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	_ = s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource")
	_ = s.ConfirmStableDeposit(context.Background(), intent.ID)

	got, _ := s.GetIntent(context.Background(), intent.ID)
	// quotedNet * received / quoted = 16246711 * 99884217 / 100000000
	want := new(big.Int).Quo(
		new(big.Int).Mul(big.NewInt(16246711), big.NewInt(99884217)),
		big.NewInt(100000000),
	)
	if got.NetNgnKobo != want.String() {
		t.Fatalf("net = %q, want %s (quoted net scaled by what arrived)", got.NetNgnKobo, want)
	}
	if got.NetNgnKobo == "16246711" {
		t.Fatal("net unchanged from the quote — the bridge shortfall was absorbed by the platform")
	}
}

func TestConfirmStableDeposit_StaysPutWhileTheBridgeIsPending(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(newStubLiFi(t, "PENDING", "", ""), &stubVerifier{}, map[string]string{"USDC": testToken})
	intent, _, _ := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	_ = s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource")

	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err == nil {
		t.Fatal("a pending bridge was treated as confirmed")
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.Status != models.ITStatusDepositReported {
		t.Fatalf("status advanced to %q while the bridge was pending", got.Status)
	}
}

func TestConfirmStableDeposit_RefusesWhenTheChainDisagreesWithLiFi(t *testing.T) {
	// LiFi says 99.88 arrived, the receipt shows nothing. The chain wins, and
	// nothing may advance on a provider's unverified word.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(
		newStubLiFi(t, "DONE", "0xdest", "99884217"),
		&stubVerifier{err: ErrTransferNotFound},
		map[string]string{"USDC": testToken},
	)
	intent, _, _ := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	_ = s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource")

	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err == nil {
		t.Fatal("unverifiable deposit confirmed")
	}
}

func TestConfirmStableDeposit_IsIdempotent(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	s.SetStableVerifier(
		newStubLiFi(t, "DONE", "0xdest", "99884217"),
		&stubVerifier{amount: big.NewInt(99884217)},
		map[string]string{"USDC": testToken},
	)
	intent, _, _ := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "BSC", testSellBank())
	_ = s.ReportStableDeposit(context.Background(), intent.UserID, intent.ID, "0xsource")

	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err != nil {
		t.Fatalf("second confirm must be a no-op, got: %v", err)
	}
	// Re-fetch after the SECOND call and check every field the confirm writes
	// — not just that it returned nil. A no-op error alone doesn't prove the
	// row wasn't reset and re-written on re-entry.
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.ReceivedAmount != "99884217" {
		t.Fatalf("receivedAmount changed on re-confirm: %q", got.ReceivedAmount)
	}
	if got.SettledTxHash != "0xdest" {
		t.Fatalf("settledTxHash changed on re-confirm: %q", got.SettledTxHash)
	}
	if got.Status != models.ITStatusDepositConfirmed {
		t.Fatalf("status changed on re-confirm: %q, want %q", got.Status, models.ITStatusDepositConfirmed)
	}
}
