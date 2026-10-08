package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// BlockchainRefundNetwork is PayRefundNetwork over the API's own chain clients:
// MainnetChainNameForID, MainnetChainIDs and TokenAddress come from the
// embedded registry as-is.
type BlockchainRefundNetwork struct{ *BlockchainService }

func NewBlockchainRefundNetwork(chains *BlockchainService) *BlockchainRefundNetwork {
	return &BlockchainRefundNetwork{chains}
}

func (n *BlockchainRefundNetwork) ChainName(chain string) string {
	if n.cfg != nil {
		if c, ok := n.cfg.Networks["mainnet"].Chains[chain]; ok && c.Name != "" {
			return c.Name
		}
	}
	return chain
}

func (n *BlockchainRefundNetwork) client(chain string) (*blockchain.EVMClient, error) {
	c := n.ClientForChain(chain)
	if c == nil || c.Client == nil {
		return nil, fmt.Errorf("no client for chain %q", chain)
	}
	return c, nil
}

// TokenDecimals is read on-chain (the client memoises it): never a table,
// since BSC's USDT and USDC have 18 where others have 6.
func (n *BlockchainRefundNetwork) TokenDecimals(chain, token string) (int, error) {
	c, err := n.client(chain)
	if err != nil {
		return 0, err
	}
	d, err := c.GetTokenDecimals(token)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s on %s reports %d decimals", token, chain, d)
	}
	return d, nil
}

func (n *BlockchainRefundNetwork) Broadcast(ctx context.Context, chain string, tx *types.Transaction) error {
	c, err := n.client(chain)
	if err != nil {
		return err
	}
	return c.Client.SendTransaction(ctx, tx)
}

func (n *BlockchainRefundNetwork) ConfirmedNonce(ctx context.Context, chain string, account common.Address, depth uint64) (uint64, error) {
	c, err := n.client(chain)
	if err != nil {
		return 0, err
	}
	head, err := c.Client.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	if depth < 1 {
		depth = 1
	}
	if head+1 < depth {
		return 0, nil
	}
	return c.Client.NonceAt(ctx, account, new(big.Int).SetUint64(head-depth+1))
}

// payRefundTestNairaPerUSD sizes a TEST payment's refund cap: no money moved,
// so nothing records what it was worth (stated in the merchant docs).
const payRefundTestNairaPerUSD = 1500

// payRefundSource is what a refund on one payment is sized and defaulted by:
// the cap (D16), the wallet that funded the payment (D19's fallback), and the
// chain and stablecoin the payer paid in (D21's default option).
type payRefundSource struct {
	capMicroUSD    int64
	fundingWallet  string
	depositChainID int64
	depositSymbol  string
}

// source sizes a refund on p by its route: test mode at the nominal rate, a
// crypto payment from its intent, a naira payment (direct or converted) from
// its Paycrest order.
func (s *PayRefundService) source(ctx context.Context, p *models.PayPayment) (*payRefundSource, error) {
	if p.Mode == models.PayModeTest {
		capMicro, err := payTestRefundCap(p.CustomerTotalKobo)
		if err != nil {
			return nil, err
		}
		return &payRefundSource{capMicroUSD: capMicro}, nil
	}
	if p.SettlementMode == models.PaySettlementCrypto {
		return s.cryptoSource(ctx, p)
	}
	return s.orderSource(ctx, p)
}

// orderSource: the cap is the payer's deposit into the order (the sender fee
// is inside it) less anything returned. On the converted route the order is
// the Base-USDC deposit the swap made, handed over to the payment.
func (s *PayRefundService) orderSource(ctx context.Context, p *models.PayPayment) (*payRefundSource, error) {
	var o models.PaycrestOrderRecord
	q := s.db.WithContext(ctx)
	var err error
	if p.PaycrestOrderID != nil {
		err = q.First(&o, "id = ?", *p.PaycrestOrderID).Error
	} else {
		err = q.Where("pay_payment_id = ? AND status = ?", p.ID, models.PaycrestStatusSettled).Order("created_at DESC").First(&o).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("[tsa-pay] refund: payment %s has no settled order to size a refund on", p.ID)
		return nil, ErrPayRefundNotRefundable
	}
	if err != nil {
		return nil, err
	}
	deposited, err := microUSDFloor(o.TokenAmount)
	if err != nil {
		log.Printf("[tsa-pay] refund: payment %s order %s token amount %q: %v", p.ID, o.ID, o.TokenAmount, err)
		return nil, ErrPayRefundNotRefundable
	}
	returned, err := microUSDFloor(o.AmountReturned)
	if err != nil {
		log.Printf("[tsa-pay] refund: payment %s order %s amount returned %q: %v", p.ID, o.ID, o.AmountReturned, err)
		return nil, ErrPayRefundNotRefundable
	}
	capMicro := deposited - returned
	if capMicro <= 0 {
		return nil, ErrPayRefundNotRefundable
	}
	return &payRefundSource{capMicroUSD: capMicro, fundingWallet: o.WalletAddress,
		depositChainID: s.mainnetChainID(o.ChainKey), depositSymbol: strings.ToUpper(o.Token)}, nil
}

// cryptoSource: only a payment settled in a listed stablecoin at its listed
// address can be refunded — an admin may settle a merchant in anything. The
// cap is the owed floor (DestAmount, not what the swap actually delivered)
// scaled to what the customer paid.
func (s *PayRefundService) cryptoSource(ctx context.Context, p *models.PayPayment) (*payRefundSource, error) {
	if s.network == nil {
		return nil, ErrPayRefundUnavailable
	}
	symbol, err := payListedStablecoin(s.network, p.SettleChainID, p.SettleTokenAddress)
	if err != nil || !strings.EqualFold(symbol, p.SettleTokenSymbol) {
		return nil, ErrPayRefundNotRefundable
	}
	chain, _ := s.network.MainnetChainNameForID(p.SettleChainID)
	decimals, err := s.network.TokenDecimals(chain, p.SettleTokenAddress)
	if err != nil {
		return nil, fmt.Errorf("%w: %s decimals on %s: %s", ErrPayRefundUnavailable, symbol, chain, RedactAlertText(err.Error()))
	}
	var in models.RampSellIntent
	q := s.db.WithContext(ctx)
	if p.SellIntentID != nil {
		err = q.First(&in, "id = ?", *p.SellIntentID).Error
	} else {
		err = q.Where("pay_payment_id = ?", p.ID).Order("created_at DESC").First(&in).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("[tsa-pay] refund: crypto payment %s has no intent to size a refund on", p.ID)
		return nil, ErrPayRefundNotRefundable
	}
	if err != nil {
		return nil, err
	}
	capMicro, err := payCryptoRefundCap(in.DestAmount, decimals, p.CustomerTotalKobo, p.MerchantNetKobo)
	if err != nil {
		return nil, err
	}
	src := &payRefundSource{capMicroUSD: capMicro, fundingWallet: in.WalletAddress, depositChainID: p.SettleChainID, depositSymbol: symbol}
	if paid, err := payListedStablecoin(s.network, in.SourceChainID, in.SourceTokenAddress); err == nil {
		src.depositChainID, src.depositSymbol = in.SourceChainID, paid
	}
	return src, nil
}

// mainnetChainID is the id of the mainnet chain keyed key, 0 when none.
func (s *PayRefundService) mainnetChainID(key string) int64 {
	if s.network == nil {
		return 0
	}
	for _, id := range s.network.MainnetChainIDs() {
		if name, ok := s.network.MainnetChainNameForID(id); ok && strings.EqualFold(name, key) {
			return id
		}
	}
	return 0
}

// payCryptoRefundCap is floor(destAmount / 10^decimals × 10^6 ×
// customerTotal / merchantNet), exactly.
func payCryptoRefundCap(destAmount string, decimals int, customerTotalKobo, merchantNetKobo int64) (int64, error) {
	dest, ok := new(big.Int).SetString(strings.TrimSpace(destAmount), 10)
	if !ok || dest.Sign() <= 0 || decimals < 0 || merchantNetKobo <= 0 || customerTotalKobo <= 0 {
		return 0, ErrPayRefundNotRefundable
	}
	num := new(big.Int).Mul(dest, big.NewInt(customerTotalKobo))
	num.Mul(num, big.NewInt(1_000_000))
	den := new(big.Int).Mul(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil), big.NewInt(merchantNetKobo))
	r := new(big.Rat).SetFrac(num, den)
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if !q.IsInt64() || q.Sign() <= 0 {
		return 0, ErrPayRefundNotRefundable
	}
	return q.Int64(), nil
}

// payTestRefundCap is floor(customerTotalKobo / 100 / payRefundTestNairaPerUSD
// × 10^6), exactly: a test payment of nothing has nothing to refund.
func payTestRefundCap(customerTotalKobo int64) (int64, error) {
	if customerTotalKobo <= 0 {
		return 0, ErrPayRefundNotRefundable
	}
	q := new(big.Int).Mul(big.NewInt(customerTotalKobo), big.NewInt(1_000_000))
	q.Quo(q, big.NewInt(100*payRefundTestNairaPerUSD))
	if !q.IsInt64() || q.Sign() <= 0 {
		return 0, ErrPayRefundNotRefundable
	}
	return q.Int64(), nil
}

// microUSDFloor reads a human stablecoin amount ("34.1234567") as micro-USD,
// rounded DOWN: a cap must never exceed what moved. "" is 0 (AmountReturned is
// usually unset on a settled order).
func microUSDFloor(amount string) (int64, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return 0, nil
	}
	// The whole string is held to DecimalToBaseUnits' shape before the fraction
	// is cut, so the cut can never make a malformed amount ("34.") readable.
	if !sponsorAmountShape.MatchString(amount) {
		return 0, fmt.Errorf("not a plain decimal: %q", amount)
	}
	whole, frac, _ := strings.Cut(amount, ".")
	if len(frac) > usdMicrosDecimals {
		frac = frac[:usdMicrosDecimals]
	}
	trimmed := whole
	if frac != "" {
		trimmed += "." + frac
	}
	v, err := DecimalToBaseUnits(trimmed, usdMicrosDecimals)
	if err != nil {
		return 0, err
	}
	// DecimalToBaseUnits already refuses a sign; this keeps it so if that ever
	// loosens. A negative AmountReturned would RAISE the cap.
	if v.Sign() < 0 {
		return 0, fmt.Errorf("amount %q is negative", amount)
	}
	if !v.IsInt64() {
		return 0, fmt.Errorf("amount %q is out of range", amount)
	}
	return v.Int64(), nil
}

// usdMicrosFloor is base units of a $1 stablecoin with decimals as micro-USD,
// rounded down — what a transfer credited, for the record.
func usdMicrosFloor(base *big.Int, decimals int) int64 {
	if decimals >= usdMicrosDecimals {
		return new(big.Int).Quo(base, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-usdMicrosDecimals)), nil)).Int64()
	}
	return new(big.Int).Mul(base, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(usdMicrosDecimals-decimals)), nil)).Int64()
}
