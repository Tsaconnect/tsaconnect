package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

var notUploadedImageURLs = []string{
	"",
	"http://res.cloudinary.com/tsa/image/upload/p.png",      // not https
	"https://res.cloudinary.com.evil.tld/p.png",             // lookalike suffix
	"https://evil.tld/res.cloudinary.com/p.png",             // host in the path
	"https://evil.tld/p.png?u=https://res.cloudinary.com/x", // host in the query
	"javascript:alert(1)//https://res.cloudinary.com/p.png", // script scheme
	"https://user@evil.tld/res.cloudinary.com",              // userinfo trick
	"res.cloudinary.com/tsa/image/upload/p.png",             // no scheme
}

func TestIsUploadedImageURL(t *testing.T) {
	for _, ok := range []string{
		"https://res.cloudinary.com/tsa/image/upload/p.png",
		"  https://res.cloudinary.com:443/tsa/image/upload/p.png ",
	} {
		if !isUploadedImageURL(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range notUploadedImageURLs {
		if isUploadedImageURL(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The payment proof renders in the seller's app and the admin console, so it
// gets the same host pin as chat images and dispute evidence.
func TestPrepareMarkPaid_RefusesProofNotUploadedInTheApp(t *testing.T) {
	svc, _, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
	for _, bad := range notUploadedImageURLs[1:] {
		if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, bad); !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("proof %q: err = %v, want ErrInvalidParams", bad, err)
		}
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.ProofImageURL != "" {
		t.Fatalf("a refused proof must not be saved, got %q", after.ProofImageURL)
	}
}

func TestPrepareRaiseDispute_RefusesEvidenceNotUploadedInTheApp(t *testing.T) {
	svc, _, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPaid)
	for _, bad := range notUploadedImageURLs {
		evidence := []string{"https://res.cloudinary.com/tsa/image/upload/ok.png", bad}
		if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "not released", evidence); !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("evidence %q: err = %v, want ErrInvalidParams", bad, err)
		}
	}
	var disputes int64
	db.Model(&models.P2PDispute{}).Where("trade_id = ?", tr.ID).Count(&disputes)
	if disputes != 0 {
		t.Fatal("refused evidence must not leave a dispute row")
	}
}

// Chat images and order-dispute evidence share the same check.
func TestChatImageAndDisputeEvidence_UseTheUploadHostPin(t *testing.T) {
	chat := NewP2PMessageService(nil, nil)
	for _, bad := range notUploadedImageURLs {
		if _, err := chat.SendImage(context.Background(), uuid.New(), uuid.New(), bad); !errors.Is(err, ErrImageURLInvalid) {
			t.Errorf("chat image %q: err = %v, want ErrImageURLInvalid", bad, err)
		}
		if _, err := validateEvidence([]string{bad}); !errors.Is(err, ErrDisputeBadRequest) {
			t.Errorf("dispute evidence %q: err = %v, want ErrDisputeBadRequest", bad, err)
		}
	}
	if _, err := validateEvidence([]string{"https://res.cloudinary.com/tsa/image/upload/ok.png"}); err != nil {
		t.Errorf("uploaded evidence refused: %v", err)
	}
}
