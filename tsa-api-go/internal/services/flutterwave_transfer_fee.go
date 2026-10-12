package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// PayoutFeeReader reads the provider's actual fee for a confirmed transfer
// (spec S47). *FlutterwaveClient satisfies it.
type PayoutFeeReader interface {
	TransferFeeKobo(ctx context.Context, payoutID string) (*big.Int, error)
}

// TransferFeeKobo is GetTransfer's fee in kobo.
func (c *FlutterwaveClient) TransferFeeKobo(ctx context.Context, payoutID string) (*big.Int, error) {
	tr, err := c.GetTransfer(ctx, strings.TrimSpace(payoutID))
	if err != nil {
		return nil, err
	}
	return decodeFWTransferFeeKobo(tr.Fee)
}

// decodeFWTransferFeeKobo reads a v4 transfer's `fee`, whose live shape is
// unmeasured (flutterwave_client.go:1052-1075; Task 17 probes it): a number, a
// numeric string or an array (FWFeeAmount), or an object with `value`/`amount`.
// Absent, null, "" or anything else is an error: a zero fee would overstate
// the keep.
func decodeFWTransferFeeKobo(raw json.RawMessage) (*big.Int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return nil, errors.New("flutterwave transfer: no fee on the transfer")
	}
	if raw[0] == '{' {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("flutterwave transfer fee: %w", err)
		}
		inner, ok := obj["value"]
		if !ok {
			inner, ok = obj["amount"]
		}
		if !ok {
			return nil, fmt.Errorf("flutterwave transfer fee: unrecognised object %s", raw)
		}
		return decodeFWTransferFeeKobo(inner)
	}
	var amt FWFeeAmount
	if err := amt.UnmarshalJSON(raw); err != nil {
		return nil, err
	}
	if amt.Float64() < 0 {
		return nil, fmt.Errorf("flutterwave transfer fee: negative %v", amt.Float64())
	}
	f := new(big.Float).Mul(big.NewFloat(amt.Float64()), big.NewFloat(100))
	f.Add(f, big.NewFloat(0.5))
	k, _ := f.Int(nil)
	return k, nil
}

// Forward-transfer lookups scan a narrow window (re-review I-D): started_at
// ± 2 h keeps the date-only listing to at most two days, and 200 pages of 50
// is 10,000 transfers — at ±24 h and 20 pages a busy account always ran out
// of pages and every lookup read "scan incomplete".
const (
	fwForwardLookupSpan     = 2 * time.Hour
	fwForwardLookupMaxPages = 200
)

// FindForwardTransfer finds a P2P settlement forward's transfer by its
// reference around the forward's started_at. It satisfies TransferLookup.
func (c *FlutterwaveClient) FindForwardTransfer(ctx context.Context, reference string, around time.Time) (FWTransfer, bool, error) {
	return c.findTransferByReference(ctx, reference, around, fwForwardLookupSpan, fwForwardLookupMaxPages)
}
