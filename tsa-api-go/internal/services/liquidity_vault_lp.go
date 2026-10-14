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
	ErrLPWalletMismatch  = errors.New("liquidity: your account wallet is not your liquidity wallet — activate again from this wallet")
	ErrLPWalletInUse     = errors.New("liquidity: that wallet already provides liquidity for another account")
	ErrLiquidityNotVault = errors.New("liquidity: the shared pool is not configured")
	// ErrLPOldWalletHasFunds: re-activating from a new wallet while the old
	// one still holds principal or earnings would strand them in the app.
	ErrLPOldWalletHasFunds = errors.New("liquidity: withdraw and claim from your old wallet first")
	ErrLPClaimTo           = errors.New("liquidity: the claim destination is not a valid address")
	ErrLPNothingToClaim    = errors.New("liquidity: you have no earnings to claim yet")
)

// lpRevertError turns the vault's own refusal of an LP tx at estimate (its
// revert reason) into the LP's error: a withdraw over her principal is
// ErrLPAmount, a claim with no earnings ErrLPNothingToClaim. Anything else —
// an RPC failure included — is returned as is.
func lpRevertError(err error) error {
	switch revertReasonOf(err) {
	case "Bad amount":
		return fmt.Errorf("%w: more than your deposited principal", ErrLPAmount)
	case "Nothing to claim":
		return ErrLPNothingToClaim
	}
	return err
}

// lpPoolViewTTL: the pool card is one read for every caller this often.
const lpPoolViewTTL = 5 * time.Second

const (
	LiquidityStatusNone   = "none"
	LiquidityStatusActive = "active"
	LiquidityStatusPaused = "paused"
)

var (
	ErrNotLiquidityProvider    = errors.New("liquidity: not a liquidity provider")
	ErrLiquidityActivityCursor = errors.New("liquidity: invalid activity cursor")
)

type LiquidityChain struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type LiquidityToken struct {
	Symbol   string `json:"symbol"`
	Address  string `json:"address"`
	Decimals int    `json:"decimals"`
}

// LiquidityLimits are USD at the 1 USDT = $1 peg.
type LiquidityLimits struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

func trimUSD(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

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

type VaultLPService struct {
	c    VaultLPConfig
	pool *cachedReader[*vaultPoolSnapshot]
}

// vaultPoolSnapshot is one reading of the vault's state and the pool card.
type vaultPoolSnapshot struct {
	state *VaultState
	view  *VaultPoolView
}

func NewVaultLPService(c VaultLPConfig) (*VaultLPService, error) {
	if c.DB == nil || c.PM == nil || c.Vault == nil || c.Tokens == nil || c.Sender == nil || c.Token.Address == "" {
		return nil, errors.New("liquidity: vault LP service is missing a part")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &VaultLPService{c: c, pool: newCachedReader[*vaultPoolSnapshot](lpPoolViewTTL, lpPoolViewTTL/2)}, nil
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
	// WalletChanged: the account's wallet is no longer the LP wallet. Deposits
	// and gas-free claims wait for Activate from the current wallet;
	// withdrawing and claiming (signed by the LP wallet itself) never do.
	WalletChanged bool           `json:"walletChanged"`
	Pool          *VaultPoolView `json:"pool,omitempty"`
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

// isVaultLP: the row was activated on the shared vault from a proven wallet.
// An old slice-pool row (wallet typed in, never proven) is not, until it
// activates again.
func isVaultLP(lp *models.PaymentMerchant) bool { return lp != nil && lp.VaultActivatedAt != nil }

// vaultLPRow is the caller's LP row when it is a vault LP; ErrPMNotFound
// otherwise.
func (s *VaultLPService) vaultLPRow(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error) {
	lp, err := s.lpRow(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !isVaultLP(lp) {
		return nil, ErrPMNotFound
	}
	return lp, nil
}

// Me is the caller's own position. No id is ever taken from the request.
func (s *VaultLPService) Me(ctx context.Context, userID uuid.UUID) (*LiquidityVaultMe, error) {
	out := s.baseMe()
	snap, err := s.poolSnapshot(ctx)
	if err == nil {
		out.Pool = snap.view
	} else {
		log.Printf("[liquidity] vault pool view unreadable: %s", RedactAlertText(err.Error()))
	}
	lp, err := s.vaultLPRow(ctx, userID)
	switch {
	case errors.Is(err, ErrPMNotFound):
		return out, nil // no row, or an old unproven one: Activate is the way in
	case err != nil:
		return nil, err
	}
	out.Wallet = lp.OperatorWalletAddress
	if current, err := s.accountWallet(ctx, userID); err == nil && !SameEVMAddress(current, lp.OperatorWalletAddress) {
		out.WalletChanged = true
	}
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
	if snap != nil && snap.state.TotalPrincipal.Sign() > 0 {
		out.SharePercent = ratioPercent(pos.Principal, snap.state.TotalPrincipal)
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

// poolSnapshot is the vault state and pool card, read at most once per
// lpPoolViewTTL for every caller: Me is open to any signed-in user, and each
// read is an RPC plus a 30-day ledger scan.
func (s *VaultLPService) poolSnapshot(ctx context.Context) (*vaultPoolSnapshot, error) {
	const key = "pool"
	return s.pool.Get(key, s.c.Now(), func() (*vaultPoolSnapshot, error) {
		// Shared by every waiting caller: one caller's cancel must not fail them all.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		snap, err := s.readPoolSnapshot(rctx)
		s.pool.Put(key, s.c.Now(), snap, err)
		return snap, err
	})
}

func (s *VaultLPService) readPoolSnapshot(ctx context.Context) (*vaultPoolSnapshot, error) {
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
	return &vaultPoolSnapshot{state: st, view: &VaultPoolView{TotalLiquidity: s.units(st.TotalPrincipal), Collateral: s.units(st.Collateral),
		Outstanding: s.units(st.Outstanding), CoveragePct: "100", APR30d: apr}}, nil
}

func (s *VaultLPService) vaultScope(db *gorm.DB) *gorm.DB {
	return db.Where("chain_id = ? AND vault = ?", s.c.Token.ChainID, strings.ToLower(s.c.Vault.Address()))
}

func (s *VaultLPService) spreadSince(ctx context.Context, since time.Time) (*big.Int, error) {
	return ledgerSpreadSince(ctx, s.c.DB, s.c.Token.ChainID, s.c.Vault.Address(), since)
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
//
// The replay is bounded: nothing before the LP's first deposit can earn it
// anything, so only the pool's total principal at that point is needed (summed
// in the database), then the rows from that deposit on.
func (s *VaultLPService) Activity(ctx context.Context, userID uuid.UUID, cursor string, limit int) (*LiquidityVaultActivityPage, error) {
	lp, err := s.vaultLPRow(ctx, userID)
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
	rows, total, err := s.activityRows(ctx, lp.OperatorWalletAddress)
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(lp.OperatorWalletAddress)
	mine := new(big.Int)
	var items []LiquidityVaultActivity
	for _, r := range rows {
		who := strings.ToLower(r.Account)
		amt := bigFromDecimal(r.Amount)
		switch r.Kind {
		case VaultEventDeposited:
			total.Add(total, amt)
			if who == me {
				mine.Add(mine, amt)
				items = append(items, LiquidityVaultActivity{Kind: "deposit", Amount: s.units(amt), TxHash: r.TxHash, At: r.BlockTime})
			}
		case VaultEventWithdrawn:
			total.Sub(total, amt)
			if who == me {
				mine.Sub(mine, amt)
				items = append(items, LiquidityVaultActivity{Kind: "withdrawal", Amount: s.units(amt), TxHash: r.TxHash, At: r.BlockTime})
			}
		case VaultEventClaimed:
			if who == me {
				items = append(items, LiquidityVaultActivity{Kind: "claim", Amount: s.units(amt), To: r.To, TxHash: r.TxHash, At: r.BlockTime})
			}
		case VaultEventFilled:
			spread := bigFromDecimal(r.Spread)
			if mine.Sign() == 0 || total.Sign() == 0 || spread.Sign() == 0 {
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

func lpDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// activityRows is what the replay needs for wallet, in chain order: the
// wallet's claims, and every deposit, withdrawal and fill from its first
// deposit on; plus the pool's total principal just before that deposit.
func (s *VaultLPService) activityRows(ctx context.Context, wallet string) ([]models.LiquidityLedgerEntry, *big.Int, error) {
	me := strings.ToLower(wallet)
	scope := func() *gorm.DB { return s.vaultScope(s.c.DB.WithContext(ctx)) }
	var first []models.LiquidityLedgerEntry
	if err := scope().Where("kind = ? AND LOWER(account) = ?", VaultEventDeposited, me).
		Order("block ASC").Order("log_index ASC").Limit(1).Select("block", "log_index").Find(&first).Error; err != nil {
		return nil, nil, err
	}
	var rows []models.LiquidityLedgerEntry
	if len(first) == 0 { // never deposited: nothing earned, only claims (if any) to show
		err := scope().Where("kind = ? AND LOWER(account) = ?", VaultEventClaimed, me).
			Order("block ASC").Order("log_index ASC").Find(&rows).Error
		return rows, new(big.Int), err
	}
	b, li := first[0].Block, first[0].LogIndex
	var before struct{ Total *string }
	if err := scope().Model(&models.LiquidityLedgerEntry{}).
		Select("CAST(COALESCE(SUM(CASE WHEN kind = ? THEN amount ELSE -amount END), 0) AS TEXT) AS total", VaultEventDeposited).
		Where("kind IN ? AND (block < ? OR (block = ? AND log_index < ?))", []string{VaultEventDeposited, VaultEventWithdrawn}, b, b, li).
		Scan(&before).Error; err != nil {
		return nil, nil, err
	}
	total, ok := new(big.Int).SetString(strings.TrimSpace(lpDeref(before.Total)), 10)
	if !ok {
		return nil, nil, fmt.Errorf("liquidity: pool principal before block %d unreadable: %q", b, lpDeref(before.Total))
	}
	if err := scope().
		Where("((kind IN ? AND (block > ? OR (block = ? AND log_index >= ?))) OR (kind = ? AND LOWER(account) = ?))",
			[]string{VaultEventDeposited, VaultEventWithdrawn, VaultEventFilled}, b, b, li, VaultEventClaimed, me).
		Order("block ASC").Order("log_index ASC").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	return rows, total, nil
}

// ─── Activation and the allow-list ──────────────────────────────────────────

// Activate makes the caller an LP at once (spec §5): an active TSA Pay
// merchant, or a KYC-verified user — no application, no admin approval. The
// wallet is the caller's proven wallet (the handler checks the proof). The
// on-chain allow-list follows (setDepositor; the sync job retries it).
func (s *VaultLPService) Activate(ctx context.Context, userID uuid.UUID, wallet string) (*LiquidityVaultMe, error) {
	if !common.IsHexAddress(wallet) {
		return nil, ErrLPWalletMismatch
	}
	wallet = common.HexToAddress(wallet).Hex()
	if err := s.eligible(ctx, userID); err != nil {
		return nil, err
	}
	if err := s.walletFree(ctx, userID, wallet); err != nil {
		return nil, err
	}
	lp, err := s.lpRow(ctx, userID)
	switch {
	case errors.Is(err, ErrPMNotFound):
		if err := s.releaseUnprovenHolders(ctx, userID, wallet); err != nil {
			return nil, err
		}
		lp, err = s.createLP(ctx, userID, wallet)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case lp.Status == models.PaymentMerchantStatusPaused:
		return nil, ErrLPPausedByAdmin
	default:
		if err := s.repoint(ctx, lp, wallet); err != nil {
			return nil, err
		}
	}
	if err := s.setDepositor(ctx, lp.ID, wallet, true); err != nil {
		log.Printf("[liquidity] allow-list lp=%s wallet=%s deferred to the sync job: %s", lp.ID, wallet, RedactAlertText(err.Error()))
	}
	return s.Me(ctx, userID)
}

// repoint (re-)activates an existing LP row on wallet. Moving off a proven
// wallet that still holds principal or earnings is refused: the app builds
// withdraw and claim for the row's wallet only, so they would be stranded.
// An empty old wallet is revoked as a depositor (best effort here, kept
// revoked by SyncDepositors through previous_operator_wallets).
func (s *VaultLPService) repoint(ctx context.Context, lp *models.PaymentMerchant, wallet string) error {
	old := lp.OperatorWalletAddress
	moving := old != "" && !SameEVMAddress(old, wallet)
	if moving && isVaultLP(lp) {
		pos, err := s.c.Vault.Position(ctx, old)
		if err != nil {
			return fmt.Errorf("liquidity: old wallet position unreadable: %w", err)
		}
		if pos.Principal.Sign() > 0 || pos.Claimable.Sign() > 0 {
			return ErrLPOldWalletHasFunds
		}
	}
	if !SameEVMAddress(old, wallet) {
		if err := s.releaseUnprovenHolders(ctx, lp.UserID, wallet); err != nil {
			return err
		}
	}
	now := s.c.Now()
	updates := map[string]any{"status": models.PaymentMerchantStatusActive, "operator_wallet_address": wallet,
		"pool_chain_id": s.c.Token.ChainID, "review_note": "activated (shared pool, spec 2026-10-14 §5)"}
	if lp.VaultActivatedAt == nil {
		updates["vault_activated_at"] = now
	}
	prev := previousWallets(lp)
	if moving {
		prev = append(prev, old)
	}
	prev = withoutWallet(prev, wallet)
	if joined := strings.Join(prev, ","); joined != lpDeref(lp.PreviousOperatorWallets) {
		updates["previous_operator_wallets"] = joined
	}
	// Guarded on the wallet read: a concurrent re-activation must not drop a
	// previous wallet from the list (it would stay a depositor).
	res := s.c.DB.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("id = ? AND COALESCE(operator_wallet_address, '') = ?", lp.ID, old).Updates(updates)
	if res.Error != nil {
		if isUniqueViolation(res.Error) { // another account took the wallet first
			return ErrLPWalletInUse
		}
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("liquidity: the LP row changed during activation — try again")
	}
	lp.Status, lp.OperatorWalletAddress = models.PaymentMerchantStatusActive, wallet
	if lp.VaultActivatedAt == nil {
		lp.VaultActivatedAt = &now
	}
	if moving {
		log.Printf("[liquidity] AUDIT lp_wallet_repointed=1 lp=%s from=%s to=%s", lp.ID, old, wallet)
		if err := s.setDepositor(ctx, lp.ID, old, false); err != nil {
			log.Printf("[liquidity] revoke old wallet lp=%s wallet=%s deferred to the sync job: %s", lp.ID, old, RedactAlertText(err.Error()))
		}
	}
	return nil
}

// previousWallets is lp's previous_operator_wallets list.
func previousWallets(lp *models.PaymentMerchant) []string {
	var out []string
	for _, w := range strings.Split(lpDeref(lp.PreviousOperatorWallets), ",") {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// withoutWallet drops wallet (and duplicates) from list, case-insensitively.
func withoutWallet(list []string, wallet string) []string {
	seen := map[string]bool{strings.ToLower(wallet): true}
	out := list[:0:0]
	for _, w := range list {
		if k := strings.ToLower(w); !seen[k] {
			seen[k] = true
			out = append(out, w)
		}
	}
	return out
}

// walletFree: a vault position belongs to one account. Another account's
// vault LP row on the same wallet would read (and gas-free claim) that
// position. Old unproven rows do not count: a proof beats a typed-in claim.
func (s *VaultLPService) walletFree(ctx context.Context, userID uuid.UUID, wallet string) error {
	var n int64
	if err := s.c.DB.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("kind = ? AND user_id <> ? AND vault_activated_at IS NOT NULL AND LOWER(operator_wallet_address) = ?",
			models.PaymentMerchantKindLP, userID, strings.ToLower(wallet)).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrLPWalletInUse
	}
	return nil
}

// releaseUnprovenHolders clears wallet from other accounts' old slice-pool LP
// rows (never proven), so the wallet's proven owner can activate on it
// (uq_payment_merchants_lp_wallet would refuse them otherwise) — the same rule
// as wallet linking: a valid proof evicts holders that never proved it.
func (s *VaultLPService) releaseUnprovenHolders(ctx context.Context, userID uuid.UUID, wallet string) error {
	res := s.c.DB.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("kind = ? AND user_id <> ? AND vault_activated_at IS NULL AND LOWER(operator_wallet_address) = ?",
			models.PaymentMerchantKindLP, userID, strings.ToLower(wallet)).
		Updates(map[string]any{"operator_wallet_address": "", "review_note": "wallet released: proven by another account (shared pool)"})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[liquidity] AUDIT lp_unproven_wallet_evicted=1 wallet=%s by_user=%s rows=%d", wallet, userID, res.RowsAffected)
	}
	return nil
}

// accountWallet is the account's linked wallet now (users.wallet_address).
func (s *VaultLPService) accountWallet(ctx context.Context, userID uuid.UUID) (string, error) {
	var u models.User
	if err := s.c.DB.WithContext(ctx).Select("id", "wallet_address").First(&u, "id = ?", userID).Error; err != nil {
		return "", err
	}
	return u.WalletAddress, nil
}

// currentLP is activeLP whose wallet is still the account's: what deposits
// and gas-free claims need (withdraw and claim are signed by the LP wallet
// itself, so they never do).
func (s *VaultLPService) currentLP(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error) {
	lp, err := s.activeLP(ctx, userID)
	if err != nil {
		return nil, err
	}
	current, err := s.accountWallet(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !SameEVMAddress(current, lp.OperatorWalletAddress) {
		return nil, ErrLPWalletMismatch
	}
	return lp, nil
}

// eligible: an active TSA Pay merchant or a KYC-verified user, never the
// company account. The company check fails closed: only "no company account
// configured" (ErrCompanyPMUnusable) lets it pass.
func (s *VaultLPService) eligible(ctx context.Context, userID uuid.UUID) error {
	if s.c.CompanyUser != nil {
		company, err := s.c.CompanyUser(ctx)
		switch {
		case err == nil:
			if company == userID {
				return ErrCompanyUserCannotBeLP
			}
		case errors.Is(err, ErrCompanyPMUnusable):
		default:
			return fmt.Errorf("liquidity: company account check: %w", err)
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
		OperatorWalletAddress: wallet, ProvisioningStatus: models.PMProvisioningActive, VaultActivatedAt: &now,
		ReviewedAt: &now, ReviewNote: "activated (shared pool, spec 2026-10-14 §5)"}
	// vault_activated_at is update-only on the model: set it in the same transaction.
	err := s.c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(lp).Error; err != nil {
			return err
		}
		return tx.Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).Update("vault_activated_at", now).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			// A concurrent activation of this account won (use its row), or
			// another account took the wallet first (uq_payment_merchants_lp_wallet).
			if row, rerr := s.vaultLPRow(ctx, userID); rerr == nil && SameEVMAddress(row.OperatorWalletAddress, wallet) {
				return row, nil
			}
			return nil, ErrLPWalletInUse
		}
		return nil, err
	}
	return lp, nil
}

// setDepositor writes the allow-list for wallet (lp's current or a previous
// wallet) when the vault does not already say so (sendDepositor).
func (s *VaultLPService) setDepositor(ctx context.Context, lpID uuid.UUID, wallet string, allowed bool) error {
	_, err := s.sendDepositor(ctx, lpID, wallet, allowed)
	return err
}

// sendDepositor is setDepositor reporting whether a setDepositor call went
// to the key lane (false: the vault already says so). Idempotent across
// retries and processes: the operation key names the LP, the wallet and the
// target state, so the key lane answers a repeat from the call already
// committed — a write sent but not yet mined is never sent twice (a mined
// call frees its key, so a later flip back is a new call).
func (s *VaultLPService) sendDepositor(ctx context.Context, lpID uuid.UUID, wallet string, allowed bool) (bool, error) {
	pos, err := s.c.Vault.Position(ctx, wallet)
	if err != nil {
		return false, err
	}
	if pos.Allowed == allowed {
		return false, nil
	}
	op := fmt.Sprintf("pool_depositor:%s:%s:%t", lpID, strings.ToLower(wallet), allowed)
	hash, err := s.c.Sender.Send(WithOperationRef(ctx, op), s.c.Token.ChainID, func() ([]byte, error) {
		return s.c.Vault.PrepareSetDepositor(s.c.Sender.Address(), wallet, allowed)
	})
	if err != nil && !errors.Is(err, ErrPendingBroadcast) {
		return false, err
	}
	log.Printf("[liquidity] METRIC liquidity_depositor_set lp=%s wallet=%s allowed=%t tx=%s", lpID, wallet, allowed, hash)
	return true, nil
}

// indexedDepositors is each wallet's (lower-case) allow-list state as the
// ledger last indexed it: the latest DepositorSet event on this vault. A
// wallet with no event is absent.
func (s *VaultLPService) indexedDepositors(ctx context.Context) (map[string]bool, error) {
	var rows []models.LiquidityLedgerEntry
	if err := s.vaultScope(s.c.DB.WithContext(ctx)).Where("kind = ? AND allowed IS NOT NULL", VaultEventDepositorSet).
		Order("block, log_index").Select("account", "allowed").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.Account)] = *r.Allowed
	}
	return out, nil
}

// wantAllowed is the allow-list state lp's current wallet should have: a vault
// LP row that is active, whose account exists and is active (as the auth
// middleware decides it), and that is still eligible. A read failure returns
// an error and the caller leaves the wallet as it is.
func (s *VaultLPService) wantAllowed(ctx context.Context, lp *models.PaymentMerchant) (bool, string, error) {
	if !isVaultLP(lp) {
		return false, "not a vault LP (unproven slice-pool row)", nil
	}
	if lp.Status != models.PaymentMerchantStatusActive {
		return false, "row " + lp.Status, nil
	}
	var u models.User
	err := s.c.DB.WithContext(ctx).Select("id", "account_status").First(&u, "id = ?", lp.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, "account gone", nil
	}
	if err != nil {
		return false, "", err
	}
	if u.AccountStatus != models.AccountStatusActive {
		return false, "account " + u.AccountStatus, nil
	}
	if err := s.eligible(ctx, lp.UserID); err != nil {
		if errors.Is(err, ErrLPNotEligible) || errors.Is(err, ErrCompanyUserCannotBeLP) {
			return false, "no longer eligible", nil
		}
		return false, "", err
	}
	return true, "", nil
}

// SyncDepositors makes the vault's allow-list match the LP rows (withdraw and
// claim never need it): a wallet is allowed only while wantAllowed holds, and
// every wallet a row moved away from stays revoked. It returns how many
// setDepositor calls it sent.
//
// RUNBOOK: to stop an LP's deposits, an owner/guardian pauses the LP row
// (admin console). Never call setDepositor(wallet, false) on the vault by
// hand — this job would allow the wallet again within two minutes.
func (s *VaultLPService) SyncDepositors(ctx context.Context) (int, error) {
	var rows []models.PaymentMerchant
	if err := s.c.DB.WithContext(ctx).Where("kind = ? AND pool_chain_id = ?", models.PaymentMerchantKindLP, s.c.Token.ChainID).
		Find(&rows).Error; err != nil {
		return 0, err
	}
	current := map[string]bool{} // every row's current wallet: never revoked as someone's previous one
	for i := range rows {
		if w := rows[i].OperatorWalletAddress; w != "" {
			current[strings.ToLower(w)] = true
		}
	}
	// The ledger's last DepositorSet per wallet spares the Position read for
	// a wallet already as wanted. It lags the chain by the indexer's depth: a
	// write it has not indexed yet is read from the chain (and a pending one
	// is answered by its operation key, never sent again).
	indexed, err := s.indexedDepositors(ctx)
	if err != nil {
		log.Printf("[liquidity] depositor sync: ledger unreadable, reading every wallet from the vault: %s", RedactAlertText(err.Error()))
		indexed = nil
	}
	changed := 0
	var firstErr error
	fail := func(lp *models.PaymentMerchant, err error) {
		if firstErr == nil {
			firstErr = err
		}
		log.Printf("[liquidity] depositor sync lp=%s: %s", lp.ID, RedactAlertText(err.Error()))
	}
	apply := func(lp *models.PaymentMerchant, wallet string, want bool) {
		if got, ok := indexed[strings.ToLower(wallet)]; ok && got == want {
			return
		}
		sent, err := s.sendDepositor(ctx, lp.ID, wallet, want)
		if err != nil {
			fail(lp, err)
			return
		}
		if sent {
			changed++
		}
	}
	for i := range rows {
		lp := &rows[i]
		for _, prev := range previousWallets(lp) {
			if current[strings.ToLower(prev)] {
				continue
			}
			apply(lp, prev, false)
		}
		if lp.OperatorWalletAddress == "" {
			continue
		}
		want, why, err := s.wantAllowed(ctx, lp)
		if err != nil {
			fail(lp, err)
			continue
		}
		if !want && isVaultLP(lp) && lp.Status == models.PaymentMerchantStatusActive {
			log.Printf("[liquidity] depositor sync lp=%s wallet=%s not allowed: %s", lp.ID, lp.OperatorWalletAddress, why)
		}
		apply(lp, lp.OperatorWalletAddress, want)
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
	lp, err := s.vaultLPRow(ctx, userID)
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
	lp, err := s.currentLP(ctx, userID)
	if err != nil {
		return nil, err
	}
	if lp.Status != models.PaymentMerchantStatusActive {
		return nil, ErrLPPausedByAdmin
	}
	if err := s.eligible(ctx, userID); err != nil {
		return nil, err
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
		_ = s.setDepositor(ctx, lp.ID, lp.OperatorWalletAddress, true)
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
		return nil, lpRevertError(err)
	}
	return &LPTxPlan{Step: "withdraw", Txs: []json.RawMessage{tx}}, nil
}

// PrepareClaim: to the LP's wallet, or (to set) straight to an off-ramp
// address such as a sell intake, in one tx the LP signs.
func (s *VaultLPService) PrepareClaim(ctx context.Context, userID uuid.UUID, to string) (*LPTxPlan, error) {
	to = strings.TrimSpace(to)
	if to != "" && (!common.IsHexAddress(to) || common.HexToAddress(to) == (common.Address{})) {
		return nil, ErrLPClaimTo
	}
	lp, err := s.activeLP(ctx, userID)
	if err != nil {
		return nil, err
	}
	var tx []byte
	if to == "" {
		tx, err = s.c.Vault.PrepareClaim(lp.OperatorWalletAddress)
	} else {
		tx, err = s.c.Vault.PrepareClaimTo(lp.OperatorWalletAddress, to)
	}
	if err != nil {
		return nil, lpRevertError(err)
	}
	return &LPTxPlan{Step: "claim", Txs: []json.RawMessage{tx}}, nil
}

// ClaimForMe sends claimFor(lp) from the operator — gas-free for the LP,
// paid only to the LP's own wallet (the contract's rule) — once per
// lpClaimForEvery and only from lpMinClaimUSD.
func (s *VaultLPService) ClaimForMe(ctx context.Context, userID uuid.UUID) (string, error) {
	lp, err := s.currentLP(ctx, userID)
	if err != nil {
		return "", err
	}
	now := s.c.Now()
	marker := "pool_claim_for:" + lp.ID.String()
	// A read-only look at the window first: a rate-limited caller costs no RPC.
	// The slot itself is taken only after the size check (claimRunAt below),
	// so a too-small claim never burns it.
	if s.claimForUsed(ctx, marker, now) {
		return "", ErrLPClaimRateLimit
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

// claimForUsed reports whether marker's window is still taken at now. A read
// failure answers false: claimRunAt still decides.
func (s *VaultLPService) claimForUsed(ctx context.Context, marker string, now time.Time) bool {
	var n int64
	err := s.c.DB.WithContext(ctx).Model(&models.JobRunMarker{}).
		Where("name = ? AND last_run_at > ?", marker, now.Add(-lpClaimForEvery)).Count(&n).Error
	return err == nil && n > 0
}
