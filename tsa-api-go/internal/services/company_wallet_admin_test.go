package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestCompanyWalletStatus_BalanceReservedFreeLow(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 60_000_000)
	seedTreasuryIntent(t, s, userID, models.ITStatusDelivering) // 7.232937 reserved
	st, err := s.CompanyWalletStatus(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if st.Address != sponsorTestSigner || st.LowBalanceUSD != 55 || st.StallMinutes != 30 || len(st.Chains) != 1 {
		t.Fatalf("status = %+v", st)
	}
	c := st.Chains[0]
	if c.Chain != "base" || c.NativeWei == "" || c.NativeFloorWei != "1000" || len(c.Tokens) != 1 {
		t.Fatalf("chain = %+v", c)
	}
	tok := c.Tokens[0]
	if tok.Token != "USDC" || tok.Balance != "60" || tok.Reserved != "7.232937" || tok.Free != "52.767063" || !tok.Low {
		t.Fatalf("token = %+v, want free 52.77 flagged low against $55", tok)
	}
}

func TestListCompanyWalletOrders_ParkedOverpaidAndStalled(t *testing.T) {
	r := newDeliverRig(t) // a paid, delivering-to-be order
	ctx := context.Background()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	parked := seedTreasuryIntent(t, r.s, seedVerifiedUser(t, r.s.db), models.ITStatusBuyAwaitingRate)
	r.s.db.Model(parked).Updates(map[string]any{"received_amount": "30000000", "failure_reason": "could not price at settlement: maximum"})

	orders, err := r.s.ListCompanyWalletOrders(ctx)
	if err != nil || len(orders) != 1 || orders[0].ID != parked.ID || !orders[0].Overpaid ||
		orders[0].ReceivedNgn != "300000.00" || orders[0].AmountNgn != "10000.00" {
		t.Fatalf("orders = %+v err = %v, want only the parked overpayment (the send is not stalled yet)", orders, err)
	}
	if got := orders[0].Actions; len(got) != 2 || got[0] != "deliver" || got[1] != "refund" {
		t.Fatalf("actions = %v", got)
	}
	r.s.db.Model(&models.RampLeg{}).Where("order_id = ?", r.intent.ID).Update("created_at", time.Now().Add(-31*time.Minute))
	orders, _ = r.s.ListCompanyWalletOrders(ctx)
	var stalled *CompanyWalletOrder
	for i := range orders {
		if orders[i].ID == r.intent.ID {
			stalled = &orders[i]
		}
	}
	find := func() *CompanyWalletOrder {
		orders, _ = r.s.ListCompanyWalletOrders(ctx)
		for i := range orders {
			if orders[i].ID == r.intent.ID {
				return &orders[i]
			}
		}
		return nil
	}
	// The rig's nonce 7 is unused (latest 0): the signed send can still land,
	// so the console offers no re-send and says a cancel comes first.
	stalled = find()
	if stalled == nil || !stalled.Stalled || stalled.SendTxHash != "0xsponsored" || len(stalled.Actions) != 0 ||
		stalled.Evidence == nil || stalled.Evidence.Allowed || !stalled.Evidence.NeedsCancel {
		t.Fatalf("unused nonce: %+v", stalled)
	}
	// Nonce 7 consumed 5+ deep and a trusted scan: the same evidence allows it.
	r.probe.pending, r.probe.latest, r.probe.head = 8, 8, 4500
	r.probe.background = []types.Log{{BlockNumber: 2099}, {BlockNumber: 4099}, {BlockNumber: 4500}}
	stalled = find()
	if stalled == nil || len(stalled.Actions) != 1 || stalled.Actions[0] != "resend" ||
		stalled.Evidence == nil || !stalled.Evidence.Allowed || !stalled.Evidence.ScanTrusted {
		t.Fatalf("consumed nonce: %+v", stalled)
	}
	// Review C1: the console shows the same refusal the API would give.
	r.probe.pending, r.probe.latest = 9, 8
	stalled = find()
	if stalled == nil || len(stalled.Actions) != 0 || stalled.Evidence == nil || stalled.Evidence.Allowed ||
		!strings.Contains(stalled.Evidence.Reason, "1 pending transaction") {
		t.Fatalf("pending tx: %+v", stalled)
	}
}

// Review C2: a delivery stuck with no send gets a refund exit on the console.
func TestListCompanyWalletOrders_StuckWithNoSendOffersRefund(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	r.s.CompanyWallet().Treasury().SetKillSwitch(true)
	_ = r.s.SendTreasuryBuy(ctx, r.intent.ID) // → delivering, refused
	r.s.db.Model(&models.ITIntent{}).Where("id = ?", r.intent.ID).Update("updated_at", time.Now().Add(-31*time.Minute))
	orders, err := r.s.ListCompanyWalletOrders(ctx)
	if err != nil || len(orders) != 1 || !orders[0].Stalled || len(orders[0].Actions) != 1 || orders[0].Actions[0] != "refund" ||
		orders[0].SendTxHash != "" {
		t.Fatalf("orders = %+v err = %v", orders, err)
	}
}
