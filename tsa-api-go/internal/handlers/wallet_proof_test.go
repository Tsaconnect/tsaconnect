package handlers

// Proof of wallet ownership (wallet_proof.go). The vulnerability these pin: any
// address — a victim's in different letter case, or one no active account held
// — could be linked to the caller's account with no signature, and the
// operator then spent that wallet's Spendable MCGP on the caller's behalf.

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── helpers ──────────────────────────────────────────────────────────────────

type testWallet struct {
	key     *ecdsa.PrivateKey
	Address string // checksummed
}

func newTestWallet(t *testing.T) testWallet {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return testWallet{key: key, Address: crypto.PubkeyToAddress(key.PublicKey).Hex()}
}

// sign is ethers' Wallet.signMessage: EIP-191 personal_sign, v in {27,28}.
func (w testWallet) sign(t *testing.T, message string) string {
	t.Helper()
	sig, err := crypto.Sign(accounts.TextHash([]byte(message)), w.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig[64] += 27
	return hexutil.Encode(sig)
}

func ensureWalletLinkChallenges(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&models.WalletLinkChallenge{}); err != nil {
		t.Fatalf("migrate wallet_link_challenges: %v", err)
	}
}

// issueTestChallenge stores a challenge exactly as IssueWalletChallenge does.
func issueTestChallenge(t *testing.T, db *gorm.DB, userID uuid.UUID, address string, expiresAt time.Time) models.WalletLinkChallenge {
	t.Helper()
	nonce := strings.ReplaceAll(uuid.NewString(), "-", "")
	ch := models.WalletLinkChallenge{
		UserID:    userID,
		Address:   strings.ToLower(address),
		Nonce:     nonce,
		Message:   walletProofMessage(userID, "tester", canonicalWalletAddress(address), nonce, expiresAt),
		ExpiresAt: expiresAt,
	}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("insert challenge: %v", err)
	}
	return ch
}

// signedProof is a fresh challenge for (userID, w) signed by w.
func signedProof(t *testing.T, db *gorm.DB, userID uuid.UUID, w testWallet) walletProof {
	t.Helper()
	ch := issueTestChallenge(t, db, userID, w.Address, time.Now().Add(walletChallengeTTL))
	return walletProof{Nonce: ch.Nonce, Signature: w.sign(t, ch.Message)}
}

// recordWalletProof leaves what an accepted proof leaves: a used challenge.
func recordWalletProof(db *gorm.DB, userID uuid.UUID, address string) error {
	now := time.Now()
	return db.Create(&models.WalletLinkChallenge{
		UserID: userID, Address: strings.ToLower(address), Nonce: strings.ReplaceAll(uuid.NewString(), "-", ""),
		Message: "test proof", ExpiresAt: now, UsedAt: &now,
	}).Error
}

func mustRecordWalletProof(t *testing.T, db *gorm.DB, userID uuid.UUID, address string) {
	t.Helper()
	ensureWalletLinkChallenges(t, db)
	if err := recordWalletProof(db, userID, address); err != nil {
		t.Fatalf("record wallet proof: %v", err)
	}
}

// withProvenWalletDB points config.DB at a fresh database in which userID has
// proven address — for handlers whose only DB use is the proof gate.
func withProvenWalletDB(t *testing.T, userID uuid.UUID, address string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	ensureWalletLinkChallenges(t, db)
	if address != "" {
		mustRecordWalletProof(t, db, userID, address)
	}
	return db
}

func callerWalletWithProof(user *models.User, claimed string, proof walletProof) (string, bool, *httptest.ResponseRecorder) {
	c, w := testGinContext()
	if proof.present() {
		c.Request.Header.Set(walletProofNonceHeader, proof.Nonce)
		c.Request.Header.Set(walletProofSignatureHeader, proof.Signature)
	}
	got, ok := callerWallet(c, user, claimed)
	return got, ok, w
}

func challengeUsed(t *testing.T, db *gorm.DB, nonce string) bool {
	t.Helper()
	var ch models.WalletLinkChallenge
	if err := db.Where("nonce = ?", nonce).First(&ch).Error; err != nil {
		t.Fatalf("load challenge: %v", err)
	}
	return ch.UsedAt != nil
}

// ── callerWallet ─────────────────────────────────────────────────────────────

// The headline exploit: the victim's address in different case slipped past
// an exact-string conflict check and a case-sensitive unique index.
func TestCallerWallet_CaseVariantOfAnotherAccountsWalletIsRefused(t *testing.T) {
	db := newCallerWalletTestDB(t)
	victimWallet := newTestWallet(t)
	victim := models.User{ID: uuid.New(), Username: "victim", WalletAddress: victimWallet.Address, AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &victim)
	attacker := models.User{ID: uuid.New(), Username: "attacker", AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &attacker)

	for _, variant := range []string{strings.ToLower(victimWallet.Address), "0x" + strings.ToUpper(victimWallet.Address[2:])} {
		_, ok, w := callerWalletWithProof(&attacker, variant, walletProof{})
		if ok || w.Code != http.StatusForbidden || responseCode(t, w) != WalletProofRequiredCode {
			t.Fatalf("claim of %s: ok=%v code=%d body=%s, want 403 %s", variant, ok, w.Code, w.Body.String(), WalletProofRequiredCode)
		}
	}
	if got := storedUserWallet(t, db, attacker.ID); got != "" {
		t.Fatalf("attacker's account was linked to %q", got)
	}

	// Once the victim has proved the wallet, even the key's real owner proving
	// it from a second account cannot hold it twice: the conflict check is
	// case-insensitive.
	mustRecordWalletProof(t, db, victim.ID, victimWallet.Address)
	proof := signedProof(t, db, attacker.ID, victimWallet)
	_, ok, w := callerWalletWithProof(&attacker, strings.ToLower(victimWallet.Address), proof)
	if ok || w.Code != http.StatusConflict {
		t.Fatalf("proven case-variant of an active account's wallet: ok=%v code=%d, want 409", ok, w.Code)
	}
	if challengeUsed(t, db, proof.Nonce) {
		t.Fatal("a refused link must leave its nonce unused")
	}
}

// Any address no active account holds used to be linkable with no trick at all.
func TestCallerWallet_UnregisteredAddressWithoutProofIsRefused(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := models.User{ID: uuid.New(), Username: "u", WalletAddress: newTestWallet(t).Address, AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)
	before := user.WalletAddress

	_, ok, w := callerWalletWithProof(&user, newTestWallet(t).Address, walletProof{})
	if ok || w.Code != http.StatusForbidden || responseCode(t, w) != WalletProofRequiredCode {
		t.Fatalf("ok=%v code=%d body=%s, want 403 %s", ok, w.Code, w.Body.String(), WalletProofRequiredCode)
	}
	if got := storedUserWallet(t, db, user.ID); got != before || user.WalletAddress != before {
		t.Fatalf("stored wallet changed to %q", got)
	}
}

func TestCallerWallet_SameWalletInAnyCaseNeedsNoProof(t *testing.T) {
	db := newCallerWalletTestDB(t)
	wallet := newTestWallet(t)
	user := models.User{ID: uuid.New(), Username: "u", WalletAddress: wallet.Address, AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)

	got, ok, w := callerWalletWithProof(&user, strings.ToLower(wallet.Address), walletProof{})
	if !ok || got != wallet.Address {
		t.Fatalf("ok=%v got=%q code=%d, want the stored wallet", ok, got, w.Code)
	}
}

func TestCallerWallet_SignedProofSwitchesAndStoresChecksum(t *testing.T) {
	db := newCallerWalletTestDB(t)
	next := newTestWallet(t)
	user := models.User{ID: uuid.New(), Username: "u", WalletAddress: newTestWallet(t).Address, AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)

	proof := signedProof(t, db, user.ID, next)
	got, ok, w := callerWalletWithProof(&user, strings.ToLower(next.Address), proof)
	if !ok {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got != next.Address || storedUserWallet(t, db, user.ID) != next.Address {
		t.Fatalf("got %q stored %q, want checksummed %q", got, storedUserWallet(t, db, user.ID), next.Address)
	}
	if !challengeUsed(t, db, proof.Nonce) {
		t.Fatal("an accepted proof must be spent")
	}
}

// A wallet the account already proved can be switched back to without a new
// signature — each wallet is signed for once.
func TestCallerWallet_SwitchBackToProvenWalletNeedsNoSignature(t *testing.T) {
	db := newCallerWalletTestDB(t)
	first, second := newTestWallet(t), newTestWallet(t)
	user := models.User{ID: uuid.New(), Username: "u", WalletAddress: second.Address, AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)
	mustRecordWalletProof(t, db, user.ID, first.Address)

	got, ok, w := callerWalletWithProof(&user, first.Address, walletProof{})
	if !ok || got != first.Address {
		t.Fatalf("ok=%v got=%q code=%d body=%s", ok, got, w.Code, w.Body.String())
	}
}

func TestCallerWallet_BadProofsAreRefused(t *testing.T) {
	wallet := newTestWallet(t)
	cases := map[string]func(t *testing.T, db *gorm.DB, user, other *models.User) walletProof{
		"another account's nonce": func(t *testing.T, db *gorm.DB, _, other *models.User) walletProof {
			return signedProof(t, db, other.ID, wallet)
		},
		"a nonce for another address": func(t *testing.T, db *gorm.DB, user, _ *models.User) walletProof {
			ch := issueTestChallenge(t, db, user.ID, newTestWallet(t).Address, time.Now().Add(time.Minute))
			return walletProof{Nonce: ch.Nonce, Signature: wallet.sign(t, ch.Message)}
		},
		"expired": func(t *testing.T, db *gorm.DB, user, _ *models.User) walletProof {
			ch := issueTestChallenge(t, db, user.ID, wallet.Address, time.Now().Add(-time.Second))
			return walletProof{Nonce: ch.Nonce, Signature: wallet.sign(t, ch.Message)}
		},
		"signed by a different key": func(t *testing.T, db *gorm.DB, user, _ *models.User) walletProof {
			ch := issueTestChallenge(t, db, user.ID, wallet.Address, time.Now().Add(time.Minute))
			return walletProof{Nonce: ch.Nonce, Signature: newTestWallet(t).sign(t, ch.Message)}
		},
		"unknown nonce": func(t *testing.T, db *gorm.DB, _, _ *models.User) walletProof {
			return walletProof{Nonce: "deadbeef", Signature: wallet.sign(t, "anything")}
		},
		"garbage signature": func(t *testing.T, db *gorm.DB, user, _ *models.User) walletProof {
			ch := issueTestChallenge(t, db, user.ID, wallet.Address, time.Now().Add(time.Minute))
			return walletProof{Nonce: ch.Nonce, Signature: "0x1234"}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			db := newCallerWalletTestDB(t)
			user := models.User{ID: uuid.New(), Username: "u", AccountStatus: models.AccountStatusActive}
			other := models.User{ID: uuid.New(), Username: "o", AccountStatus: models.AccountStatusActive}
			insertTestUser(t, db, &user)
			insertTestUser(t, db, &other)

			_, ok, w := callerWalletWithProof(&user, wallet.Address, mk(t, db, &user, &other))
			if ok || w.Code != http.StatusForbidden || responseCode(t, w) != WalletProofInvalidCode {
				t.Fatalf("ok=%v code=%d body=%s, want 403 %s", ok, w.Code, w.Body.String(), WalletProofInvalidCode)
			}
			if got := storedUserWallet(t, db, user.ID); got != "" {
				t.Fatalf("linked %q on a bad proof", got)
			}
		})
	}
}

func TestCallerWallet_ReplayedNonceIsRefused(t *testing.T) {
	db := newCallerWalletTestDB(t)
	wallet := newTestWallet(t)
	user := models.User{ID: uuid.New(), Username: "u", AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)

	proof := signedProof(t, db, user.ID, wallet)
	if _, ok, w := callerWalletWithProof(&user, wallet.Address, proof); !ok {
		t.Fatalf("first use: code=%d body=%s", w.Code, w.Body.String())
	}
	if !challengeUsed(t, db, proof.Nonce) {
		t.Fatal("the first use must spend the nonce")
	}
	// The durable proof would let the user switch back without any nonce, so
	// the replay is checked where it is enforced: the nonce itself.
	err := db.Transaction(func(tx *gorm.DB) error {
		return consumeWalletProof(tx, user.ID, wallet.Address, proof, time.Now())
	})
	if !errors.Is(err, errWalletProofInvalid) {
		t.Fatalf("replayed nonce: err=%v, want errWalletProofInvalid", err)
	}
	// And a replay from another account is refused outright.
	intruder := models.User{ID: uuid.New(), Username: "i", AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &intruder)
	if _, ok, w := callerWalletWithProof(&intruder, wallet.Address, proof); ok || w.Code != http.StatusForbidden {
		t.Fatalf("replay by another account: ok=%v code=%d", ok, w.Code)
	}
}

// ── requireOwnWallet / GET /otc/position ─────────────────────────────────────

// A read-only GET used to re-link the account to whatever walletAddress said.
func TestGetOTCPosition_NeverLinks(t *testing.T) {
	db := newCallerWalletTestDB(t)
	positions := &fakeOTCPositions{sellable: map[string]*big.Int{strings.ToLower(sellCapSigner): mcgpWei(1)}}
	h := newSellCapHandler(t, nil, positions)
	user := models.User{ID: uuid.New(), Username: "u", WalletAddress: sellCapSigner, AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, &user)
	// Even a wallet this account proved earlier is not switched to by a GET.
	other := newTestWallet(t)
	mustRecordWalletProof(t, db, user.ID, other.Address)

	get := func(addr string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/otc/position?walletAddress="+addr, nil)
		c.Set("user", &user)
		h.GetOTCPosition(c)
		return w
	}
	if w := get(other.Address); w.Code != http.StatusForbidden {
		t.Fatalf("another wallet: status=%d body=%s, want 403", w.Code, w.Body.String())
	}
	if got := storedUserWallet(t, db, user.ID); got != sellCapSigner {
		t.Fatalf("GET re-linked the account to %q", got)
	}
	if w := get(strings.ToLower(sellCapSigner)); w.Code != http.StatusOK {
		t.Fatalf("own wallet in lower case: status=%d body=%s", w.Code, w.Body.String())
	}
}

// ── requireProvenWallet (operator debits) ────────────────────────────────────

func TestBillHandlerPay_UnprovenWalletIsNotDebited(t *testing.T) {
	svc := &fakeBillSvc{order: models.BillOrder{ID: uuid.New(), Status: models.BillStatusDelivered}}
	user := billTestUser()
	withProvenWalletDB(t, user.ID, "") // no proof recorded
	r := billTestRouter(svc, user)
	w, _ := billDo(t, r, http.MethodPost, "/bills/orders/"+uuid.NewString()+"/pay", nil)
	if w.Code != http.StatusForbidden || svc.payWallet != "" {
		t.Fatalf("code=%d debited=%q, want 403 and no debit", w.Code, svc.payWallet)
	}
	if responseCode(t, w) != WalletProofRequiredCode {
		t.Fatalf("code=%q, want %s", responseCode(t, w), WalletProofRequiredCode)
	}
}

func TestPaySpendable_UnprovenWalletIsNotDebited(t *testing.T) {
	r := newSpendableRig(t)
	r.db.Where("user_id = ?", r.buyer.ID).Delete(&models.WalletLinkChallenge{})
	o := r.order(t, models.OrderStatusPendingPayment, "")
	w, _ := call(t, r.ch.PaySpendable, r.buyer, o.ID, nil)
	if w.Code != http.StatusForbidden || len(r.payer.funded) != 0 {
		t.Fatalf("code=%d funded=%d body=%s, want 403 and no hold", w.Code, len(r.payer.funded), w.Body.String())
	}
}

func TestITSell_UnprovenWalletIsNotDebited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}
	_ = h.it.AcceptITAgreement(t.Context(), user.ID, "", "")

	w := doITSellAs(h, user, map[string]any{"mcgpAmount": "1000000000000000000", "bankCode": "044", "accountNumber": "0123456789"})
	if w.Code != http.StatusForbidden || decodeITResponse(t, w)["code"] != WalletProofRequiredCode {
		t.Fatalf("status=%d body=%s, want 403 %s", w.Code, w.Body.String(), WalletProofRequiredCode)
	}
	var n int64
	db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("a sell intent was created for an unproven wallet")
	}
}

// ── POST /wallet/challenge + /wallet/register, end to end ────────────────────

func newWalletProofRouter(t *testing.T, registered string) (*gin.Engine, *models.User, *gorm.DB) {
	t.Helper()
	r, user, db := newRegisterRig(t, registered)
	r.POST("/wallet/challenge", (&Handlers{}).IssueWalletChallenge)
	return r, user, db
}

func postWalletJSON(t *testing.T, r *gin.Engine, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestWalletChallengeAndRegister_SignedProofLinksTheWallet(t *testing.T) {
	r, user, db := newWalletProofRouter(t, "")
	wallet := newTestWallet(t)

	w, out := postWalletJSON(t, r, "/wallet/challenge", map[string]string{"walletAddress": strings.ToLower(wallet.Address)})
	if w.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", w.Code, w.Body.String())
	}
	data := out["data"].(map[string]any)
	message := data["message"].(string)
	if data["proofRequired"] != true || !strings.Contains(message, wallet.Address) || !strings.Contains(message, user.ID.String()) {
		t.Fatalf("challenge must bind account and address: %v", data)
	}

	// Unsigned: refused.
	if w, _ := postWalletJSON(t, r, "/wallet/register", map[string]string{"walletAddress": wallet.Address}); w.Code != http.StatusForbidden {
		t.Fatalf("unsigned register: %d, want 403", w.Code)
	}

	w, out = postWalletJSON(t, r, "/wallet/register", map[string]string{
		"walletAddress":   wallet.Address,
		"walletNonce":     data["nonce"].(string),
		"walletSignature": wallet.sign(t, message),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("signed register: %d %s", w.Code, w.Body.String())
	}
	if got := storedWallet(t, db, user.ID); got != wallet.Address {
		t.Fatalf("stored %q, want %q", got, wallet.Address)
	}
	if out["data"].(map[string]any)["walletProven"] != true {
		t.Fatalf("response must say the wallet is proven: %v", out)
	}

	// Proven now: the challenge says there is nothing to sign.
	_, out = postWalletJSON(t, r, "/wallet/challenge", map[string]string{"walletAddress": wallet.Address})
	if out["data"].(map[string]any)["proofRequired"] != false {
		t.Fatalf("a proven wallet needs no new challenge: %v", out)
	}
}

// A wallet linked before proofs existed keeps working for reads, and is made
// debit-able by registering it again with a proof.
func TestRegister_SameWalletProofUpgradesALegacyLink(t *testing.T) {
	wallet := newTestWallet(t)
	r, user, db := newWalletProofRouter(t, strings.ToLower(wallet.Address))

	w, out := postWalletJSON(t, r, "/wallet/register", map[string]string{"walletAddress": wallet.Address})
	if w.Code != http.StatusOK || out["data"].(map[string]any)["walletProven"] != false {
		t.Fatalf("unsigned same-wallet register: %d %v", w.Code, out)
	}

	proof := signedProof(t, db, user.ID, wallet)
	w, out = postWalletJSON(t, r, "/wallet/register", map[string]string{
		"walletAddress": wallet.Address, "walletNonce": proof.Nonce, "walletSignature": proof.Signature,
	})
	if w.Code != http.StatusOK || out["data"].(map[string]any)["walletProven"] != true {
		t.Fatalf("signed same-wallet register: %d %v", w.Code, out)
	}
	if got := storedWallet(t, db, user.ID); got != wallet.Address {
		t.Fatalf("legacy lower-case link should now be checksummed, got %q", got)
	}
	if ok, _ := walletProven(db, user.ID, wallet.Address); !ok {
		t.Fatal("proof was not recorded")
	}
}
