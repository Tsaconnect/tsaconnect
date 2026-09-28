package handlers

// Proof of wallet ownership.
//
// users.wallet_address is not just a label: the backend operator spends a
// user's Spendable MCGP (IT sell, bills, marketplace pay-spendable) keyed by
// that address with no signature from the user. So an account may only be
// pointed at a wallet whose key the caller can sign with. The flow:
//
//  1. POST /api/wallet/challenge {walletAddress} → a single-use nonce, bound
//     server-side to the caller's account and that address, plus the exact
//     message to sign (EIP-191 personal_sign).
//  2. The app signs the message with the wallet's local key and sends
//     {walletNonce, walletSignature} with POST /api/wallet/register (or the
//     X-Wallet-Proof-Nonce / X-Wallet-Proof-Signature headers on any request
//     that goes through callerWallet).
//
// A consumed challenge is kept as the record that the account proved that
// address, so switching back to a wallet already proven needs no new
// signature, and requireProvenWallet can check a stored wallet before the
// operator spends from it.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

const (
	// WalletProofRequiredCode: the request would link (or spend from) a wallet
	// the account has not proven it owns. The client fetches a challenge, signs
	// it with that wallet's key and retries.
	WalletProofRequiredCode = "WALLET_PROOF_REQUIRED"
	// WalletProofInvalidCode: a proof was sent but is wrong — bad signature,
	// another account's or another address's nonce, expired or already used.
	WalletProofInvalidCode = "WALLET_PROOF_INVALID"

	walletProofRequiredMessage = "Confirm you own this wallet to link it to your account. " +
		"If the app doesn't ask you to, update TSA Connect to the latest version."
	walletProofInvalidMessage = "We couldn't confirm you own this wallet. Please try again."

	walletProofNonceHeader     = "X-Wallet-Proof-Nonce"
	walletProofSignatureHeader = "X-Wallet-Proof-Signature"

	walletChallengeTTL = 10 * time.Minute
	// maxOpenWalletChallenges bounds the rows one account can create.
	maxOpenWalletChallenges = 20
)

var (
	errWalletProofRequired = errors.New("wallet_proof_required")
	errWalletProofInvalid  = errors.New("wallet_proof_invalid")
)

// walletProof is a signed challenge presented with a request.
type walletProof struct {
	Nonce     string
	Signature string
}

func (p walletProof) present() bool { return p.Nonce != "" && p.Signature != "" }

func walletProofFromHeaders(c *gin.Context) walletProof {
	return walletProof{
		Nonce:     strings.TrimSpace(c.GetHeader(walletProofNonceHeader)),
		Signature: strings.TrimSpace(c.GetHeader(walletProofSignatureHeader)),
	}
}

// canonicalWalletAddress is the one form a wallet is stored in: EIP-55
// checksum. Callers must have validated the address first.
func canonicalWalletAddress(addr string) string {
	return common.HexToAddress(strings.TrimSpace(addr)).Hex()
}

// walletProofMessage is what the user signs. It says in plain words what the
// signature does and where it may be signed, and binds it to this account
// (by id and by a name the user recognises), this address and this nonce.
//
// The app refuses to sign anything that does not match this template line for
// line (tsa-app/services/walletProof.ts parseWalletProofMessage) — change both
// together, or every wallet link breaks.
func walletProofMessage(userID uuid.UUID, userLabel, address, nonce string, expiresAt time.Time) string {
	return fmt.Sprintf("TSA Connect: link wallet to account\n\n"+
		"Sign this message to prove you own this wallet and link it to your TSA Connect account. "+
		"It costs no gas and does not move any funds.\n\n"+
		"Wallet: %s\nAccount: %s\nUser: %s\nPurpose: link-wallet\nNonce: %s\nExpires: %s\n\n"+
		"tsa.mcgpchain.com — only sign this inside the TSA Connect app",
		address, userID, userLabel, nonce, expiresAt.UTC().Format(time.RFC3339))
}

// walletProofUserLabel names the account in the signed message so a phished
// signature request shows the user whose account it would link to: the
// username, else a masked email. Control characters are dropped so the label
// can never add a line to the message.
func walletProofUserLabel(user *models.User) string {
	label := strings.TrimSpace(user.Username)
	if label == "" {
		label = maskEmail(user.Email)
	}
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, label)
	if runes := []rune(label); len(runes) > 64 {
		label = string(runes[:64])
	}
	if strings.TrimSpace(label) == "" {
		return "-"
	}
	return label
}

// maskEmail keeps the first letter of the local part and the domain:
// "ojay@gmail.com" → "o***@gmail.com".
func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at < 1 {
		return ""
	}
	return email[:1] + "***" + email[at:]
}

type walletChallengeRequest struct {
	WalletAddress string `json:"walletAddress"`
}

// IssueWalletChallenge handles POST /api/wallet/challenge.
//
// Answers {proofRequired:false} when the account has already proven this
// wallet (nothing to sign), otherwise a fresh nonce and the message to sign.
func (h *Handlers) IssueWalletChallenge(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req walletChallengeRequest
	if err := c.ShouldBindJSON(&req); err != nil || !isValidEthAddress(strings.TrimSpace(req.WalletAddress)) {
		utils.ErrorResponse(c, http.StatusBadRequest, "A valid walletAddress is required")
		return
	}
	address := canonicalWalletAddress(req.WalletAddress)
	db := config.DB

	proven, err := walletProven(db, user.ID, address)
	if err != nil {
		log.Printf("[wallet-proof] proof lookup failed for user %s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not start wallet verification")
		return
	}
	if proven {
		utils.SuccessResponse(c, http.StatusOK, "Wallet already verified", gin.H{
			"proofRequired": false,
			"walletAddress": address,
		})
		return
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not start wallet verification")
		return
	}
	nonce := hex.EncodeToString(raw[:])
	now := time.Now()
	expiresAt := now.Add(walletChallengeTTL)
	challenge := models.WalletLinkChallenge{
		UserID:    user.ID,
		Address:   strings.ToLower(address),
		Nonce:     nonce,
		Message:   walletProofMessage(user.ID, walletProofUserLabel(user), address, nonce, expiresAt),
		ExpiresAt: expiresAt,
	}
	if err := createWalletChallenge(db, user.ID, &challenge, now); err != nil {
		if errors.Is(err, errTooManyWalletChallenges) {
			utils.ErrorResponse(c, http.StatusTooManyRequests, "Too many wallet verifications in progress. Try again in a few minutes.")
			return
		}
		log.Printf("[wallet-proof] challenge insert failed for user %s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not start wallet verification")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sign this message with your wallet", gin.H{
		"proofRequired": true,
		"walletAddress": address,
		"nonce":         nonce,
		"message":       challenge.Message,
		"expiresAt":     expiresAt.UTC(),
	})
}

var errTooManyWalletChallenges = errors.New("too many open wallet challenges")

// createWalletChallenge stores ch unless the user already has
// maxOpenWalletChallenges unused, unexpired ones. The count and the insert run
// in one transaction holding the user's row lock, so concurrent requests queue
// behind each other and cannot all pass the count (SQLite serialises writers
// on its own).
func createWalletChallenge(db *gorm.DB, userID uuid.UUID, ch *models.WalletLinkChallenge, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := lockUserRow(tx, userID); err != nil {
			return err
		}
		// Expired, unused challenges are dead weight; used ones are proofs.
		if err := tx.Where("user_id = ? AND used_at IS NULL AND expires_at < ?", userID, now).
			Delete(&models.WalletLinkChallenge{}).Error; err != nil {
			return err
		}
		var open int64
		if err := tx.Model(&models.WalletLinkChallenge{}).
			Where("user_id = ? AND used_at IS NULL", userID).Count(&open).Error; err != nil {
			return err
		}
		if open >= maxOpenWalletChallenges {
			return errTooManyWalletChallenges
		}
		return tx.Create(ch).Error
	})
}

// lockUserRow takes the caller's users row lock for the rest of tx (Postgres).
func lockUserRow(tx *gorm.DB, userID uuid.UUID) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", userID).Error
}

// walletProven reports whether the account has ever proven it owns address.
func walletProven(db *gorm.DB, userID uuid.UUID, address string) (bool, error) {
	var n int64
	err := db.Model(&models.WalletLinkChallenge{}).
		Where("user_id = ? AND address = ? AND used_at IS NOT NULL", userID, strings.ToLower(address)).
		Count(&n).Error
	return n > 0, err
}

// consumeWalletProof checks proof against the stored challenge — this account,
// this address, unexpired, unused, signed by the address — and spends it. Run
// it inside the transaction that links the wallet so a failed link leaves the
// nonce unused.
func consumeWalletProof(tx *gorm.DB, userID uuid.UUID, address string, proof walletProof, now time.Time) error {
	var ch models.WalletLinkChallenge
	if err := tx.Where("nonce = ?", proof.Nonce).First(&ch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errWalletProofInvalid
		}
		return err
	}
	switch {
	case ch.UserID != userID,
		ch.Address != strings.ToLower(address),
		ch.UsedAt != nil,
		!now.Before(ch.ExpiresAt):
		return errWalletProofInvalid
	}
	signer, err := services.RecoverPersonalSigner(ch.Message, proof.Signature)
	if err != nil || !strings.EqualFold(signer.Hex(), address) {
		return errWalletProofInvalid
	}
	// Conditional on used_at so two requests racing on one nonce cannot both win.
	res := tx.Model(&models.WalletLinkChallenge{}).
		Where("id = ? AND used_at IS NULL", ch.ID).
		Update("used_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errWalletProofInvalid
	}
	// One proof per (account, wallet) is all anything reads; keep the newest.
	return tx.Where("user_id = ? AND address = ? AND used_at IS NOT NULL AND id <> ?", userID, ch.Address, ch.ID).
		Delete(&models.WalletLinkChallenge{}).Error
}

// requireProvenWallet is the gate in front of every operator debit: the
// operator spends from users.wallet_address with no user signature, so that
// address must be one this account proved it owns. Addresses linked before
// proofs existed must be proven once (the app does it on its own).
func requireProvenWallet(c *gin.Context, user *models.User) (string, bool) {
	if user.WalletAddress == "" {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Set up a wallet first", WalletRequiredCode)
		return "", false
	}
	if config.DB == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Could not verify your wallet right now")
		return "", false
	}
	proven, err := walletProven(config.DB, user.ID, user.WalletAddress)
	if err != nil {
		log.Printf("[wallet-proof] proof lookup failed for user %s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not verify your wallet right now")
		return "", false
	}
	if !proven {
		utils.ErrorResponseWithCode(c, http.StatusForbidden, walletProofRequiredMessage, WalletProofRequiredCode)
		return "", false
	}
	return user.WalletAddress, true
}

// respondWalletLinkError maps a claimWalletTx failure to its response.
func respondWalletLinkError(c *gin.Context, user *models.User, err error) {
	switch {
	case errors.Is(err, errWalletProofRequired):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, walletProofRequiredMessage, WalletProofRequiredCode)
	case errors.Is(err, errWalletProofInvalid):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, walletProofInvalidMessage, WalletProofInvalidCode)
	case errors.Is(err, errWalletConflict):
		utils.ErrorResponse(c, http.StatusConflict, "Wallet address already in use by another account")
	default:
		log.Printf("[wallet-proof] wallet link failed for user %s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not link wallet address")
	}
}
