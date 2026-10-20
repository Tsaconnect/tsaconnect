package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Fiat LP (plan docs/plans/2026-10-20-fiat-lp-desk.md): a fiat LP sends USDT
// to the company desk wallet (the pool's refill wallet) and is owed a naira
// amount locked at that day's rate. The company carries the FX; the LP earns
// the pool share their USDT would have earned, shown in naira, and exits in
// USDT at the day's rate when an admin pays the request.

const (
	// An admin may pay up to 1% under the quote (the rate moves between quote
	// and signing); never less.
	fiatPayNum, fiatPayDen = 99, 100
	fiatMinDepositUSD      = 1
	fiatMinEarningsCents   = 50
	fiatEarningsTTL        = 30 * time.Second
	// The rate locks when the transfer is confirmed, so confirming must follow
	// the transfer closely: otherwise an LP could send, wait for a better rate
	// and confirm then. Later than this goes to support.
	fiatConfirmWithin = 3 * time.Minute
	// A transfer is recorded only once its block is this old: deep enough that a
	// reorg cannot take it back after the claim is credited.
	fiatConfirmAfter = 20 * time.Second
)

var (
	ErrFiatLPUnavailable = errors.New("fiat liquidity: not available right now")
	ErrFiatLPTxHash      = errors.New("fiat liquidity: enter the transaction hash of your transfer")
	ErrFiatLPUnproven    = errors.New("fiat liquidity: we can't see that transfer yet — wait a minute and try again")
	ErrFiatLPRefuted     = errors.New("fiat liquidity: that transaction did not send USDT between the expected wallets")
	ErrFiatLPTooSmall    = errors.New("fiat liquidity: the deposit is below the minimum")
	ErrFiatLPTaken       = errors.New("fiat liquidity: that transfer is already recorded")
	ErrFiatLPAmount      = errors.New("fiat liquidity: amount out of range")
	ErrFiatLPState       = errors.New("fiat liquidity: that request is not open")
	ErrFiatLPShort       = errors.New("fiat liquidity: the transfer is less than the quote")
	ErrFiatLPNotFound    = errors.New("fiat liquidity: not found")
	ErrFiatLPStale       = errors.New("fiat liquidity: that transfer is too old to record automatically — contact support")
	ErrFiatLPCurrency    = errors.New("fiat liquidity: that currency is not supported")
)

var (
	txHashRe = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	moneyRe  = regexp.MustCompile(`^\d+(\.\d{1,18})?$`)
)

// FiatRateFunc is the fiat per 1 USD of the pool token (USDT = $1).
type FiatRateFunc func(ctx context.Context, currency string) (*big.Rat, error)

// FiatOf is amount (token units) in fiat at rate, rounded to 2 dp.
func FiatOf(amount *big.Int, decimals int, rate *big.Rat) string {
	v := new(big.Rat).SetFrac(amount, pow10(decimals))
	return v.Mul(v, rate).FloatString(2)
}

// signedMoney is r to 2 dp, never "-0.00".
func signedMoney(r *big.Rat) string {
	out := r.FloatString(2)
	if strings.Trim(out, "-0.") == "" {
		return "0.00"
	}
	return out
}

func ratFloor(r *big.Rat) *big.Int {
	if r.Sign() <= 0 {
		return new(big.Int)
	}
	return new(big.Int).Quo(r.Num(), r.Denom())
}

type FiatLPConfig struct {
	DB       *gorm.DB
	Token    PoolToken
	Vault    string                                    // vault address: the ledger scope
	Desk     func(ctx context.Context) (string, error) // the pool's refill wallet
	Verifier ERC20SourcedTransferVerifier
	Rate     FiatRateFunc
	// Currencies lists the currency codes a position may be held in (the
	// registry); only those with a live rate are offered.
	Currencies func(ctx context.Context) []string
	// TxTime is the block time of a mined transaction (required: it bounds how
	// long after a transfer its rate may still be locked).
	TxTime func(ctx context.Context, txHash string) (time.Time, error)
	Now    func() time.Time
	// CanLP gates new deposits and requests (KYC, never the company account).
	CanLP func(ctx context.Context, userID uuid.UUID) error
}

type FiatLPService struct {
	c        FiatLPConfig
	earnings *cachedReader[map[uuid.UUID]*big.Int]
	desk     *cachedReader[string]
}

func NewFiatLPService(c FiatLPConfig) (*FiatLPService, error) {
	if c.DB == nil || c.Desk == nil || c.Verifier == nil || c.Rate == nil || c.Currencies == nil || c.TxTime == nil || c.Token.Address == "" || c.Vault == "" {
		return nil, errors.New("fiat liquidity: missing a part")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &FiatLPService{c: c,
		earnings: newCachedReader[map[uuid.UUID]*big.Int](fiatEarningsTTL, fiatEarningsTTL/3),
		desk:     newCachedReader[string](5*time.Minute, 10*time.Second)}, nil
}

func (s *FiatLPService) units(n *big.Int) string { return BaseUnitsToDecimal(n, s.c.Token.Decimals) }

// forgetEarnings drops the cached replay after anything that moves units.
func (s *FiatLPService) forgetEarnings() { s.earnings.Forget("all") }

// DeskWallet is where fiat LPs send USDT: the pool's refill wallet, read from
// the contract so a role change moves it with no deploy.
func (s *FiatLPService) DeskWallet(ctx context.Context) (string, error) {
	return s.desk.Get("desk", s.c.Now(), func() (string, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		w, err := s.c.Desk(rctx)
		if err == nil && !common.IsHexAddress(w) {
			err = errors.New("desk wallet unreadable")
		}
		s.desk.Put("desk", s.c.Now(), w, err)
		return w, err
	})
}

// ─── Deposits ───────────────────────────────────────────────────────────────

// ConfirmDeposit proves wallet sent txHash's USDT to the desk and records the
// naira claim at the rate of this moment. Idempotent per transaction.
func (s *FiatLPService) ConfirmDeposit(ctx context.Context, userID uuid.UUID, wallet, txHash, currency string) (*models.FiatLPDeposit, error) {
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if !txHashRe.MatchString(txHash) {
		return nil, ErrFiatLPTxHash
	}
	if !common.IsHexAddress(wallet) {
		return nil, ErrFiatLPUnavailable
	}
	if s.c.CanLP != nil {
		if err := s.c.CanLP(ctx, userID); err != nil {
			return nil, err
		}
	}
	if d, err := s.depositByTx(ctx, userID, txHash); d != nil || err != nil {
		return d, err
	}
	desk, err := s.DeskWallet(ctx)
	if err != nil {
		return nil, ErrFiatLPUnavailable
	}
	got, verr := s.c.Verifier.VerifyTransferFrom(ctx, txHash, s.c.Token.Address, wallet, desk)
	switch ClassifyTransfer(got, verr) {
	case TransferRefuted:
		return nil, ErrFiatLPRefuted
	case TransferUnproven:
		return nil, ErrFiatLPUnproven
	}
	if got.Cmp(new(big.Int).Mul(big.NewInt(fiatMinDepositUSD), pow10(s.c.Token.Decimals))) < 0 {
		return nil, ErrFiatLPTooSmall
	}
	at, err := s.c.TxTime(ctx, txHash)
	if err != nil {
		return nil, ErrFiatLPUnproven
	}
	switch age := s.c.Now().Sub(at); {
	case age < fiatConfirmAfter:
		return nil, ErrFiatLPUnproven // mined, but not deep enough to credit yet
	case age > fiatConfirmWithin:
		return nil, ErrFiatLPStale
	}
	rate, err := s.rateFor(ctx, currency)
	if err != nil {
		return nil, err
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	d := &models.FiatLPDeposit{ID: uuid.New(), UserID: userID, Wallet: common.HexToAddress(wallet).Hex(), TxHash: txHash,
		ChainID: s.c.Token.ChainID, Units: got.String(), Currency: currency, Rate: rate.FloatString(8),
		Fiat: FiatOf(got, s.c.Token.Decimals, rate), CreatedAt: s.c.Now().UTC()}
	if err := s.c.DB.WithContext(ctx).Create(d).Error; err != nil {
		if isDuplicateKeyErr(err) { // a concurrent confirm of the same transfer won
			if d, err := s.depositByTx(ctx, userID, txHash); d != nil || err != nil {
				return d, err
			}
		}
		return nil, err
	}
	s.forgetEarnings()
	return d, nil
}

// depositByTx is the recorded deposit of txHash: the caller's own (idempotent
// repeat), ErrFiatLPTaken when it is another user's, or nil when none exists.
func (s *FiatLPService) depositByTx(ctx context.Context, userID uuid.UUID, txHash string) (*models.FiatLPDeposit, error) {
	var d models.FiatLPDeposit
	switch err := s.c.DB.WithContext(ctx).Where("tx_hash = ?", txHash).First(&d).Error; {
	case err == nil && d.UserID == userID:
		return &d, nil
	case err == nil:
		return nil, ErrFiatLPTaken
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, err
	}
}

// ─── Currencies ─────────────────────────────────────────────────────────────

// FiatCurrencyInfo is a currency a position may be held in, with today's rate.
type FiatCurrencyInfo struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	Rate   string `json:"rate"` // fiat per $1 today
}

var fiatCurrencyMeta = map[string]struct{ name, symbol string }{
	"NGN": {"Nigerian naira", "₦"}, "GHS": {"Ghanaian cedi", "GH₵"}, "KES": {"Kenyan shilling", "KSh"},
	"UGX": {"Ugandan shilling", "USh"}, "TZS": {"Tanzanian shilling", "TSh"},
	"USD": {"US dollar", "$"}, "EUR": {"Euro", "€"}, "GBP": {"British pound", "£"},
}

// priced is every supported currency that has a live rate right now.
func (s *FiatLPService) priced(ctx context.Context) (map[string]*big.Rat, []FiatCurrencyInfo) {
	rates := map[string]*big.Rat{}
	var infos []FiatCurrencyInfo
	for _, code := range s.c.Currencies(ctx) {
		code = strings.ToUpper(strings.TrimSpace(code))
		if _, dup := rates[code]; dup || code == "" {
			continue
		}
		r, err := s.c.Rate(ctx, code)
		if err != nil || r == nil || r.Sign() <= 0 {
			continue
		}
		rates[code] = r
		name, sym := code, code
		if m, ok := fiatCurrencyMeta[code]; ok {
			name, sym = m.name, m.symbol
		}
		infos = append(infos, FiatCurrencyInfo{Code: code, Name: name, Symbol: sym, Rate: signedMoney(r)})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Code < infos[j].Code })
	return rates, infos
}

// rateFor is today's rate for a supported currency: ErrFiatLPCurrency when it
// is not one, ErrFiatLPUnavailable when it is but has no live rate.
func (s *FiatLPService) rateFor(ctx context.Context, currency string) (*big.Rat, error) {
	code := strings.ToUpper(strings.TrimSpace(currency))
	for _, c := range s.c.Currencies(ctx) {
		if strings.EqualFold(c, code) && code != "" {
			r, err := s.c.Rate(ctx, code)
			if err != nil || r == nil || r.Sign() <= 0 {
				return nil, ErrFiatLPUnavailable
			}
			return r, nil
		}
	}
	return nil, ErrFiatLPCurrency
}

// ─── Position ───────────────────────────────────────────────────────────────

// FiatPositionLine is one currency the caller holds a locked balance in.
type FiatPositionLine struct {
	Currency string `json:"currency"`
	// Principal is owed in Currency; Units is the USDT behind it; AvgRate the
	// rate it was locked at.
	Principal string `json:"principal"`
	Units     string `json:"units"`
	AvgRate   string `json:"avgRate,omitempty"`
	// CurrentRate prices the claim today; TodayUSDT is what an exit would pay.
	CurrentRate string `json:"currentRate,omitempty"`
	TodayUSDT   string `json:"todayUsdt,omitempty"`
}

// FiatLPPosition is GET /api/liquidity/fiat.
type FiatLPPosition struct {
	// Eligible: the caller may add USDT (KYC, not the company account). Exits
	// are never gated on it. Wallet is the caller's proven wallet, filled by
	// the handler: the app sends from it only.
	Eligible    bool               `json:"eligible"`
	Wallet      string             `json:"wallet,omitempty"`
	Desk        string             `json:"desk"`
	Chain       *LiquidityChain    `json:"chain,omitempty"`
	Token       *LiquidityToken    `json:"token,omitempty"`
	MinDeposit  string             `json:"minDeposit"`
	MinEarnings string             `json:"minEarnings"`
	Currencies  []FiatCurrencyInfo `json:"currencies"`
	Positions   []FiatPositionLine `json:"positions"`
	// Earned is the pool share accrued so far (USDT); EarningsAvailable what is
	// not yet requested or paid; EarningsValue that priced in each currency.
	Earned            string                    `json:"earned"`
	EarningsAvailable string                    `json:"earningsAvailable"`
	EarningsValue     map[string]string         `json:"earningsValue"`
	Deposits          []models.FiatLPDeposit    `json:"deposits"`
	Withdrawals       []models.FiatLPWithdrawal `json:"withdrawals"`
}

type fiatLine struct{ naira, units *big.Rat } // owed in one currency, and the USDT units behind it

type fiatBook struct {
	by           map[string]*fiatLine
	dep          []models.FiatLPDeposit
	wd           []models.FiatLPWithdrawal
	earnReserved *big.Int // earnings requested or paid
}

func (b *fiatBook) line(cur string) *fiatLine {
	l := b.by[cur]
	if l == nil {
		l = &fiatLine{naira: new(big.Rat), units: new(big.Rat)}
		b.by[cur] = l
	}
	return l
}

func (s *FiatLPService) book(ctx context.Context, db *gorm.DB, userID uuid.UUID) (*fiatBook, error) {
	b := &fiatBook{by: map[string]*fiatLine{}, earnReserved: new(big.Int)}
	db = db.WithContext(ctx)
	if err := db.Where("user_id = ?", userID).Order("created_at ASC").Find(&b.dep).Error; err != nil {
		return nil, err
	}
	if err := db.Where("user_id = ?", userID).Order("requested_at ASC").Find(&b.wd).Error; err != nil {
		return nil, err
	}
	for _, d := range b.dep {
		f, _ := new(big.Rat).SetString(d.Fiat)
		u, _ := new(big.Int).SetString(d.Units, 10)
		l := b.line(d.Currency)
		l.naira.Add(l.naira, f)
		l.units.Add(l.units, new(big.Rat).SetInt(u))
	}
	for _, w := range b.wd {
		if w.Status == models.FiatLPRejected {
			continue
		}
		switch w.Kind {
		case models.FiatLPKindPrincipal:
			l := b.line(withdrawalCurrency(&w))
			if w.Fiat != nil {
				f, _ := new(big.Rat).SetString(*w.Fiat)
				l.naira.Sub(l.naira, f)
			}
			l.units.Sub(l.units, new(big.Rat).SetInt(bigFromDecimal(w.UnitsReleased)))
		case models.FiatLPKindEarnings:
			b.earnReserved.Add(b.earnReserved, bigFromDecimal(w.Units))
		}
	}
	return b, nil
}

func (s *FiatLPService) Position(ctx context.Context, userID uuid.UUID) (*FiatLPPosition, error) {
	desk, err := s.DeskWallet(ctx)
	if err != nil {
		return nil, ErrFiatLPUnavailable
	}
	b, err := s.book(ctx, s.c.DB, userID)
	if err != nil {
		return nil, err
	}
	gross, err := s.earnedBy(ctx, userID)
	if err != nil {
		return nil, err
	}
	avail := new(big.Int).Sub(gross, b.earnReserved)
	if avail.Sign() < 0 {
		avail.SetInt64(0)
	}
	dec := s.c.Token.Decimals
	rates, infos := s.priced(ctx)
	out := &FiatLPPosition{Eligible: s.c.CanLP == nil || s.c.CanLP(ctx, userID) == nil, Desk: common.HexToAddress(desk).Hex(),
		Chain:      &LiquidityChain{ID: s.c.Token.ChainID, Key: s.c.Token.ChainKey},
		Token:      &LiquidityToken{Symbol: s.c.Token.Symbol, Address: s.c.Token.Address, Decimals: dec},
		MinDeposit: fmt.Sprint(fiatMinDepositUSD), MinEarnings: "0.5", Currencies: infos, Positions: []FiatPositionLine{},
		Earned: s.units(gross), EarningsAvailable: s.units(avail), EarningsValue: map[string]string{},
		Deposits: b.dep, Withdrawals: b.wd}
	for _, ci := range infos {
		out.EarningsValue[ci.Code] = FiatOf(avail, dec, rates[ci.Code])
	}
	codes := make([]string, 0, len(b.by))
	for c := range b.by {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		l := b.by[c]
		if l.units.Sign() <= 0 && l.naira.Sign() <= 0 {
			continue
		}
		line := FiatPositionLine{Currency: c, Principal: signedMoney(l.naira), Units: s.units(ratFloor(l.units))}
		if l.units.Sign() > 0 {
			line.AvgRate = signedMoney(new(big.Rat).Quo(l.naira, new(big.Rat).Quo(l.units, new(big.Rat).SetInt(pow10(dec)))))
		}
		if r := rates[c]; r != nil {
			line.CurrentRate = signedMoney(r)
			line.TodayUSDT = signedMoney(new(big.Rat).Quo(l.naira, r))
		}
		out.Positions = append(out.Positions, line)
	}
	return out, nil
}

// ─── Earnings ───────────────────────────────────────────────────────────────

// earnedBy is the USDT a fiat LP's units have earned so far.
func (s *FiatLPService) earnedBy(ctx context.Context, userID uuid.UUID) (*big.Int, error) {
	all, err := s.earnings.Get("all", s.c.Now(), func() (map[uuid.UUID]*big.Int, error) {
		m, err := s.replayEarnings(context.WithoutCancel(ctx))
		s.earnings.Put("all", s.c.Now(), m, err)
		return m, err
	})
	if err != nil {
		return nil, err
	}
	if v := all[userID]; v != nil {
		return new(big.Int).Set(v), nil
	}
	return new(big.Int), nil
}

type earnEvent struct {
	at     time.Time
	order  int // pool ledger rows first on a tie
	ledger *models.LiquidityLedgerEntry
	user   uuid.UUID
	delta  *big.Int // fiat units change for user
}

// replayEarnings replays the vault ledger beside the fiat LPs' own timeline.
// Every fill's spread is shared as the contract shares it — by units over the
// pool's total principal — except that the desk wallet's real position is
// swapped for the fiat LPs' units, which is exactly "as if their USDT were in
// the pool": the company covers any difference if the desk deploys less.
func (s *FiatLPService) replayEarnings(ctx context.Context) (map[uuid.UUID]*big.Int, error) {
	var deps []models.FiatLPDeposit
	if err := s.c.DB.WithContext(ctx).Find(&deps).Error; err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return map[uuid.UUID]*big.Int{}, nil
	}
	var wds []models.FiatLPWithdrawal
	if err := s.c.DB.WithContext(ctx).Where("kind = ? AND status <> ?", models.FiatLPKindPrincipal, models.FiatLPRejected).Find(&wds).Error; err != nil {
		return nil, err
	}
	var rows []models.LiquidityLedgerEntry
	if err := s.c.DB.WithContext(ctx).Where("chain_id = ? AND vault = ? AND kind IN ?", s.c.Token.ChainID, strings.ToLower(s.c.Vault),
		[]string{VaultEventDeposited, VaultEventWithdrawn, VaultEventWithdrawalPending, VaultEventFilled}).
		Order("block ASC, log_index ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	desk, err := s.DeskWallet(ctx)
	if err != nil {
		return nil, err
	}
	desk = strings.ToLower(desk)

	var ev []earnEvent
	for i := range rows {
		ev = append(ev, earnEvent{at: rows[i].BlockTime, order: 0, ledger: &rows[i]})
	}
	for _, d := range deps {
		u, _ := new(big.Int).SetString(d.Units, 10)
		ev = append(ev, earnEvent{at: d.CreatedAt, order: 1, user: d.UserID, delta: u})
	}
	for _, w := range wds {
		ev = append(ev, earnEvent{at: w.RequestedAt, order: 1, user: w.UserID, delta: new(big.Int).Neg(bigFromDecimal(w.UnitsReleased))})
	}
	sort.SliceStable(ev, func(i, j int) bool {
		if !ev[i].at.Equal(ev[j].at) {
			return ev[i].at.Before(ev[j].at)
		}
		return ev[i].order < ev[j].order
	})

	pool, deskPos, fiatTotal := new(big.Int), new(big.Int), new(big.Int)
	held := map[uuid.UUID]*big.Int{}
	earn := map[uuid.UUID]*big.Rat{}
	for _, e := range ev {
		if e.ledger == nil {
			if held[e.user] == nil {
				held[e.user] = new(big.Int)
			}
			held[e.user].Add(held[e.user], e.delta)
			fiatTotal.Add(fiatTotal, e.delta)
			continue
		}
		r := e.ledger
		amt := bigFromDecimal(r.Amount)
		isDesk := strings.ToLower(r.Account) == desk
		switch r.Kind {
		case VaultEventDeposited:
			pool.Add(pool, amt)
			if isDesk {
				deskPos.Add(deskPos, amt)
			}
		case VaultEventWithdrawn, VaultEventWithdrawalPending:
			pool.Sub(pool, amt)
			if isDesk {
				deskPos.Sub(deskPos, amt)
			}
		case VaultEventFilled:
			spread := bigFromDecimal(r.Spread)
			virtual := new(big.Int).Add(new(big.Int).Sub(pool, deskPos), fiatTotal)
			if spread.Sign() == 0 || virtual.Sign() <= 0 {
				continue
			}
			for u, n := range held {
				if n.Sign() <= 0 {
					continue
				}
				if earn[u] == nil {
					earn[u] = new(big.Rat)
				}
				earn[u].Add(earn[u], new(big.Rat).SetFrac(new(big.Int).Mul(spread, n), virtual))
			}
		}
	}
	out := make(map[uuid.UUID]*big.Int, len(earn))
	for u, r := range earn {
		out[u] = ratFloor(r)
	}
	return out, nil
}

// ─── Withdrawals ────────────────────────────────────────────────────────────

// RequestWithdrawal reserves an exit. Principal: amount is in currency, at most
// what the LP is owed in it. Earnings: amount is USDT (currency ignored), at
// most what has accrued and not been asked for, and at least the minimum. Money
// moves only when an admin pays the request, and only to the wallet recorded
// here. An exit is never gated on eligibility (the vault's custody promise).
func (s *FiatLPService) RequestWithdrawal(ctx context.Context, userID uuid.UUID, wallet, kind, currency, amount string) (*models.FiatLPWithdrawal, error) {
	if !common.IsHexAddress(wallet) {
		return nil, ErrFiatLPUnavailable
	}
	amount = strings.TrimSpace(amount)
	amt, ok := new(big.Rat).SetString(amount)
	if !moneyRe.MatchString(amount) || !ok || amt.Sign() <= 0 {
		return nil, ErrFiatLPAmount
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	var gross *big.Int
	if kind == models.FiatLPKindEarnings {
		g, err := s.earnedBy(ctx, userID)
		if err != nil {
			return nil, err
		}
		gross, currency = g, ""
	}
	w := &models.FiatLPWithdrawal{ID: uuid.New(), UserID: userID, Wallet: common.HexToAddress(wallet).Hex(), Kind: kind,
		Currency: currency, Status: models.FiatLPRequested, RequestedAt: s.c.Now().UTC()}
	// One request at a time per user, across API instances: the check against
	// what is owed and the insert happen under the same advisory lock.
	err := withAdvisoryLock(ctx, s.c.DB, "fiat_lp:"+userID.String(), "fiat liquidity", func(tx *gorm.DB) error {
		b, err := s.book(ctx, tx, userID)
		if err != nil {
			return err
		}
		switch kind {
		case models.FiatLPKindPrincipal:
			l := b.by[currency]
			if currency == "" || l == nil || l.naira.Sign() <= 0 {
				return fmt.Errorf("%w: nothing held in %q", ErrFiatLPAmount, currency)
			}
			fiat := new(big.Rat).SetInt(ratFloor(new(big.Rat).Mul(amt, big.NewRat(100, 1))))
			fiat.Quo(fiat, big.NewRat(100, 1)) // whole minor units
			if fiat.Sign() <= 0 || fiat.Cmp(l.naira) > 0 {
				return fmt.Errorf("%w: more than you are owed", ErrFiatLPAmount)
			}
			rel := new(big.Rat).Mul(l.units, new(big.Rat).Quo(fiat, l.naira))
			if fiat.Cmp(l.naira) == 0 {
				rel.Set(l.units)
			}
			fiatS, relS := fiat.FloatString(2), ratFloor(rel).String()
			w.Fiat, w.UnitsReleased = &fiatS, &relS
		case models.FiatLPKindEarnings:
			units := ratFloor(new(big.Rat).Mul(amt, new(big.Rat).SetInt(pow10(s.c.Token.Decimals))))
			avail := new(big.Int).Sub(gross, b.earnReserved)
			min := new(big.Int).Div(new(big.Int).Mul(big.NewInt(fiatMinEarningsCents), pow10(s.c.Token.Decimals)), big.NewInt(100))
			if units.Cmp(avail) > 0 || units.Cmp(min) < 0 {
				return fmt.Errorf("%w: earnings available %s, minimum %s", ErrFiatLPAmount, s.units(avail), s.units(min))
			}
			us := units.String()
			w.Units = &us
		default:
			return ErrFiatLPAmount
		}
		return tx.Create(w).Error
	})
	if err != nil {
		return nil, err
	}
	s.forgetEarnings()
	return w, nil
}

// ─── Admin ──────────────────────────────────────────────────────────────────

// FiatLPRequest is one exit request in the console, with its quote.
type FiatLPRequest struct {
	models.FiatLPWithdrawal
	UserName string `json:"userName,omitempty"`
	// QuoteUnits is the USDT (chain units) to send now; QuoteUSDT the same in
	// whole tokens; Rate the fiat per $1 it was priced at (principal only).
	QuoteUnits string `json:"quoteUnits,omitempty"`
	QuoteUSDT  string `json:"quoteUsdt,omitempty"`
	Rate       string `json:"rate,omitempty"`
}

// FiatLPCurrencySummary is what the company owes in one currency.
type FiatLPCurrencySummary struct {
	Currency    string `json:"currency"`
	LPs         int    `json:"lps"`
	Fiat        string `json:"fiat"`  // owed at the locked rates
	Units       string `json:"units"` // USDT behind it
	CurrentRate string `json:"currentRate,omitempty"`
	TodayUSDT   string `json:"todayUsdt,omitempty"`
	GapUSDT     string `json:"gapUsdt,omitempty"` // todayUsdt − units: > 0 the company tops up
}

// FiatLPSummary is the company's side: what it owes fiat LPs, per currency.
type FiatLPSummary struct {
	Desk  string `json:"desk"`
	LPs   int    `json:"lps"`
	Units string `json:"units"` // all USDT behind fiat balances
	Open  int    `json:"open"`  // requested exits
	// GapUSDT sums every currency's gap; omitted while any held currency has no rate.
	GapUSDT    string                  `json:"gapUsdt,omitempty"`
	Currencies []FiatLPCurrencySummary `json:"currencies"`
}

// legacyFiatCurrency is the currency of rows written before positions were
// per currency: the naira-only version stored none.
const legacyFiatCurrency = "NGN"

// withdrawalCurrency is the currency a principal exit is in (NGN for a row the
// naira-only version wrote), "" for an earnings exit.
func withdrawalCurrency(w *models.FiatLPWithdrawal) string {
	if w.Kind != models.FiatLPKindPrincipal {
		return ""
	}
	if w.Currency == "" {
		return legacyFiatCurrency
	}
	return strings.ToUpper(w.Currency)
}

func (s *FiatLPService) quote(w *models.FiatLPWithdrawal, rate *big.Rat) *big.Int {
	if w.Kind == models.FiatLPKindEarnings {
		return bigFromDecimal(w.Units)
	}
	f, _ := new(big.Rat).SetString(*w.Fiat)
	return ratFloor(new(big.Rat).Mul(new(big.Rat).Quo(f, rate), new(big.Rat).SetInt(pow10(s.c.Token.Decimals))))
}

// AdminOverview is the summary and the requests (status "" = all).
func (s *FiatLPService) AdminOverview(ctx context.Context, status string) (*FiatLPSummary, []FiatLPRequest, error) {
	desk, err := s.DeskWallet(ctx)
	if err != nil {
		return nil, nil, ErrFiatLPUnavailable
	}
	var deps []models.FiatLPDeposit
	var wds []models.FiatLPWithdrawal
	if err := s.c.DB.WithContext(ctx).Find(&deps).Error; err != nil {
		return nil, nil, err
	}
	if err := s.c.DB.WithContext(ctx).Where("status <> ?", models.FiatLPRejected).Find(&wds).Error; err != nil {
		return nil, nil, err
	}
	type tot struct {
		fiat, units *big.Rat
		users       map[uuid.UUID]bool
	}
	by, allUsers := map[string]*tot{}, map[uuid.UUID]bool{}
	get := func(c string) *tot {
		t := by[c]
		if t == nil {
			t = &tot{fiat: new(big.Rat), units: new(big.Rat), users: map[uuid.UUID]bool{}}
			by[c] = t
		}
		return t
	}
	for _, d := range deps {
		f, _ := new(big.Rat).SetString(d.Fiat)
		u, _ := new(big.Int).SetString(d.Units, 10)
		t := get(d.Currency)
		t.fiat.Add(t.fiat, f)
		t.units.Add(t.units, new(big.Rat).SetInt(u))
		t.users[d.UserID], allUsers[d.UserID] = true, true
	}
	open := 0
	for _, w := range wds {
		if w.Status == models.FiatLPRequested {
			open++
		}
		if w.Kind == models.FiatLPKindPrincipal && w.Fiat != nil {
			f, _ := new(big.Rat).SetString(*w.Fiat)
			t := get(withdrawalCurrency(&w))
			t.fiat.Sub(t.fiat, f)
			t.units.Sub(t.units, new(big.Rat).SetInt(bigFromDecimal(w.UnitsReleased)))
		}
	}
	rates, _ := s.priced(ctx)
	sum := &FiatLPSummary{Desk: common.HexToAddress(desk).Hex(), LPs: len(allUsers), Open: open, Currencies: []FiatLPCurrencySummary{}}
	codes := make([]string, 0, len(by))
	for c := range by {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	allUnits, gapSum, allRated := new(big.Rat), new(big.Rat), true
	for _, c := range codes {
		t := by[c]
		line := FiatLPCurrencySummary{Currency: c, LPs: len(t.users), Fiat: signedMoney(t.fiat), Units: s.units(ratFloor(t.units))}
		allUnits.Add(allUnits, t.units)
		if r := rates[c]; r != nil {
			today := new(big.Rat).Quo(t.fiat, r)
			gap := new(big.Rat).Sub(today, new(big.Rat).Quo(t.units, new(big.Rat).SetInt(pow10(s.c.Token.Decimals))))
			line.CurrentRate, line.TodayUSDT, line.GapUSDT = signedMoney(r), signedMoney(today), signedMoney(gap)
			gapSum.Add(gapSum, gap)
		} else if t.fiat.Sign() > 0 {
			allRated = false
		}
		sum.Currencies = append(sum.Currencies, line)
	}
	sum.Units = s.units(ratFloor(allUnits))
	if allRated && len(codes) > 0 {
		sum.GapUSDT = signedMoney(gapSum)
	}
	q := s.c.DB.WithContext(ctx).Order("requested_at ASC")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var list []models.FiatLPWithdrawal
	if err := q.Limit(200).Find(&list).Error; err != nil {
		return nil, nil, err
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, w := range list {
		ids = append(ids, w.UserID)
	}
	names := map[uuid.UUID]string{}
	if len(ids) > 0 {
		var us []models.User
		if err := s.c.DB.WithContext(ctx).Select("id", "name").Where("id IN ?", ids).Find(&us).Error; err == nil {
			for _, u := range us {
				names[u.ID] = u.Name
			}
		}
	}
	out := make([]FiatLPRequest, 0, len(list))
	for i := range list {
		r := FiatLPRequest{FiatLPWithdrawal: list[i], UserName: names[list[i].UserID]}
		if list[i].Status == models.FiatLPRequested {
			switch rate := rates[withdrawalCurrency(&list[i])]; {
			case list[i].Kind == models.FiatLPKindEarnings:
				qu := s.quote(&list[i], nil)
				r.QuoteUnits, r.QuoteUSDT = qu.String(), s.units(qu)
			case rate != nil:
				qu := s.quote(&list[i], rate)
				r.QuoteUnits, r.QuoteUSDT, r.Rate = qu.String(), s.units(qu), signedMoney(rate)
			}
		}
		out = append(out, r)
	}
	return sum, out, nil
}

const fiatAuditPaid, fiatAuditRejected = "fiat_lp.paid", "fiat_lp.rejected"

// MarkPaid proves the desk sent the LP's recorded wallet at least the quote
// (less 1%) in txHash and closes the request.
func (s *FiatLPService) MarkPaid(ctx context.Context, adminID, id uuid.UUID, txHash string) (*models.FiatLPWithdrawal, error) {
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if !txHashRe.MatchString(txHash) {
		return nil, ErrFiatLPTxHash
	}
	w, err := s.getWithdrawal(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.Status != models.FiatLPRequested {
		return nil, ErrFiatLPState
	}
	desk, err := s.DeskWallet(ctx)
	if err != nil {
		return nil, ErrFiatLPUnavailable
	}
	got, verr := s.c.Verifier.VerifyTransferFrom(ctx, txHash, s.c.Token.Address, desk, w.Wallet)
	switch ClassifyTransfer(got, verr) {
	case TransferRefuted:
		return nil, ErrFiatLPRefuted
	case TransferUnproven:
		return nil, ErrFiatLPUnproven
	}
	var rate *big.Rat
	var rateS any
	if w.Kind == models.FiatLPKindPrincipal {
		if rate, err = s.rateFor(ctx, withdrawalCurrency(w)); err != nil {
			return nil, err
		}
		rateS = rate.FloatString(8)
	}
	want := s.quote(w, rate)
	min := want
	if w.Kind == models.FiatLPKindPrincipal {
		min = new(big.Int).Div(new(big.Int).Mul(want, big.NewInt(fiatPayNum)), big.NewInt(fiatPayDen))
	}
	if got.Cmp(min) < 0 {
		return nil, fmt.Errorf("%w: sent %s, quote %s", ErrFiatLPShort, s.units(got), s.units(want))
	}
	now, paid := s.c.Now().UTC(), got.String()
	err = s.c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.FiatLPWithdrawal{}).Where("id = ? AND status = ?", id, models.FiatLPRequested).
			Updates(map[string]any{"status": models.FiatLPPaid, "tx_hash": txHash, "paid_units": paid, "actor_id": adminID,
				"resolved_at": now, "pay_rate": rateS})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrFiatLPState
		}
		raw, _ := json.Marshal(map[string]any{"withdrawalId": id, "txHash": txHash, "paidUnits": paid, "kind": w.Kind, "wallet": w.Wallet})
		return tx.Create(&models.P2PAuditLog{ID: uuid.New(), ActorID: &adminID, Action: fiatAuditPaid, Metadata: datatypes.JSON(raw)}).Error
	})
	if isDuplicateKeyErr(err) { // that transfer already closed another request
		return nil, ErrFiatLPTaken
	}
	if err != nil {
		return nil, err
	}
	if over := new(big.Int).Div(new(big.Int).Mul(want, big.NewInt(102)), big.NewInt(100)); got.Cmp(over) > 0 {
		log.Printf("[fiat-lp] ALERT fiat_lp_overpaid id=%s kind=%s quote=%s paid=%s tx=%s", id, w.Kind, want, paid, txHash)
	}
	log.Printf("[fiat-lp] METRIC fiat_lp_paid id=%s kind=%s units=%s tx=%s", id, w.Kind, paid, txHash)
	return s.getWithdrawal(ctx, id)
}

func (s *FiatLPService) getWithdrawal(ctx context.Context, id uuid.UUID) (*models.FiatLPWithdrawal, error) {
	var w models.FiatLPWithdrawal
	if err := s.c.DB.WithContext(ctx).First(&w, "id = ?", id).Error; err != nil {
		return nil, ErrFiatLPNotFound
	}
	return &w, nil
}

// Reject closes a request that has not been paid; what it reserved returns to
// the LP's position.
func (s *FiatLPService) Reject(ctx context.Context, adminID, id uuid.UUID, note string) (*models.FiatLPWithdrawal, error) {
	now := s.c.Now().UTC()
	note = strings.TrimSpace(note)
	if len(note) > 256 {
		note = note[:256]
	}
	err := s.c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.FiatLPWithdrawal{}).Where("id = ? AND status = ?", id, models.FiatLPRequested).
			Updates(map[string]any{"status": models.FiatLPRejected, "note": note, "actor_id": adminID, "resolved_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrFiatLPState
		}
		raw, _ := json.Marshal(map[string]any{"withdrawalId": id, "note": note})
		return tx.Create(&models.P2PAuditLog{ID: uuid.New(), ActorID: &adminID, Action: fiatAuditRejected, Metadata: datatypes.JSON(raw)}).Error
	})
	if err != nil {
		return nil, err
	}
	s.forgetEarnings()
	return s.getWithdrawal(ctx, id)
}
