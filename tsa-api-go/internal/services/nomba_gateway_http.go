package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Compile-time assertion: NombaHTTPGateway must satisfy NombaGateway.
var _ NombaGateway = (*NombaHTTPGateway)(nil)

// NombaHTTPGateway is the live NombaGateway backed by the Nomba transfer API
// (POST /v2/transfers/bank). It delegates auth and HTTP concerns to NombaClient
// so the gateway layer is thin: amount parsing, request mapping, and id extraction.
type NombaHTTPGateway struct {
	client *NombaClient
}

// NewNombaHTTPGateway returns a NombaHTTPGateway using the supplied NombaClient
// for OAuth-authenticated requests. The client must be non-nil (i.e. credentials
// must be configured); callers should check client.IsEnabled() before constructing.
func NewNombaHTTPGateway(client *NombaClient) *NombaHTTPGateway {
	return &NombaHTTPGateway{client: client}
}

// InitiatePayout sends NGN to the recipient's bank account via the Nomba transfer
// API and returns Nomba's transfer id (e.g. "API-TRANSFER-abc…"). Both "SUCCESS"
// and "PENDING_BILLING" responses are accepted — the final status arrives via the
// payout webhook. An empty id from Nomba is treated as an error (defense-in-depth).
func (g *NombaHTTPGateway) InitiatePayout(ctx context.Context, req PayoutRequest) (string, error) {
	amount, err := strconv.ParseFloat(strings.TrimSpace(req.AmountNGN), 64)
	if err != nil {
		return "", fmt.Errorf("bad payout amount %q: %w", req.AmountNGN, err)
	}
	res, err := g.client.Transfer(ctx, NombaTransferRequest{
		Amount:        amount,
		AccountNumber: req.AccountNumber,
		AccountName:   req.AccountName,
		BankCode:      req.BankCode,
		MerchantTxRef: req.Reference,
		SenderName:    req.SenderName,
		Narration:     "TSA P2P payout",
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.ID) == "" {
		return "", fmt.Errorf("nomba transfer returned empty id (status=%s)", res.Status)
	}
	return res.ID, nil
}
