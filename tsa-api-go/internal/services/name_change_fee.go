package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// The re-verification fee's money legs. MCGP is taken as an operator debit hold
// (BillPaymentService, scoped to name changes); a stablecoin is a transfer the
// user signs to the ramp treasury, proven on-chain and — if the change is
// cancelled before verification starts — sent back by the treasury. Neither leg
// is new machinery: this file only adapts the two to one fee.

// usdMicros is the fee's unit: USD at the 6 decimals USDC quotes in, so a fee
// converts to MCGP (priced in USDC) and to any stablecoin without a float.
const usdMicrosDecimals = 6

// ParseFeeUSDMicros reads a fee like "1.00" into USD micro-units. More than six
// decimal places, a negative or a malformed value is refused rather than rounded.
func ParseFeeUSDMicros(fee string) (*big.Int, error) {
	micros, err := DecimalToBaseUnits(fee, usdMicrosDecimals)
	if err != nil {
		return nil, fmt.Errorf("fee %q: %w", fee, err)
	}
	return micros, nil
}

// mcgpForUSDMicros is how much MCGP (18dp) covers usdMicros at usdcPerMcgp — the
// USDC (6dp) one whole MCGP is worth. Rounded UP so the fee is never under-paid.
func mcgpForUSDMicros(usdMicros, usdcPerMcgp *big.Int) (*big.Int, error) {
	if usdcPerMcgp == nil || usdcPerMcgp.Sign() <= 0 {
		return nil, errors.New("no MCGP price")
	}
	num := new(big.Int).Mul(usdMicros, oneE18)
	q, m := new(big.Int).QuoRem(num, usdcPerMcgp, new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q, nil
}

// stableForUSDMicros is usdMicros in a $1 stablecoin's base units. A token with
// fewer than six decimals rounds UP, like the MCGP amount.
func stableForUSDMicros(usdMicros *big.Int, decimals int) *big.Int {
	if decimals >= usdMicrosDecimals {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-usdMicrosDecimals)), nil)
		return new(big.Int).Mul(usdMicros, scale)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(usdMicrosDecimals-decimals)), nil)
	q, m := new(big.Int).QuoRem(usdMicros, scale, new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// StableFeeOption is one stablecoin, on one chain, a fee can be paid in.
type StableFeeOption struct {
	Chain        string
	ChainID      int64
	Token        string // symbol
	TokenAddress string
	Decimals     int
}

// stableFeeRail is where a stablecoin fee is paid, proven and refunded.
// *TreasuryFeeRail is the real one.
type stableFeeRail interface {
	Available() bool
	// To is the address fees are paid to.
	To() string
	Options(ctx context.Context) []StableFeeOption
	// PrepareTransfer builds the unsigned transfer the user signs, or fails —
	// the caller treats that as "no ready-made transaction", not as a refusal.
	PrepareTransfer(chain, tokenAddress, from string, amount *big.Int) ([]byte, error)
	// VerifyPayment is what the transaction moved of the token from `from` to
	// `to`, and the facts about the transaction a fee is held to — errors as
	// ERC20SourcedTransferVerifier reports them. On ErrTransferNoMatch (mined,
	// nothing matched) it still returns the payment, with nothing credited, so
	// the caller can tell how deep the transaction is.
	VerifyPayment(ctx context.Context, chain, txHash, tokenAddress, from, to string) (*StablePayment, error)
	// Refund sends a fee back. Same uncertain-broadcast contract as
	// Treasury.TransferToken: attempted with an error means the money may have left.
	Refund(ctx context.Context, chain, token, to string, amount *big.Int, usd float64) (txHash string, attempted bool, err error)
	// TransactionKnown reports whether the chain knows txHash at all, mined or
	// pending, and who signed it (empty when that cannot be recovered).
	// known=false with a nil error only on a definite "no such transaction" —
	// a read that fails is an error, never "unknown".
	TransactionKnown(ctx context.Context, chain, txHash string) (known bool, sender string, err error)
}

// StablePayment is what one fee transaction proved.
type StablePayment struct {
	// Credited is what the Transfer logs moved from the payer to the treasury.
	Credited *big.Int
	// Sender signed the transaction. A fee must be sent by the proven wallet
	// itself, not moved out of it by some contract it once approved.
	Sender string
	// BlockTime dates the transaction: a transfer from before the change was
	// opened cannot have been made for it.
	BlockTime time.Time
	// Confirmations is how deep the transaction is.
	Confirmations uint64
}

// FeeTxReader is the slice of an Ethereum client a fee proof needs;
// *ethclient.Client satisfies it.
type FeeTxReader interface {
	TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error)
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	BlockNumber(ctx context.Context) (uint64, error)
}

// TreasuryFeeRail takes stablecoin fees into the ramp treasury, on the chains it
// has lanes for, and refunds out of the same lanes. It needs no key or float of
// its own: an unwired treasury simply means the stablecoin method is unavailable.
type TreasuryFeeRail struct {
	treasury  *Treasury
	verifiers map[string]ERC20TransferVerifier
	readers   map[string]FeeTxReader
}

// NewTreasuryFeeRail wires the treasury, the per-chain transfer verifiers (the
// same map converted buys prove their deliveries with) and a transaction reader
// per chain for the sender, time and depth of a payment.
func NewTreasuryFeeRail(treasury *Treasury, verifiers map[string]ERC20TransferVerifier, readers map[string]FeeTxReader) *TreasuryFeeRail {
	return &TreasuryFeeRail{treasury: treasury, verifiers: verifiers, readers: readers}
}

// FeeTxReaders is a transaction reader for each chain key that has a client.
func FeeTxReaders(chains *BlockchainService, keys []string) map[string]FeeTxReader {
	out := map[string]FeeTxReader{}
	for _, k := range keys {
		if c := chains.ClientForChain(k); c != nil && c.Client != nil {
			out[strings.ToLower(k)] = c.Client
		}
	}
	return out
}

func (r *TreasuryFeeRail) Available() bool {
	return r != nil && r.treasury.Enabled() && r.treasury.Address() != "" && !r.treasury.Killed()
}

func (r *TreasuryFeeRail) To() string {
	if r == nil || !r.treasury.Enabled() {
		return ""
	}
	return r.treasury.Address()
}

// Options lists every stablecoin on every treasury lane whose decimals can be
// read, ordered by chain then symbol.
func (r *TreasuryFeeRail) Options(_ context.Context) []StableFeeOption {
	if !r.Available() {
		return nil
	}
	var out []StableFeeOption
	for _, chain := range r.treasury.ChainKeys() {
		lane := r.treasury.Lane(chain)
		if lane == nil || lane.SponsorLane == nil {
			continue
		}
		symbols := make([]string, 0, len(lane.Tokens))
		for symbol := range lane.Tokens {
			if IsStableAsset(symbol) {
				symbols = append(symbols, symbol)
			}
		}
		sort.Strings(symbols)
		for _, symbol := range symbols {
			addr := lane.Tokens[symbol]
			// Read on-chain through the treasury's one lookup: a hardcoded
			// table would be a second opinion on the only number that makes a
			// fee cost $1 rather than a trillionth of it.
			decimals, err := r.treasury.TokenDecimals(chain, symbol)
			if err != nil || decimals <= 0 {
				continue
			}
			out = append(out, StableFeeOption{Chain: chain, ChainID: lane.ChainID, Token: symbol, TokenAddress: addr, Decimals: decimals})
		}
	}
	return out
}

func (r *TreasuryFeeRail) PrepareTransfer(chain, tokenAddress, from string, amount *big.Int) ([]byte, error) {
	lane := r.treasury.Lane(chain)
	if lane == nil || lane.SponsorLane == nil {
		return nil, ErrSponsorNotAvailable
	}
	return lane.Native.PrepareERC20Transfer(tokenAddress, from, r.To(), amount)
}

func (r *TreasuryFeeRail) VerifyPayment(ctx context.Context, chain, txHash, tokenAddress, from, to string) (*StablePayment, error) {
	key := strings.ToLower(strings.TrimSpace(chain))
	v, ok := r.verifiers[key].(ERC20SourcedTransferVerifier)
	reader := r.readers[key]
	if !ok || reader == nil {
		return nil, fmt.Errorf("%w: no verifier for %s", ErrTransferNotFound, chain)
	}
	credited, verr := v.VerifyTransferFrom(ctx, txHash, tokenAddress, from, to)
	if verr != nil && !errors.Is(verr, ErrTransferNoMatch) {
		return nil, verr
	}
	hash := common.HexToHash(txHash)
	tx, _, err := reader.TransactionByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("verify: read transaction: %w", err)
	}
	sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return nil, fmt.Errorf("verify: recover sender: %w", err)
	}
	receipt, err := reader.TransactionReceipt(ctx, hash)
	if err != nil || receipt == nil || receipt.BlockNumber == nil {
		return nil, fmt.Errorf("verify: read receipt: %v", err)
	}
	header, err := reader.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("verify: read block: %w", err)
	}
	head, err := reader.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify: read chain head: %w", err)
	}
	var depth uint64
	if n := receipt.BlockNumber.Uint64(); head >= n {
		depth = head - n + 1
	}
	p := &StablePayment{Credited: credited, Sender: sender.Hex(), BlockTime: time.Unix(int64(header.Time), 0).UTC(), Confirmations: depth}
	if verr != nil {
		p.Credited = new(big.Int)
		return p, verr
	}
	return p, nil
}

func (r *TreasuryFeeRail) Refund(ctx context.Context, chain, token, to string, amount *big.Int, usd float64) (string, bool, error) {
	lane := r.treasury.Lane(chain)
	if lane == nil {
		return "", false, ErrSponsorNotAvailable
	}
	// A refund is money leaving the treasury, so it passes the same kill switch
	// and value caps every treasury transfer does. It is not in-flight
	// conversion exposure, so it adds none.
	if err := r.treasury.CheckCaps(lane.ChainKey, usd, 0); err != nil {
		return "", false, err
	}
	return r.treasury.TransferToken(ctx, lane, token, to, amount)
}

func (r *TreasuryFeeRail) TransactionKnown(ctx context.Context, chain, txHash string) (bool, string, error) {
	reader, err := r.reader(chain)
	if err != nil {
		return false, "", err
	}
	tx, _, err := reader.TransactionByHash(ctx, common.HexToHash(txHash))
	switch {
	case errors.Is(err, ethereum.NotFound):
		return false, "", nil
	case err != nil:
		return false, "", err
	}
	// The signer is readable while the transaction is still pending — which is
	// what lets a hash named by someone other than its sender be told apart
	// before it mines.
	if tx == nil {
		return true, "", nil
	}
	sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return true, "", nil
	}
	return true, sender.Hex(), nil
}

func (r *TreasuryFeeRail) reader(chain string) (FeeTxReader, error) {
	reader := r.readers[strings.ToLower(strings.TrimSpace(chain))]
	if reader == nil {
		return nil, fmt.Errorf("no transaction reader for %s", chain)
	}
	return reader, nil
}
