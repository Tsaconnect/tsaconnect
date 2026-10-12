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
// numeric string or an array of them (summed), or an object with
// `value`/`amount`. Absent, null, "" or anything else is an error: a zero fee
// would overstate the keep. Decoded exactly from the decimal text (review
// m-11: the books are big.Int, never float64); half a kobo rounds up.
func decodeFWTransferFeeKobo(raw json.RawMessage) (*big.Int, error) {
	naira, err := decodeFWFeeNaira(raw)
	if err != nil {
		return nil, err
	}
	if naira.Sign() < 0 {
		return nil, fmt.Errorf("flutterwave transfer fee: negative %s", naira.FloatString(2))
	}
	kobo := new(big.Rat).Mul(naira, big.NewRat(100, 1))
	q, r := new(big.Int).QuoRem(kobo.Num(), kobo.Denom(), new(big.Int))
	if new(big.Int).Mul(r, big.NewInt(2)).Cmp(kobo.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q, nil
}

// decodeFWFeeNaira is the fee in naira as an exact rational.
func decodeFWFeeNaira(raw json.RawMessage) (*big.Rat, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return nil, errors.New("flutterwave transfer: no fee on the transfer")
	}
	switch raw[0] {
	case '{':
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
		return decodeFWFeeNaira(inner)
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return nil, fmt.Errorf("flutterwave transfer fee: invalid array %s: %w", raw, err)
		}
		sum := new(big.Rat)
		for _, e := range elems {
			v, err := decodeFWFeeNaira(e)
			if err != nil {
				return nil, err
			}
			sum.Add(sum, v)
		}
		return sum, nil
	case '"':
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return nil, fmt.Errorf("flutterwave transfer fee: %w", err)
		}
		raw = json.RawMessage(strings.TrimSpace(str))
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return nil, fmt.Errorf("flutterwave transfer fee: not a number %s", raw)
	}
	v, ok := new(big.Rat).SetString(num.String())
	if !ok {
		return nil, fmt.Errorf("flutterwave transfer fee: not a number %s", raw)
	}
	return v, nil
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
