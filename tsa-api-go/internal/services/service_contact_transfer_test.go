package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	scPayer     = "0x00000000000000000000000000000000000Ca11e"
	scRecipient = "0x00000000000000000000000000000000005E5701"
	scToken     = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
)

// scReceipts serves fixed receipts; a missing hash is "not mined".
type scReceipts map[common.Hash]*types.Receipt

func (r scReceipts) TransactionReceipt(_ context.Context, h common.Hash) (*types.Receipt, error) {
	if rc, ok := r[h]; ok {
		return rc, nil
	}
	return nil, ethereum.NotFound
}

func scIntent(amount string) models.ServiceContactIntent {
	return models.ServiceContactIntent{ChainID: 146, TokenSymbol: "USDC", TokenAddress: scToken,
		FeeRecipient: scRecipient, PayerWallet: scPayer, Amount: amount, CreatedAt: time.Now()}
}

func TestServiceContactTransfer_Match(t *testing.T) {
	ok, short, other, wrongToken, reverted := common.HexToHash("0x1"), common.HexToHash("0x2"), common.HexToHash("0x3"),
		common.HexToHash("0x4"), common.HexToHash("0x5")
	rpc := scReceipts{
		ok:         {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{transferLog(scToken, scPayer, scRecipient, big.NewInt(100_000))}},
		short:      {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{transferLog(scToken, scPayer, scRecipient, big.NewInt(99_999))}},
		other:      {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{transferLog(scToken, scRecipient, scPayer, big.NewInt(100_000))}},
		wrongToken: {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{transferLog(coreUSDT, scPayer, scRecipient, big.NewInt(100_000))}},
		reverted:   {Status: types.ReceiptStatusFailed},
	}
	tr := &ServiceContactTransfer{chainID: 146, verifier: NewLogTransferVerifier(rpc)}

	// The quote it paid is found, even when a later quote (a fee change) differs.
	in, err := tr.Match(context.Background(), ok.Hex(), []models.ServiceContactIntent{scIntent("120000"), scIntent("100000")})
	if err != nil || in.Amount != "100000" {
		t.Fatalf("match = %+v, %v", in, err)
	}
	for name, h := range map[string]common.Hash{"short": short, "wrong direction": other, "wrong token": wrongToken, "reverted": reverted} {
		if _, err := tr.Match(context.Background(), h.Hex(), []models.ServiceContactIntent{scIntent("100000")}); !errors.Is(err, ErrContactTransferUnproven) {
			t.Errorf("%s: err = %v, want unproven", name, err)
		}
	}
	// Not mined yet is not a refusal: the caller asks again later.
	if _, err := tr.Match(context.Background(), common.HexToHash("0x99").Hex(), []models.ServiceContactIntent{scIntent("100000")}); err == nil ||
		errors.Is(err, ErrContactTransferUnproven) {
		t.Fatalf("unmined: err = %v, want a retryable error", err)
	}
	// A quote for another chain is never matched.
	cross := scIntent("100000")
	cross.ChainID = 56
	if _, err := tr.Match(context.Background(), ok.Hex(), []models.ServiceContactIntent{cross}); !errors.Is(err, ErrContactTransferUnproven) {
		t.Fatalf("other chain: err = %v", err)
	}
}

// With no core set (or one not ready) contacts stay on the contract.
func TestServiceContactTransfer_ActiveFollowsTheCoreSet(t *testing.T) {
	var none *ServiceContactTransfer
	if none.Active() || NewServiceContactTransfer(nil, 146, scReceipts{}) != nil {
		t.Fatal("no core set: transfer must be off")
	}
	now := time.Unix(1_800_000_000, 0)
	chain := healthyCoreChain()
	reg := testCoreRegistry(chain, &now)
	tr := NewServiceContactTransfer(reg, 146, scReceipts{})
	if tr.Active() {
		t.Fatal("not active before the gates pass")
	}
	if err := reg.Gate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tr.Active() {
		t.Fatal("active once the set is ready")
	}
	if r, err := tr.Recipient(context.Background()); err != nil || r != common.HexToAddress(coreFees) {
		t.Fatalf("recipient = %s, %v", r.Hex(), err)
	}
}
