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
	"strings"
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
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
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
		events.SecurityPayEndpointsChanged, events.SecurityPayWebhookSecret} {
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
