package services

// Verifies the realtime contract the P2P trade cockpit (#31) depends on: that a
// P2P bus event flows through the REAL NotificationService → ws.Hub and reaches
// a connected socket as a frame whose shape is exactly what the app consumes.
//
// The frontend cockpit matches frames with hooks/tradeFrame.ts#classifyTradeFrame
// — top-level `type` (p2p.trade.* / p2p.message.new) + `data.tradeId`. This test
// publishes synthetic events and asserts the emitted frame satisfies that
// classifier, closing the one link the frontend unit tests can't reach. No chain,
// no funds, deterministic.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/ws"
)

// In-memory sqlite with the minimal users + notifications tables handleEvent
// touches. Raw DDL (not AutoMigrate) because the models carry Postgres-only
// gen_random_uuid()/jsonb defaults AutoMigrate can't honor on sqlite — same
// approach as setupP2PTestDB and the wallet tests.
func openNotifWSTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT,
			email TEXT,
			mute_notifications INTEGER NOT NULL DEFAULT 0,
			mute_email INTEGER NOT NULL DEFAULT 0,
			deleted_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT
		)`,
		`CREATE TABLE notifications (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			user_id TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			message TEXT NOT NULL,
			data TEXT,
			channel TEXT NOT NULL DEFAULT 'in_app',
			is_read INTEGER NOT NULL DEFAULT 0,
			read_at DATETIME,
			created_at DATETIME,
			deleted_at DATETIME
		)`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

// classifyTradeFrameGo mirrors hooks/tradeFrame.ts#classifyTradeFrame exactly so
// this test asserts the BACKEND frame is what the FRONTEND cockpit acts on.
func classifyTradeFrameGo(typ, tradeID string, data map[string]interface{}) string {
	if typ == "" || tradeID == "" || data == nil {
		return ""
	}
	dt, _ := data["tradeId"].(string)
	if dt == "" || dt != tradeID {
		return ""
	}
	if typ == "p2p.message.new" {
		return "message"
	}
	if strings.HasPrefix(typ, "p2p.trade.") {
		return "trade"
	}
	return ""
}

func TestNotificationServicePushesP2PFramesForCockpit(t *testing.T) {
	db := openNotifWSTestDB(t)
	userID := uuid.New()
	// mute_email=1: this test exercises only the WS frame path (WS is gated by
	// mute_notifications, which stays 0). Muting email keeps the trade-milestone
	// email goroutine — irrelevant here and unable to load a trade from this
	// minimal test DB — from spawning and adding timing noise to the frame read.
	if err := db.Exec(
		`INSERT INTO users (id, name, email, mute_notifications, mute_email) VALUES (?,?,?,0,1)`,
		userID.String(), "Buyer", "buyer@test.local",
	).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	bus := events.NewBus()
	defer bus.Stop()
	hub := ws.NewHub()

	// Capture client: nil Conn — we read Send directly and never run the pumps.
	client := &ws.Client{Hub: hub, UserID: userID, Send: make(chan []byte, 16)}
	hub.Register <- client
	time.Sleep(50 * time.Millisecond) // let the hub goroutine finish registering

	// The bridge under test — subscribes "*" to the bus in its constructor.
	// nil push service: this test exercises the WS frame, not Expo delivery.
	_ = NewNotificationService(db, bus, hub, NewEmailService("", ""), nil)

	tradeID := uuid.New().String()
	cases := []struct {
		name      string
		eventType string
		wantKind  string
	}{
		{"trade transition", events.P2PTradePaid, "trade"},
		{"chat message", "p2p.message.new", "message"}, // raw string in p2p_message_service.go
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus.Publish(events.Event{
				Type:    tc.eventType,
				UserID:  userID,
				Title:   "title",
				Message: "message",
				Data:    map[string]interface{}{"tradeId": tradeID},
			})

			var raw []byte
			select {
			case raw = <-client.Send:
			case <-time.After(2 * time.Second):
				t.Fatalf("no WS frame received for %s", tc.eventType)
			}

			var frame struct {
				ID   string                 `json:"id"`
				Type string                 `json:"type"`
				Data map[string]interface{} `json:"data"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("frame is not valid JSON: %v (%s)", err, raw)
			}
			if frame.Type != tc.eventType {
				t.Errorf("frame.type = %q, want %q", frame.Type, tc.eventType)
			}
			if dt, _ := frame.Data["tradeId"].(string); dt != tradeID {
				t.Errorf("frame.data.tradeId = %q, want %q", frame.Data["tradeId"], tradeID)
			}
			// The decisive cross-stack assertion: the app's classifier would act.
			if got := classifyTradeFrameGo(frame.Type, tradeID, frame.Data); got != tc.wantKind {
				t.Errorf("classifyTradeFrame(%q) = %q, want %q — cockpit would NOT refresh", frame.Type, got, tc.wantKind)
			}
		})
	}
}

// A muted user still gets every channel for a payout, key, webhook or webhook
// secret change: muting needs only a session, so an attacker could otherwise
// silence the one alert that shows them.
func TestDeliveryChannels_APayoutChangeCannotBeMuted(t *testing.T) {
	for _, kind := range []string{events.SecurityPaySettlementChanged, events.SecurityPayKeyRolled,
		events.SecurityPayEndpointsChanged, events.SecurityPayWebhookSecret, events.SecurityPayKeysRevoked} {
		for _, mute := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
			if live, email := deliveryChannels(kind, mute[0], mute[1]); !live || !email {
				t.Fatalf("%s mute %v: live=%v email=%v", kind, mute, live, email)
			}
		}
	}
	// Everything else still honours the user's settings.
	if live, email := deliveryChannels(events.OrderShipped, true, false); live || email {
		t.Fatalf("muted order event: live=%v email=%v", live, email)
	}
	if live, email := deliveryChannels(events.OrderShipped, false, true); !live || email {
		t.Fatalf("email-muted order event: live=%v email=%v", live, email)
	}
}

// The live switch is news to the owner, not an alarm: emailed and pushed, and
// muted like any other informational notice.
func TestNotificationMaps_TheLiveSwitchIsEmailedAndPushedButMutable(t *testing.T) {
	for _, kind := range []string{events.PayLiveEnabled, events.PayLiveDisabled} {
		if !emailEvents[kind] || !pushWorthy(kind) {
			t.Errorf("%s: email=%v push=%v, want both", kind, emailEvents[kind], pushWorthy(kind))
		}
		if live, email := deliveryChannels(kind, true, false); live || email {
			t.Errorf("%s ignored a mute", kind)
		}
	}
}

// ─── API-18: the actor of a P2P event gets no push or email ──────────────────

func TestActedOnByRecipient(t *testing.T) {
	u := uuid.New()
	cases := []struct {
		name string
		ev   events.Event
		want bool
	}{
		{"actor is recipient", events.Event{UserID: u, ActorID: u}, true},
		{"counterparty", events.Event{UserID: u, ActorID: uuid.New()}, false},
		{"system event", events.Event{UserID: u, Data: map[string]any{"tradeId": "x"}}, false},
		// Only the typed field counts: a data key is not a claim about who acted.
		{"untyped data key", events.Event{UserID: u, Data: map[string]any{"actorId": u.String()}}, false},
	}
	for _, tc := range cases {
		if got := actedOnByRecipient(tc.ev); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestP2PEvent_ActorGetsNoPushButStillGetsTheFrame(t *testing.T) {
	db := openNotifWSTestDB(t)
	if err := db.Exec(`CREATE TABLE device_tokens (id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
		user_id TEXT, token TEXT, platform TEXT, last_seen DATETIME, created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	var pushes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushes, 1)
		_, _ = w.Write([]byte(`{"data":[{"status":"ok"}]}`))
	}))
	defer srv.Close()
	push := NewExpoPushService("")
	push.url = srv.URL

	actor, other := uuid.New(), uuid.New()
	for i, u := range []uuid.UUID{actor, other} {
		if err := db.Exec(`INSERT INTO users (id, name, email, mute_notifications, mute_email) VALUES (?,?,?,0,1)`,
			u.String(), "U", "u@test.local").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO device_tokens (user_id, token) VALUES (?, ?)`,
			u.String(), "ExponentPushToken[t"+string(rune('a'+i))+"]").Error; err != nil {
			t.Fatal(err)
		}
	}

	bus := events.NewBus()
	defer bus.Stop()
	hub := ws.NewHub()
	actorClient := &ws.Client{Hub: hub, UserID: actor, Send: make(chan []byte, 16)}
	otherClient := &ws.Client{Hub: hub, UserID: other, Send: make(chan []byte, 16)}
	hub.Register <- actorClient
	hub.Register <- otherClient
	time.Sleep(50 * time.Millisecond)
	_ = NewNotificationService(db, bus, hub, NewEmailService("", ""), push)

	tradeID := uuid.New().String()
	data := map[string]any{"tradeId": tradeID}
	bus.Publish(events.Event{Type: events.P2PTradePaid, UserID: actor, ActorID: actor, Title: "Buyer marked payment sent", Data: data})

	select {
	case raw := <-actorClient.Send:
		var frame struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Data["actorId"] != actor.String() {
			t.Fatalf("frame lacks data.actorId: %v", frame.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("actor still needs the WS frame to refresh")
	}
	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt32(&pushes); n != 0 {
		t.Fatalf("actor got %d push(es)", n)
	}

	// Control: the counterparty of the same action is still pushed.
	bus.Publish(events.Event{Type: events.P2PTradePaid, UserID: other, ActorID: actor, Title: "Buyer marked payment sent", Data: data})
	select {
	case raw := <-otherClient.Send:
		// The app tells its own actions from the counterparty's by data.actorId.
		var frame struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil || frame.Data["actorId"] != actor.String() {
			t.Fatalf("counterparty frame data = %v (%v), want actorId %s", frame.Data, err, actor)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("counterparty got no WS frame")
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&pushes) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&pushes); n != 1 {
		t.Fatalf("counterparty pushes = %d, want 1", n)
	}

	// The actor's own row is kept but stored read, so the unread badge
	// (/notifications/unread-count) does not count it; the counterparty's does.
	ns := &NotificationService{db: db}
	var actorRow struct {
		IsRead bool
		ReadAt *time.Time
	}
	if err := db.Raw(`SELECT is_read, read_at FROM notifications WHERE user_id = ?`, actor.String()).Scan(&actorRow).Error; err != nil {
		t.Fatal(err)
	}
	if !actorRow.IsRead || actorRow.ReadAt == nil {
		t.Fatalf("actor's own row = %+v, want stored read with read_at", actorRow)
	}
	if n, err := ns.GetUnreadCount(actor); err != nil || n != 0 {
		t.Fatalf("actor unread = %d (%v), want 0", n, err)
	}
	if n, err := ns.GetUnreadCount(other); err != nil || n != 1 {
		t.Fatalf("counterparty unread = %d (%v), want 1", n, err)
	}
}

func TestP2PTradeServiceEmit_TagsTheActor(t *testing.T) {
	bus := &mockBus{}
	s := &P2PTradeService{bus: bus}
	actor, other := uuid.New(), uuid.New()
	orig := map[string]any{"tradeId": "t1"}

	s.emit(events.P2PTradePaid, other, actor, "x", "", orig)
	s.emit(events.P2PTradeReleased, other, uuid.Nil, "y", "", map[string]any{"tradeId": "t1"})

	if got := bus.published[0].ActorID; got != actor {
		t.Fatalf("ActorID = %v, want %s", got, actor)
	}
	if _, tagged := orig["actorId"]; tagged {
		t.Fatal("emit mutated the caller's map")
	}
	if got := bus.published[1].ActorID; got != uuid.Nil {
		t.Fatalf("system event must carry no actor: %v", got)
	}
}
