package services

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// PayRefundOption is one network and stablecoin a refund can be sent on, with
// the exact amount at that token's decimals — what the app prepares and signs.
type PayRefundOption struct {
	ChainID         int64  `json:"chainId"`
	ChainKey        string `json:"chainKey"`
	ChainName       string `json:"chainName"`
	TokenSymbol     string `json:"tokenSymbol"`
	TokenAddress    string `json:"tokenAddress"`
	Decimals        int    `json:"decimals"`
	AmountBaseUnits string `json:"amountBaseUnits"`
	AmountDisplay   string `json:"amountDisplay"`
	IsDefault       bool   `json:"isDefault"`
}

// PayRefundPaymentSummary is the payment a refund is for, as the refund
// screen shows it.
type PayRefundPaymentSummary struct {
	ID            uuid.UUID  `json:"id"`
	Code          string     `json:"code"`
	Description   string     `json:"description"`
	CustomerTotal int64      `json:"customerTotal"`
	SucceededAt   *time.Time `json:"succeededAt"`
	MerchantName  string     `json:"merchantName"`
}

// PayRefundDetail is everything the owner's app needs to send a refund:
// FromAddress is the only wallet it may sign with (the owner's proven one; ""
// until they prove it), Options what it may send.
type PayRefundDetail struct {
	Refund         PayRefundView           `json:"refund"`
	Payment        PayRefundPaymentSummary `json:"payment"`
	PayerFirstName string                  `json:"payerFirstName"`
	FromAddress    string                  `json:"fromAddress"`
	Options        []PayRefundOption       `json:"options"`
}

// Detail is the merchant's refund id with its payment, payer and — while a
// live refund still waits to be sent — the networks it can be sent on.
func (s *PayRefundService) Detail(ctx context.Context, m *models.PayMerchant, id uuid.UUID) (*PayRefundDetail, error) {
	r, err := s.Get(ctx, m.ID, id)
	if err != nil {
		return nil, err
	}
	p, err := s.payments.Get(ctx, r.PaymentID)
	if err != nil {
		return nil, err
	}
	return &PayRefundDetail{
		Refund: s.View(r),
		Payment: PayRefundPaymentSummary{ID: p.ID, Code: s.payments.payCodeFor(ctx, p), Description: p.Description,
			CustomerTotal: p.CustomerTotalKobo, SucceededAt: p.SucceededAt, MerchantName: m.BusinessName},
		PayerFirstName: s.payerFirstName(ctx, p),
		FromAddress:    s.ownerWallet(ctx, m.UserID),
		Options:        s.options(ctx, p, r),
	}, nil
}

// ownerWallet is the owner's linked wallet when they have proven it, else ""
// (a failed read too: it is logged).
func (s *PayRefundService) ownerWallet(ctx context.Context, userID uuid.UUID) string {
	w, err := s.provenOwnerWallet(ctx, userID)
	if err != nil {
		log.Printf("[tsa-pay] refund: owner %s wallet: %s", userID, RedactAlertText(err.Error()))
		return ""
	}
	return w
}

// provenOwnerWallet is the owner's linked wallet, checksummed, when they have
// proven they hold it; "" when there is none or it is unproven.
func (s *PayRefundService) provenOwnerWallet(ctx context.Context, userID uuid.UUID) (string, error) {
	var w sql.NullString
	res := s.db.WithContext(ctx).Table("users").Select("wallet_address").Where("id = ?", userID).Limit(1).Scan(&w)
	if res.Error != nil {
		return "", res.Error
	}
	addr := strings.TrimSpace(w.String)
	if res.RowsAffected == 0 || !common.IsHexAddress(addr) {
		return "", nil
	}
	proven, err := WalletProven(s.db.WithContext(ctx), userID, addr)
	if err != nil || !proven {
		return "", err
	}
	return common.HexToAddress(addr).Hex(), nil
}

// options is every listed stablecoin on every live mainnet chain whose
// decimals can be read, the payer's own deposit (D21's default) first — or,
// when she paid in something not offered (or her deposit chain is unknown),
// the first. Only a live refund waiting to be sent has any.
func (s *PayRefundService) options(ctx context.Context, p *models.PayPayment, r *models.PayRefund) []PayRefundOption {
	out := []PayRefundOption{}
	if r.Mode != models.PayModeLive || r.Status != models.PayRefundRequested || s.network == nil {
		return out
	}
	var depositChainID int64
	depositSymbol := ""
	if src, err := s.source(ctx, p); err == nil {
		depositChainID, depositSymbol = src.depositChainID, src.depositSymbol
	}
	for _, id := range s.network.MainnetChainIDs() {
		chain, ok := s.network.MainnetChainNameForID(id)
		if !ok {
			continue
		}
		for _, symbol := range payOwnerSettlementTokens {
			addr := s.network.TokenAddress(chain, symbol)
			if addr == "" {
				continue
			}
			decimals, err := s.network.TokenDecimals(chain, addr)
			if err != nil {
				log.Printf("[tsa-pay] refund %s: %s decimals on %s: %s", r.ID, symbol, chain, RedactAlertText(err.Error()))
				continue
			}
			amount := stableForUSDMicros(big.NewInt(r.AmountMicroUSD), decimals)
			out = append(out, PayRefundOption{ChainID: id, ChainKey: chain, ChainName: s.network.ChainName(chain), TokenSymbol: symbol,
				TokenAddress: addr, Decimals: decimals, AmountBaseUnits: amount.String(), AmountDisplay: BaseUnitsToDecimal(amount, decimals),
				IsDefault: depositChainID != 0 && id == depositChainID && symbol == depositSymbol})
		}
	}
	slices.SortStableFunc(out, func(a, b PayRefundOption) int {
		switch {
		case a.IsDefault && !b.IsDefault:
			return -1
		case b.IsDefault && !a.IsDefault:
			return 1
		}
		return 0
	})
	if len(out) > 0 {
		out[0].IsDefault = true
	}
	return out
}

// PayRefundSubmit is the owner's app handing over the refund transfer it
// signed — not yet broadcast.
type PayRefundSubmit struct {
	ChainID     int64  `json:"chainId"`
	TokenSymbol string `json:"tokenSymbol"`
	SignedTx    string `json:"signedTx"`
}

// payRefundSigned is a signed transfer held to its refund: safe to record and
// broadcast.
type payRefundSigned struct {
	tx           *types.Transaction
	chain        string
	symbol       string
	tokenAddress string
}

// Submit takes the signed refund transfer (D23). Before anything is written the
// server decodes it and holds it to exactly this refund: the live chain, the
// listed stablecoin's contract (derived here, never taken from the app),
// transfer(to_address, amount at the token's on-chain decimals), no native
// value, a plain transaction type, signed by the owner's proven wallet with a
// nonce the wallet has not used yet. One guarded write then records its hash
// and raw bytes — only then is it broadcast, so a crash after the write still
// knows the hash, and no hash but this refund's own can ever be attached. A
// failed broadcast leaves the refund submitted for the confirmer, which sends
// it again.
func (s *PayRefundService) Submit(ctx context.Context, m *models.PayMerchant, id uuid.UUID, ownerWallet string, in PayRefundSubmit) (*models.PayRefund, error) {
	r, err := s.Get(ctx, m.ID, id)
	if err != nil {
		return nil, err
	}
	if r.Status != models.PayRefundRequested || r.Mode != models.PayModeLive {
		return nil, ErrPayRefundState
	}
	if s.network == nil || s.verifier == nil {
		return nil, ErrPayRefundUnavailable
	}
	from, err := s.provenOwnerWallet(ctx, m.UserID)
	if err != nil {
		return nil, err
	}
	if from == "" || !strings.EqualFold(from, strings.TrimSpace(ownerWallet)) {
		return nil, &PayRefundTxError{Why: "your TSA wallet isn't verified — verify it in TSA Connect and try again"}
	}
	signed, err := s.checkSignedRefund(ctx, r, from, in)
	if err != nil {
		return nil, err
	}
	bin, err := signed.tx.MarshalBinary()
	if err != nil {
		return nil, &PayRefundTxError{Why: "it isn't a signed transaction"}
	}
	now := s.now()
	res := s.db.WithContext(ctx).Model(&models.PayRefund{}).
		Where("id = ? AND status = ?", r.ID, models.PayRefundRequested).
		Updates(map[string]any{"status": models.PayRefundSubmitted, "tx_hash": signed.tx.Hash().Hex(), "raw_tx": hexutil.Encode(bin),
			"chain_id": in.ChainID, "token_symbol": signed.symbol, "token_address": signed.tokenAddress,
			"from_address": from, "submitted_at": &now})
	if res.Error != nil {
		if isPayRefundTxHashViolation(res.Error) {
			return nil, ErrPayRefundTxUsed
		}
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrPayRefundState
	}
	s.broadcast(ctx, signed.chain, r, signed.tx)
	return s.Get(ctx, m.ID, id)
}

// isPayRefundTxHashViolation reports whether err is pay_refunds' unique tx_hash
// refusing a transaction another refund already holds. Postgres names the
// index (idx_pay_refunds_tx_hash), SQLite the column (pay_refunds.tx_hash).
func isPayRefundTxHashViolation(err error) bool {
	return isUniqueViolation(err) && strings.Contains(err.Error(), "tx_hash")
}

// broadcast sends r's signed transfer and records how that went on the row
// (review I6): a refusal leaves an owner-safe reason in last_broadcast_error —
// shown on every refund surface — until a later send goes through, which
// clears it. The refund stays submitted either way; the confirmer sends it
// again. The raw error is only logged, redacted: an RPC error can carry the
// provider's URL and key.
func (s *PayRefundService) broadcast(ctx context.Context, chain string, r *models.PayRefund, tx *types.Transaction) {
	err := s.network.Broadcast(ctx, chain, tx)
	if err != nil && alreadyKnownTx(err) {
		err = nil // the node has it already: that is a send that went through
	}
	reason := ""
	if err != nil {
		reason = payRefundBroadcastReason(err)
		log.Printf("[tsa-pay] refund %s: broadcast on %s refused (%s), the confirmer sends it again: %s",
			r.ID, chain, reason, RedactAlertText(err.Error()))
	}
	at := s.now()
	if uerr := s.db.WithContext(ctx).Model(&models.PayRefund{}).Where("id = ?", r.ID).
		UpdateColumns(map[string]any{"last_broadcast_error": reason, "last_broadcast_at": &at}).Error; uerr != nil {
		log.Printf("[tsa-pay] refund %s: record broadcast: %s", r.ID, RedactAlertText(uerr.Error()))
		return
	}
	r.LastBroadcastError, r.LastBroadcastAt = reason, &at
}

// alreadyKnownTx reports a node answering that it already has the
// transaction: geth says "already known", nethermind "AlreadyKnown".
func alreadyKnownTx(err error) bool {
	return strings.Contains(strings.ToLower(strings.ReplaceAll(err.Error(), " ", "")), "alreadyknown")
}

// payRefundBroadcastReason is a refused broadcast in words the owner can act
// on. The node's own text never reaches them.
func payRefundBroadcastReason(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case rejectedForFunds(err):
		return "Not enough gas in your wallet"
	case strings.Contains(msg, "underpriced"), strings.Contains(msg, "fee too low"), strings.Contains(msg, "less than block base fee"):
		return "The network fee was too low"
	case strings.Contains(msg, "nonce too low"):
		return "Another transaction from your wallet already used its place"
	}
	return "The network didn't take it"
}

// checkSignedRefund decodes in.SignedTx and holds it to r, sent from the
// owner's proven wallet from. Every refusal of the transaction itself is a
// *PayRefundTxError worded for the owner; a nonce the wallet already used is
// ErrPayRefundNonceUsed; a check the chain can't answer right now
// (decimals, nonce) is ErrPayRefundUnavailable — never a pass.
func (s *PayRefundService) checkSignedRefund(ctx context.Context, r *models.PayRefund, from string, in PayRefundSubmit) (*payRefundSigned, error) {
	refuse := func(why string) (*payRefundSigned, error) { return nil, &PayRefundTxError{Why: why} }
	if !common.IsHexAddress(r.ToAddress) {
		return nil, ErrPayRefundNotRefundable // a live refund always has one; never pay the zero address
	}
	chain, ok := s.network.MainnetChainNameForID(in.ChainID)
	if !ok {
		return refuse("refunds can't be sent on that network")
	}
	symbol := strings.ToUpper(strings.TrimSpace(in.TokenSymbol))
	if !slices.Contains(payOwnerSettlementTokens, symbol) {
		return refuse("refunds are sent in " + strings.Join(payOwnerSettlementTokens, " or "))
	}
	tokenAddr := s.network.TokenAddress(chain, symbol)
	if tokenAddr == "" {
		return refuse(symbol + " isn't available on " + s.network.ChainName(chain))
	}
	decimals, err := s.network.TokenDecimals(chain, tokenAddr)
	if err != nil {
		log.Printf("[tsa-pay] refund %s: %s decimals on %s: %s", r.ID, symbol, chain, RedactAlertText(err.Error()))
		return nil, ErrPayRefundUnavailable
	}
	tx, signer, err := blockchain.DecodeSignedTx(strings.TrimSpace(in.SignedTx))
	if err != nil {
		return refuse("it isn't a signed transaction")
	}
	// P10: blob and set-code (EIP-7702) transactions carry more than a
	// transfer — a set-code authorization moves nonces of its own.
	if tx.Type() > types.DynamicFeeTxType {
		return refuse("it isn't a plain token transfer")
	}
	if tx.ChainId() == nil || tx.ChainId().Cmp(big.NewInt(in.ChainID)) != 0 {
		return refuse("it was signed for another network")
	}
	if tx.To() == nil || !strings.EqualFold(tx.To().Hex(), tokenAddr) {
		return refuse("it doesn't send " + symbol)
	}
	if tx.Value().Sign() != 0 {
		return refuse("it also sends the network's own coin")
	}
	amount := stableForUSDMicros(big.NewInt(r.AmountMicroUSD), decimals)
	want, err := blockchain.ERC20TransferData(common.HexToAddress(r.ToAddress), amount)
	if err != nil || !bytes.Equal(tx.Data(), want) {
		return refuse("it doesn't send exactly " + BaseUnitsToDecimal(amount, decimals) + " " + symbol + " to the customer")
	}
	if signer != common.HexToAddress(from) {
		return refuse("it's signed by " + signer.Hex() + ", not your TSA wallet " + from + " — switch to that wallet and try again")
	}
	// P9: a nonce the wallet already used can never be mined. Taken anyway, it
	// would sit until the confirmer read it as replaced and parked it in review.
	next, err := s.network.ConfirmedNonce(ctx, chain, signer, 1)
	if err != nil {
		log.Printf("[tsa-pay] refund %s: nonce of %s on %s: %s", r.ID, signer.Hex(), chain, RedactAlertText(err.Error()))
		return nil, ErrPayRefundUnavailable
	}
	if next > tx.Nonce() {
		return nil, ErrPayRefundNonceUsed
	}
	return &payRefundSigned{tx: tx, chain: chain, symbol: symbol, tokenAddress: tokenAddr}, nil
}
