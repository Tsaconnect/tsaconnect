package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// Fee constants (conservative; refine against Nile testnet during execution).
const (
	trc20FeeLimitSun  = 100_000_000 // 100 TRX cap for TRC-20 transfer
	activationCostSun = 1_100_000   // ~1.1 TRX to activate a new account
	nativeFeeEstSun   = 300_000     // ~0.3 TRX bandwidth headroom
)

type Client struct {
	cfg config.NonEVMChainConfig
	tg  *TronGrid
}

func NewClient(cfg config.NonEVMChainConfig, tg *TronGrid) *Client { return &Client{cfg: cfg, tg: tg} }

func (c *Client) Family() string { return "tron" }

func (c *Client) ValidateAddress(addr string) error {
	if !IsValidBase58(addr) {
		return fmt.Errorf("invalid tron address: %s", addr)
	}
	return nil
}

func (c *Client) GetBalances(ctx context.Context, addr string) ([]chains.Balance, error) {
	acc, err := c.tg.GetAccount(ctx, addr)
	if err != nil {
		return nil, err
	}
	out := []chains.Balance{{
		Symbol: "TRX", Raw: strconv.FormatInt(acc.BalanceSun, 10), Decimals: 6, IsNative: true,
	}}
	for sym, tokenAddr := range c.cfg.Tokens {
		out = append(out, chains.Balance{
			Symbol: sym, Raw: firstNonEmpty(acc.TRC20[tokenAddr], "0"),
			Decimals: 6, TokenAddress: tokenAddr,
		})
	}
	return out, nil
}

func (c *Client) EstimateFee(ctx context.Context, req chains.TransferRequest) (chains.FeeQuote, error) {
	sender, err := c.tg.GetAccount(ctx, req.From)
	if err != nil {
		return chains.FeeQuote{}, err
	}
	recipient, err := c.tg.GetAccount(ctx, req.To)
	if err != nil {
		return chains.FeeQuote{}, err
	}
	estFee := int64(nativeFeeEstSun)
	if req.TokenAddr != "" {
		estFee = trc20FeeLimitSun / 10 // typical energy burn is well under the cap
	}
	needsActivation := !recipient.Active
	if needsActivation {
		estFee += activationCostSun
	}
	note := ""
	if sender.BalanceSun < estFee {
		if req.TokenAddr != "" {
			note = "You need TRX in this wallet to send " + req.Symbol + "."
		} else {
			note = "Not enough TRX to cover the network fee."
		}
	}
	return chains.FeeQuote{
		GasSymbol: "TRX", EstFeeRaw: strconv.FormatInt(estFee, 10), GasDecimals: 6,
		NeedsActivation: needsActivation, HasEnoughGas: sender.BalanceSun >= estFee, Note: note,
	}, nil
}

func (c *Client) PrepareTransfer(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	if err := c.ValidateAddress(req.To); err != nil {
		return chains.PreparedTx{}, err
	}
	var raw *RawTx
	var err error
	if req.TokenAddr == "" {
		amt, ok := new(big.Int).SetString(req.Amount, 10)
		if !ok {
			return chains.PreparedTx{}, fmt.Errorf("bad amount")
		}
		raw, err = c.tg.CreateTransfer(ctx, req.From, req.To, amt.Int64())
	} else {
		amt, ok := new(big.Int).SetString(req.Amount, 10)
		if !ok {
			return chains.PreparedTx{}, fmt.Errorf("bad amount")
		}
		raw, err = c.tg.TriggerTransfer(ctx, req.From, req.TokenAddr, req.To, amt, trc20FeeLimitSun)
	}
	if err != nil {
		return chains.PreparedTx{}, err
	}
	rawJSON, _ := json.Marshal(raw)
	return chains.PreparedTx{
		Family: "tron",
		Raw:    string(rawJSON),
		// The Tron txID IS the sha256 digest of raw_data — sign it directly.
		Payloads: []chains.SigningPayload{{Index: 0, HashHex: raw.TxID, Curve: chains.CurveSecp256k1}},
	}, nil
}

func (c *Client) AssembleAndBroadcast(ctx context.Context, prepared chains.PreparedTx, sigs []chains.Signature) (string, error) {
	if len(sigs) != 1 {
		return "", fmt.Errorf("tron expects exactly one signature, got %d", len(sigs))
	}
	var raw RawTx
	if err := json.Unmarshal([]byte(prepared.Raw), &raw); err != nil {
		return "", err
	}
	return c.tg.Broadcast(ctx, &raw, sigs[0].SigHex)
}

func (c *Client) GetTxStatus(ctx context.Context, txHash string) (chains.TxStatus, error) {
	info, err := c.tg.GetTxInfo(ctx, txHash)
	if err != nil {
		return chains.TxStatus{}, err
	}
	return chains.TxStatus{
		Found: info.ID != "", Confirmed: info.Confirmed, Success: info.Success,
		Confirmations: boolToConf(info.Confirmed),
	}, nil
}

func (c *Client) History(ctx context.Context, addr string, cursor string) ([]chains.Activity, string, error) {
	// Minimal first pass: return empty history; hydrated by the reconciler/webhookless
	// polling in a later task. Kept non-nil to satisfy the interface contract.
	return []chains.Activity{}, "", nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func boolToConf(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
