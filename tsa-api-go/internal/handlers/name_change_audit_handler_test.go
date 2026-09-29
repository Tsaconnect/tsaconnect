package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/datatypes"
)

// L4: a re-verification session the change has since moved on from is no
// longer any change's current one — but it is still never first-time KYC's
// to approve, even when its event names the user.
func TestKYCWebhook_ASupersededNameChangeSessionIsIgnored(t *testing.T) {
	db := nameChangeHandlerDB(t)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	ch := openVerifyingChange(t, db, user, "sess-new")
	db.Model(ch).Update("didit_session_ids", datatypes.JSONSlice[string]{"sess-old", "sess-new"})
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret, DiditBaseURL: "http://unused"},
		NameChanges: freeNameChanges(db)}

	for _, status := range []string{"Approved", "Declined"} {
		body := nameChangeWebhookBody("sess-old", status, user.ID.String())
		w := postKYCWebhook(h, body, signedHeaders(body, secret))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "past name-change session") {
			t.Fatalf("%s: %d %s", status, w.Code, w.Body.String())
		}
	}
	if got := reloadUser(t, db, user.ID); got.VerifiedLegalName != "ADA CHIOMA OBI" || got.VerificationStatus != models.VerificationStatusVerified ||
		got.DiditSessionID != user.DiditSessionID {
		t.Fatalf("user %+v", got)
	}
}

// Create names the device's wallet: one other than the proven linked wallet is
// refused with WALLET_PROOF_REQUIRED (it would be linked only with a proof),
// so the fee is never quoted from a wallet the phone cannot sign for.
func TestCreateNameChange_TheDevicesWalletMustBeTheProvenOne(t *testing.T) {
	db := nameChangeHandlerDB(t)
	ensureWalletLinkChallenges(t, db)
	user := verifiedUser(t, db, "ADA CHIOMA OBI")
	hashed, _ := models.HashPassword("correct-horse")
	linked := canonicalWalletAddress("0x87c2422be9a11b71a5c530ecd156134d3b04e3ce")
	db.Model(user).Updates(map[string]any{"password": hashed, "wallet_address": linked})
	user.Password, user.WalletAddress = hashed, linked
	mustRecordWalletProof(t, db, user.ID, linked)
	h := &Handlers{Config: &config.Config{}, NameChanges: paidNameChanges(db)}
	body := map[string]any{"payMethod": "stablecoin", "chain": "base", "token": "USDC", "password": "correct-horse"}

	other := "0x1111111111111111111111111111111111111111"
	w := nameChangeRequest(h, user, http.MethodPost, "/api/users/me/name-change?walletAddress="+other, body)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"`+WalletProofRequiredCode+`"`) {
		t.Fatalf("another device wallet: %d %s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.UserNameChange{}).Count(&n)
	if n != 0 || reloadUser(t, db, user.ID).WalletAddress != linked {
		t.Fatalf("a refused create wrote something: changes=%d", n)
	}
	w = nameChangeRequest(h, user, http.MethodPost, "/api/users/me/name-change?walletAddress="+strings.ToLower(linked), body)
	if w.Code != http.StatusCreated {
		t.Fatalf("the linked wallet: %d %s", w.Code, w.Body.String())
	}
}

// Every business-name refusal carries business_name_invalid and the reason on
// its own, so no client parses the text.
func TestBusinessNameRefusalsCarryTheirCode(t *testing.T) {
	codeOf := func(t *testing.T, raw []byte) (code, message string) {
		t.Helper()
		var body struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("body %s: %v", raw, err)
		}
		return body.Code, body.Message
	}

	t.Run("owner rename", func(t *testing.T) {
		o := newOwnerRig(t)
		res := o.do(http.MethodPut, "/api/pay/merchant/business-name", map[string]any{
			"name": "TꜱA Pay", "password": "correct-horse", "expectedVersion": o.m.Version})
		if res.status != http.StatusUnprocessableEntity || res.Code != services.BusinessNameInvalidCode {
			t.Fatalf("status %d body %s", res.status, res.raw)
		}
		if _, msg := codeOf(t, []byte(res.raw)); strings.HasPrefix(msg, services.ErrPayMerchantInvalid.Error()) || msg == "" {
			t.Fatalf("message %q", msg)
		}
	})

	t.Run("merchant request, submitted and approved", func(t *testing.T) {
		db := setupMerchantTestDB(t)
		prov := &fakePayProvisioner{db: db}
		h := &MerchantRequestHandler{DB: db, PayMerchants: prov}
		user := createMerchantTestUser(t, db, "user")
		verifyMerchantTestUser(t, db, user)
		in := validPayAcceptanceInput()
		in["businessName"] = "ꓢupport Desk"
		w, c := newMerchantContext("POST", "/api/merchant-requests", in, user)
		h.SubmitMerchantRequest(c)
		if code, msg := codeOf(t, w.Body.Bytes()); w.Code != http.StatusBadRequest || code != services.BusinessNameInvalidCode ||
			!strings.HasPrefix(msg, "A business name") {
			t.Fatalf("submit: %d %s", w.Code, w.Body.String())
		}

		in["businessName"] = "Sky Air"
		w, c = newMerchantContext("POST", "/api/merchant-requests", in, user)
		h.SubmitMerchantRequest(c)
		var stored models.MerchantRequest
		db.First(&stored, "user_id = ?", user.ID)
		// Approved before, its setup never finished, under a name the rule
		// now refuses: it can't be rejected any more, so the way on offered
		// is a super admin's rename — not "reject".
		db.Model(&stored).Updates(map[string]any{"business_name": "TSA Pay Refunds", "status": models.MerchantRequestStatusApproved})
		admin := createMerchantTestUser(t, db, models.RoleAdmin)
		w = approvePay(h, admin, stored.ID.String())
		code, msg := codeOf(t, w.Body.Bytes())
		if w.Code != http.StatusUnprocessableEntity || code != services.BusinessNameInvalidCode ||
			strings.Contains(msg, "Reject") || !strings.Contains(msg, "super admin") || prov.calls != 0 {
			t.Fatalf("approved-but-unprovisioned: %d %s", w.Code, w.Body.String())
		}
		// An admin may not rename; a super admin may, and it provisions.
		rename := func(u *models.User, name string) int {
			w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+stored.ID.String()+"/approve",
				map[string]any{"note": "renamed", "businessName": name}, u)
			c.Params = gin.Params{{Key: "id", Value: stored.ID.String()}}
			h.ApproveMerchantRequest(c)
			return w.Code
		}
		if got := rename(admin, "Sky Air Travel"); got != http.StatusForbidden || prov.calls != 0 {
			t.Fatalf("an admin renaming: %d", got)
		}
		super := createMerchantTestUser(t, db, models.RoleSuperAdmin)
		if got := rename(super, "TSA  Pay Two"); got != http.StatusUnprocessableEntity || prov.calls != 0 {
			t.Fatalf("a rename the rule refuses: %d", got)
		}
		if got := rename(super, "  Sky   Air Travel "); got != http.StatusOK || prov.calls != 1 {
			t.Fatalf("a super admin's rename: %d calls=%d", got, prov.calls)
		}
		var m models.PayMerchant
		if err := db.First(&m, "merchant_request_id = ?", stored.ID).Error; err != nil || m.BusinessName != "Sky Air Travel" {
			t.Fatalf("provisioned %+v err %v", m, err)
		}
	})
}
