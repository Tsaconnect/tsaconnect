package services

import (
	"context"
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

	if err := s.ReportStableDeposit(context.Background(), intent.ID, "0xsource"); err != nil {
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

	if err := s.ReportStableDeposit(context.Background(), intent.ID, "0xsource"); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if err := s.ReportStableDeposit(context.Background(), intent.ID, "0xsource"); err != nil {
		t.Fatalf("second report: %v", err)
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
	_ = s.ReportStableDeposit(context.Background(), intent.ID, "0xsource")

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
	_ = s.ReportStableDeposit(context.Background(), intent.ID, "0xsource")
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
	_ = s.ReportStableDeposit(context.Background(), intent.ID, "0xsource")

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
	_ = s.ReportStableDeposit(context.Background(), intent.ID, "0xsource")

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
	_ = s.ReportStableDeposit(context.Background(), intent.ID, "0xsource")

	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	if err := s.ConfirmStableDeposit(context.Background(), intent.ID); err != nil {
		t.Fatalf("second confirm must be a no-op, got: %v", err)
	}
	got, _ := s.GetIntent(context.Background(), intent.ID)
	if got.ReceivedAmount != "99884217" {
		t.Fatalf("receivedAmount changed on re-confirm: %q", got.ReceivedAmount)
	}
}
