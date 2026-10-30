package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

type chatBus struct{ got []events.Event }

func (b *chatBus) Publish(e events.Event) { b.got = append(b.got, e) }

type chatClock struct{ now time.Time }

func (c *chatClock) Now() time.Time { return c.now }

type chatFixture struct {
	db       *gorm.DB
	bus      *chatBus
	clock    *chatClock
	svc      *ChatService
	customer uuid.UUID
	provider uuid.UUID
	stranger uuid.UUID
	service  uuid.UUID
}

func newChatFixture(t *testing.T) *chatFixture {
	t.Helper()
	db := testutil.TempPostgres(t, "chat_", &models.User{}, &models.Product{}, &models.ServiceContactPayment{},
		&models.ChatThread{}, &models.ChatMessage{})
	f := &chatFixture{db: db, bus: &chatBus{}, clock: &chatClock{now: time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)}}
	user := func(name string) uuid.UUID {
		u := models.User{ID: uuid.New(), Name: name, Username: name + uuid.NewString()[:8], Email: name + uuid.NewString()[:8] + "@t.io", Password: "x"}
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	f.customer, f.provider, f.stranger = user("ada"), user("bayo"), user("chi")
	p := models.Product{ID: uuid.New(), Name: "Mechanic, Kubwa", UserID: f.provider}
	p.SetImages([]models.ProductImage{{URL: "https://res.cloudinary.com/x/a.jpg"}})
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	f.service = p.ID
	if err := db.Create(&models.ServiceContactPayment{CallerID: f.customer, ServiceProviderID: f.provider, ServiceID: p.ID}).Error; err != nil {
		t.Fatal(err)
	}
	f.svc = NewChatService(db, f.bus).WithClock(f.clock)
	return f
}

func (f *chatFixture) tick() { f.clock.now = f.clock.now.Add(time.Minute) }

func TestChat_OpenNeedsThePaidContactFee(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Open(ctx, f.stranger, models.ChatSubjectServiceContact, f.service); !errors.Is(err, ErrChatNotEntitled) {
		t.Fatalf("stranger open: err = %v, want ErrChatNotEntitled", err)
	}
	if _, err := f.svc.Open(ctx, f.customer, "nope", f.service); !errors.Is(err, ErrChatSubjectUnknown) {
		t.Fatalf("unknown subject: err = %v", err)
	}
	a, err := f.svc.Open(ctx, f.customer, models.ChatSubjectServiceContact, f.service)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.Open(ctx, f.customer, models.ChatSubjectServiceContact, f.service)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("second open made a new thread: %s vs %s", a.ID, b.ID)
	}
	if a.Role != "customer" || a.CounterpartName != "bayo" || a.SubjectTitle != "Mechanic, Kubwa" || a.SubjectImage == "" {
		t.Fatalf("view = %+v", a)
	}
}

func TestChat_ConversationBetweenTheTwoOnly(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	th, err := f.svc.Open(ctx, f.customer, models.ChatSubjectServiceContact, f.service)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing written yet: the provider has nothing to answer.
	if got, _ := f.svc.Inbox(ctx, f.provider, 0); len(got) != 0 {
		t.Fatalf("provider inbox before any message = %d threads", len(got))
	}

	f.tick()
	if _, err := f.svc.SendText(ctx, th.ID, f.customer, "  Can you come to Kubwa today?  "); err != nil {
		t.Fatal(err)
	}
	if len(f.bus.got) != 1 || f.bus.got[0].UserID != f.provider || f.bus.got[0].ActorID != f.customer ||
		f.bus.got[0].Type != events.ChatMessageNew || f.bus.got[0].Message != "Can you come to Kubwa today?" {
		t.Fatalf("event = %+v", f.bus.got)
	}

	inbox, err := f.svc.Inbox(ctx, f.provider, 0)
	if err != nil || len(inbox) != 1 || !inbox[0].Unread || inbox[0].Role != "provider" || inbox[0].CounterpartName != "ada" {
		t.Fatalf("provider inbox = %+v, err %v", inbox, err)
	}
	if n, _ := f.svc.UnreadCount(ctx, f.provider); n != 1 {
		t.Fatalf("provider unread = %d", n)
	}
	if n, _ := f.svc.UnreadCount(ctx, f.customer); n != 0 {
		t.Fatalf("sender's own message counted unread: %d", n)
	}

	f.tick()
	if err := f.svc.MarkRead(ctx, th.ID, f.provider); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.svc.UnreadCount(ctx, f.provider); n != 0 {
		t.Fatalf("unread after read = %d", n)
	}

	f.tick()
	if _, err := f.svc.SendImage(ctx, th.ID, f.provider, "https://res.cloudinary.com/x/quote.jpg"); err != nil {
		t.Fatal(err)
	}
	if f.bus.got[1].UserID != f.customer {
		t.Fatalf("reply notified %s, want the customer", f.bus.got[1].UserID)
	}
	msgs, err := f.svc.List(ctx, th.ID, f.customer, MessageFilter{})
	if err != nil || len(msgs) != 2 || msgs[0].Body != "Can you come to Kubwa today?" || msgs[1].Type != models.ChatMessageTypeImage {
		t.Fatalf("messages = %+v, err %v", msgs, err)
	}
	since := msgs[0].CreatedAt
	if later, _ := f.svc.List(ctx, th.ID, f.provider, MessageFilter{Since: &since}); len(later) != 1 {
		t.Fatalf("since paging = %d messages", len(later))
	}

	// Outsiders see nothing and cannot write.
	if _, err := f.svc.SendText(ctx, th.ID, f.stranger, "hi"); !errors.Is(err, ErrChatThreadNotFound) {
		t.Fatalf("stranger send: %v", err)
	}
	if _, err := f.svc.List(ctx, th.ID, f.stranger, MessageFilter{}); !errors.Is(err, ErrChatThreadNotFound) {
		t.Fatalf("stranger list: %v", err)
	}
	if _, err := f.svc.Get(ctx, th.ID, f.stranger); !errors.Is(err, ErrChatThreadNotFound) {
		t.Fatalf("stranger get: %v", err)
	}
}

func TestChat_MessageRules(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	th, err := f.svc.Open(ctx, f.customer, models.ChatSubjectServiceContact, f.service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SendText(ctx, th.ID, f.customer, "   "); !errors.Is(err, ErrMessageEmpty) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := f.svc.SendImage(ctx, th.ID, f.customer, "https://res.cloudinary.com.evil.tld/a.jpg"); !errors.Is(err, ErrImageURLInvalid) {
		t.Fatalf("foreign image host: %v", err)
	}
	if len(f.bus.got) != 0 {
		t.Fatalf("refused messages notified: %+v", f.bus.got)
	}
}

func TestPreviewOf(t *testing.T) {
	if got := previewOf("a\n\nb   c"); got != "a b c" {
		t.Fatalf("got %q", got)
	}
	long := make([]rune, 300)
	for i := range long {
		long[i] = 'é'
	}
	if got := []rune(previewOf(string(long))); len(got) != previewLen || got[len(got)-1] != '…' {
		t.Fatalf("len %d", len(got))
	}
}
