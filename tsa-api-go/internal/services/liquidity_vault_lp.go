package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The LP side of the shared pool (spec 2026-10-14 §5, §6): instant
// activation, the on-chain depositor allow-list, the LP's position and
// earnings, and the unsigned txs the app signs. The LP's money only ever
// moves by the LP's own signature, except claimFor, which pays the LP itself.

var (
	ErrLPNotEligible     = errors.New("liquidity: verify your identity (or activate TSA Pay) to provide liquidity")
	ErrLPPausedByAdmin   = errors.New("liquidity: your liquidity is paused — contact support")
	ErrLPNotAllowedYet   = errors.New("liquidity: your wallet is being enabled for deposits — try again in a minute")
	ErrLPAmount          = errors.New("liquidity: amount out of range")
	ErrLPClaimTooSmall   = errors.New("liquidity: earnings below the minimum claim")
	ErrLPClaimRateLimit  = errors.New("liquidity: a gas-free claim was made recently — try again later or claim from your wallet")
	ErrLPWalletMismatch  = errors.New("liquidity: that is not your liquidity wallet")
	ErrLiquidityNotVault = errors.New("liquidity: the shared pool is not configured")
)

// lpMinClaimUSD is the smallest gas-free claim (PM, 2026-10-14: $0.50).
const lpMinClaimUSD = 0.5

// lpClaimForEvery: one relayer-paid claim per LP per window (abuse guard).
const lpClaimForEvery = 8 * time.Hour

// VaultLPChain is what the LP side asks of the vault.
type VaultLPChain interface {
	Address() string
	State(ctx context.Context) (*VaultState, error)
	Position(ctx context.Context, lp string) (*VaultPosition, error)
	PrepareDeposit(lp string, amount *big.Int) ([]byte, error)
	PrepareWithdraw(lp string, amount *big.Int) ([]byte, error)
	PrepareClaim(lp string) ([]byte, error)
	PrepareClaimTo(lp, to string) ([]byte, error)
	PrepareClaimFor(from, lp string) ([]byte, error)
	PrepareSetDepositor(from, account string, allowed bool) ([]byte, error)
}

var _ VaultLPChain = (*LiquidityVault)(nil)

// TokenAllowances builds and reads the pool token's ERC-20 approvals
// (*blockchain.EVMClient is one).
type TokenAllowances interface {
	GetTokenAllowance(tokenAddress, owner, spender string) (*big.Int, error)
	PrepareERC20Approve(tokenAddress, owner, spender string, amount *big.Int) ([]byte, error)
}

type VaultLPConfig struct {
	DB        *gorm.DB
	PM        *PMService
	Vault     VaultLPChain
	Tokens    TokenAllowances
	Sender    ChainSender // the operator: allow-list writes and gas-free claims
	Token     PoolToken
	ChainName string
	Gate      PoolOpener
	// CompanyUser is the company payment merchant's user, never an LP (nil:
	// no company account configured).
	CompanyUser func(ctx context.Context) (uuid.UUID, error)
	Now         func() time.Time
}

type VaultLPService struct{ c VaultLPConfig }

func NewVaultLPService(c VaultLPConfig) (*VaultLPService, error) {
	if c.DB == nil || c.PM == nil || c.Vault == nil || c.Tokens == nil || c.Sender == nil || c.Token.Address == "" {
		return nil, errors.New("liquidity: vault LP service is missing a part")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &VaultLPService{c: c}, nil
}

// ─── Read model ─────────────────────────────────────────────────────────────

// LiquidityVaultMe is GET /api/liquidity/me in shared-pool mode. Token
// amounts are decimal strings in whole tokens.
type LiquidityVaultMe struct {
	Mode            string          `json:"mode"` // "vault"
	Status          string          `json:"status"`
	PoolOpen        bool            `json:"poolOpen"`
	Chain           *LiquidityChain `json:"chain,omitempty"`
	Token           *LiquidityToken `json:"token,omitempty"`
	Vault           string          `json:"vault"`
	Wallet          string          `json:"wallet,omitempty"`
	Allowed         bool            `json:"allowed"` // the wallet may deposit
	BalanceReadable bool            `json:"balanceReadable"`
	Principal       string          `json:"principal,omitempty"`
	Claimable       string          `json:"claimable,omitempty"`
	Earned          string          `json:"earned,omitempty"` // claimed + claimable, all time
	SharePercent    string          `json:"sharePercent,omitempty"`
	Limits          LiquidityLimits `json:"limits"`
	MinClaim        string          `json:"minClaim"`
	Paused          bool            `json:"paused"`
	Pool            *VaultPoolView  `json:"pool,omitempty"`
}

// VaultPoolView is the pool card every LP sees.
type VaultPoolView struct {
	TotalLiquidity string `json:"totalLiquidity"`
	Collateral     string `json:"collateral"`
	Outstanding    string `json:"outstanding"`
	CoveragePct    string `json:"coveragePercent"` // collateral ≥ outstanding always: "100"
	APR30d         string `json:"apr30d"`          // trailing 30-day spread ÷ liquidity, annualised, percent
}

func (s *VaultLPService) units(n *big.Int) string { return BaseUnitsToDecimal(n, s.c.Token.Decimals) }

func (s *VaultLPService) baseMe() *LiquidityVaultMe {
	minUSD, maxUSD := PoolLPLimitsUSD()
	return &LiquidityVaultMe{Mode: "vault", Status: LiquidityStatusNone, PoolOpen: s.c.Gate != nil && s.c.Gate.Open(),
		Chain:  &LiquidityChain{ID: s.c.Token.ChainID, Key: s.c.Token.ChainKey, Name: s.c.ChainName},
		Token:  &LiquidityToken{Symbol: s.c.Token.Symbol, Address: s.c.Token.Address, Decimals: s.c.Token.Decimals},
		Vault:  s.c.Vault.Address(),
		Limits: LiquidityLimits{Min: trimUSD(minUSD), Max: trimUSD(maxUSD)}, MinClaim: trimUSD(lpMinClaimUSD)}
}

func (s *VaultLPService) lpRow(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error) {
	return s.c.PM.GetByUserIDKind(ctx, userID, models.PaymentMerchantKindLP)
}

// Me is the caller's own position. No id is ever taken from the request.
func (s *VaultLPService) Me(ctx context.Context, userID uuid.UUID) (*LiquidityVaultMe, error) {
	out := s.baseMe()
	if pool, err := s.poolView(ctx); err == nil {
		out.Pool = pool
	} else {
		log.Printf("[liquidity] vault pool view unreadable: %s", RedactAlertText(err.Error()))
	}
	lp, err := s.lpRow(ctx, userID)
	switch {
	case errors.Is(err, ErrPMNotFound):
		return out, nil
	case err != nil:
		return nil, err
	}
	out.Wallet = lp.OperatorWalletAddress
	switch lp.Status {
	case models.PaymentMerchantStatusActive:
		out.Status = LiquidityStatusActive
	case models.PaymentMerchantStatusPaused:
		out.Status, out.Paused = LiquidityStatusPaused, true
	default:
		out.Status = LiquidityStatusNone // an old application: Activate is the way in now
	}
	if lp.OperatorWalletAddress == "" {
		return out, nil
	}
	pos, err := s.c.Vault.Position(ctx, lp.OperatorWalletAddress)
	if err != nil {
		log.Printf("[liquidity] LP %s vault position unreadable: %s", lp.ID, RedactAlertText(err.Error()))
		return out, nil
	}
	claimed, err := s.claimedTotal(ctx, lp.OperatorWalletAddress)
	if err != nil {
		return nil, err
	}
	out.BalanceReadable, out.Allowed = true, pos.Allowed
	out.Principal, out.Claimable = s.units(pos.Principal), s.units(pos.Claimable)
	out.Earned = s.units(new(big.Int).Add(claimed, pos.Claimable))
	if st, err := s.c.Vault.State(ctx); err == nil && st.TotalPrincipal.Sign() > 0 {
		out.SharePercent = ratioPercent(pos.Principal, st.TotalPrincipal)
	}
	return out, nil
}

// ratioPercent is a/b as a percentage with two decimals, floored.
func ratioPercent(a, b *big.Int) string {
	if b.Sign() == 0 {
		return "0"
	}
	bp := new(big.Int).Mul(a, big.NewInt(10_000))
	bp.Quo(bp, b)
	return strconv.FormatFloat(float64(bp.Int64())/100, 'f', 2, 64)
}

func (s *VaultLPService) poolView(ctx context.Context) (*VaultPoolView, error) {
	st, err := s.c.Vault.State(ctx)
	if err != nil {
		return nil, err
	}
	spread30, err := s.spreadSince(ctx, s.c.Now().Add(-30*24*time.Hour))
	if err != nil {
		return nil, err
	}
	apr := "0"
	if st.TotalPrincipal.Sign() > 0 {
		annual := new(big.Int).Mul(spread30, big.NewInt(365))
		annual.Quo(annual, big.NewInt(30))
		apr = ratioPercent(annual, st.TotalPrincipal)
	}
	return &VaultPoolView{TotalLiquidity: s.units(st.TotalPrincipal), Collateral: s.units(st.Collateral),
		Outstanding: s.units(st.Outstanding), CoveragePct: "100", APR30d: apr}, nil
}

func (s *VaultLPService) vaultScope(db *gorm.DB) *gorm.DB {
	return db.Where("chain_id = ? AND vault = ?", s.c.Token.ChainID, strings.ToLower(s.c.Vault.Address()))
}

func (s *VaultLPService) spreadSince(ctx context.Context, since time.Time) (*big.Int, error) {
	var rows []models.LiquidityLedgerEntry
	if err := s.vaultScope(s.c.DB.WithContext(ctx)).Where("kind = ? AND block_time >= ?", VaultEventFilled, since).
		Select("spread").Find(&rows).Error; err != nil {
		return nil, err
	}
	sum := new(big.Int)
	for _, r := range rows {
		sum.Add(sum, bigFromDecimal(r.Spread))
	}
	return sum, nil
}

func (s *VaultLPService) claimedTotal(ctx context.Context, wallet string) (*big.Int, error) {
	var rows []models.LiquidityLedgerEntry
	if err := s.vaultScope(s.c.DB.WithContext(ctx)).Where("kind = ? AND LOWER(account) = ?", VaultEventClaimed, strings.ToLower(wallet)).
		Select("amount").Find(&rows).Error; err != nil {
		return nil, err
	}
	sum := new(big.Int)
	for _, r := range rows {
		sum.Add(sum, bigFromDecimal(r.Amount))
	}
	return sum, nil
}

// ─── Activity ───────────────────────────────────────────────────────────────

// LiquidityVaultActivity is one line of an LP's history. Kind: deposit,
// withdrawal, claim, earned (its share of one buyer's spread). Never names a
// buyer or an order.
type LiquidityVaultActivity struct {
	Kind   string    `json:"kind"`
	Amount string    `json:"amount"`
	To     string    `json:"to,omitempty"` // a claim's destination
	TxHash string    `json:"txHash"`
	At     time.Time `json:"at"`
}

type LiquidityVaultActivityPage struct {
	Items      []LiquidityVaultActivity `json:"items"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

// Activity replays the ledger in chain order, as the contract accounts it:
// each fill's spread split by principal at that moment (floored), so the LP
// sees what every buy earned them. Newest first; cursor is an offset.
func (s *VaultLPService) Activity(ctx context.Context, userID uuid.UUID, cursor string, limit int) (*LiquidityVaultActivityPage, error) {
	lp, err := s.lpRow(ctx, userID)
	if errors.Is(err, ErrPMNotFound) {
		return nil, ErrNotLiquidityProvider
	}
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	offset := 0
	if cursor != "" {
		if offset, err = strconv.Atoi(cursor); err != nil || offset < 0 {
			return nil, ErrLiquidityActivityCursor
		}
	}
	var rows []models.LiquidityLedgerEntry
	if err := s.vaultScope(s.c.DB.WithContext(ctx)).
		Where("kind IN ?", []string{VaultEventDeposited, VaultEventWithdrawn, VaultEventClaimed, VaultEventFilled}).
		Order("block ASC").Order("log_index ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	me := strings.ToLower(lp.OperatorWalletAddress)
	principal := map[string]*big.Int{}
	total := new(big.Int)
	var items []LiquidityVaultActivity
	for _, r := range rows {
		who := strings.ToLower(r.Account)
		amt := bigFromDecimal(r.Amount)
		if principal[who] == nil {
			principal[who] = new(big.Int)
		}
		switch r.Kind {
		case VaultEventDeposited:
			principal[who].Add(principal[who], amt)
			total.Add(total, amt)
			if who == me {
				items = append(items, LiquidityVaultActivity{Kind: "deposit", Amount: s.units(amt), TxHash: r.TxHash, At: r.BlockTime})
			}
		case VaultEventWithdrawn:
			principal[who].Sub(principal[who], amt)
			total.Sub(total, amt)
			if who == me {
				items = append(items, LiquidityVaultActivity{Kind: "withdrawal", Amount: s.units(amt), TxHash: r.TxHash, At: r.BlockTime})
			}
		case VaultEventClaimed:
			if who == me {
				items = append(items, LiquidityVaultActivity{Kind: "claim", Amount: s.units(amt), To: r.To, TxHash: r.TxHash, At: r.BlockTime})
			}
		case VaultEventFilled:
			mine := principal[me]
			spread := bigFromDecimal(r.Spread)
			if mine == nil || mine.Sign() == 0 || total.Sign() == 0 || spread.Sign() == 0 {
				continue
			}
			share := new(big.Int).Mul(spread, mine)
			share.Quo(share, total)
			if share.Sign() > 0 {
				items = append(items, LiquidityVaultActivity{Kind: "earned", Amount: s.units(share), TxHash: r.TxHash, At: r.BlockTime})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	page := &LiquidityVaultActivityPage{Items: []LiquidityVaultActivity{}}
	if offset < len(items) {
		end := min(offset+limit, len(items))
		page.Items = items[offset:end]
		if end < len(items) {
			page.NextCursor = strconv.Itoa(end)
		}
	}
	return page, nil
}

// ─── Activation and the allow-list ──────────────────────────────────────────

// Activate makes the caller an LP at once (spec §5): an active TSA Pay
// merchant, or a KYC-verified user — no application, no admin approval. The
// wallet is the caller's proven wallet (the handler checks the proof). The
// on-chain allow-list follows (allowDepositor; the sync job retries it).
func (s *VaultLPService) Activate(ctx context.Context, userID uuid.UUID, wallet string) (*LiquidityVaultMe, error) {
	if !common.IsHexAddress(wallet) {
		return nil, ErrLPWalletMismatch
	}
	wallet = common.HexToAddress(wallet).Hex()
	if err := s.eligible(ctx, userID); err != nil {
		return nil, err
	}
	lp, err := s.lpRow(ctx, userID)
	switch {
	case errors.Is(err, ErrPMNotFound):
		lp, err = s.createLP(ctx, userID, wallet)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case lp.Status == models.PaymentMerchantStatusPaused:
		return nil, ErrLPPausedByAdmin
	default:
		chain := s.c.Token.ChainID
		if err := s.c.DB.WithContext(ctx).Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).
			Updates(map[string]any{"status": models.PaymentMerchantStatusActive, "operator_wallet_address": wallet,
				"pool_chain_id": chain, "review_note": "activated (shared pool, spec 2026-10-14 §5)"}).Error; err != nil {
			return nil, err
		}
		lp.Status, lp.OperatorWalletAddress = models.PaymentMerchantStatusActive, wallet
	}
	if err := s.allowDepositor(ctx, lp, true); err != nil {
		log.Printf("[liquidity] allow-list lp=%s wallet=%s deferred to the sync job: %s", lp.ID, wallet, RedactAlertText(err.Error()))
	}
	return s.Me(ctx, userID)
}

func (s *VaultLPService) eligible(ctx context.Context, userID uuid.UUID) error {
	if s.c.CompanyUser != nil {
		if company, err := s.c.CompanyUser(ctx); err == nil && company == userID {
			return ErrCompanyUserCannotBeLP
		}
	}
	var pay int64
	if err := s.c.DB.WithContext(ctx).Model(&models.PayMerchant{}).
		Where("user_id = ? AND status = ?", userID, models.PayMerchantActive).Count(&pay).Error; err != nil {
		return err
	}
	if pay > 0 {
		return nil
	}
	if err := requireKYCVerified(ctx, s.c.DB, userID); err != nil {
		if errors.Is(err, ErrKYCVerificationRequired) {
			return ErrLPNotEligible
		}
		return err
	}
	return nil
}

func (s *VaultLPService) createLP(ctx context.Context, userID uuid.UUID, wallet string) (*models.PaymentMerchant, error) {
	var user models.User
	if err := s.c.DB.WithContext(ctx).Select("id", "name").First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	chain := s.c.Token.ChainID
	now := s.c.Now()
	lp := &models.PaymentMerchant{ID: uuid.New(), UserID: userID, Name: firstNonEmpty(strings.TrimSpace(user.Name), "Liquidity provider"),
		Status: models.PaymentMerchantStatusActive, Kind: models.PaymentMerchantKindLP, PoolChainID: &chain,
		OperatorWalletAddress: wallet, ProvisioningStatus: models.PMProvisioningActive,
		ReviewedAt: &now, ReviewNote: "activated (shared pool, spec 2026-10-14 §5)"}
	if err := s.c.DB.WithContext(ctx).Create(lp).Error; err != nil {
		if isUniqueViolation(err) { // a concurrent activation won: use its row
			return s.lpRow(ctx, userID)
		}
		return nil, err
	}
	return lp, nil
}

// allowDepositor writes the allow-list for lp's wallet when the vault does not
// already say so. Idempotent across retries and processes: the operation key
// names the LP, the wallet and the target state, so the key lane answers a
// repeat from the call already committed.
func (s *VaultLPService) allowDepositor(ctx context.Context, lp *models.PaymentMerchant, allowed bool) error {
	wallet := lp.OperatorWalletAddress
	pos, err := s.c.Vault.Position(ctx, wallet)
	if err != nil {
		return err
	}
	if pos.Allowed == allowed {
		return nil
	}
	op := fmt.Sprintf("pool_depositor:%s:%s:%t", lp.ID, strings.ToLower(wallet), allowed)
	hash, err := s.c.Sender.Send(WithOperationRef(ctx, op), s.c.Token.ChainID, func() ([]byte, error) {
		return s.c.Vault.PrepareSetDepositor(s.c.Sender.Address(), wallet, allowed)
	})
	if err != nil && !errors.Is(err, ErrPendingBroadcast) {
		return err
	}
	log.Printf("[liquidity] METRIC liquidity_depositor_set lp=%s wallet=%s allowed=%t tx=%s", lp.ID, wallet, allowed, hash)
	return nil
}

// SyncDepositors makes the vault's allow-list match the LP rows: active LPs
// allowed, paused or rejected ones not (withdraw and claim never need it).
func (s *VaultLPService) SyncDepositors(ctx context.Context) (int, error) {
	var rows []models.PaymentMerchant
	if err := s.c.DB.WithContext(ctx).Where("kind = ? AND pool_chain_id = ? AND operator_wallet_address <> ''",
		models.PaymentMerchantKindLP, s.c.Token.ChainID).Find(&rows).Error; err != nil {
		return 0, err
	}
	changed := 0
	var firstErr error
	for i := range rows {
		want := rows[i].Status == models.PaymentMerchantStatusActive
		if err := s.allowDepositor(ctx, &rows[i], want); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[liquidity] depositor sync lp=%s: %s", rows[i].ID, RedactAlertText(err.Error()))
			continue
		}
		changed++
	}
	return changed, firstErr
}

// ─── Unsigned txs for the LP ────────────────────────────────────────────────

// LPTxPlan is what the app signs next, in order. Step "approve" means the
// deposit itself is prepared once the approval has mined.
type LPTxPlan struct {
	Step string            `json:"step"`
	Txs  []json.RawMessage `json:"txs"`
}

func (s *VaultLPService) activeLP(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error) {
	lp, err := s.lpRow(ctx, userID)
	if errors.Is(err, ErrPMNotFound) {
		return nil, ErrNotLiquidityProvider
	}
	if err != nil {
		return nil, err
	}
	if lp.OperatorWalletAddress == "" {
		return nil, ErrNotLiquidityProvider
	}
	return lp, nil
}

func (s *VaultLPService) parseAmount(amount string) (*big.Int, error) {
	n, err := DecimalToBaseUnits(strings.TrimSpace(amount), s.c.Token.Decimals)
	if err != nil || n.Sign() <= 0 {
		return nil, fmt.Errorf("%w: %q", ErrLPAmount, amount)
	}
	return n, nil
}

// PrepareDeposit: within the LP limits and allowed on-chain; an approval
// first when the allowance is short.
func (s *VaultLPService) PrepareDeposit(ctx context.Context, userID uuid.UUID, amount string) (*LPTxPlan, error) {
	lp, err := s.activeLP(ctx, userID)
	if err != nil {
		return nil, err
	}
	if lp.Status != models.PaymentMerchantStatusActive {
		return nil, ErrLPPausedByAdmin
	}
	n, err := s.parseAmount(amount)
	if err != nil {
		return nil, err
	}
	pos, err := s.c.Vault.Position(ctx, lp.OperatorWalletAddress)
	if err != nil {
		return nil, err
	}
	if !pos.Allowed {
		_ = s.allowDepositor(ctx, lp, true)
		return nil, ErrLPNotAllowedYet
	}
	minU, maxU, err := PoolLPLimitUnits(s.c.Token.Decimals)
	if err != nil {
		return nil, err
	}
	if n.Cmp(minU) < 0 || new(big.Int).Add(pos.Principal, n).Cmp(maxU) > 0 {
		return nil, fmt.Errorf("%w: deposit between %s and a total of %s %s", ErrLPAmount, s.units(minU), s.units(maxU), s.c.Token.Symbol)
	}
	allowance, err := s.c.Tokens.GetTokenAllowance(s.c.Token.Address, lp.OperatorWalletAddress, s.c.Vault.Address())
	if err != nil {
		return nil, err
	}
	if allowance.Cmp(n) < 0 {
		tx, err := s.c.Tokens.PrepareERC20Approve(s.c.Token.Address, lp.OperatorWalletAddress, s.c.Vault.Address(), n)
		if err != nil {
			return nil, err
		}
		return &LPTxPlan{Step: "approve", Txs: []json.RawMessage{tx}}, nil
	}
	tx, err := s.c.Vault.PrepareDeposit(lp.OperatorWalletAddress, n)
	if err != nil {
		return nil, err
	}
	return &LPTxPlan{Step: "deposit", Txs: []json.RawMessage{tx}}, nil
}

// PrepareWithdraw is never refused for a pause: the custody promise.
func (s *VaultLPService) PrepareWithdraw(ctx context.Context, userID uuid.UUID, amount string) (*LPTxPlan, error) {
	lp, err := s.activeLP(ctx, userID)
	if err != nil {
		return nil, err
	}
	n, err := s.parseAmount(amount)
	if err != nil {
		return nil, err
	}
	tx, err := s.c.Vault.PrepareWithdraw(lp.OperatorWalletAddress, n)
	if err != nil {
		return nil, err
	}
	return &LPTxPlan{Step: "withdraw", Txs: []json.RawMessage{tx}}, nil
}

// PrepareClaim: to the LP's wallet, or (to set) straight to an off-ramp
// address such as a sell intake, in one tx the LP signs.
func (s *VaultLPService) PrepareClaim(ctx context.Context, userID uuid.UUID, to string) (*LPTxPlan, error) {
	lp, err := s.activeLP(ctx, userID)
	if err != nil {
		return nil, err
	}
	var tx []byte
	if strings.TrimSpace(to) == "" {
		tx, err = s.c.Vault.PrepareClaim(lp.OperatorWalletAddress)
	} else {
		tx, err = s.c.Vault.PrepareClaimTo(lp.OperatorWalletAddress, strings.TrimSpace(to))
	}
	if err != nil {
		return nil, err
	}
	return &LPTxPlan{Step: "claim", Txs: []json.RawMessage{tx}}, nil
}

// ClaimForMe sends claimFor(lp) from the operator — gas-free for the LP,
// paid only to the LP's own wallet (the contract's rule) — once per
// lpClaimForEvery and only from lpMinClaimUSD.
func (s *VaultLPService) ClaimForMe(ctx context.Context, userID uuid.UUID) (string, error) {
	lp, err := s.activeLP(ctx, userID)
	if err != nil {
		return "", err
	}
	pos, err := s.c.Vault.Position(ctx, lp.OperatorWalletAddress)
	if err != nil {
		return "", err
	}
	floor, err := usdToChainUnits(lpMinClaimUSD, s.c.Token.Decimals)
	if err != nil {
		return "", err
	}
	if pos.Claimable.Cmp(floor) < 0 {
		return "", fmt.Errorf("%w (%s %s)", ErrLPClaimTooSmall, trimUSD(lpMinClaimUSD), s.c.Token.Symbol)
	}
	now := s.c.Now()
	marker := "pool_claim_for:" + lp.ID.String()
	if !claimRunAt(ctx, s.c.DB, marker, lpClaimForEvery, now) {
		return "", ErrLPClaimRateLimit
	}
	op := fmt.Sprintf("%s:%d", marker, now.Unix()/int64(lpClaimForEvery.Seconds()))
	hash, err := s.c.Sender.Send(WithOperationRef(ctx, op), s.c.Token.ChainID, func() ([]byte, error) {
		return s.c.Vault.PrepareClaimFor(s.c.Sender.Address(), lp.OperatorWalletAddress)
	})
	if errors.Is(err, ErrTxNotBroadcast) { // nothing sent: give the window back
		s.c.DB.WithContext(context.WithoutCancel(ctx)).Model(&models.JobRunMarker{}).
			Where("name = ? AND last_run_at = ?", marker, now).Update("last_run_at", now.Add(-lpClaimForEvery))
		return "", err
	}
	if err != nil && !errors.Is(err, ErrPendingBroadcast) {
		return "", err
	}
	log.Printf("[liquidity] METRIC liquidity_claim_for lp=%s amount=%s tx=%s", lp.ID, pos.Claimable, hash)
	return hash, nil
}
