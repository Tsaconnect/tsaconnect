package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// RelayerService sponsors gas for user-signed EIP-2771 ForwardRequests.
//
// WHAT THIS PROTECTS, AND WHAT IT DOES NOT.
//
// OpenZeppelin's ERC2771Forwarder.execute is PERMISSIONLESS: anyone holding a
// validly signed request can submit it, and pay for it themselves. So none of the
// controls below are access control on the target contract — they exist to stop
// OUR relayer's balance from being drained, and to stop us broadcasting
// transactions that will fail. On-chain, the only guarantees are the forwarder's
// EIP-712 signature check, its per-`from` nonce, the request deadline, and
// whatever the target enforces via _msgSender().
//
// Dormant-by-default: with no signer or no forwarder address configured the
// service reports IsEnabled() == false and every Relay call returns
// ErrRelayerDisabled, which the app treats as an explicit "self-pay instead"
// signal rather than an error.
var (
	ErrRelayerDisabled       = errors.New("relayer not configured")
	ErrSenderMismatch        = errors.New("forward request `from` does not match the authenticated wallet")
	ErrTargetNotAllowed      = errors.New("relaying to this contract is not allowed")
	ErrSelectorNotAllowed    = errors.New("relaying this function is not allowed")
	ErrDeadlineExpired       = errors.New("forward request deadline has passed")
	ErrDeadlineTooFar        = errors.New("forward request deadline is too far in the future")
	ErrGasCeiling            = errors.New("forward request gas exceeds the sponsored ceiling")
	ErrBelowMinSponsored     = errors.New("amount is below the minimum we sponsor gas for")
	ErrRateLimited           = errors.New("too many sponsored transactions; try again shortly")
	ErrDailyCapExceeded      = errors.New("daily gas-sponsorship budget exhausted")
	ErrRelayerUnderfunded    = errors.New("the sending wallet cannot pay for this transaction's gas")
	ErrSimulationReverted    = errors.New("transaction would revert")
	ErrSimulationUnavailable = errors.New("could not simulate the transaction; try again")
	ErrRoleSeparation        = errors.New("relayer key must not also be owner, operator, or guardian")
)

// ForwardRequest mirrors ERC2771Forwarder.ForwardRequestData.
type ForwardRequest struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	Value     *big.Int `json:"value"`
	Gas       uint64   `json:"gas"`
	Deadline  uint64   `json:"deadline"` // uint48 on-chain
	Data      []byte   `json:"data"`
	Signature []byte   `json:"signature"`
}

// AllowedCall is a (target, selector) pair we are willing to sponsor. The
// allowlist is a PAIR list, not a selector set: the same 4 bytes can mean
// something entirely different on another contract.
type AllowedCall struct {
	Target   string
	Selector [4]byte
	// AmountWordIndex is the zero-based 32-byte word of the amount argument
	// within the calldata (after the selector), or -1 when the call has no
	// amount to threshold on. Used for the minimum-sponsored-amount check so
	// dust transfers cannot burn more gas than they move.
	AmountWordIndex int
}

// RelayerLimits are the budget controls. All are required; zero values would
// disable the protection they exist to provide.
type RelayerLimits struct {
	// SponsorLimits (per-user window, daily gas budget, balance floor) are
	// enforced by the SponsorGuard.
	SponsorLimits
	MaxDeadline  time.Duration // reject requests valid for longer than this
	GasCeiling   uint64        // per-request gas cap
	MinSponsored *big.Int      // minimum amount (token base units) worth sponsoring
}

// relayChain is the slice of the EVM client the relayer needs.
type relayChain interface {
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error)
	ChainID() *big.Int
}

// EIP712Domain is the domain the app must sign under. It is READ FROM THE
// FORWARDER rather than assembled from config: an independently-packed domain
// that disagrees with the contract fails as an opaque InvalidSigner, which is
// the same class of bug as the P2P chainId invariant in tsa-dev/CLAUDE.md.
type EIP712Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           string `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// ForwardRequestScaffold is everything the app needs to build and sign a
// request. The app still reconstructs `Data` locally and compares before
// signing — see the app's relay path — so this is a convenience, not a source
// of truth for what is being authorised.
type ForwardRequestScaffold struct {
	Domain   EIP712Domain `json:"domain"`
	To       string       `json:"to"`
	Data     string       `json:"data"`
	Gas      uint64       `json:"gas"`
	Nonce    string       `json:"nonce"`
	Deadline uint64       `json:"deadline"`
	Value    string       `json:"value"`
}

// relayTxBuilder builds the unsigned tx the signer will broadcast.
type relayTxBuilder interface {
	BuildUnsignedTx(from, to string, callData []byte) ([]byte, error)
}

// relayRoleReader is used once at boot to prove the relayer key holds no
// privileged role on the swap contract.
type relayRoleReader interface {
	Owner(ctx context.Context) (string, error)
	IsOperator(ctx context.Context, addr string) (bool, error)
	IsGuardian(ctx context.Context, addr string) (bool, error)
}

// RelayMetrics is an injected recorder so tests can assert on counters instead
// of scraping log lines.
type RelayMetrics interface {
	RelayAttempt(outcome, reason string)
	// RelayGasUnits records the request's declared gas, in GAS UNITS. Deliberately
	// not wei: converting would need a gas price per request, and the budget this
	// feeds is expressed in units. Note it counts the INNER request gas only, so
	// actual S burn is higher by the outer transaction's overhead — size the
	// budget with headroom.
	RelayGasUnits(units *big.Int)
}

// NoopRelayMetrics is the default when nothing is wired.
type NoopRelayMetrics struct{}

func (NoopRelayMetrics) RelayAttempt(string, string) {}
func (NoopRelayMetrics) RelayGasUnits(*big.Int)      {}

const forwarderExecuteABI = `[
{"inputs":[{"components":[{"name":"from","type":"address"},{"name":"to","type":"address"},{"name":"value","type":"uint256"},{"name":"gas","type":"uint256"},{"name":"deadline","type":"uint48"},{"name":"data","type":"bytes"},{"name":"signature","type":"bytes"}],"name":"request","type":"tuple"}],"name":"execute","outputs":[],"stateMutability":"payable","type":"function"},
{"inputs":[{"name":"owner","type":"address"}],"name":"nonces","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
{"inputs":[],"name":"eip712Domain","outputs":[{"name":"fields","type":"bytes1"},{"name":"name","type":"string"},{"name":"version","type":"string"},{"name":"chainId","type":"uint256"},{"name":"verifyingContract","type":"address"},{"name":"salt","type":"bytes32"},{"name":"extensions","type":"uint256[]"}],"stateMutability":"view","type":"function"}
]`

type RelayerService struct {
	signer ReleaseSigner
	// lane is the relayer key's KeyLane on Sonic (lock + nonce floor),
	// shared with every other role on the same key. Before it existed the
	// relayer built and broadcast with no lock at all.
	lane      *KeyLane
	chain     relayChain
	builder   relayTxBuilder
	forwarder string
	allow     []AllowedCall
	limits    RelayerLimits
	metrics   RelayMetrics
	now       func() time.Time
	fwdABI    abi.ABI

	guard *SponsorGuard

	mu     sync.Mutex
	domain EIP712Domain // cached after the first read from the forwarder
}

func NewRelayerService(
	signer ReleaseSigner,
	chain relayChain,
	builder relayTxBuilder,
	forwarder string,
	allow []AllowedCall,
	limits RelayerLimits,
	metrics RelayMetrics,
	now func() time.Time,
) (*RelayerService, error) {
	parsed, err := abi.JSON(strings.NewReader(forwarderExecuteABI))
	if err != nil {
		return nil, fmt.Errorf("relayer: parse forwarder ABI: %w", err)
	}
	if metrics == nil {
		metrics = NoopRelayMetrics{}
	}
	if now == nil {
		now = time.Now
	}
	var lane *KeyLane
	if signer != nil {
		lane = soloLane(signer, chainBalances{chain})
	}
	return &RelayerService{
		signer: signer, lane: lane, chain: chain, builder: builder,
		forwarder: strings.ToLower(strings.TrimSpace(forwarder)),
		allow:     allow, limits: limits, metrics: metrics, now: now, fwdABI: parsed,
		guard: NewSponsorGuard("relayer", limits.SponsorLimits, now),
	}, nil
}

// UseKeyLane makes the relayer sign and send through its key's shared lane
// (SignerRegistry), so it never races another role on the same key for a
// nonce. Boot calls it right after NewRelayerService.
func (r *RelayerService) UseKeyLane(lane *KeyLane) {
	if lane == nil {
		return
	}
	r.signer, r.lane = lane.Signer(), lane
}

// IsEnabled reports whether relaying is available. The app keys its self-pay
// fallback off this, so it must be an explicit signal — never an error.
//
// `killed` is read under the mutex: this is the incident-response lever, and an
// unsynchronised read of it is a data race the detector flags the moment two
// requests overlap.
func (r *RelayerService) IsEnabled() bool {
	if r == nil || r.signer == nil || r.lane == nil || r.chain == nil || r.builder == nil || r.forwarder == "" {
		return false
	}
	return !r.guard.Killed()
}

// SetKillSwitch forces every request to self-pay regardless of configuration.
// For a relayer that is up but misbehaving — bad nonces, unexpected spend — where
// removing the key is slower than flipping a flag.
func (r *RelayerService) SetKillSwitch(on bool) {
	r.guard.SetKillSwitch(on)
}

// AssertRoleSeparation reports an error when the relayer key holds a
// privileged role on the swap — owner, operator or guardian. A relayer key is
// hot (it broadcasts constantly from a server), so a compromise of it must not
// be a compromise of the operator, whose debits move users' MCGP (owner
// decision 2026-10-13: the relayer keeps its own key). Boot treats an error as
// "relayer dormant", never as a reason to stop the whole API.
func (r *RelayerService) AssertRoleSeparation(ctx context.Context, roles relayRoleReader) error {
	if !r.IsEnabled() || roles == nil {
		return nil
	}
	addr := strings.ToLower(r.signer.Address())

	owner, err := roles.Owner(ctx)
	if err != nil {
		return fmt.Errorf("relayer: read owner: %w", err)
	}
	if strings.EqualFold(owner, addr) {
		return fmt.Errorf("%w: relayer is the owner", ErrRoleSeparation)
	}
	isOp, err := roles.IsOperator(ctx, addr)
	if err != nil {
		return fmt.Errorf("relayer: read operator: %w", err)
	}
	if isOp {
		return fmt.Errorf("%w: relayer is an operator", ErrRoleSeparation)
	}
	isGuard, err := roles.IsGuardian(ctx, addr)
	if err != nil {
		return fmt.Errorf("relayer: read guardian: %w", err)
	}
	if isGuard {
		return fmt.Errorf("%w: relayer is a guardian", ErrRoleSeparation)
	}
	return nil
}

// Relay validates and broadcasts a user-signed ForwardRequest.
//
// `authenticatedWallet` is the wallet the API session belongs to. Binding it to
// req.From is the single most important check here: without it the endpoint is a
// free "relay anything anyone signed" oracle.
func (r *RelayerService) Relay(ctx context.Context, authenticatedWallet string, req ForwardRequest) (string, error) {
	if !r.IsEnabled() {
		r.metrics.RelayAttempt("rejected", "disabled")
		return "", ErrRelayerDisabled
	}
	if err := r.validate(authenticatedWallet, req); err != nil {
		return "", err
	}

	execData, err := r.packExecute(req)
	if err != nil {
		r.metrics.RelayAttempt("rejected", "encode")
		return "", err
	}

	if err := r.checkBalance(ctx); err != nil {
		return "", err
	}

	// Reserve the user's slot and the day's gas BEFORE doing anything that can
	// broadcast, then hand back whatever the request did not consume.
	slot, err := r.guard.ReserveUser(req.From)
	if err != nil {
		r.metrics.RelayAttempt("rejected", "rate_limit")
		return "", err
	}
	res, err := r.guard.ReserveGas(req.Gas)
	if err != nil {
		r.guard.ReleaseUser(slot)
		r.metrics.RelayAttempt("rejected", "daily_cap")
		return "", err
	}

	// Nothing below broadcasts without succeeding first, so the budget is always
	// handed back on failure. The rate-limit slot is only handed back when the
	// failure was OURS: a request that would revert is the caller's doing, and
	// letting it retry for free is a griefing vector once relaying is gasless.
	releaseOurFault := func() {
		r.guard.ReleaseGas(res)
		r.guard.ReleaseUser(slot)
	}

	if err := r.simulate(ctx, execData); err != nil {
		if errors.Is(err, ErrSimulationUnavailable) {
			releaseOurFault()
		} else {
			r.guard.ReleaseGas(res)
		}
		return "", err
	}

	// Built and broadcast under the key's lane: the nonce read in the build
	// and the broadcast never interleave with another send on the same key.
	built := false
	txHash, _, err := r.lane.Send(ctx, func() ([]byte, error) {
		unsigned, err := r.builder.BuildUnsignedTx(r.signer.Address(), r.forwarder, execData)
		if err != nil {
			return nil, err
		}
		built = true
		return unsigned, nil
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrRelayerUnderfunded):
		// The key lane's fee gate: this relay's own fee (plus the lane's
		// reservation) is more than the relayer holds.
		releaseOurFault()
		r.metrics.RelayAttempt("rejected", "underfunded")
		return "", err
	case !built:
		releaseOurFault()
		r.metrics.RelayAttempt("rejected", "build")
		return "", fmt.Errorf("relayer: build tx: %w", err)
	case errors.Is(err, ErrTxNotBroadcast):
		// Refused before it reached any mempool (a definite node refusal, a
		// tx the lane would not sign): nothing was broadcast.
		releaseOurFault()
		r.metrics.RelayAttempt("rejected", "not_broadcast")
		return "", fmt.Errorf("relayer: not broadcast: %w", err)
	default:
		releaseOurFault()
		r.metrics.RelayAttempt("error", "broadcast")
		return "", fmt.Errorf("relayer: broadcast: %w", err)
	}

	r.metrics.RelayGasUnits(new(big.Int).SetUint64(req.Gas))
	r.metrics.RelayAttempt("ok", "")
	return txHash, nil
}

func (r *RelayerService) validate(authenticatedWallet string, req ForwardRequest) error {
	if !strings.EqualFold(strings.TrimSpace(authenticatedWallet), strings.TrimSpace(req.From)) {
		r.metrics.RelayAttempt("rejected", "sender_mismatch")
		// Durable trace, not just a counter: a session trying to relay for a
		// DIFFERENT wallet is the signature of a compromised client or a probing
		// attacker, and "it happened N times" is useless after the fact without
		// knowing who and when. Signatures are never logged.
		log.Printf("[relayer] ALERT relayer_sender_mismatch_total=1 session=%s claimed_from=%s",
			strings.ToLower(strings.TrimSpace(authenticatedWallet)), strings.ToLower(strings.TrimSpace(req.From)))
		return ErrSenderMismatch
	}
	if len(req.Signature) == 0 {
		r.metrics.RelayAttempt("rejected", "signature")
		return fmt.Errorf("relayer: missing signature")
	}
	if len(req.Data) < 4 {
		r.metrics.RelayAttempt("rejected", "calldata")
		return fmt.Errorf("relayer: calldata too short")
	}

	allowed, err := r.lookupAllowed(req.To, req.Data)
	if err != nil {
		return err
	}

	now := r.now()
	if req.Deadline <= uint64(now.Unix()) {
		r.metrics.RelayAttempt("rejected", "deadline_expired")
		return ErrDeadlineExpired
	}
	// A signed request is executable by ANYONE until it expires, so a generous
	// deadline is a standing replay window against a much later contract state.
	if req.Deadline > uint64(now.Add(r.limits.MaxDeadline).Unix()) {
		r.metrics.RelayAttempt("rejected", "deadline_too_far")
		return ErrDeadlineTooFar
	}
	if req.Gas > r.limits.GasCeiling {
		r.metrics.RelayAttempt("rejected", "gas_ceiling")
		return ErrGasCeiling
	}
	// The rate-limit slot is NOT claimed here: Relay reserves it once the request
	// is known to be well-formed, so it can be handed back if we fail it later.
	return r.checkMinSponsored(allowed, req.Data)
}

// lookupAllowed returns the matching allowlist entry, or an error naming which
// half of the (target, selector) pair failed. Matching on the pair matters: the
// same 4 bytes mean something entirely different on another contract.
func (r *RelayerService) lookupAllowed(to string, data []byte) (AllowedCall, error) {
	var sel [4]byte
	copy(sel[:], data[:4])

	targetKnown := false
	for _, a := range r.allow {
		if strings.EqualFold(a.Target, to) {
			targetKnown = true
			if a.Selector == sel {
				return a, nil
			}
		}
	}
	if !targetKnown {
		r.metrics.RelayAttempt("rejected", "target")
		return AllowedCall{}, ErrTargetNotAllowed
	}
	r.metrics.RelayAttempt("rejected", "selector")
	return AllowedCall{}, ErrSelectorNotAllowed
}

func (r *RelayerService) checkMinSponsored(a AllowedCall, data []byte) error {
	if a.AmountWordIndex < 0 || r.limits.MinSponsored == nil || r.limits.MinSponsored.Sign() == 0 {
		return nil
	}
	start := 4 + a.AmountWordIndex*32
	if len(data) < start+32 {
		r.metrics.RelayAttempt("rejected", "calldata")
		return fmt.Errorf("relayer: calldata too short for the amount argument")
	}
	amount := new(big.Int).SetBytes(data[start : start+32])
	if amount.Cmp(r.limits.MinSponsored) < 0 {
		r.metrics.RelayAttempt("rejected", "below_min")
		return ErrBelowMinSponsored
	}
	return nil
}

// checkBalance reads the relayer's native balance and raises the refill ALERT
// below its floor. The floor never refuses (owner 2026-10-13): the key lane's
// fee gate decides whether this relay's own transaction can be paid.
func (r *RelayerService) checkBalance(ctx context.Context) error {
	bal, err := r.chain.BalanceAt(ctx, common.HexToAddress(r.signer.Address()), nil)
	if err != nil {
		r.metrics.RelayAttempt("error", "balance")
		return fmt.Errorf("relayer: read balance: %w", err)
	}
	r.guard.AlertFloor(bal)
	return nil
}

// simulate runs the exact call through eth_call first. A revert here means the
// request is bad and must not be broadcast; an RPC failure is transient and the
// caller may retry — conflating the two is how a relayer either burns gas on
// doomed transactions or drops good ones.
func (r *RelayerService) simulate(ctx context.Context, execData []byte) error {
	to := common.HexToAddress(r.forwarder)
	from := common.HexToAddress(r.signer.Address())
	_, err := r.chain.CallContract(ctx, ethereum.CallMsg{From: from, To: &to, Data: execData}, nil)
	if err == nil {
		return nil
	}
	if reason, isRevert := revertReason(err); isRevert {
		r.metrics.RelayAttempt("rejected", "would_revert")
		return fmt.Errorf("%w: %s", ErrSimulationReverted, reason)
	}
	r.metrics.RelayAttempt("error", "simulate_unavailable")
	return fmt.Errorf("%w: %v", ErrSimulationUnavailable, err)
}

// revertReason distinguishes an on-chain revert from an RPC/transport failure.
func revertReason(err error) (string, bool) {
	msg := err.Error()
	for _, marker := range []string{"execution reverted", "revert", "VM Exception"} {
		if strings.Contains(msg, marker) {
			return msg, true
		}
	}
	return "", false
}

func (r *RelayerService) packExecute(req ForwardRequest) ([]byte, error) {
	value := req.Value
	if value == nil {
		value = new(big.Int)
	}
	// The tuple field order must match ERC2771Forwarder.ForwardRequestData.
	arg := struct {
		From      common.Address `json:"from"`
		To        common.Address `json:"to"`
		Value     *big.Int       `json:"value"`
		Gas       *big.Int       `json:"gas"`
		Deadline  *big.Int       `json:"deadline"`
		Data      []byte         `json:"data"`
		Signature []byte         `json:"signature"`
	}{
		From:      common.HexToAddress(req.From),
		To:        common.HexToAddress(req.To),
		Value:     value,
		Gas:       new(big.Int).SetUint64(req.Gas),
		Deadline:  new(big.Int).SetUint64(req.Deadline),
		Data:      req.Data,
		Signature: req.Signature,
	}
	// executeBatch is deliberately never used: with a non-zero refundReceiver it
	// lets individual sub-requests fail silently instead of reverting.
	return r.fwdABI.Pack("execute", arg)
}

// SpentToday exposes the running daily total, in gas units, for monitoring.
func (r *RelayerService) SpentToday() *big.Int {
	return r.guard.SpentToday()
}

// Nonce reads the forwarder's per-`from` nonce. Requests are only valid at the
// current nonce, so a stale one fails signature validation.
func (r *RelayerService) Nonce(ctx context.Context, from string) (*big.Int, error) {
	if !r.IsEnabled() {
		return nil, ErrRelayerDisabled
	}
	data, err := r.fwdABI.Pack("nonces", common.HexToAddress(from))
	if err != nil {
		return nil, fmt.Errorf("relayer: pack nonces: %w", err)
	}
	to := common.HexToAddress(r.forwarder)
	out, err := r.chain.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("relayer: read nonce: %w", err)
	}
	vals, err := r.fwdABI.Unpack("nonces", out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("relayer: decode nonce: %w", err)
	}
	n, ok := vals[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("relayer: unexpected nonce type")
	}
	return n, nil
}

// Domain reads the forwarder's own EIP-712 domain. Read, never assembled: a
// domain the backend guesses at is a signature that silently fails to verify.
func (r *RelayerService) Domain(ctx context.Context) (EIP712Domain, error) {
	if !r.IsEnabled() {
		return EIP712Domain{}, ErrRelayerDisabled
	}
	r.mu.Lock()
	cached := r.domain
	r.mu.Unlock()
	if cached.Name != "" {
		return cached, nil
	}

	data, err := r.fwdABI.Pack("eip712Domain")
	if err != nil {
		return EIP712Domain{}, fmt.Errorf("relayer: pack eip712Domain: %w", err)
	}
	to := common.HexToAddress(r.forwarder)
	out, err := r.chain.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return EIP712Domain{}, fmt.Errorf("relayer: read eip712Domain: %w", err)
	}
	vals, err := r.fwdABI.Unpack("eip712Domain", out)
	if err != nil || len(vals) < 5 {
		return EIP712Domain{}, fmt.Errorf("relayer: decode eip712Domain: %w", err)
	}
	name, _ := vals[1].(string)
	version, _ := vals[2].(string)
	chainID, _ := vals[3].(*big.Int)
	verifying, _ := vals[4].(common.Address)
	if chainID == nil {
		chainID = r.chain.ChainID()
	}
	d := EIP712Domain{
		Name:              name,
		Version:           version,
		ChainID:           chainID.String(),
		VerifyingContract: verifying.Hex(),
	}

	r.mu.Lock()
	r.domain = d
	r.mu.Unlock()
	return d, nil
}

// Scaffold assembles everything the app needs to sign one request.
func (r *RelayerService) Scaffold(ctx context.Context, from, to string, data []byte, gas uint64) (ForwardRequestScaffold, error) {
	if !r.IsEnabled() {
		return ForwardRequestScaffold{}, ErrRelayerDisabled
	}
	domain, err := r.Domain(ctx)
	if err != nil {
		return ForwardRequestScaffold{}, err
	}
	nonce, err := r.Nonce(ctx, from)
	if err != nil {
		return ForwardRequestScaffold{}, err
	}
	if gas == 0 || gas > r.limits.GasCeiling {
		gas = r.limits.GasCeiling
	}
	return ForwardRequestScaffold{
		Domain:   domain,
		To:       to,
		Data:     "0x" + common.Bytes2Hex(data),
		Gas:      gas,
		Nonce:    nonce.String(),
		Deadline: uint64(r.now().Add(r.limits.MaxDeadline).Unix()),
		Value:    "0",
	}, nil
}

// SelectorOf returns the 4-byte selector for a canonical signature such as
// "spend(address,uint256,bytes32)".
func SelectorOf(signature string) [4]byte {
	var out [4]byte
	copy(out[:], crypto.Keccak256([]byte(signature))[:4])
	return out
}
