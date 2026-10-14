package services

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func validAuth() TransferAuthorization {
	var nonce [32]byte
	copy(nonce[:], bytes.Repeat([]byte{0xab}, 32))
	sig := common.FromHex("0xa484f5c66a0d60049bdef52b6f804bcbcb48ddf0f86c125202d2e60cbe5806c245510cbb6624175e65fb4b6c73c72a395aa56be2bc4194468039fd8605f9e4061c")
	return TransferAuthorization{
		ValidAfter: 0,
		// This exact value is part of what the fixture signature above was
		// signed over (order from/to/value + this window + the nonce above,
		// domain = Base USDC). Changing it invalidates the signature.
		ValidBefore: 1790000000,
		Nonce:       nonce,
		Signature:   sig,
	}
}

func newTestAuthRelayer(r *sponsorRig) *AuthorizationRelayer {
	return NewAuthorizationRelayer(r.orders, r.store, r.lanes(), func() time.Time { return sponsorTestNow })
}

func TestAuthorizationRelayer_RelaysValidAuthorization(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	got, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if got.TxHash != "0xsponsored" || got.AlreadyUsed {
		t.Fatalf("result = %+v", got)
	}
	if r.signer.broadcastCalls != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.broadcastCalls)
	}
	if !stringsEqualFold(r.builder.to, sponsorTestUSDC) {
		t.Fatalf("tx sent to %s, want the token contract", r.builder.to)
	}
	args, err := sponsorTokenABIParsed.Methods["transferWithAuthorization"].Inputs.Unpack(r.builder.data[4:])
	if err != nil {
		t.Fatal(err)
	}
	if args[0].(common.Address) != common.HexToAddress(sponsorTestUser) ||
		args[1].(common.Address) != common.HexToAddress(sponsorTestDeposit) {
		t.Fatalf("from/to = %v/%v", args[0], args[1])
	}
	if v := args[2].(interface{ String() string }).String(); v != "36421800" {
		t.Fatalf("value = %s, want the order's exact token amount 36421800", v)
	}
	if args[6].(uint8) != 28 {
		t.Fatalf("v = %d, want 28", args[6])
	}
	if r.store.count() != 1 {
		t.Fatalf("store rows = %d, want 1", r.store.count())
	}
}

func TestAuthorizationRelayer_RejectsForeignWallet(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, "0x9999999999999999999999999999999999999999", orderID, validAuth())
	if !errors.Is(err, ErrSponsorWallet) {
		t.Fatalf("want ErrSponsorWallet, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("broadcast for a foreign wallet")
	}
}

func TestAuthorizationRelayer_RejectsTopUpModeRoute(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("want ErrSponsorNotAvailable, got %v", err)
	}
}

// USDC checks a wallet with code through ERC-1271, which a 7702 delegate
// rejects: the relay must refuse before any simulation or reservation.
func TestAuthorizationRelayer_RefusesWalletWithCode(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.chain.code = map[string][]byte{strings.ToLower(sponsorTestUser): eip7702Code}
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("want ErrSponsorNotAvailable, got %v", err)
	}
	if r.signer.broadcastCalls != 0 || r.store.count() != 0 {
		t.Fatal("a wallet with code must not broadcast or reserve the order's slot")
	}
}

func TestAuthorizationRelayer_RejectsValidBeforePastOrderExpiry(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	auth := validAuth()
	auth.ValidBefore = uint64(r.orders.orders[orderID].ValidUntil.Unix()) + 60
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, auth)
	if !errors.Is(err, ErrAuthorizationWindow) {
		t.Fatalf("want ErrAuthorizationWindow, got %v", err)
	}
}

// The offer issues validBefore offerValiditySlack ahead of the order's expiry;
// validBefore is client-supplied, so the relay must enforce that slack itself.
func TestAuthorizationRelayer_RejectsValidBeforeInsideExpirySlack(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	auth := validAuth()
	auth.ValidBefore = uint64(r.orders.orders[orderID].ValidUntil.Unix()) - 10
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, auth)
	if !errors.Is(err, ErrAuthorizationWindow) {
		t.Fatalf("want ErrAuthorizationWindow, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.signer.broadcastCalls)
	}
}

func TestAuthorizationRelayer_RejectsNonSellOrExpiredOrder(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	r.orders.orders[orderID].Status = models.PaycrestStatusSettled
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSponsorOrderState) {
		t.Fatalf("want ErrSponsorOrderState, got %v", err)
	}
}

func TestAuthorizationRelayer_AlreadyUsedIsSuccessWithoutBroadcast(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.chain.used = true
	got, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if err != nil || !got.AlreadyUsed {
		t.Fatalf("result = %+v, err = %v; want AlreadyUsed", got, err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("broadcast an authorization that is already used")
	}
}

func TestAuthorizationRelayer_SecondRelayReturnsFirstHash(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	svc := newTestAuthRelayer(r)
	if _, err := svc.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth()); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if err != nil || got.TxHash != "0xsponsored" {
		t.Fatalf("second relay = %+v, %v", got, err)
	}
	if r.signer.broadcastCalls != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.broadcastCalls)
	}
}

func TestAuthorizationRelayer_SimulationRevertFreesOrderSlot(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.chain.simErr = errors.New("execution reverted: ECRecover: invalid signature")
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSimulationReverted) {
		t.Fatalf("want ErrSimulationReverted, got %v", err)
	}
	if r.signer.broadcastCalls != 0 || r.store.count() != 0 {
		t.Fatalf("broadcasts=%d rows=%d; a reverted request must not broadcast or hold the slot", r.signer.broadcastCalls, r.store.count())
	}
}

// A broadcast error is not proof the tx was rejected (an RPC timeout can hide
// an accepted tx). Freeing the order slot would let a retry relay a second,
// freshly-signed authorization and move the user's deposit twice.
func TestAuthorizationRelayer_UncertainBroadcastKeepsOrderSlot(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	svc := newTestAuthRelayer(r)

	r.signer.err = errors.New("i/o timeout")
	_, err := svc.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("first relay: want ErrSponsorshipInProgress, got %v", err)
	}
	if row := r.store.onlyRow(); row == nil || row.TxHash != "" {
		t.Fatalf("row = %+v; want the order slot kept with an empty tx hash", row)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.calls())
	}

	r.signer.mu.Lock()
	r.signer.err = nil
	r.signer.mu.Unlock()
	_, err = svc.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("retry while pending: want ErrSponsorshipInProgress, got %v", err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d after retry, want 1", r.signer.calls())
	}

	r.chain.mu.Lock()
	r.chain.used = true
	r.chain.mu.Unlock()
	got, err := svc.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if err != nil || !got.AlreadyUsed {
		t.Fatalf("retry once mined = %+v, %v; want AlreadyUsed", got, err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d after mined retry, want 1", r.signer.calls())
	}
}

func TestAuthorizationRelayer_BuildFailureFreesOrderSlot(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.builder.err = errors.New("estimate gas failed")
	if _, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth()); err == nil {
		t.Fatal("want build error")
	}
	if r.store.count() != 0 || r.signer.calls() != 0 {
		t.Fatalf("rows=%d broadcasts=%d; nothing was broadcast, so the slot must be free", r.store.count(), r.signer.calls())
	}
}

func TestAuthorizationRelayer_KilledLaneIsNotAvailable(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	r.lane.Guard.SetKillSwitch(true)
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("want ErrSponsorNotAvailable, got %v", err)
	}
}

func TestAuthorizationRelayer_RejectsSignatureFromAnotherSigner(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.chain.used = true // would short-circuit as AlreadyUsed if checked before the signature
	auth := validAuth()
	auth.Nonce[0] = 0xff // a nonce the fixture signature was never issued over
	_, err := newTestAuthRelayer(r).Relay(context.Background(), r.userID, sponsorTestUser, orderID, auth)
	if !errors.Is(err, ErrAuthorizationSignature) {
		t.Fatalf("want ErrAuthorizationSignature, got %v", err)
	}
	if r.signer.broadcastCalls != 0 || r.store.count() != 0 {
		t.Fatalf("broadcasts=%d rows=%d; a signature that doesn't recover to the wallet must not proceed", r.signer.broadcastCalls, r.store.count())
	}
}

// A domain cached before a token upgrade changed name/version must not fail
// every relay until a restart: a signer mismatch drops the cached entry so the
// next request re-reads it on-chain.
func TestAuthorizationRelayer_StaleCachedDomainSelfHeals(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	svc := newTestAuthRelayer(r)
	svc.domains["base:USDC"] = EIP712Domain{
		Name: "USD Coin", Version: "1", ChainID: "8453",
		VerifyingContract: common.HexToAddress(sponsorTestUSDC).Hex(),
	}

	_, err := svc.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrAuthorizationSignature) {
		t.Fatalf("stale domain: want ErrAuthorizationSignature, got %v", err)
	}
	if r.signer.calls() != 0 {
		t.Fatalf("broadcasts = %d after stale-domain relay, want 0", r.signer.calls())
	}

	got, err := svc.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if err != nil || got.TxHash != "0xsponsored" {
		t.Fatalf("retry: result = %+v, err = %v; want the relay to succeed on a re-read domain", got, err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.calls())
	}
}

// ctxCheckingStore fails Release and SetTxHash exactly like a real GORM store
// would when handed an already-cancelled/expired context, so tests can prove
// the relayer releases/records on a context that survives the caller's own
// ctx dying.
type ctxCheckingStore struct {
	*fakeSponsorStore
}

func (s *ctxCheckingStore) Release(ctx context.Context, id uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeSponsorStore.Release(ctx, id)
}

func (s *ctxCheckingStore) SetTxHash(ctx context.Context, id uuid.UUID, txHash, amountWei string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeSponsorStore.SetTxHash(ctx, id, txHash, amountWei)
}

func TestAuthorizationRelayer_ReleaseSurvivesCancelledContext(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	r.chain.simErr = errors.New("connection refused") // not a revert: releaseOurFault path

	store := &ctxCheckingStore{fakeSponsorStore: r.store}
	svc := NewAuthorizationRelayer(r.orders, store, r.lanes(), func() time.Time { return sponsorTestNow })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // dead before Relay even starts

	_, err := svc.Relay(ctx, r.userID, sponsorTestUser, orderID, validAuth())
	if !errors.Is(err, ErrSimulationUnavailable) {
		t.Fatalf("want ErrSimulationUnavailable, got %v", err)
	}
	if r.store.count() != 0 {
		t.Fatalf("store rows = %d, want 0; release must survive a cancelled request ctx", r.store.count())
	}
}

func TestAuthorizationRelayer_RecordsHashWhenRequestContextCancelled(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)

	store := &ctxCheckingStore{fakeSponsorStore: r.store}
	svc := NewAuthorizationRelayer(r.orders, store, r.lanes(), func() time.Time { return sponsorTestNow })

	// The request dies right as the broadcast returns (a dead ctx never even
	// acquires the key lane, so it cannot be dead before the send).
	ctx, cancel := context.WithCancel(context.Background())
	r.lane.Signer = cancellingSigner{fakeRelaySigner: r.signer, cancel: cancel}

	got, err := svc.Relay(ctx, r.userID, sponsorTestUser, orderID, validAuth())
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if got.TxHash != "0xsponsored" {
		t.Fatalf("result = %+v; want the broadcast hash", got)
	}
	row := r.store.onlyRow()
	if row == nil || row.TxHash != "0xsponsored" {
		t.Fatalf("row = %+v; want TxHash recorded despite the cancelled request ctx", row)
	}
}

func stringsEqualFold(a, b string) bool { return common.HexToAddress(a) == common.HexToAddress(b) }

// cancellingSigner cancels the request's ctx as its broadcast returns.
type cancellingSigner struct {
	*fakeRelaySigner
	cancel context.CancelFunc
}

func (c cancellingSigner) SignAndBroadcast(ctx context.Context, tx []byte) (string, error) {
	defer c.cancel()
	return c.fakeRelaySigner.SignAndBroadcast(ctx, tx)
}
