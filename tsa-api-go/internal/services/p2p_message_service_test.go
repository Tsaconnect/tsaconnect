package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// msgTestSetup seeds an ad + open trade and returns the trade ID, buyer ID,
// seller ID, plus the message service ready for testing.
func msgTestSetup(t *testing.T) (*P2PMessageService, *gorm.DB, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	db := setupTradeDB(t)
	svc, _, bus := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	res, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xtx")
	tr, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	ms := NewP2PMessageService(db, bus).WithClock(fixedClock{
		t: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	return ms, db, tr.ID, buyerID, sellerID
}

func TestSendText_BuyerCanPost(t *testing.T) {
	ms, _, tradeID, buyerID, _ := msgTestSetup(t)
	msg, err := ms.SendText(context.Background(), tradeID, buyerID, "I'm paying now.")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if msg.Type != models.P2PMessageTypeText || msg.Body != "I'm paying now." {
		t.Fatalf("malformed message: %+v", msg)
	}
	if msg.SenderID == nil || *msg.SenderID != buyerID {
		t.Fatalf("sender attribution wrong")
	}
}

func TestSendText_SellerCanPost(t *testing.T) {
	ms, _, tradeID, _, sellerID := msgTestSetup(t)
	_, err := ms.SendText(context.Background(), tradeID, sellerID, "Confirmed bank — releasing now.")
	if err != nil {
		t.Fatalf("seller send: %v", err)
	}
}

func TestSendText_RejectsNonParticipant(t *testing.T) {
	ms, _, tradeID, _, _ := msgTestSetup(t)
	_, err := ms.SendText(context.Background(), tradeID, uuid.New(), "hi")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
}

func TestSendText_RejectsEmpty(t *testing.T) {
	ms, _, tradeID, buyerID, _ := msgTestSetup(t)
	_, err := ms.SendText(context.Background(), tradeID, buyerID, "   ")
	if !errors.Is(err, ErrMessageEmpty) {
		t.Fatalf("want ErrMessageEmpty, got %v", err)
	}
}

func TestSendText_RejectsTooLong(t *testing.T) {
	ms, _, tradeID, buyerID, _ := msgTestSetup(t)
	long := strings.Repeat("x", maxBodyLen+1)
	_, err := ms.SendText(context.Background(), tradeID, buyerID, long)
	if !errors.Is(err, ErrMessageTooLong) {
		t.Fatalf("want ErrMessageTooLong, got %v", err)
	}
}

func TestSendImage_HappyPath(t *testing.T) {
	ms, _, tradeID, buyerID, _ := msgTestSetup(t)
	msg, err := ms.SendImage(context.Background(), tradeID, buyerID, "https://res.cloudinary.com/tsa/proof.png")
	if err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if msg.Type != models.P2PMessageTypeImage || msg.ImageURL == "" {
		t.Fatalf("image not stored: %+v", msg)
	}
}

func TestSendImage_RejectsNonHTTPS(t *testing.T) {
	ms, _, tradeID, buyerID, _ := msgTestSetup(t)
	for _, bad := range []string{"", "http://x.com/y.png", "ftp://x.com/y.png", "javascript:alert(1)"} {
		_, err := ms.SendImage(context.Background(), tradeID, buyerID, bad)
		if !errors.Is(err, ErrImageURLInvalid) {
			t.Errorf("url=%q: want ErrImageURLInvalid, got %v", bad, err)
		}
	}
}

func TestSendImage_RejectsForeignHost(t *testing.T) {
	// Even HTTPS URLs from non-Cloudinary hosts must be refused — guards
	// against tracking-pixel SSRF / malware-as-image attacks.
	ms, _, tradeID, buyerID, _ := msgTestSetup(t)
	for _, bad := range []string{
		"https://attacker.example/track.png",
		"https://evil.cloudinary.com/x.png", // subdomain hijack
		"https://cloudinary.com/x.png",       // missing res. prefix
	} {
		_, err := ms.SendImage(context.Background(), tradeID, buyerID, bad)
		if !errors.Is(err, ErrImageURLInvalid) {
			t.Errorf("url=%q: want ErrImageURLInvalid, got %v", bad, err)
		}
	}
}

func TestPostSystem_NoSender(t *testing.T) {
	ms, _, tradeID, _, _ := msgTestSetup(t)
	if err := ms.PostSystem(context.Background(), tradeID, "Trade opened. Pay within 30 min."); err != nil {
		t.Fatalf("PostSystem: %v", err)
	}
}

func TestPostSystem_NilServiceNoOp(t *testing.T) {
	// Nil-safe — trade service might run without messages wired.
	var ms *P2PMessageService
	if err := ms.PostSystem(context.Background(), uuid.New(), "x"); err != nil {
		t.Fatalf("nil receiver should be no-op, got %v", err)
	}
}

func TestList_ChronologicalOrder(t *testing.T) {
	ms, _, tradeID, buyerID, sellerID := msgTestSetup(t)
	ctx := context.Background()
	// Interleave system + user messages.
	_ = ms.PostSystem(ctx, tradeID, "Trade opened.")
	_, _ = ms.SendText(ctx, tradeID, buyerID, "Paying now.")
	_, _ = ms.SendText(ctx, tradeID, sellerID, "Got it, releasing.")

	out, err := ms.List(ctx, tradeID, buyerID, MessageFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 messages, got %d", len(out))
	}
	if out[0].Type != models.P2PMessageTypeSystem {
		t.Fatalf("first message should be the system trade-opened, got %s", out[0].Type)
	}
}

func TestList_SinceFilter(t *testing.T) {
	ms, _, tradeID, buyerID, _ := msgTestSetup(t)
	ctx := context.Background()
	_ = ms.PostSystem(ctx, tradeID, "Trade opened.")
	_, _ = ms.SendText(ctx, tradeID, buyerID, "First.")

	// Advance clock and post one more.
	ms2 := ms.WithClock(fixedClock{t: time.Date(2026, 5, 18, 12, 5, 0, 0, time.UTC)})
	_, _ = ms2.SendText(ctx, tradeID, buyerID, "Second.")

	since := time.Date(2026, 5, 18, 12, 1, 0, 0, time.UTC)
	out, err := ms.List(ctx, tradeID, buyerID, MessageFilter{Since: &since})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].Body != "Second." {
		t.Fatalf("since filter wrong: %+v", out)
	}
}

func TestList_RejectsNonParticipant(t *testing.T) {
	ms, _, tradeID, _, _ := msgTestSetup(t)
	_, err := ms.List(context.Background(), tradeID, uuid.New(), MessageFilter{})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
}

func TestTradeService_AutoEmitsSystemMessageOnTransition(t *testing.T) {
	// Wire messages into trade service and verify that MarkPaid auto-emits
	// a system message in the chat thread.
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	msgSvc := NewP2PMessageService(db, nil)
	svc.SetMessageService(msgSvc)

	sellerID := uuid.New()
	buyerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	res, _ := svc.CreateAd(context.Background(), sellerID, "0xs", CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, sellerID, "", "0xtx")
	tr, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xb", OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})
	_, _ = svc.MarkPaid(context.Background(), tr.ID, buyerID, "url")

	// Expect at least 2 system messages: openTrade + markPaid.
	var msgs []models.P2PMessage
	db.Where("trade_id = ? AND type = ?", tr.ID, models.P2PMessageTypeSystem).
		Order("created_at ASC").Find(&msgs)
	if len(msgs) < 2 {
		t.Fatalf("want at least 2 system messages, got %d", len(msgs))
	}
}
