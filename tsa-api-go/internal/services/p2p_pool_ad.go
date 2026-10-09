package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// LP stock-ad voucher terms (spec S28).
const (
	poolVoucherTTL         = 90 * 24 * time.Hour
	poolVoucherRenewWindow = 14 * 24 * time.Hour
	poolVoucherMaxMultiple = 100 // maxAmount = 100 × pool_lp_max
)

// ErrPoolStockAdExists: the LP already has a live stock ad that is not due for
// renewal (one per LP, chain and token; spec §8.4).
var ErrPoolStockAdExists = errors.New("p2p: you already have a live stock ad; it can be renewed when it is due")

// IsPoolChain reports whether chainID is the TSA Liquidity pool chain.
func (s *P2PTradeService) IsPoolChain(chainID int64) bool {
	return chainID != 0 && s.escrows != nil && s.escrows.Purpose(chainID) == config.P2PPurposePool
}

// poolVoucherRenewDue (S28, M12): 14 days or less to the voucher deadline, or
// remaining below 2 × the voucher's OWN signed maxAmount ÷ 100 — never today's
// pool_lp_max, so an admin change cannot stretch or shrink an old voucher.
func poolVoucherRenewDue(ad *models.P2PAd, now time.Time) bool {
	if ad.VoucherDeadline == nil || ad.VoucherDeadline.Sub(now) <= poolVoucherRenewWindow {
		return true
	}
	max, ok1 := new(big.Int).SetString(ad.Amount, 10)
	rem, ok2 := new(big.Int).SetString(ad.Remaining, 10)
	if !ok1 || !ok2 {
		return true
	}
	floor := new(big.Int).Div(new(big.Int).Mul(max, big.NewInt(2)), big.NewInt(poolVoucherMaxMultiple))
	return rem.Cmp(floor) < 0
}

var liveStockAdStatuses = []string{models.P2PAdStatusPending, models.P2PAdStatusActive, models.P2PAdStatusPaused}

// liveStockAds are the LP's pending, active and paused stock ads on one chain
// and token, newest first.
func (s *P2PTradeService) liveStockAds(ctx context.Context, lpID uuid.UUID, chainID int64, token string) ([]models.P2PAd, error) {
	var ads []models.P2PAd
	err := s.db.WithContext(ctx).
		Where("pm_id = ? AND chain_id = ? AND token = ? AND side = ? AND status IN ?",
			lpID, chainID, strings.ToUpper(token), models.P2PAdSideSell, liveStockAdStatuses).
		Order("created_at DESC").Find(&ads).Error
	return ads, err
}

// LatestStockAd is the LP's newest active or paused stock ad; (nil, nil) if none.
func (s *P2PTradeService) LatestStockAd(ctx context.Context, lp *models.PaymentMerchant, chainID int64, token string) (*models.P2PAd, error) {
	ads, err := s.liveStockAds(ctx, lp.ID, chainID, token)
	if err != nil {
		return nil, err
	}
	for i := range ads {
		if ads[i].Status != models.P2PAdStatusPending {
			return &ads[i], nil
		}
	}
	return nil, nil
}

// endedTradeStatuses are the trade states that no longer hold stock on an ad.
var endedTradeStatuses = []string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}

// liveTradeCount counts the ad's trades that have not ended (a Plan C1 slice is
// such a trade).
func (s *P2PTradeService) liveTradeCount(ctx context.Context, adID uuid.UUID) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("ad_id = ? AND status NOT IN ?", adID, endedTradeStatuses).
		Count(&n).Error
	return n, err
}

// closeStockAd retires a stock ad off-chain (status cancelled, audit ad.closed)
// and reports whether it did. Its voucher simply expires on-chain (spec §8.4
// step 6); the allocator never picks a closed ad. The update re-checks for a
// live trade itself, so a trade opened after a caller's liveTradeCount still
// keeps the ad live.
func (s *P2PTradeService) closeStockAd(ctx context.Context, ad *models.P2PAd, reason string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND status IN ?", ad.ID, liveStockAdStatuses).
		Where("NOT EXISTS (SELECT 1 FROM p2p_trades t WHERE t.ad_id = p2p_ads.id AND t.status NOT IN ?)", endedTradeStatuses).
		Updates(map[string]any{"status": models.P2PAdStatusCancelled, "updated_at": s.clock.Now()})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	s.audit(ctx, models.P2PAuditAdClosed, nil, &ad.ID, nil, map[string]any{"reason": reason, "pool": true})
	return true, nil
}

// createPoolAd is CreateAd's LP branch (spec §8.4, plan B P8): a sell ad on the
// pool chain whose voucher covers 100 × pool_lp_max for 90 days. No rate, no
// payment methods, no rate-feed check: the pool prices orders (Plan C1), the
// ad's Rate is a display cache that is never a price source (§9.4).
func (s *P2PTradeService) createPoolAd(ctx context.Context, lp *models.PaymentMerchant, sellerID uuid.UUID, sellerAddress string, params CreateAdParams) (*CreateAdResult, error) {
	if strings.EqualFold(strings.TrimSpace(params.Side), models.P2PAdSideBuy) {
		return nil, fmt.Errorf("%w: a liquidity provider publishes only its stock (sell) ad", ErrInvalidParams)
	}
	if !common.IsHexAddress(sellerAddress) {
		return nil, fmt.Errorf("%w: seller address", ErrInvalidParams)
	}
	chainID := params.ChainID
	token := strings.ToUpper(params.Token)
	tokenAddr, err := s.resolveTokenAddress(chainID, token)
	if err != nil {
		return nil, err
	}
	escrow, domainChainID, version, err := s.resolve(chainID)
	if err != nil {
		return nil, err
	}
	_, maxUnits, err := PoolLPLimitUnits(s.TokenDecimals(chainID, token))
	if err != nil {
		return nil, err
	}
	voucherMax := new(big.Int).Mul(maxUnits, big.NewInt(poolVoucherMaxMultiple))
	if voucherMax.Cmp(uint96Max) > 0 {
		return nil, ErrAmountOverflow
	}
	now := s.clock.Now()
	live, err := s.liveStockAds(ctx, lp.ID, chainID, token)
	if err != nil {
		return nil, err
	}
	// One live stock ad: an active one that is not yet due refuses the create.
	// A paused one never blocks (review I3): it is closed when the new one is
	// signed (closeSupersededStockAds). Checked before any draft is touched, so
	// a refused create changes nothing.
	for i := range live {
		if live[i].Status == models.P2PAdStatusActive && !poolVoucherRenewDue(&live[i], now) {
			return nil, ErrPoolStockAdExists
		}
	}

	nonce := HashID(uuid.New())
	deadline := now.Add(poolVoucherTTL)
	voucher := AdVoucher{
		Seller: common.HexToAddress(sellerAddress), Token: common.HexToAddress(tokenAddr),
		MaxAmount: voucherMax, MinPerTrade: big.NewInt(0), MaxPerTrade: big.NewInt(0),
		Deadline: big.NewInt(deadline.Unix()), Nonce: nonce,
	}
	hash, err := escrow.HashVoucher(voucher)
	if err != nil {
		return nil, fmt.Errorf("hash voucher: %w", err)
	}
	ad := &models.P2PAd{
		ID: uuid.New(), SellerID: sellerID, Side: models.P2PAdSideSell, Token: token, ChainID: chainID,
		Amount: voucherMax.String(), Remaining: voucherMax.String(), Reserved: "0",
		FiatCurrency: "NGN", Rate: "0", RateType: models.P2PAdRateTypeFixed, FloatAnchor: models.P2PAdFloatAnchorBybitSell,
		PaymentMethodIDs: stringifyUUIDs(nil), Status: models.P2PAdStatusPending,
		SellerAddress: common.HexToAddress(sellerAddress).Hex(),
		VoucherNonce:  "0x" + common.Bytes2Hex(nonce[:]), VoucherDeadline: &deadline,
		VoucherHash: "0x" + common.Bytes2Hex(hash[:]),
	}
	// The LP's registered wallet only, and pm_id = the LP row (spec §7.3).
	if err := s.applyPMAdTag(lp, sellerAddress, ad); err != nil {
		return nil, err
	}
	// A never-signed draft is replaced, so at most one draft waits to be signed.
	for i := range live {
		if live[i].Status == models.P2PAdStatusPending {
			if _, err := s.closeStockAd(ctx, &live[i], "draft_replaced"); err != nil {
				return nil, err
			}
		}
	}
	if err := s.db.WithContext(ctx).Create(ad).Error; err != nil {
		return nil, fmt.Errorf("persist ad: %w", err)
	}
	s.audit(ctx, models.P2PAuditAdCreated, &sellerID, &ad.ID, nil, map[string]any{
		"token": ad.Token, "amount": ad.Amount, "voucherHash": ad.VoucherHash, "pool": true, "chainId": chainID,
	})
	s.emit(events.P2PAdCreated, sellerID, "Liquidity stock ad created", "", map[string]any{"adId": ad.ID, "status": ad.Status})
	return &CreateAdResult{Ad: ad, TypedData: s.buildVoucherTypedData(voucher, escrow.ContractAddress(), domainChainID, version)}, nil
}

// closeSupersededStockAds runs when a stock ad is signed: every OLDER live
// stock ad of the LP on the same chain and token with no live trade is closed,
// so the LP has one signed voucher in use. One still carrying a trade stays
// until that trade ends. A newer one is never touched (review M3): with two
// drafts signed at once, the newer survives whichever order the signs land in,
// instead of each closing the other.
func (s *P2PTradeService) closeSupersededStockAds(ctx context.Context, fresh *models.P2PAd) {
	if fresh.PMID == nil {
		return
	}
	ads, err := s.liveStockAds(ctx, *fresh.PMID, fresh.ChainID, fresh.Token)
	if err != nil {
		log.Printf("[p2p pool] superseded stock ads of %s not read: %v", fresh.ID, err)
		return
	}
	for i := range ads {
		if !stockAdBefore(&ads[i], fresh) {
			continue
		}
		n, err := s.liveTradeCount(ctx, ads[i].ID)
		if err != nil {
			log.Printf("[p2p pool] superseded stock ad %s kept, trade count failed: %v", ads[i].ID, err)
			continue
		}
		if n > 0 {
			continue
		}
		if _, err := s.closeStockAd(ctx, &ads[i], "superseded"); err != nil {
			log.Printf("[p2p pool] superseded stock ad %s not closed: %v", ads[i].ID, err)
		}
	}
}

// stockAdBefore orders stock ads by (created_at, id), so a tie still has one
// newest.
func stockAdBefore(a, b *models.P2PAd) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID.String() < b.ID.String()
}
