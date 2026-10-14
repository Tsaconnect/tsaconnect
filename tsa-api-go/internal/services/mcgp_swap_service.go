package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"golang.org/x/sync/errgroup"
)

// MCGPSwapService is the backend adapter for McgpEntitlementSwap ("Swap
// Contract B"). It follows the signed-but-not-broadcast pattern used across the
// codebase: the backend PREPARES unsigned txs (the user signs + broadcasts in
// the app), and CONFIRMS by verifying the on-chain event. Reads are eth_call.
//
// Dormant-by-default: when MCGP_SWAP_B_ADDRESS is unset, IsConfigured() is false
// and the routes are not registered.
// chainTxBuilder is the slice of *blockchain.EVMClient that buildUnsignedTx
// needs. An interface so a test can observe when the nonce is read — the whole
// point of signMu is that the read happens inside it, and a fake signer alone
// cannot see that.
type chainTxBuilder interface {
	GetNonce(address string) (uint64, error)
	SuggestGasPrice() (*big.Int, error)
	EstimateGas(msg ethereum.CallMsg) (uint64, error)
	ChainID() *big.Int
}

type MCGPSwapService struct {
	client   *blockchain.EVMClient
	chain    chainTxBuilder // == client in production; nil when there is no client
	address  string
	cfg      *config.Config
	operator ReleaseSigner // signs+broadcasts the IT operator ops; nil = IT dormant

	// operatorLane is the operator key's KeyLane on this chain: the lock held
	// from the nonce read in buildUnsignedTx to the broadcast, and the nonce
	// floor, shared with every other role signing with the same key on Sonic
	// (PM release, gas sponsor, relayer). PendingNonceAt reflects only what the
	// node has already seen, so two concurrent callers would otherwise read the
	// same nonce and the node silently drops one — after which the caller burns
	// a 60s WaitForReceipt on a transaction that will never exist. The
	// scheduler's bill-reconciler settles and reverses on this key while the
	// HTTP request path debits on it.
	//
	// The floor is not the in-process counter this comment once warned about:
	// it moves only after a build succeeded, so an EstimateGas revert (a
	// duplicate debitRef, an over-draw — routine here) never burns a nonce; it
	// steps back on a definite rejection; and it expires after 30 s, so a gap
	// self-heals from PendingNonceAt as before.
	operatorLane *KeyLane

	// operatorVerified is whether operators(<operator>) is true on the swap, as
	// last read (ProbeOperatorRole). Until it is, IT, bills and MCGP checkout
	// read as dormant (HasOperator) and every operator op is refused before it
	// is built: a non-operator key reverts every debit, and a boot-time read
	// error must not leave the money paths open on a guess.
	operatorVerified atomic.Bool

	// receiptWait is one bounded receipt wait (client.WaitForReceipt; tests).
	receiptWait func(txHash string, timeout time.Duration) (*types.Receipt, error)

	// signMu is the operator SEQUENCE lock: it orders operator operations, and
	// OperatorSequence holds it across receipt waits (up to 60 s). It is never
	// the nonce lock — holding the shared KeyLane that long would stall PM
	// releases, pool releases and gas top-ups on the same key. Lock order is
	// always signMu → operatorLane, never the other way.
	signMu sync.Mutex
}

// NewMCGPSwapService returns a service bound to cfg.MCGPSwapBAddress on the
// given Sonic client.
func NewMCGPSwapService(client *blockchain.EVMClient, cfg *config.Config) *MCGPSwapService {
	addr := ""
	if cfg != nil {
		addr = cfg.MCGPSwapBAddress
	}
	svc := &MCGPSwapService{client: client, address: addr, cfg: cfg}
	// Guard against storing a typed-nil in the interface: a nil *EVMClient
	// assigned to chainTxBuilder would compare non-nil and defeat the
	// no-client short-circuit in buildUnsignedTx.
	if client != nil {
		svc.chain = client
	}
	return svc
}

// SetOperatorLane wires the operator EOA through its key's shared KeyLane
// (SignerRegistry). nil ⇒ IT operator ops disabled.
//
// The operator starts unverified: ProbeOperatorRole turns it on once
// operators(<address>) reads true.
func (s *MCGPSwapService) SetOperatorLane(lane *KeyLane) {
	s.operator, s.operatorLane = nil, lane
	if lane != nil {
		s.operator = lane.Signer()
	}
	s.operatorVerified.Store(false)
}

// ErrOperatorRoleMissing: the operator key is wired but not (yet) verified as
// an operator on the swap.
var ErrOperatorRoleMissing = errors.New("the IT operator key is not a verified operator on the swap")

// ProbeOperatorRole reads operators(<operator>) on the swap and records the
// answer. A read error leaves the last answer standing (a boot read error
// leaves it unverified, i.e. dormant) and is returned; so is a "no", so a
// failure-streak alert fires while the role is missing.
func (s *MCGPSwapService) ProbeOperatorRole(ctx context.Context) error {
	if s == nil || s.operator == nil {
		return nil
	}
	addr := s.operator.Address()
	ok, err := s.callBool(ctx, "operators", common.HexToAddress(addr))
	if err != nil {
		return fmt.Errorf("read operators(%s): %w", addr, err)
	}
	was := s.operatorVerified.Swap(ok)
	if ok && !was {
		log.Printf("[config] IT operator %s verified on the swap — IT, bills and MCGP checkout live", addr)
	}
	if !ok {
		if was {
			log.Printf("[config] ALERT signer_role_missing=1 role=it_operator address=%s — operators() turned false; IT dormant", addr)
		}
		return fmt.Errorf("%w: operators(%s) is false — the PM must call addOperator", ErrOperatorRoleMissing, addr)
	}
	return nil
}

// OperatorConfigured reports whether an operator key is wired at all (boot
// wiring of IT, bills and checkout), verified or not.
func (s *MCGPSwapService) OperatorConfigured() bool { return s != nil && s.operator != nil }

// HasOperator reports whether the IT operator signer is configured.
//
// It is false until the key is verified on the swap (ProbeOperatorRole).
func (s *MCGPSwapService) HasOperator() bool {
	return s != nil && s.operator != nil && s.operatorVerified.Load()
}

// OperatorAddress returns the IT operator EOA address (empty if unset).
func (s *MCGPSwapService) OperatorAddress() string {
	if s == nil || s.operator == nil {
		return ""
	}
	return s.operator.Address()
}

// IsConfigured reports whether the swap contract address is set (gates routes).
func (s *MCGPSwapService) IsConfigured() bool {
	return s != nil && s.address != ""
}

// ─────────────────────────── unsigned tx builder ──────────────────────────
// buildUnsignedTx mirrors EscrowService.buildUnsignedTx: in non-production with
// no client it returns a minimal tx (to+data) for tests; otherwise it fills
// nonce/gas/chainId from the chain.
func (s *MCGPSwapService) buildUnsignedTx(from string, callData []byte) ([]byte, error) {
	if s.address == "" {
		return nil, fmt.Errorf("MCGP swap contract address is empty")
	}
	if s.chain == nil {
		if s.cfg.IsProduction() {
			return nil, fmt.Errorf("blockchain client not available")
		}
		tx := blockchain.UnsignedTx{To: s.address, Value: "0", Data: common.Bytes2Hex(callData)}
		return json.Marshal(tx)
	}

	fromAddr := common.HexToAddress(from)
	contractAddr := common.HexToAddress(s.address)

	nonce, err := s.chain.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}
	gasPrice, err := s.chain.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}
	gasLimit, err := s.chain.EstimateGas(ethereum.CallMsg{From: fromAddr, To: &contractAddr, Data: callData})
	if err != nil {
		return nil, fmt.Errorf("gas estimation failed (transaction would likely revert): %w", err)
	}

	tx := blockchain.UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       s.address,
		Value:    "0",
		Data:     common.Bytes2Hex(callData),
		ChainID:  s.chain.ChainID().String(),
	}
	return json.Marshal(tx)
}

func (s *MCGPSwapService) pack(method string, args ...interface{}) ([]byte, error) {
	data, err := parsedMCGPSwapABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	return data, nil
}

// ────────────────────────────── prepare (write) ───────────────────────────

// PrepareBuyPremium builds an unsigned buyPremium(mcgpAmount, maxUsdc) tx.
func (s *MCGPSwapService) PrepareBuyPremium(from string, mcgpAmount, maxUsdc *big.Int) ([]byte, error) {
	data, err := s.pack("buyPremium", mcgpAmount, maxUsdc)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareSpend builds an unsigned spend(to, amount, ref) tx (marketplace order
// or peer pay). ref is a single-use idempotency key, e.g. keccak256(orderID).
func (s *MCGPSwapService) PrepareSpend(from, to string, amount *big.Int, ref [32]byte) ([]byte, error) {
	data, err := s.pack("spend", common.HexToAddress(to), amount, ref)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareSellPremium builds an unsigned sellPremium(mcgpAmount, minUsdc) tx.
func (s *MCGPSwapService) PrepareSellPremium(from string, mcgpAmount, minUsdc *big.Int) ([]byte, error) {
	data, err := s.pack("sellPremium", mcgpAmount, minUsdc)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareSellLegacy builds an unsigned sellLegacy(mcgpAmount, minUsdc) tx
// (redeem-only legacy off-ramp, capped at the snapshot entitlement).
func (s *MCGPSwapService) PrepareSellLegacy(from string, mcgpAmount, minUsdc *big.Int) ([]byte, error) {
	data, err := s.pack("sellLegacy", mcgpAmount, minUsdc)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareWithdrawPremium builds an unsigned withdrawPremiumToWallet(mcgpAmount)
// tx (trustless exit; withdrawn MCGP becomes ordinary, non-spendable wallet MCGP).
func (s *MCGPSwapService) PrepareWithdrawPremium(from string, mcgpAmount *big.Int) ([]byte, error) {
	data, err := s.pack("withdrawPremiumToWallet", mcgpAmount)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// ──────────────────────────── ERC-20 approve ──────────────────────────────
// buyPremium pulls USDC and sellLegacy pulls MCGP, so the app must approve the
// swap contract as spender first. tokenAddr resolves the symbol to its config
// address; the swap contract (s.address) is always the spender.

func (s *MCGPSwapService) tokenAddr(token string) (string, error) {
	if s.cfg == nil {
		return "", fmt.Errorf("config not available")
	}
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "usdc":
		return s.cfg.USDCTokenAddress, nil
	case "mcgp":
		return s.cfg.MCGPTokenAddress, nil
	}
	return "", fmt.Errorf("unsupported token %q (want usdc|mcgp)", token)
}

// Allowance reads the wallet's current ERC-20 allowance to the swap contract.
func (s *MCGPSwapService) Allowance(token, owner string) (*big.Int, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	ta, err := s.tokenAddr(token)
	if err != nil {
		return nil, err
	}
	return s.client.GetTokenAllowance(ta, owner, s.address)
}

// PrepareApprove builds an unsigned ERC-20 approve(swapContract, amount) tx for
// the given token (usdc for buy, mcgp for legacy-sell).
func (s *MCGPSwapService) PrepareApprove(from, token string, amount *big.Int) ([]byte, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	ta, err := s.tokenAddr(token)
	if err != nil {
		return nil, err
	}
	return s.client.PrepareERC20Approve(ta, from, s.address, amount)
}

// ────────────────────────────── reads (eth_call) ──────────────────────────

func (s *MCGPSwapService) callUint(method string, args ...interface{}) (*big.Int, error) {
	return s.callUintCtx(context.Background(), method, args...)
}

// callUintCtx is callUint bounded by ctx as well as the per-call timeout.
func (s *MCGPSwapService) callUintCtx(ctx context.Context, method string, args ...interface{}) (*big.Int, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	data, err := s.pack(method, args...)
	if err != nil {
		return nil, err
	}
	addr := common.HexToAddress(s.address)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := s.client.Client.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("%s call failed: %w", method, err)
	}
	vals, err := parsedMCGPSwapABI.Unpack(method, out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("decode %s: %w", method, err)
	}
	v, ok := vals[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("%s returned unexpected type %T", method, vals[0])
	}
	return v, nil
}

// callAddress and callBool mirror callUint for the V2 role/config reads used by
// the relayer's boot-time separation check and by ops tooling.
func (s *MCGPSwapService) callRaw(ctx context.Context, method string, args ...interface{}) ([]interface{}, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	data, err := s.pack(method, args...)
	if err != nil {
		return nil, err
	}
	addr := common.HexToAddress(s.address)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := s.client.Client.CallContract(cctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("%s call failed: %w", method, err)
	}
	vals, err := parsedMCGPSwapABI.Unpack(method, out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("decode %s: %w", method, err)
	}
	return vals, nil
}

func (s *MCGPSwapService) callAddress(ctx context.Context, method string, args ...interface{}) (string, error) {
	vals, err := s.callRaw(ctx, method, args...)
	if err != nil {
		return "", err
	}
	a, ok := vals[0].(common.Address)
	if !ok {
		return "", fmt.Errorf("%s returned unexpected type %T", method, vals[0])
	}
	return a.Hex(), nil
}

func (s *MCGPSwapService) callBool(ctx context.Context, method string, args ...interface{}) (bool, error) {
	vals, err := s.callRaw(ctx, method, args...)
	if err != nil {
		return false, err
	}
	b, ok := vals[0].(bool)
	if !ok {
		return false, fmt.Errorf("%s returned unexpected type %T", method, vals[0])
	}
	return b, nil
}

// TrustedForwarder reports the forwarder V2 currently trusts (zero = gasless off).
func (s *MCGPSwapService) TrustedForwarder(ctx context.Context) (string, error) {
	return s.callAddress(ctx, "trustedForwarder")
}

// PremiumBalance returns the total spendable premium (bought + credited, 18dp).
func (s *MCGPSwapService) PremiumBalance(user string) (*big.Int, error) {
	return s.callUint("premiumBalanceOf", common.HexToAddress(user))
}

// PremiumBought returns the self-exitable (USDC-bought) premium bucket (18dp).
func (s *MCGPSwapService) PremiumBought(user string) (*big.Int, error) {
	return s.callUint("premiumBought", common.HexToAddress(user))
}

// PremiumCredited returns the spend-only (operator/IT) premium bucket (18dp).
func (s *MCGPSwapService) PremiumCredited(user string) (*big.Int, error) {
	return s.callUint("premiumCredited", common.HexToAddress(user))
}

// PendingSettle returns the in-flight NGN-sell amount awaiting payout (18dp).
func (s *MCGPSwapService) PendingSettle(user string) (*big.Int, error) {
	return s.callUint("pendingSettle", common.HexToAddress(user))
}

// LegacyEntitlement returns user's snapshot legacy entitlement cap (18dp).
func (s *MCGPSwapService) LegacyEntitlement(user string) (*big.Int, error) {
	return s.callUint("legacyEntitlement", common.HexToAddress(user))
}

// LegacyRedeemed returns the cumulative legacy MCGP user has redeemed (18dp).
func (s *MCGPSwapService) LegacyRedeemed(user string) (*big.Int, error) {
	return s.callUint("legacyRedeemed", common.HexToAddress(user))
}

// LegacyRemaining returns the legacy MCGP user may still redeem (18dp).
func (s *MCGPSwapService) LegacyRemaining(user string) (*big.Int, error) {
	return s.callUint("legacyRemaining", common.HexToAddress(user))
}

// Quote returns the USDC (6dp) for mcgpAmount (18dp) at the active phase of kind.
func (s *MCGPSwapService) Quote(kind uint8, mcgpAmount *big.Int) (*big.Int, error) {
	return s.callUint("quote", kind, mcgpAmount)
}

// Backing returns (held MCGP, owed = accountedMcgp). The money invariant the
// reconciler asserts is held >= owed.
func (s *MCGPSwapService) Backing() (held, owed *big.Int, err error) {
	if s.client == nil {
		return nil, nil, fmt.Errorf("blockchain client not available")
	}
	data, err := s.pack("backing")
	if err != nil {
		return nil, nil, err
	}
	addr := common.HexToAddress(s.address)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.client.Client.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("backing call failed: %w", err)
	}
	vals, err := parsedMCGPSwapABI.Unpack("backing", out)
	if err != nil || len(vals) < 2 {
		return nil, nil, fmt.Errorf("decode backing: %w", err)
	}
	held, _ = vals[0].(*big.Int)
	owed, _ = vals[1].(*big.Int)
	if held == nil || owed == nil {
		return nil, nil, fmt.Errorf("backing returned unexpected types")
	}
	return held, owed, nil
}

// ───────────────────── operator ops (IT / NGN, backend-signed) ────────────
// Unlike the user-signed prepare* methods, these are signed+broadcast by the
// operator EOA (s.operator) — the backend drives them after an off-chain NGN
// event. Each returns the broadcast tx hash; callers should then verify the
// event (VerifyOperatorEvent) before writing the audit ledger.

func (s *MCGPSwapService) operatorBroadcast(ctx context.Context, callData []byte) (string, error) {
	if s.operator == nil {
		return "", notBroadcast(fmt.Errorf("IT operator signer not configured"))
	}

	// Operator operations are ordered by signMu; the nonce read and broadcast
	// are covered by the key's lane — see operatorLane.
	s.signMu.Lock()
	defer s.signMu.Unlock()
	return s.operatorBroadcastLocked(ctx, callData)
}

// operatorBroadcastLocked is operatorBroadcast for a caller already holding
// signMu (OperatorSequence).
func (s *MCGPSwapService) operatorBroadcastLocked(ctx context.Context, callData []byte) (string, error) {
	if s.operator == nil {
		return "", notBroadcast(fmt.Errorf("IT operator signer not configured"))
	}
	if !s.operatorVerified.Load() {
		return "", notBroadcast(ErrOperatorRoleMissing)
	}
	// The call itself (it carries the debit/credit ref) is the operation: a
	// retry of the same op never gets a new nonce.
	opCtx := WithOperationRef(ctx, "it_op:"+crypto.Keccak256Hash(callData).Hex())
	hash, _, err := s.operatorLane.Send(opCtx, func() ([]byte, error) {
		unsigned, err := s.buildUnsignedTx(s.operator.Address(), callData)
		if err != nil {
			// Nonce, gas price and gas estimate are all reads: nothing was sent.
			return nil, notBroadcast(err)
		}
		return unsigned, nil
	})
	return hash, err
}

// OperatorCallCancelled is the operator lane's verdict that an operator
// transaction never executed (cancelled at its nonce, proven at the finality
// depth): the op it carried can be decided as not done.
func (s *MCGPSwapService) OperatorCallCancelled(ctx context.Context, txHash string) (bool, error) {
	c, err := s.operatorLane.CallCancelled(ctx, txHash)
	if err != nil || !c {
		return false, err
	}
	// Given up only through RetireOp: nothing live or executed holds it.
	if s.operator == nil || !s.operatorLane.GiveUp(ctx, s.operator.Address(), "", txHash) {
		return false, nil
	}
	return true, nil
}

// OperatorOps are the operator transactions a sequence may send.
type OperatorOps interface {
	OperatorDebitSpendable(ctx context.Context, user string, amount, maxFromBought *big.Int, debitRef [32]byte) (string, error)
	OperatorSettleDebit(ctx context.Context, debitRef [32]byte) (string, error)
	OperatorReverseDebit(ctx context.Context, debitRef [32]byte) (string, error)
	OperatorCreditPremium(ctx context.Context, user string, amount *big.Int, ref [32]byte) (string, error)
}

// OperatorSequence runs fn holding the operator sequence lock (signMu), so the
// transactions fn sends (and the receipts it waits for) are not interleaved
// with any other operator transaction — an IT credit cannot land between a
// settle and the credit that depends on the MCGP it freed. Only operator-key
// transactions are excluded; an owner-key call (withdraw of excess) is not.
// fn must send through ops, never through the service, or it deadlocks.
func (s *MCGPSwapService) OperatorSequence(ctx context.Context, fn func(ops OperatorOps) error) error {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	return fn(lockedOperatorOps{s})
}

type lockedOperatorOps struct{ s *MCGPSwapService }

func (o lockedOperatorOps) send(ctx context.Context, method string, args ...interface{}) (string, error) {
	data, err := o.s.pack(method, args...)
	if err != nil {
		return "", notBroadcast(err)
	}
	return o.s.operatorBroadcastLocked(ctx, data)
}

func (o lockedOperatorOps) OperatorDebitSpendable(ctx context.Context, user string, amount, maxFromBought *big.Int, debitRef [32]byte) (string, error) {
	return o.send(ctx, "debitSpendable", common.HexToAddress(user), amount, maxFromBought, debitRef)
}
func (o lockedOperatorOps) OperatorSettleDebit(ctx context.Context, debitRef [32]byte) (string, error) {
	return o.send(ctx, "settleDebit", debitRef)
}
func (o lockedOperatorOps) OperatorReverseDebit(ctx context.Context, debitRef [32]byte) (string, error) {
	return o.send(ctx, "reverseDebit", debitRef)
}
func (o lockedOperatorOps) OperatorCreditPremium(ctx context.Context, user string, amount *big.Int, ref [32]byte) (string, error) {
	return o.send(ctx, "creditPremium", common.HexToAddress(user), amount, ref)
}

// WindowHeadroom is what one operator credit or debit may still move right now.
type WindowHeadroom struct {
	Paused bool
	// WindowCap is the rolling window's ceiling; WindowRemaining how much of
	// it is left (the whole cap once the window has certainly rolled over).
	WindowCap       *big.Int
	WindowRemaining *big.Int
	// Window is the rolling window's length in seconds.
	Window *big.Int
	// MaxPerTx is the per-call cap (debit headroom only; credits read it from
	// SpendableLimits).
	MaxPerTx *big.Int
}

// windowRollMargin is how far past the on-chain roll-over the local clock
// must be before the window is treated as reset. Local time tracks Sonic
// block time closely but can run ahead; without the margin a clock a few
// seconds fast would read a full window as empty.
const windowRollMargin = 30 * time.Second

// windowRemaining is cap - used, or the whole cap once now is past
// start + window + windowRollMargin. It errs toward "not rolled", i.e.
// toward refusing rather than toward a transaction that reverts.
func windowRemaining(now time.Time, start, window, capAmt, used *big.Int) *big.Int {
	rollAt := new(big.Int).Add(start, window)
	rollAt.Add(rollAt, big.NewInt(int64(windowRollMargin/time.Second)))
	switch {
	case big.NewInt(now.Unix()).Cmp(rollAt) >= 0:
		return new(big.Int).Set(capAmt)
	case capAmt.Cmp(used) > 0:
		return new(big.Int).Sub(capAmt, used)
	default:
		return new(big.Int)
	}
}

// readUints reads each named uint getter concurrently: callers run these
// inside OperatorSequence, where every sequential round trip is time the
// operator signing lock (shared with bills and IT) is held.
func (s *MCGPSwapService) readUints(ctx context.Context, names ...string) ([]*big.Int, error) {
	out := make([]*big.Int, len(names))
	g, gctx := errgroup.WithContext(ctx)
	for i, n := range names {
		g.Go(func() error {
			v, err := s.callUintCtx(gctx, n)
			out[i] = v
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// headroom reads paused alongside the named window getters, concurrently.
func (s *MCGPSwapService) headroom(ctx context.Context, names ...string) (bool, []*big.Int, error) {
	var paused bool
	var vals []*big.Int
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		paused, err = s.callBool(gctx, "paused")
		return err
	})
	g.Go(func() (err error) {
		vals, err = s.readUints(gctx, names...)
		return err
	})
	if err := g.Wait(); err != nil {
		return false, nil, err
	}
	return paused, vals, nil
}

// CreditHeadroom reads pause state and the creditPremium rolling window.
func (s *MCGPSwapService) CreditHeadroom(ctx context.Context) (WindowHeadroom, error) {
	var out WindowHeadroom
	paused, v, err := s.headroom(ctx, "creditWindowCap", "creditedInWindow", "windowStart", "creditWindow")
	if err != nil {
		return out, err
	}
	out.Paused, out.WindowCap, out.Window = paused, v[0], v[3]
	out.WindowRemaining = windowRemaining(time.Now(), v[2], v[3], v[0], v[1])
	return out, nil
}

// DebitHeadroom reads pause state, the per-call debit cap and the debit
// rolling window. reverseDebit does not give window usage back, so a split
// (reverse, then re-debit a share) must check the re-debit fits first.
func (s *MCGPSwapService) DebitHeadroom(ctx context.Context) (WindowHeadroom, error) {
	var out WindowHeadroom
	paused, v, err := s.headroom(ctx, "debitWindowCap", "debitedInWindow", "debitWindowStart", "debitWindow", "maxDebitPerTx")
	if err != nil {
		return out, err
	}
	out.Paused, out.WindowCap, out.Window, out.MaxPerTx = paused, v[0], v[3], v[4]
	out.WindowRemaining = windowRemaining(time.Now(), v[2], v[3], v[0], v[1])
	return out, nil
}

// ErrTxNotBroadcast marks an operator-transaction error raised BEFORE the
// transaction could have reached the network (build, estimate, sign). Only
// these may be read as "the on-chain op definitely did not happen". Any other
// error from a broadcast — a send that timed out, a node that answered with an
// error after accepting the bytes — leaves the outcome unknown, and the caller
// must resolve it from chain state rather than assume failure.
var ErrTxNotBroadcast = errors.New("transaction was not broadcast")

type notBroadcastError struct{ err error }

func (e notBroadcastError) Error() string   { return e.err.Error() }
func (e notBroadcastError) Unwrap() []error { return []error{e.err, ErrTxNotBroadcast} }

func notBroadcast(err error) error { return notBroadcastError{err: err} }

// broadcastUncertainError is the other side of ErrTxNotBroadcast: a signed
// transaction whose broadcast failed and so may be on the network. It carries
// the transaction's hash for a caller that must later ask the chain about it;
// the signer still returns no hash with the error, so callers that read one as
// success are unaffected. Error and Unwrap are the broadcast error's own.
type broadcastUncertainError struct {
	err    error
	txHash string
}

func (e broadcastUncertainError) Error() string { return e.err.Error() }
func (e broadcastUncertainError) Unwrap() error { return e.err }

// uncertainTxHash is the hash of the transaction err says may have been
// broadcast, or "" when err carries none.
func uncertainTxHash(err error) string {
	var u broadcastUncertainError
	if errors.As(err, &u) {
		return u.txHash
	}
	return ""
}

// DebitState mirrors the contract's DebitState enum for a debitRef.
type DebitState uint8

const (
	DebitStateNone     DebitState = 0 // no debit was ever recorded for the ref
	DebitStatePending  DebitState = 1 // debited, awaiting settle or reverse
	DebitStateResolved DebitState = 2 // settled or reversed
)

// DebitRecord is the contract's per-ref debit entry (`debits(bytes32)`).
type DebitRecord struct {
	User   common.Address
	Amount *big.Int
	State  DebitState
}

// DebitRecord reads the on-chain debit keyed by debitRef. It is the positive
// evidence the reconcilers use to decide whether a debit whose broadcast or
// verification was lost actually landed: Pending means it did, None means it
// has not (yet). Both V1 and V2 of the swap expose this public mapping.
func (s *MCGPSwapService) DebitRecord(ctx context.Context, debitRef [32]byte) (DebitRecord, error) {
	vals, err := s.callRaw(ctx, "debits", debitRef)
	if err != nil {
		return DebitRecord{}, err
	}
	if len(vals) != 3 {
		return DebitRecord{}, fmt.Errorf("debits returned %d values, want 3", len(vals))
	}
	user, ok1 := vals[0].(common.Address)
	amount, ok2 := vals[1].(*big.Int)
	state, ok3 := vals[2].(uint8)
	if !ok1 || !ok2 || !ok3 {
		return DebitRecord{}, fmt.Errorf("debits returned unexpected types %T/%T/%T", vals[0], vals[1], vals[2])
	}
	if state > uint8(DebitStateResolved) {
		return DebitRecord{}, fmt.Errorf("debits returned unknown state %d", state)
	}
	return DebitRecord{User: user, Amount: amount, State: DebitState(state)}, nil
}

// UsedCreditRef reports whether a creditPremium ref has already been consumed
// on-chain. It is the read-before-act check that lets a credit whose broadcast
// or verification was lost be retried without ever crediting twice.
func (s *MCGPSwapService) UsedCreditRef(ctx context.Context, ref [32]byte) (bool, error) {
	return s.callBool(ctx, "usedCreditRef", ref)
}

// SpendableLimits are the operator caps that bound one debit and one credit.
type SpendableLimits struct {
	MaxDebitPerTx  *big.Int
	MaxCreditPerTx *big.Int
	MinCredit      *big.Int
}

// SpendableLimits reads the per-transaction operator caps.
func (s *MCGPSwapService) SpendableLimits(ctx context.Context) (SpendableLimits, error) {
	v, err := s.readUints(ctx, "maxDebitPerTx", "maxCreditPerTx", "minCredit")
	if err != nil {
		return SpendableLimits{}, err
	}
	return SpendableLimits{MaxDebitPerTx: v[0], MaxCreditPerTx: v[1], MinCredit: v[2]}, nil
}

// OperatorCreditPremium credits spend-only premium after an NGN payment settles.
func (s *MCGPSwapService) OperatorCreditPremium(ctx context.Context, user string, amount *big.Int, ref [32]byte) (string, error) {
	data, err := s.pack("creditPremium", common.HexToAddress(user), amount, ref)
	if err != nil {
		return "", err
	}
	return s.operatorBroadcast(ctx, data)
}

// OperatorDebitPremium begins an NGN sell (credited → pendingSettle), keyed by debitRef.
func (s *MCGPSwapService) OperatorDebitPremium(ctx context.Context, user string, amount *big.Int, debitRef [32]byte) (string, error) {
	data, err := s.pack("debitPremium", common.HexToAddress(user), amount, debitRef)
	if err != nil {
		return "", err
	}
	return s.operatorBroadcast(ctx, data)
}

// OperatorDebitSpendable begins a debit that may draw on the user's BOUGHT
// premium, up to maxFromBought. The contract drains credited first.
//
// Product rule: spendable MCGP is spendable everywhere — bills and IT sell
// both use this. OperatorDebitPremium remains for callers that must be
// confined to the credited bucket.
func (s *MCGPSwapService) OperatorDebitSpendable(ctx context.Context, user string, amount, maxFromBought *big.Int, debitRef [32]byte) (string, error) {
	data, err := s.pack("debitSpendable", common.HexToAddress(user), amount, maxFromBought, debitRef)
	if err != nil {
		return "", err
	}
	return s.operatorBroadcast(ctx, data)
}

// DebitFromBought reports how much of a pending debit came from bought premium.
func (s *MCGPSwapService) DebitFromBought(debitRef [32]byte) (*big.Int, error) {
	return s.callUint("debitFromBought", debitRef)
}

// OperatorSettleDebit finalizes a debit after a confirmed Nomba payout.
func (s *MCGPSwapService) OperatorSettleDebit(ctx context.Context, debitRef [32]byte) (string, error) {
	data, err := s.pack("settleDebit", debitRef)
	if err != nil {
		return "", err
	}
	return s.operatorBroadcast(ctx, data)
}

// OperatorReverseDebit reverses a debit whose payout failed (user made whole).
func (s *MCGPSwapService) OperatorReverseDebit(ctx context.Context, debitRef [32]byte) (string, error) {
	data, err := s.pack("reverseDebit", debitRef)
	if err != nil {
		return "", err
	}
	return s.operatorBroadcast(ctx, data)
}

// VerifyOperatorEvent confirms an operator event (PremiumCredited /
// PremiumDebitPending / PremiumDebitSettled / PremiumDebitReversed) by matching
// the indexed user + indexed ref in the receipt, returning the MCGP amount.
func (s *MCGPSwapService) VerifyOperatorEvent(eventName, txHash string, user common.Address, ref [32]byte) (*big.Int, error) {
	ev, ok := parsedMCGPSwapABI.Events[eventName]
	if !ok {
		return nil, fmt.Errorf("unknown event %s", eventName)
	}
	receipt, err := s.receipt(txHash)
	if err != nil {
		return nil, err
	}
	contractAddr := common.HexToAddress(s.address)
	for _, lg := range receipt.Logs {
		// topics: [sig, user(indexed), ref(indexed)]
		if lg.Address != contractAddr || len(lg.Topics) < 3 || lg.Topics[0] != ev.ID {
			continue
		}
		if common.BytesToAddress(lg.Topics[1].Bytes()) != user {
			continue
		}
		var evRef [32]byte
		copy(evRef[:], lg.Topics[2].Bytes())
		if evRef != ref {
			continue
		}
		nonIndexed, err := ev.Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode %s data: %w", eventName, err)
		}
		amt, _ := nonIndexed[0].(*big.Int)
		if amt == nil {
			return nil, fmt.Errorf("%s amount not *big.Int", eventName)
		}
		return amt, nil
	}
	return nil, fmt.Errorf("%s for %s not found in receipt", eventName, user.Hex())
}

// ──────────────────────────── confirm (verify event) ──────────────────────

// PremiumSpentEvent is the decoded PremiumSpent log.
type PremiumSpentEvent struct {
	From   common.Address
	To     common.Address
	Amount *big.Int
	Ref    [32]byte
}

// VerifyPremiumSpent confirms a spend by locating the PremiumSpent log in the
// receipt that matches (from, to, ref) and returns it. Used to mark a
// marketplace order paid / a peer payment settled.
func (s *MCGPSwapService) VerifyPremiumSpent(txHash string, from, to common.Address, ref [32]byte) (*PremiumSpentEvent, error) {
	receipt, err := s.receipt(txHash)
	if err != nil {
		return nil, err
	}
	contractAddr := common.HexToAddress(s.address)
	sigID := parsedMCGPSwapABI.Events["PremiumSpent"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 4 || lg.Topics[0] != sigID {
			continue
		}
		evFrom := common.BytesToAddress(lg.Topics[1].Bytes())
		evTo := common.BytesToAddress(lg.Topics[2].Bytes())
		var evRef [32]byte
		copy(evRef[:], lg.Topics[3].Bytes())
		if evFrom != from || evTo != to || evRef != ref {
			continue
		}
		nonIndexed, err := parsedMCGPSwapABI.Events["PremiumSpent"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode PremiumSpent data: %w", err)
		}
		return &PremiumSpentEvent{From: evFrom, To: evTo, Amount: nonIndexed[0].(*big.Int), Ref: evRef}, nil
	}
	return nil, fmt.Errorf("PremiumSpent(from=%s,to=%s) not found in receipt", from.Hex(), to.Hex())
}

// AmountEvent is the decoded form of the single-account swap events
// (PremiumBought / PremiumSold / PremiumWithdrawn / LegacyRedeemed).
type AmountEvent struct {
	Account    common.Address
	McgpAmount *big.Int
	UsdcAmount *big.Int // zero for PremiumWithdrawn (no USDC leg)
}

// VerifyAmountEvent confirms one of the single-account events by name, matching
// the indexed account, and returns the decoded amounts.
func (s *MCGPSwapService) VerifyAmountEvent(eventName, txHash string, account common.Address) (*AmountEvent, error) {
	ev, ok := parsedMCGPSwapABI.Events[eventName]
	if !ok {
		return nil, fmt.Errorf("unknown event %s", eventName)
	}
	receipt, err := s.receipt(txHash)
	if err != nil {
		return nil, err
	}
	contractAddr := common.HexToAddress(s.address)
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 2 || lg.Topics[0] != ev.ID {
			continue
		}
		if common.BytesToAddress(lg.Topics[1].Bytes()) != account {
			continue
		}
		nonIndexed, err := ev.Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode %s data: %w", eventName, err)
		}
		out := &AmountEvent{Account: account, McgpAmount: nonIndexed[0].(*big.Int), UsdcAmount: big.NewInt(0)}
		if len(nonIndexed) >= 2 {
			if u, ok := nonIndexed[1].(*big.Int); ok {
				out.UsdcAmount = u
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s for %s not found in receipt", eventName, account.Hex())
}

func (s *MCGPSwapService) receipt(txHash string) (*types.Receipt, error) {
	if s.client == nil && s.receiptWait == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	// The app confirms right after broadcast, so the tx is usually not mined
	// yet on the node we query. Wait (Sonic blocks are ~1s) rather than failing
	// immediately on a single-shot lookup — following an operator tx to its
	// speed-up, if the key lane re-signed it, so the replacement's receipt
	// answers for the original.
	wait := s.receiptWait
	if wait == nil {
		wait = s.client.WaitForReceipt
	}
	r, final, err := waitFollowingReplacements(context.Background(), s.operatorLane, txHash, 60*time.Second,
		func(_ context.Context, h string, d time.Duration) (*types.Receipt, error) { return wait(h, d) })
	if err != nil {
		if c, cerr := s.operatorLane.CallCancelled(context.Background(), txHash); cerr == nil && c {
			return nil, fmt.Errorf("get receipt %s: %w", txHash, ErrCallCancelled)
		}
		return nil, fmt.Errorf("get receipt %s: %w", txHash, err)
	}
	if r.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("tx %s reverted", final)
	}
	return r, nil
}
