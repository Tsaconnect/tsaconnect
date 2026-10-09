//go:build bsctestnet

package services

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// TestLiquidityRehearsal_BSCTestnet runs Plan B's chain path for real:
// approve → deposit → confirm → stock ad → sign → over-max refused → withdraw → confirm.
// Run with the guarded command in Task 17 Step 3 (never -mod=mod).
func TestLiquidityRehearsal_BSCTestnet(t *testing.T) {
	pk := os.Getenv("LIQ_REHEARSE_LP_PK")
	if pk == "" || os.Getenv("BSC_NETWORK") != "testnet" {
		t.Skip("needs LIQ_REHEARSE_LP_PK and BSC_NETWORK=testnet")
	}
	cfg := config.Load()
	bs := NewBlockchainService(cfg)
	reg := NewP2PEscrowRegistry(bs, cfg)
	pool, ok := reg.PoolChain()
	if !ok || pool.ChainID != 97 {
		t.Fatalf("pool chain = %+v %v: refusing anything but BSC testnet", pool, ok)
	}
	client, _ := bs.ClientForChainID(97)
	key, err := crypto.HexToECDSA(trim0x(pk))
	if err != nil {
		t.Fatal("bad LP key")
	}
	lpAddr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	signer := NewLocalKeyReleaseSigner(key, client)

	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil // $5 / $5,000
	svc := NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	svc.SetTokenResolver(bs.TokenAddressForChainID)
	svc.SetPMService(NewPMService(db))
	svc.SetPoolGate(fakePoolOpen(true)) // Plan A rehearsed the real gates
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpAddr)
	ctx := context.Background()
	ten := usdt18(10)

	send := func(label string, raw []byte, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("%s prepare: %v", label, err)
		}
		hash, err := signer.SignAndBroadcast(ctx, raw)
		if err != nil {
			t.Fatalf("%s broadcast: %v", label, err)
		}
		if _, err := client.WaitForReceipt(hash, 90*time.Second); err != nil {
			t.Fatalf("%s receipt: %v", label, err)
		}
		t.Logf("%s %s", label, hash)
		return hash
	}
	raw, err := svc.PrepareApprove(ctx, lp.UserID, lpAddr, "USDT", ten.String(), 97)
	send("approve", raw, err)
	raw, err = svc.PrepareDeposit(ctx, lp.UserID, lpAddr, "USDT", ten.String(), 97)
	h := send("deposit", raw, err)
	if _, err := svc.ConfirmDeposit(ctx, lp.UserID, lpAddr, "USDT", ten.String(), h, 97); err != nil {
		t.Fatalf("confirm deposit: %v", err)
	}

	res, err := svc.CreateAd(ctx, lp.UserID, lpAddr, CreateAdParams{Token: "USDT", TokenAmount: "1", FiatCurrency: "NGN", ChainID: 97})
	if err != nil {
		t.Fatalf("stock ad: %v", err)
	}
	digest, _ := decodeHex32(res.Ad.VoucherHash)
	sig, _ := crypto.Sign(digest[:], key)
	sig[64] += 27
	if ad, err := svc.SignAd(ctx, res.Ad.ID, lp.UserID, lpAddr, sig); err != nil || ad.Status != models.P2PAdStatusActive {
		t.Fatalf("sign: %v", err)
	}

	var lim *LiquidityLimitError
	if _, err := svc.PrepareDeposit(ctx, lp.UserID, lpAddr, "USDT", usdt18(4991).String(), 97); !asLimit(err, &lim) || lim.Code != LiquidityAboveMaxCode {
		t.Fatalf("over-max deposit = %v, want above_liquidity_max on a live read", err)
	}

	raw, err = svc.PrepareWithdraw(ctx, lp.UserID, lpAddr, "USDT", ten.String(), 97)
	h = send("withdraw", raw, err)
	if _, err := svc.ConfirmWithdraw(ctx, lp.UserID, lpAddr, "USDT", ten.String(), h, 97); err != nil {
		t.Fatalf("confirm withdraw: %v", err)
	}
	bal, _ := svc.GetPoolBalance(ctx, lpAddr, "USDT", 97)
	if bal.Available.Sign() != 0 || bal.Locked.Sign() != 0 {
		t.Fatalf("after withdraw: %+v", bal)
	}
}

func trim0x(s string) string {
	if len(s) > 2 && s[:2] == "0x" {
		return s[2:]
	}
	return s
}

func asLimit(err error, target **LiquidityLimitError) bool { return errors.As(err, target) }
