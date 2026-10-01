package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	ErrKYCRequired       = errors.New("identity verification is required for gas sponsorship")
	ErrTopUpUnusedCap    = errors.New("too many gas top-ups went unused today; complete a sale or try again tomorrow")
	ErrTopUpTooExpensive = errors.New("network gas is too expensive to sponsor right now")
	ErrTopUpUnconfirmed  = errors.New("gas is on its way but not confirmed yet; try again in a moment")
	ErrTopUpFailed       = errors.New("gas top-up failed on-chain")
	ErrTopUpDailyCap     = errors.New("you've used today's gas top-ups")
)

const (
	topUpGasUnits = 21_000
	// topUpFallbackTransferGas covers an ERC-20 transfer when estimation fails
	// (BSC USDT measures ~50k; a first transfer to a fresh address costs more).
	topUpFallbackTransferGas = 80_000
	// topUpGasBuffer multiplies today's transfer cost. The app prices its own
	// send seconds later (prepare-tx re-reads the gas price and estimate), and a
	// 1.5x buffer left only ~1.37x of that measured cost in a fork rehearsal.
	// 2x costs a fraction of a cent and survives a price move twice as large.
	topUpGasBuffer = 2
	// intentTopUpDailyCap is how many sell-intent top-ups one user may have
	// claimed since the start of the UTC day, used or not (design §5.2).
	intentTopUpDailyCap = 3
)

type GasTopUpResult struct {
	TxHash    string `json:"txHash,omitempty"`
	AmountWei string `json:"amountWei,omitempty"`
	Skipped   bool   `json:"skipped"`
}

// GasTopUp sends a user just enough native gas to make their own deposit
// transfer, for tokens with no EIP-3009 (BSC USDT/USDC, Polygon USDT) and for
// wallets with code, which cannot use it on any token (see orderMode). Worth
// ~$0.003 on these chains, so the controls are about scripted farming, not the
// unit cost: KYC, once per open order, and a cap on top-ups that went unused.
type GasTopUp struct {
	orders         sponsorOrderReader
	store          sponsorshipStore
	lanes          map[string]*SponsorLane
	unusedCap      int
	receiptTimeout time.Duration
	now            func() time.Time
	// chains resolves a sell intent's source chain id to a lane key: a lane
	// knows its chain by name only. Nil leaves intent top-ups unavailable.
	chains *BlockchainService
}

func NewGasTopUp(orders sponsorOrderReader, store sponsorshipStore, lanes map[string]*SponsorLane, unusedCap int, receiptTimeout time.Duration, now func() time.Time) *GasTopUp {
	if now == nil {
		now = time.Now
	}
	if unusedCap <= 0 {
		unusedCap = 2
	}
	if receiptTimeout <= 0 {
		receiptTimeout = 30 * time.Second
	}
	return &GasTopUp{orders: orders, store: store, lanes: lanes, unusedCap: unusedCap, receiptTimeout: receiptTimeout, now: now}
}

func (g *GasTopUp) TopUp(ctx context.Context, user *models.User, orderID uuid.UUID) (GasTopUpResult, error) {
	if err := requireTopUpUser(user); err != nil {
		return GasTopUpResult{}, err
	}
	now := g.now()
	order, err := g.orders.Get(ctx, user.ID, orderID)
	if err != nil {
		return GasTopUpResult{}, err
	}
	if err := sponsorableOrder(order, user.WalletAddress, now); err != nil {
		return GasTopUpResult{}, err
	}
	lane := g.liveLane(order.ChainKey)
	if lane == nil || lane.tokenAddress(order.Token) == "" || lane.orderMode(ctx, order) != SponsorModeTopUp {
		return GasTopUpResult{}, ErrSponsorNotAvailable
	}

	row := &models.GasSponsorship{
		UserID: user.ID, OrderID: orderID, Kind: models.GasSponsorshipKindTopUp,
		ChainKey: lane.ChainKey, Token: strings.ToUpper(order.Token), Wallet: order.WalletAddress,
	}
	existing, err := g.store.Reserve(ctx, row)
	if err != nil {
		return GasTopUpResult{}, fmt.Errorf("sponsor: reserve: %w", err)
	}
	if existing != nil {
		if existing.TxHash == "" {
			// A broadcast whose outcome was unknown keeps this row with no hash,
			// but that top-up usually landed. If the gas is there, let the user
			// make their own transfer rather than dead-ending the sale. need
			// carries topUpGasBuffer, so compare against the unbuffered cost:
			// the price may have moved since the top-up. The row stays put so a
			// retry still cannot send a second top-up.
			if need, nerr := g.gasNeeded(lane, order); nerr == nil {
				have, berr := lane.Chain.BalanceAt(ctx, common.HexToAddress(order.WalletAddress), nil)
				if berr == nil && new(big.Int).Mul(have, big.NewInt(topUpGasBuffer)).Cmp(need) >= 0 {
					return GasTopUpResult{Skipped: true}, nil
				}
			}
			return GasTopUpResult{}, ErrSponsorshipInProgress
		}
		prior := GasTopUpResult{TxHash: existing.TxHash, AmountWei: existing.AmountWei}
		return prior, g.confirm(ctx, lane, prior.TxHash)
	}
	releaseRow := func() { releaseSponsorshipRow(ctx, g.store, row.ID) }

	need, err := g.gasNeeded(lane, order)
	if err != nil {
		releaseRow()
		return GasTopUpResult{}, err
	}
	return g.sendTopUp(ctx, topUpSend{
		lane: lane, wallet: order.WalletAddress, need: need, ref: "order=" + orderID.String(),
		// The unused cap only applies when gas would actually be sent, so it
		// runs after the balance read: a capped user who already holds enough
		// gas still gets Skipped and can make their own transfer, rather than
		// losing the sale. This order is still awaiting deposit, so it never
		// counts toward the cap; a user holds one open sell order at a time,
		// so at most one of their top-ups is ever undecided.
		admit: func() error {
			unused, err := g.store.CountUnusedTopUpsSince(ctx, user.ID, now.Add(-24*time.Hour))
			if err != nil {
				return fmt.Errorf("sponsor: count unused top-ups: %w", err)
			}
			if unused >= int64(g.unusedCap) {
				return ErrTopUpUnusedCap
			}
			return nil
		},
		// The row was reserved up front, so every path that sends nothing
		// hands it back; a sent or possibly-sent top-up keeps it.
		release: releaseRow,
		record:  func(txHash, amountWei string) { recordSponsorshipTx(ctx, g.store, row.ID, txHash, amountWei) },
	})
}

// SetChainRegistry lets TopUpForIntent find the lane for a chain id.
func (g *GasTopUp) SetChainRegistry(chains *BlockchainService) { g.chains = chains }

// IntentTopUpStore is the sell-intent side of a top-up: the per-user caps,
// the once-per-intent claim on topped_up_at, and the record of what was sent.
// *SellIntentService fills it.
type IntentTopUpStore interface {
	CountToppedUpSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error)
	CountUnusedSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error)
	MarkToppedUp(ctx context.Context, id uuid.UUID, at time.Time) (bool, error)
	ClearToppedUp(ctx context.Context, id uuid.UUID) error
	RecordTopUp(ctx context.Context, id uuid.UUID, txHash, amountWei string) error
}

// TopUpForIntent sends her the source-chain gas to sign a sale's swap when she
// sells an EVM token and holds none of the chain's coin (design §5.2, §5.4).
//
// Only for a sale whose quote budgeted one (intent.TopUpBudgeted). The amount
// is the one the sale's QUOTE budgeted (intent.QuotedTopUpWei):
// that figure was priced into the round trip she was quoted against, so
// sending a fresh one priced off today's gas would spend a budget nobody set.
// Only when the route quoted no gas in wei is there nothing to honour, and the
// quote's own gas limit is priced live instead.
//
// Once per intent (the claim on topped_up_at), at most intentTopUpDailyCap per
// UTC day, and at most unusedCap that went unused in the last 24h.
func (g *GasTopUp) TopUpForIntent(ctx context.Context, user *models.User, intent *models.RampSellIntent, store IntentTopUpStore) (GasTopUpResult, error) {
	if err := requireTopUpUser(user); err != nil {
		return GasTopUpResult{}, err
	}
	if intent == nil || intent.UserID != user.ID {
		return GasTopUpResult{}, ErrSellIntentNotFound
	}
	// Only before the swap is sent: after it, gas cannot help this sale.
	if intent.Status != models.SellIntentSwapping || intent.SwapTxHash != nil {
		return GasTopUpResult{}, ErrSellIntentState
	}
	// Gas goes only to her own reconciled wallet, which must be the address
	// the swap is sent from — never to another address the sale names.
	if !common.IsHexAddress(strings.TrimSpace(intent.SourceAddress)) || !sameAddress(intent.SourceAddress, user.WalletAddress) {
		return GasTopUpResult{}, ErrSponsorWallet
	}
	// Selling the chain's own coin pays its gas out of the coin (§5.4).
	if isNativeTokenAddress(intent.SourceTokenAddress) {
		return GasTopUpResult{}, ErrSponsorNotAvailable
	}
	// Only gas the quote paid for. A sale admitted with no top-up in its g
	// has no budget to send one from — sending anyway spends past B.
	if !intent.TopUpBudgeted {
		return GasTopUpResult{}, fmt.Errorf("%w: this sale's quote budgeted no gas top-up", ErrSponsorNotAvailable)
	}
	lane := g.intentLane(intent.SourceChainID)
	if lane == nil {
		return GasTopUpResult{}, ErrSponsorNotAvailable
	}
	need, err := g.intentGasNeeded(lane, intent)
	if err != nil {
		return GasTopUpResult{}, err
	}

	now := g.now()
	claimed := false
	return g.sendTopUp(ctx, topUpSend{
		lane: lane, wallet: intent.SourceAddress, need: need, ref: "intent=" + intent.ID.String(),
		admit: func() error {
			y, m, d := now.UTC().Date()
			today, err := store.CountToppedUpSince(ctx, user.ID, time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return fmt.Errorf("sponsor: count today's top-ups: %w", err)
			}
			if today >= intentTopUpDailyCap {
				return ErrTopUpDailyCap
			}
			unused, err := store.CountUnusedSince(ctx, user.ID, now.Add(-24*time.Hour))
			if err != nil {
				return fmt.Errorf("sponsor: count unused top-ups: %w", err)
			}
			if unused >= int64(g.unusedCap) {
				return ErrTopUpUnusedCap
			}
			return nil
		},
		// Claimed last, just before the send, so a skip or a refusal never
		// uses up the sale's one top-up. Losing the claim means another
		// request already topped this sale up.
		claim: func() error {
			ok, err := store.MarkToppedUp(ctx, intent.ID, now)
			if errors.Is(err, ErrTopUpDailyCap) {
				return ErrTopUpDailyCap // the atomic cap: another of her top-ups won the last slot
			}
			if err != nil {
				return fmt.Errorf("sponsor: claim intent top-up: %w", err)
			}
			if !ok {
				return ErrTopUpUnusedCap
			}
			claimed = true
			return nil
		},
		// Only a top-up that provably never left gives the claim back.
		release: func() {
			if !claimed {
				return
			}
			clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := store.ClearToppedUp(clearCtx, intent.ID); err != nil {
				log.Printf("[ramp-sponsor] ALERT ramp_intent_topup_clear_failed_total=1 intent=%s err=%v", intent.ID, err)
			}
		},
		// The same trail the order path leaves on its sponsorship row. A
		// failed write loses the record, never the gas or the claim.
		record: func(txHash, amountWei string) {
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := store.RecordTopUp(recordCtx, intent.ID, txHash, amountWei); err != nil {
				log.Printf("[ramp-sponsor] ALERT ramp_intent_topup_record_failed_total=1 intent=%s tx=%s wei=%s err=%v",
					intent.ID, txHash, amountWei, err)
			}
		},
	})
}

// DestTopUpStore is the destination side of a marketplace intent's top-up: its
// own daily cap, the once-per-intent claim on dest_topped_up_at and the record
// of what was sent. *SellIntentService fills it.
type DestTopUpStore interface {
	CountDestToppedUpSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error)
	MarkDestToppedUp(ctx context.Context, id uuid.UUID, at time.Time) (bool, error)
	ClearDestToppedUp(ctx context.Context, id uuid.UUID) error
	RecordDestTopUp(ctx context.Context, id uuid.UUID, txHash, amountWei string) error
}

// ErrDestGasUnavailable: a delivered checkout needs the destination chain's
// coin to pay its sellers, and we cannot send it right now (no lane, a kill
// switch, the float under its floor, a cap). Her tokens are already in her
// wallet either way, so this fails closed with a sentence rather than
// stranding anything: she can add the coin herself and pay as usual.
var ErrDestGasUnavailable = errors.New("we can't cover the network fee on Sonic right now — your funds are in your wallet; add a little S to pay the seller, or try again later")

// TopUpDestForIntent sends her the DESTINATION chain's coin a delivered
// marketplace funding intent budgeted: the approve + escrow of each order
// are hers to sign, and the swap landed her order token but none of the coin
// that pays for them. Same gates as the source-chain top-up — KYC, her own
// wallet, only what the quote priced in, once per intent (the claim on
// dest_topped_up_at), a daily cap — and the same send (sendTopUp: only the
// shortfall against DestGasNeedWei, skipped when she already holds it, the
// lane's ceiling, float floor and daily gas budget). A refusal of ours comes
// back as ErrDestGasUnavailable so the caller says plainly what to do.
func (g *GasTopUp) TopUpDestForIntent(ctx context.Context, user *models.User, intent *models.RampSellIntent, store DestTopUpStore) (GasTopUpResult, error) {
	if err := requireTopUpUser(user); err != nil {
		return GasTopUpResult{}, err
	}
	if intent == nil || intent.UserID != user.ID {
		return GasTopUpResult{}, ErrSellIntentNotFound
	}
	// Only once the funds have landed: before, there is nothing to escrow yet.
	if !intent.IsMarketplace() || intent.Status != models.SellIntentDelivered {
		return GasTopUpResult{}, ErrSellIntentState
	}
	// Her own reconciled wallet, which the delivery paid — never another address.
	if !common.IsHexAddress(strings.TrimSpace(intent.DestAddress)) || !sameAddress(intent.DestAddress, user.WalletAddress) {
		return GasTopUpResult{}, ErrSponsorWallet
	}
	if !intent.DestTopUpBudgeted {
		return GasTopUpResult{}, fmt.Errorf("%w: this checkout's quote budgeted no gas on Sonic", ErrSponsorNotAvailable)
	}
	need, ok := new(big.Int).SetString(strings.TrimSpace(intent.DestGasNeedWei), 10)
	if !ok || need.Sign() <= 0 {
		log.Printf("[ramp-sponsor] ALERT ramp_dest_topup_bad_quote_total=1 intent=%s need=%q", intent.ID, intent.DestGasNeedWei)
		return GasTopUpResult{}, ErrSponsorNotAvailable
	}
	// Never more than the shortfall the quote priced: the budget was spent on
	// that figure, so a balance she has spent down since is not ours to refill.
	priced, ok := new(big.Int).SetString(strings.TrimSpace(intent.DestTopUpMaxWei), 10)
	if !ok || priced.Sign() <= 0 {
		log.Printf("[ramp-sponsor] ALERT ramp_dest_topup_bad_quote_total=1 intent=%s priced=%q", intent.ID, intent.DestTopUpMaxWei)
		return GasTopUpResult{}, ErrSponsorNotAvailable
	}
	lane := g.intentLane(intent.DestChainID)
	if lane == nil {
		return GasTopUpResult{}, ErrDestGasUnavailable
	}
	now := g.now()
	claimed := false
	res, err := g.sendTopUp(ctx, topUpSend{
		lane: lane, wallet: intent.DestAddress, need: need, maxSend: priced, ref: "dest-intent=" + intent.ID.String(),
		admit: func() error {
			y, m, d := now.UTC().Date()
			today, err := store.CountDestToppedUpSince(ctx, user.ID, time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return fmt.Errorf("sponsor: count today's destination top-ups: %w", err)
			}
			if today >= intentTopUpDailyCap {
				return ErrTopUpDailyCap
			}
			return nil
		},
		claim: func() error {
			ok, err := store.MarkDestToppedUp(ctx, intent.ID, now)
			if errors.Is(err, ErrTopUpDailyCap) {
				return ErrTopUpDailyCap
			}
			if err != nil {
				return fmt.Errorf("sponsor: claim destination top-up: %w", err)
			}
			if !ok {
				// Already topped up once for this checkout (or it is no longer
				// delivered): a second send is never made.
				return ErrSponsorshipInProgress
			}
			claimed = true
			return nil
		},
		release: func() {
			if !claimed {
				return
			}
			clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := store.ClearDestToppedUp(clearCtx, intent.ID); err != nil {
				log.Printf("[ramp-sponsor] ALERT ramp_dest_topup_clear_failed_total=1 intent=%s err=%v", intent.ID, err)
			}
		},
		record: func(txHash, amountWei string) {
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := store.RecordDestTopUp(recordCtx, intent.ID, txHash, amountWei); err != nil {
				log.Printf("[ramp-sponsor] ALERT ramp_dest_topup_record_failed_total=1 intent=%s tx=%s wei=%s err=%v",
					intent.ID, txHash, amountWei, err)
			}
		},
	})
	switch {
	case err == nil:
		return res, nil
	case errors.Is(err, ErrRelayerUnderfunded), errors.Is(err, ErrDailyCapExceeded), errors.Is(err, ErrTopUpTooExpensive),
		errors.Is(err, ErrTopUpDailyCap):
		log.Printf("[ramp-sponsor] ALERT ramp_dest_topup_refused_total=1 intent=%s chain=%s err=%v", intent.ID, lane.ChainKey, err)
		return res, fmt.Errorf("%w (%w)", ErrDestGasUnavailable, err)
	}
	return res, err
}

// CanSponsorChain reports a live sponsor lane on chainID — whether a top-up
// there could be sent at all. The float is checked at send time.
func (g *GasTopUp) CanSponsorChain(chainID int64) bool { return g.intentLane(chainID) != nil }

// CheckTopUp is sendTopUp's lane-side refusals, asked BEFORE anything is
// signed so a quote that would later be refused its gas is refused up front:
// a live lane on chainID, `amount` within its per-top-up ceiling, the sponsor's
// float above its floor, and room left in its daily gas budget. It sends and
// reserves nothing, so it can pass and the send still be refused later (the
// float spent meanwhile) — the send re-checks everything.
func (g *GasTopUp) CheckTopUp(ctx context.Context, chainID int64, amount *big.Int) error {
	lane := g.intentLane(chainID)
	if lane == nil {
		return ErrSponsorNotAvailable
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil
	}
	if lane.TopUpMaxWei != nil && amount.Cmp(lane.TopUpMaxWei) > 0 {
		return ErrTopUpTooExpensive
	}
	bal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(lane.Signer.Address()), nil)
	if err != nil {
		return fmt.Errorf("sponsor: read sponsor balance: %w", err)
	}
	if err := lane.Guard.CheckFloor(bal); err != nil {
		return err
	}
	return lane.Guard.CanReserveGas(topUpGasUnits)
}

// intentLane is the live sponsor lane on a sell intent's source chain, or nil.
func (g *GasTopUp) intentLane(chainID int64) *SponsorLane {
	if g.chains == nil {
		return nil
	}
	key, ok := g.chains.ChainNameForID(chainID)
	if !ok {
		return nil
	}
	return g.liveLane(key)
}

// intentGasNeeded is what her wallet must hold to sign the sale's swap.
//
// QuotedTopUpWei already carries topUpGasBuffer (quotedTopUpWei computed it as
// RouteGasWei x topUpGasBuffer, the wei twin of TopUpUSDForRoute), so it is
// used as stored — never re-buffered and never re-priced. A stored value that
// is not a positive integer is corrupt rather than unknown, and is refused
// rather than silently replaced by a live price.
func (g *GasTopUp) intentGasNeeded(lane *SponsorLane, intent *models.RampSellIntent) (*big.Int, error) {
	if quoted := strings.TrimSpace(intent.QuotedTopUpWei); quoted != "" {
		need, ok := new(big.Int).SetString(quoted, 10)
		if !ok || need.Sign() <= 0 {
			log.Printf("[ramp-sponsor] ALERT ramp_intent_topup_bad_quote_total=1 intent=%s quoted=%q", intent.ID, quoted)
			return nil, ErrSponsorNotAvailable
		}
		log.Printf("[ramp-sponsor] intent top-up priced from quote intent=%s chain=%s wei=%s", intent.ID, lane.ChainKey, need)
		return need, nil
	}
	// The route quoted no gas in wei — an unknown, not a zero. Price the
	// quote's own gas limit (the ceiling when it quoted none, as the quote
	// itself did) plus the approve, at today's price, with the same buffer.
	units := routeGasUnits(&LiFiRoute{GasLimit: intent.QuotedGasLimit}) + treasuryERC20Gas
	need, err := lane.gasCost(units)
	if err != nil {
		return nil, fmt.Errorf("sponsor: %w", err)
	}
	need.Mul(need, big.NewInt(topUpGasBuffer))
	log.Printf("[ramp-sponsor] intent top-up priced LIVE (no quoted wei) intent=%s chain=%s units=%d wei=%s", intent.ID, lane.ChainKey, units, need)
	return need, nil
}

// requireTopUpUser is the gate every top-up shares: a signed-in, KYC-verified
// user. Gas is the platform's own money, and KYC is what makes scripted
// farming of it expensive.
func requireTopUpUser(user *models.User) error {
	if user == nil {
		return ErrSponsorWallet
	}
	if user.VerificationStatus != models.VerificationStatusVerified {
		return ErrKYCRequired
	}
	return nil
}

// liveLane is the sponsor lane for a chain key, or nil when there is none or
// its kill switch is on.
func (g *GasTopUp) liveLane(chainKey string) *SponsorLane {
	lane := g.lanes[strings.ToLower(strings.TrimSpace(chainKey))]
	if lane == nil || lane.Guard.Killed() {
		return nil
	}
	return lane
}

// topUpSend is one top-up after its path has decided what the wallet needs.
// The hooks are the only things the order and intent paths do differently.
type topUpSend struct {
	lane   *SponsorLane
	wallet string
	need   *big.Int // what the wallet must hold; only the shortfall is sent
	// maxSend, when set, caps the shortfall sent (the destination top-up's
	// priced figure). Nil: the shortfall, up to the lane's ceiling.
	maxSend *big.Int
	ref     string // "order=<id>" / "intent=<id>", for logs
	// admit runs the path's own caps once gas would really be sent.
	admit func() error
	// claim takes the path's once-per-sale slot, if the path claims late
	// (the intent path). Nil when the slot was taken before need was known.
	claim func() error
	// release gives the slot back when nothing was sent. It is never called
	// once a broadcast was attempted: a tx that may have landed is a spend.
	release func()
	record  func(txHash, amountWei string)
}

// sendTopUp is the send side both paths share: skip when the wallet already
// holds `need`, refuse a shortfall over the lane's ceiling, the path's caps,
// the sponsor's float floor and daily gas budget, then broadcast and wait for
// the receipt.
func (g *GasTopUp) sendTopUp(ctx context.Context, s topUpSend) (GasTopUpResult, error) {
	lane := s.lane
	release := func() {
		if s.release != nil {
			s.release()
		}
	}
	have, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(s.wallet), nil)
	if err != nil {
		release()
		return GasTopUpResult{}, fmt.Errorf("sponsor: read user balance: %w", err)
	}
	if have.Cmp(s.need) >= 0 {
		release()
		return GasTopUpResult{Skipped: true}, nil
	}
	amount := new(big.Int).Sub(s.need, have)
	if s.maxSend != nil && amount.Cmp(s.maxSend) > 0 {
		amount.Set(s.maxSend)
	}
	if lane.TopUpMaxWei != nil && amount.Cmp(lane.TopUpMaxWei) > 0 {
		release()
		log.Printf("[ramp-sponsor] ALERT ramp_topup_too_expensive_total=1 chain=%s %s amount=%s max=%s", lane.ChainKey, s.ref, amount, lane.TopUpMaxWei)
		return GasTopUpResult{}, ErrTopUpTooExpensive
	}
	if s.admit != nil {
		if err := s.admit(); err != nil {
			release()
			return GasTopUpResult{}, err
		}
	}

	sponsorBal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(lane.Signer.Address()), nil)
	if err != nil {
		release()
		return GasTopUpResult{}, fmt.Errorf("sponsor: read sponsor balance: %w", err)
	}
	if err := lane.Guard.CheckFloor(sponsorBal); err != nil {
		release()
		return GasTopUpResult{}, err
	}
	gas, err := lane.Guard.ReserveGas(topUpGasUnits)
	if err != nil {
		release()
		return GasTopUpResult{}, err
	}
	if s.claim != nil {
		if err := s.claim(); err != nil {
			lane.Guard.ReleaseGas(gas)
			return GasTopUpResult{}, err
		}
	}

	txHash, broadcastAttempted, err := lane.send(ctx, func() ([]byte, error) {
		return lane.Native.PrepareNativeTransfer(lane.Signer.Address(), s.wallet, amount)
	})
	if err != nil {
		if broadcastAttempted {
			// The top-up may have landed; keep the slot so a retry cannot send
			// the user a second one.
			log.Printf("[ramp-sponsor] ALERT ramp_sponsor_broadcast_uncertain_total=1 %s chain=%s err=%v", s.ref, lane.ChainKey, err)
			return GasTopUpResult{}, fmt.Errorf("%w: broadcast outcome unknown: %v", ErrSponsorshipInProgress, err)
		}
		lane.Guard.ReleaseGas(gas)
		release()
		return GasTopUpResult{}, fmt.Errorf("sponsor: broadcast top-up: %w", err)
	}
	if s.record != nil {
		s.record(txHash, amount.String())
	}
	log.Printf("[ramp-sponsor] topped up %s chain=%s wallet=%s wei=%s tx=%s", s.ref, lane.ChainKey, s.wallet, amount, txHash)

	result := GasTopUpResult{TxHash: txHash, AmountWei: amount.String()}
	return result, g.confirm(ctx, lane, txHash)
}

// topUpUnused reports whether a top-up's gas was never used for a deposit: the
// sell order it was sent for ended expired, cancelled or failed with nothing
// paid. Any other outcome, or any amount paid, means the deposit happened.
// Statuses that are not yet decided (awaiting_deposit) are not unused.
//
// The order's outcome decides this rather than the wallet's history: it is
// authoritative and already in our DB, where reading what the wallet did with
// the gas would need an indexer per chain and is easy to disguise.
func topUpUnused(status, amountPaid string) bool {
	switch status {
	case models.PaycrestStatusExpired, models.PaycrestStatusCancelled, models.PaycrestStatusFailed:
	default:
		return false
	}
	paid, ok := new(big.Rat).SetString(strings.TrimSpace(amountPaid))
	return !ok || paid.Sign() == 0
}

// gasNeeded is the user's deposit transfer at today's price, times
// topUpGasBuffer so a price move between top-up and send does not strand them
// short: a retry reuses this order's top-up, so there is no second chance.
func (g *GasTopUp) gasNeeded(lane *SponsorLane, order *models.PaycrestOrderRecord) (*big.Int, error) {
	token := lane.tokenAddress(order.Token)
	decimals, err := lane.Native.GetTokenDecimals(token)
	if err != nil {
		return nil, fmt.Errorf("sponsor: token decimals: %w", err)
	}
	value, err := DecimalToBaseUnits(order.TokenAmount, decimals)
	if err != nil {
		return nil, fmt.Errorf("sponsor: order amount: %w", err)
	}
	data, err := sponsorTokenABIParsed.Pack("transfer", common.HexToAddress(order.DepositAddress), value)
	if err != nil {
		return nil, err
	}
	tokenAddr := common.HexToAddress(token)
	limit, err := lane.Native.EstimateGas(ethereum.CallMsg{From: common.HexToAddress(order.WalletAddress), To: &tokenAddr, Data: data})
	if err != nil || limit == 0 {
		limit = topUpFallbackTransferGas
	}
	need, err := lane.gasCost(limit)
	if err != nil {
		return nil, fmt.Errorf("sponsor: %w", err)
	}
	return need.Mul(need, big.NewInt(topUpGasBuffer)), nil
}

// confirm waits for the top-up to land, so the app's own send that follows is
// not priced against a balance that has not arrived yet.
func (g *GasTopUp) confirm(ctx context.Context, lane *SponsorLane, txHash string) error {
	receipt, err := lane.Native.WaitForReceiptContext(ctx, txHash, g.receiptTimeout)
	if err != nil {
		// Keep the cause: an RPC that refuses every receipt read looks exactly
		// like a slow chain from the outside.
		log.Printf("[ramp-sponsor] top-up unconfirmed chain=%s tx=%s: %v", lane.ChainKey, txHash, err)
		return fmt.Errorf("%w: %v", ErrTopUpUnconfirmed, err)
	}
	if receipt.Status != 1 {
		log.Printf("[ramp-sponsor] ALERT ramp_topup_reverted_total=1 chain=%s tx=%s", lane.ChainKey, txHash)
		return ErrTopUpFailed
	}
	return nil
}
