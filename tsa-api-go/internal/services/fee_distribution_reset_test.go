package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestResetForV2Cutover_RequiresConfirm(t *testing.T) {
	db := setupFeeDistTestDB(t)
	mkFeeUser(t, db, "a"+uuidShort(), nil, models.AccountStatusActive)

	if _, err := ResetForV2Cutover(context.Background(), db, "cut", "nope"); err == nil {
		t.Fatal("expected refusal without the confirm phrase")
	}
	if _, err := ResetForV2Cutover(context.Background(), db, "", ResetConfirmPhrase); err == nil {
		t.Fatal("expected refusal without a label")
	}
}

func TestResetForV2Cutover_ArchivesAndZeros(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// Seed a distribution so there are balances + ledger rows to archive.
	users := buildFeeChain(t, db, 3)
	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "USDT", FeeUSD: 100,
		UserID: users[0].ID, SourceType: "p2p_release", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if tpBalance(t, db, users[0].ID) == 0 {
		t.Fatal("precondition: gen0 should have TP before reset")
	}

	sum, err := ResetForV2Cutover(context.Background(), db, "2026-06-07-cutover", ResetConfirmPhrase)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if sum.TPEarningsArchived == 0 || sum.BalancesSnapshotted != 3 {
		t.Fatalf("unexpected summary: %+v", sum)
	}

	// Live balances zeroed.
	for _, u := range users {
		if got := tpBalance(t, db, u.ID); got != 0 {
			t.Fatalf("tp_balance not zeroed for %s: %v", u.ID, got)
		}
		if got := cashbackBalance(t, db, u.ID); got != 0 {
			t.Fatalf("cashback_balance not zeroed for %s: %v", u.ID, got)
		}
	}

	// Legacy archive populated.
	var legacyBalances int64
	db.Raw(`SELECT COUNT(*) FROM user_balances_legacy WHERE cutover_label = ?`, "2026-06-07-cutover").Scan(&legacyBalances)
	if legacyBalances != 3 {
		t.Fatalf("expected 3 archived balance rows, got %d", legacyBalances)
	}

	// Re-running the same label is refused.
	if _, err := ResetForV2Cutover(context.Background(), db, "2026-06-07-cutover", ResetConfirmPhrase); err == nil {
		t.Fatal("expected refusal on re-run of the same label")
	}
}

// TestResetForV2Cutover_EmptyUsersTableStillGuardsRerun locks the sentinel guard:
// with NO users (BalancesSnapshotted == 0) the FIRST run must still succeed and
// the SECOND run with the same label must be REFUSED. The old guard counted
// archived balance rows, so an empty users table left prior==0 and permitted a
// second destructive run — the sentinel table fixes that.
func TestResetForV2Cutover_EmptyUsersTableStillGuardsRerun(t *testing.T) {
	db := setupFeeDistTestDB(t) // no users seeded

	sum, err := ResetForV2Cutover(context.Background(), db, "empty-cutover", ResetConfirmPhrase)
	if err != nil {
		t.Fatalf("first run on empty users table: %v", err)
	}
	if sum.BalancesSnapshotted != 0 {
		t.Fatalf("expected 0 balances snapshotted on empty users table, got %d", sum.BalancesSnapshotted)
	}

	// Sentinel must make the second run refuse even though nothing was archived.
	if _, err := ResetForV2Cutover(context.Background(), db, "empty-cutover", ResetConfirmPhrase); err == nil {
		t.Fatal("expected refusal on re-run of the same label even with an empty users table")
	}
}

// TestResetForV2Cutover_PortableTypes asserts the archive DDL uses
// PostgreSQL-portable column types. It guards against a regression back to
// DATETIME/REAL, which error on PostgreSQL (DATETIME is not a PG type) and would
// roll the entire cutover back in production. We cannot run PostgreSQL here, so
// we assert the source DDL declares the portable types and never the broken ones
// in a column definition.
func TestResetForV2Cutover_PortableTypes(t *testing.T) {
	// This runs the real path against SQLite, which accepts both the portable
	// and the broken tokens as affinities — so a green run here does NOT prove
	// portability. The source-level assertion in
	// fee_distribution_reset_ddl_test.go is the actual portability guard; this
	// test simply confirms the live path still creates and uses the tables.
	db := setupFeeDistTestDB(t)
	mkFeeUser(t, db, "p"+uuidShort(), nil, models.AccountStatusActive)
	if _, err := ResetForV2Cutover(context.Background(), db, "portable-cut", ResetConfirmPhrase); err != nil {
		t.Fatalf("reset with portable DDL: %v", err)
	}
}
