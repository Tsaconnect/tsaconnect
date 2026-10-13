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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	ErrAuthorizationWindow    = errors.New("authorization validity window is not acceptable for this order")
	ErrAuthorizationSignature = errors.New("authorization signature does not authorize this deposit")
)

const (
	// authorizationGasUnits is what one relay reserves against the daily budget.
	// transferWithAuthorization measures ~70-90k; reserve with headroom.
	authorizationGasUnits = 120_000
	// minAuthorizationWindow: an authorization about to expire would land after
	// validBefore and revert after we paid for it.
	minAuthorizationWindow = 60 * time.Second
)

type TransferAuthorization struct {
	ValidAfter  uint64
	ValidBefore uint64
	Nonce       [32]byte
	Signature   []byte // 65 bytes: r || s || v
}

type AuthorizationRelayResult struct {
	TxHash      string `json:"txHash,omitempty"`
	AlreadyUsed bool   `json:"alreadyUsed"`
}

// AuthorizationRelayer submits a user's EIP-3009 transferWithAuthorization so
// the stablecoin moves from their wallet straight to the Paycrest deposit
// address while we pay gas. We never hold the funds: the signed message fixes
// from, to and value, and the token contract enforces them.
//
// from/to/value are taken from the ORDER, never from the request. The client
// only supplies the validity window, nonce and signature, so an authorization
// can only ever be relayed as a deposit to the order it was issued for.
type AuthorizationRelayer struct {
	orders sponsorOrderReader
	store  sponsorshipStore
	lanes  map[string]*SponsorLane
	now    func() time.Time

	domainMu sync.Mutex
	domains  map[string]EIP712Domain
}

func NewAuthorizationRelayer(orders sponsorOrderReader, store sponsorshipStore, lanes map[string]*SponsorLane, now func() time.Time) *AuthorizationRelayer {
	if now == nil {
		now = time.Now
	}
	return &AuthorizationRelayer{orders: orders, store: store, lanes: lanes, now: now, domains: map[string]EIP712Domain{}}
}

// Domain returns the verified EIP-712 domain for a lane's token, cached after
// the first successful read. A proxy upgrade can change name/version, so Relay
// drops the entry via forgetDomain when a signature fails to recover.
func (a *AuthorizationRelayer) Domain(ctx context.Context, chainKey, token string) (EIP712Domain, error) {
	lane := a.lanes[strings.ToLower(chainKey)]
	if lane == nil || lane.tokenAddress(token) == "" {
		return EIP712Domain{}, ErrSponsorNotAvailable
	}
	key := lane.ChainKey + ":" + strings.ToUpper(token)
	a.domainMu.Lock()
	cached, ok := a.domains[key]
	a.domainMu.Unlock()
	if ok {
		return cached, nil
	}
	d, err := ReadAuthorizationDomain(ctx, lane.Chain, lane.Chain.ChainID(), lane.tokenAddress(token))
	if err != nil {
		return EIP712Domain{}, err
	}
	a.domainMu.Lock()
	a.domains[key] = d
	a.domainMu.Unlock()
	return d, nil
}

// forgetDomain drops a cached domain, keyed exactly as Domain stores it.
func (a *AuthorizationRelayer) forgetDomain(chainKey, token string) {
	lane := a.lanes[strings.ToLower(chainKey)]
	if lane == nil {
		return
	}
	key := lane.ChainKey + ":" + strings.ToUpper(token)
	a.domainMu.Lock()
	delete(a.domains, key)
	a.domainMu.Unlock()
}

func (a *AuthorizationRelayer) Relay(ctx context.Context, userID uuid.UUID, wallet string, orderID uuid.UUID, auth TransferAuthorization) (AuthorizationRelayResult, error) {
	now := a.now()
	order, err := a.orders.Get(ctx, userID, orderID)
	if err != nil {
		return AuthorizationRelayResult{}, err
	}
	if err := sponsorableOrder(order, wallet, now); err != nil {
		return AuthorizationRelayResult{}, err
	}
	lane := a.lanes[strings.ToLower(order.ChainKey)]
	if lane == nil || lane.Guard.Killed() || lane.tokenAddress(order.Token) == "" || lane.orderMode(ctx, order) != SponsorModeAuthorization {
		return AuthorizationRelayResult{}, ErrSponsorNotAvailable
	}
	token := lane.tokenAddress(order.Token)

	if auth.ValidAfter > uint64(now.Unix()) ||
		auth.ValidBefore < uint64(now.Add(minAuthorizationWindow).Unix()) ||
		// validBefore is client-supplied: enforce the slack the offer issues so
		// a late relay still lands before the order dies.
		auth.ValidBefore > uint64(order.ValidUntil.Add(-offerValiditySlack).Unix()) {
		return AuthorizationRelayResult{}, ErrAuthorizationWindow
	}
	if len(auth.Signature) != 65 {
		return AuthorizationRelayResult{}, ErrAuthorizationSignature
	}
	var r, s [32]byte
	copy(r[:], auth.Signature[0:32])
	copy(s[:], auth.Signature[32:64])
	v := auth.Signature[64]
	if v < 27 {
		v += 27
	}

	decimals, err := lane.Native.GetTokenDecimals(token)
	if err != nil {
		return AuthorizationRelayResult{}, fmt.Errorf("sponsor: token decimals: %w", err)
	}
	value, err := DecimalToBaseUnits(order.TokenAmount, decimals)
	if err != nil {
		return AuthorizationRelayResult{}, fmt.Errorf("sponsor: order amount: %w", err)
	}

	// Verify the signature recovers to the order's own wallet BEFORE trusting
	// anything the client sent, and before the authorizationState read below.
	// authorizationState only proves a NONCE was spent — not that it was spent
	// on THIS order's from/to/value/window, so checking it first would let any
	// nonce this wallet has ever used anywhere (a different order, a plain
	// transfer, a cancelAuthorization) short-circuit as success here.
	domain, err := a.Domain(ctx, lane.ChainKey, order.Token)
	if err != nil {
		return AuthorizationRelayResult{}, err
	}
	signer, err := recoverAuthorizationSigner(lane.Chain.ChainID(), domain, order.WalletAddress, order.DepositAddress, value, auth)
	if err != nil {
		return AuthorizationRelayResult{}, err
	}
	if signer != common.HexToAddress(order.WalletAddress) {
		// Also what a stale cached domain looks like after a token upgrade
		// changed its name or version; drop it so the next request re-reads
		// and re-verifies the domain on-chain.
		a.forgetDomain(lane.ChainKey, order.Token)
		return AuthorizationRelayResult{}, ErrAuthorizationSignature
	}

	from := common.HexToAddress(order.WalletAddress)
	calldata, err := sponsorTokenABIParsed.Pack("transferWithAuthorization",
		from, common.HexToAddress(order.DepositAddress), value,
		new(big.Int).SetUint64(auth.ValidAfter), new(big.Int).SetUint64(auth.ValidBefore),
		auth.Nonce, v, r, s)
	if err != nil {
		return AuthorizationRelayResult{}, fmt.Errorf("sponsor: encode: %w", err)
	}

	// Someone (the user's own wallet, or anyone holding the signature) may have
	// submitted it already. The funds can only have gone to the order's deposit
	// address, so that is success — and relaying again would revert on our gas.
	used, err := a.authorizationUsed(ctx, lane, token, from, auth.Nonce)
	if err != nil {
		return AuthorizationRelayResult{}, err
	}
	if used {
		return AuthorizationRelayResult{AlreadyUsed: true}, nil
	}

	row := &models.GasSponsorship{
		UserID: userID, OrderID: orderID, Kind: models.GasSponsorshipKindAuthorization,
		ChainKey: lane.ChainKey, Token: strings.ToUpper(order.Token), Wallet: order.WalletAddress,
	}
	existing, err := a.store.Reserve(ctx, row)
	if err != nil {
		return AuthorizationRelayResult{}, fmt.Errorf("sponsor: reserve: %w", err)
	}
	if existing != nil {
		if existing.TxHash != "" {
			return AuthorizationRelayResult{TxHash: existing.TxHash}, nil
		}
		return AuthorizationRelayResult{}, ErrSponsorshipInProgress
	}
	releaseRow := func() { releaseSponsorshipRow(ctx, a.store, row.ID) }

	// Floors are alert-only: refused only when the sponsor cannot pay this
	// relay's own fee (its lane re-checks at the send).
	if err := lane.canPay(ctx, authorizationGasUnits, nil); err != nil {
		releaseRow()
		return AuthorizationRelayResult{}, err
	}
	gas, err := lane.Guard.ReserveGas(authorizationGasUnits)
	if err != nil {
		releaseRow()
		return AuthorizationRelayResult{}, err
	}
	releaseOurFault := func() {
		lane.Guard.ReleaseGas(gas)
		releaseRow()
	}

	tokenAddr := common.HexToAddress(token)
	signerAddr := common.HexToAddress(lane.Signer.Address())
	if _, err := lane.Chain.CallContract(ctx, ethereum.CallMsg{From: signerAddr, To: &tokenAddr, Data: calldata}, nil); err != nil {
		if reason, isRevert := revertReason(err); isRevert {
			// The caller's doing (bad signature, spent balance): nothing was
			// broadcast, so hand back the gas and the order slot.
			lane.Guard.ReleaseGas(gas)
			releaseRow()
			return AuthorizationRelayResult{}, fmt.Errorf("%w: %s", ErrSimulationReverted, reason)
		}
		releaseOurFault()
		return AuthorizationRelayResult{}, fmt.Errorf("%w: %v", ErrSimulationUnavailable, err)
	}

	txHash, broadcastAttempted, err := lane.send(ctx, func() ([]byte, error) {
		return lane.Builder.BuildUnsignedTx(lane.Signer.Address(), token, calldata)
	})
	if err != nil {
		if broadcastAttempted {
			// The tx may be pending or mined. Keep the order slot (and the
			// budget it consumed) so a retry cannot relay a second
			// authorization; the same one resolves via authorizationState.
			log.Printf("[ramp-sponsor] ALERT ramp_sponsor_broadcast_uncertain_total=1 order=%s chain=%s err=%v", orderID, lane.ChainKey, err)
			return AuthorizationRelayResult{}, fmt.Errorf("%w: broadcast outcome unknown: %v", ErrSponsorshipInProgress, err)
		}
		releaseOurFault()
		return AuthorizationRelayResult{}, fmt.Errorf("sponsor: broadcast: %w", err)
	}
	recordSponsorshipTx(ctx, a.store, row.ID, txHash, "")
	log.Printf("[ramp-sponsor] relayed authorization order=%s chain=%s token=%s tx=%s", orderID, lane.ChainKey, order.Token, txHash)
	return AuthorizationRelayResult{TxHash: txHash}, nil
}

func (a *AuthorizationRelayer) authorizationUsed(ctx context.Context, lane *SponsorLane, token string, from common.Address, nonce [32]byte) (bool, error) {
	data, err := sponsorTokenABIParsed.Pack("authorizationState", from, nonce)
	if err != nil {
		return false, err
	}
	to := common.HexToAddress(token)
	raw, err := lane.Chain.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return false, fmt.Errorf("%w: authorizationState: %v", ErrSimulationUnavailable, err)
	}
	out, err := sponsorTokenABIParsed.Unpack("authorizationState", raw)
	if err != nil || len(out) != 1 {
		return false, fmt.Errorf("%w: decode authorizationState: %v", ErrSimulationUnavailable, err)
	}
	used, _ := out[0].(bool)
	return used, nil
}

var authorizationTypedDataTypes = apitypes.Types{
	"EIP712Domain": {
		{Name: "name", Type: "string"},
		{Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"},
		{Name: "verifyingContract", Type: "address"},
	},
	"TransferWithAuthorization": {
		{Name: "from", Type: "address"},
		{Name: "to", Type: "address"},
		{Name: "value", Type: "uint256"},
		{Name: "validAfter", Type: "uint256"},
		{Name: "validBefore", Type: "uint256"},
		{Name: "nonce", Type: "bytes32"},
	},
}

// recoverAuthorizationSigner recovers the address that signed the EIP-3009
// TransferWithAuthorization message binding from/to/value/window/nonce to the
// order, hashed under the token's OWN verified domain (never an assumed one).
// A mismatch here means the signature does not authorize this deposit, no
// matter what authorizationState later reports about the nonce.
func recoverAuthorizationSigner(chainID *big.Int, domain EIP712Domain, from, to string, value *big.Int, auth TransferAuthorization) (common.Address, error) {
	if len(auth.Signature) != 65 {
		return common.Address{}, ErrAuthorizationSignature
	}
	td := apitypes.TypedData{
		Types:       authorizationTypedDataTypes,
		PrimaryType: "TransferWithAuthorization",
		Domain: apitypes.TypedDataDomain{
			Name:              domain.Name,
			Version:           domain.Version,
			ChainId:           (*math.HexOrDecimal256)(chainID),
			VerifyingContract: domain.VerifyingContract,
		},
		Message: apitypes.TypedDataMessage{
			"from":        from,
			"to":          to,
			"value":       (*math.HexOrDecimal256)(value),
			"validAfter":  (*math.HexOrDecimal256)(new(big.Int).SetUint64(auth.ValidAfter)),
			"validBefore": (*math.HexOrDecimal256)(new(big.Int).SetUint64(auth.ValidBefore)),
			"nonce":       auth.Nonce[:],
		},
	}
	digest, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		return common.Address{}, fmt.Errorf("%w: hash: %v", ErrAuthorizationSignature, err)
	}

	// crypto.SigToPub wants a recovery id (0/1) in the last byte; ethers signs
	// with v in {27,28}. Work on a copy — the caller's signature is never
	// mutated.
	sig := make([]byte, 65)
	copy(sig, auth.Signature)
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		return common.Address{}, fmt.Errorf("%w: %v", ErrAuthorizationSignature, err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}
