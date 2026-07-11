package btc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Esplora is a thin client for Esplora-compatible REST APIs
// (blockstream.info, mempool.space). All BTC reads and the broadcast go
// through here; the node itself is never contacted directly.
type Esplora struct {
	base string
	http *http.Client
}

func NewEsplora(base string) *Esplora {
	return &Esplora{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// AddressStats mirrors GET /address/{addr}. Esplora reports cumulative
// funded/spent sums; the live balance is funded - spent per section.
type AddressStats struct {
	ChainStats   txoStats `json:"chain_stats"`
	MempoolStats txoStats `json:"mempool_stats"`
}

type txoStats struct {
	FundedSum int64 `json:"funded_txo_sum"`
	SpentSum  int64 `json:"spent_txo_sum"`
	TxCount   int64 `json:"tx_count"`
}

// BalanceSat is the confirmed balance plus the net mempool delta (incoming
// minus outgoing pending), floored at zero for display safety.
func (s AddressStats) BalanceSat() int64 {
	total := (s.ChainStats.FundedSum - s.ChainStats.SpentSum) +
		(s.MempoolStats.FundedSum - s.MempoolStats.SpentSum)
	if total < 0 {
		return 0
	}
	return total
}

func (e *Esplora) GetAddressStats(ctx context.Context, addr string) (AddressStats, error) {
	var out AddressStats
	err := e.getJSON(ctx, "/address/"+addr, &out)
	return out, err
}

// UTXO mirrors GET /address/{addr}/utxo entries.
type UTXO struct {
	Txid   string `json:"txid"`
	Vout   uint32 `json:"vout"`
	Value  int64  `json:"value"` // sats
	Status struct {
		Confirmed bool `json:"confirmed"`
	} `json:"status"`
}

// GetUTXOs returns the address's spendable outputs, confirmed first then by
// value descending, so callers can select greedily without re-sorting.
func (e *Esplora) GetUTXOs(ctx context.Context, addr string) ([]UTXO, error) {
	var out []UTXO
	if err := e.getJSON(ctx, "/address/"+addr+"/utxo", &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status.Confirmed != out[j].Status.Confirmed {
			return out[i].Status.Confirmed
		}
		return out[i].Value > out[j].Value
	})
	return out, nil
}

// FeeRateSatVB returns a sat/vB rate for roughly half-hour confirmation.
// Primary source is Esplora's /fee-estimates (blockstream + mempool.space);
// mempool.space's /v1/fees/recommended is the fallback so either provider
// works. Errors when both fail — a made-up rate risks stuck or overpaying txs.
func (e *Esplora) FeeRateSatVB(ctx context.Context) (float64, error) {
	var est map[string]float64
	if err := e.getJSON(ctx, "/fee-estimates", &est); err == nil {
		// Prefer the 3-block target; fall back to the closest available.
		for _, target := range []string{"3", "2", "4", "6", "1"} {
			if rate, ok := est[target]; ok && rate > 0 {
				return math.Max(rate, 1), nil
			}
		}
	}
	var rec struct {
		HalfHourFee float64 `json:"halfHourFee"`
	}
	if err := e.getJSON(ctx, "/v1/fees/recommended", &rec); err == nil && rec.HalfHourFee > 0 {
		return math.Max(rec.HalfHourFee, 1), nil
	}
	return 0, fmt.Errorf("btc: fee rate unavailable from %s", e.base)
}

// AddressTx mirrors the entries of GET /address/{addr}/txs (recent history:
// all mempool txs plus the latest confirmed page).
type AddressTx struct {
	Txid   string `json:"txid"`
	Status struct {
		Confirmed bool  `json:"confirmed"`
		BlockTime int64 `json:"block_time"`
	} `json:"status"`
	Vin []struct {
		Prevout struct {
			Address string `json:"scriptpubkey_address"`
			Value   int64  `json:"value"`
		} `json:"prevout"`
	} `json:"vin"`
	Vout []struct {
		Address string `json:"scriptpubkey_address"`
		Value   int64  `json:"value"`
	} `json:"vout"`
}

func (e *Esplora) GetAddressTxs(ctx context.Context, addr string) ([]AddressTx, error) {
	var out []AddressTx
	err := e.getJSON(ctx, "/address/"+addr+"/txs", &out)
	return out, err
}

// TxStatus mirrors GET /tx/{txid}'s status section.
type TxStatus struct {
	Found       bool
	Confirmed   bool
	BlockHeight int64
}

func (e *Esplora) GetTxStatus(ctx context.Context, txid string) (TxStatus, error) {
	var tx struct {
		Status struct {
			Confirmed   bool  `json:"confirmed"`
			BlockHeight int64 `json:"block_height"`
		} `json:"status"`
	}
	err := e.getJSON(ctx, "/tx/"+txid, &tx)
	if err != nil {
		if isNotFound(err) {
			return TxStatus{Found: false}, nil
		}
		return TxStatus{}, err
	}
	return TxStatus{Found: true, Confirmed: tx.Status.Confirmed, BlockHeight: tx.Status.BlockHeight}, nil
}

func (e *Esplora) TipHeight(ctx context.Context) (int64, error) {
	body, err := e.get(ctx, "/blocks/tip/height")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
}

// Broadcast submits a raw signed tx (hex) and returns the txid.
func (e *Esplora) Broadcast(ctx context.Context, rawHex string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/tx", bytes.NewBufferString(rawHex))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain")
	res, err := e.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusOK {
		// Esplora returns the node's reject reason as plain text — surface it.
		return "", fmt.Errorf("broadcast rejected (%d): %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}

// --- plumbing ---

type httpError struct {
	status int
	body   string
}

func (h *httpError) Error() string { return fmt.Sprintf("esplora %d: %s", h.status, h.body) }

func isNotFound(err error) bool {
	he, ok := err.(*httpError)
	return ok && he.status == http.StatusNotFound
}

func (e *Esplora) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+path, nil)
	if err != nil {
		return nil, err
	}
	res, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, &httpError{status: res.StatusCode, body: strings.TrimSpace(string(body))}
	}
	return body, nil
}

func (e *Esplora) getJSON(ctx context.Context, path string, out any) error {
	body, err := e.get(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
