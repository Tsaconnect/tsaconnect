package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The notice reaches every non-PM with a live Sonic ad or a Sonic deposit,
// once; PMs, BSC users, finished ads and users already told are skipped.
func TestNotifyDirectMoved_OnceToEachSonicNonPM(t *testing.T) {
	r := newAudienceRig(t)
	ctx := context.Background()
	bus := &mockBus{}
	r.svc.bus = bus
	if err := r.db.Exec(`CREATE TABLE notifications (id TEXT PRIMARY KEY, user_id TEXT, type TEXT, title TEXT,
		message TEXT, data TEXT, channel TEXT, is_read BOOLEAN, read_at DATETIME, created_at DATETIME, deleted_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}

	withAd, paused, depositor := uuid.New(), uuid.New(), uuid.New()
	pmUser, bscOnly, finished, told := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seedActiveAd(t, r.db, withAd, 146, nil, "1500", "10")
	pausedAd := seedActiveAd(t, r.db, paused, 146, nil, "1500", "10")
	r.db.Model(pausedAd).Update("status", models.P2PAdStatusPaused)
	seedActivePMForUser(t, r.db, pmUser, "0x00000000000000000000000000000000000000p1")
	seedActiveAd(t, r.db, pmUser, 146, nil, "1500", "10") // a PM's manual ad: still a PM
	seedActiveAd(t, r.db, bscOnly, bscDirectChain, nil, "1500", "10")
	done := seedActiveAd(t, r.db, finished, 146, nil, "1500", "10")
	r.db.Model(done).Update("status", models.P2PAdStatusCancelled)
	seedActiveAd(t, r.db, told, 146, nil, "1500", "10")
	r.db.Create(&models.Notification{ID: uuid.New(), UserID: told, Type: events.P2PDirectMoved, Title: "x", Message: "x", Channel: "both"})
	for _, d := range []struct {
		user  uuid.UUID
		chain int64
	}{{depositor, 146}, {bscOnly, bscDirectChain}} {
		meta, _ := json.Marshal(map[string]any{"chainId": d.chain, "token": "USDT"})
		actor := d.user
		r.db.Create(&models.P2PAuditLog{ID: uuid.New(), ActorID: &actor, Action: models.P2PAuditDepositConfirmed, Metadata: meta, CreatedAt: time.Now()})
	}

	n, err := r.svc.NotifyDirectMoved(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]bool{}
	for _, ev := range bus.published {
		if ev.Type != events.P2PDirectMoved {
			continue
		}
		if ev.ActorID != uuid.Nil || ev.Title != directMovedTitle {
			t.Fatalf("notice = %+v, want a system notice titled %q", ev, directMovedTitle)
		}
		got[ev.UserID] = true
	}
	want := map[uuid.UUID]bool{withAd: true, paused: true, depositor: true}
	if n != len(want) || len(got) != len(want) {
		t.Fatalf("sent %d to %v, want exactly %v", n, got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("user %s missed the notice; got %v", id, got)
		}
	}
}

// Before the switch (no pm escrow) nothing is sent.
func TestNotifyDirectMoved_NothingBeforeTheSwitch(t *testing.T) {
	r := newAudienceRig(t)
	bus := &mockBus{}
	r.svc.bus = bus
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		146: {Escrow: r.sonic, ChainKey: "sonic", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"},
			Decimals: map[string]int{"USDT": 6}, Audience: config.P2PAudienceAll},
	}, "2")
	r.svc.escrows = reg
	seedActiveAd(t, r.db, uuid.New(), 146, nil, "1500", "10")
	if n, err := r.svc.NotifyDirectMoved(context.Background()); err != nil || n != 0 || len(bus.published) != 0 {
		t.Fatalf("n=%d err=%v published=%d, want nothing", n, err, len(bus.published))
	}
}
