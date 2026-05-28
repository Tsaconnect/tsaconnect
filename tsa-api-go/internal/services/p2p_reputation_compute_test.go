package services

import (
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestCompletionRate(t *testing.T) {
	if got := CompletionRate(0, 0); got != nil {
		t.Fatalf("0/0 should be nil (new), got %v", *got)
	}
	got := CompletionRate(95, 5)
	if got == nil || *got != 0.95 {
		t.Fatalf("95/(95+5) = 0.95, got %v", got)
	}
}

func TestPositivePct(t *testing.T) {
	if got := PositivePct(0, 0); got != nil {
		t.Fatalf("0/0 should be nil, got %v", *got)
	}
	got := PositivePct(9, 1)
	if got == nil || *got != 0.9 {
		t.Fatalf("9/(9+1) = 0.9, got %v", got)
	}
}

func TestIsOnline(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Minute)
	stale := now.Add(-10 * time.Minute)
	if !IsOnline(&recent, now) {
		t.Fatal("2m ago should be online")
	}
	if IsOnline(&stale, now) {
		t.Fatal("10m ago should be offline")
	}
	if IsOnline(nil, now) {
		t.Fatal("nil last-seen should be offline")
	}
}

func TestEvaluateTrusted(t *testing.T) {
	cfg := TrustedThresholds{MinReleased: 50, MinCompletion: 0.95, MinAgeDays: 30}
	if !EvaluateTrusted(false, 0, nil, 0, true, cfg) {
		t.Fatal("manual override should force trusted")
	}
	if !EvaluateTrusted(true, 60, f64(0.97), 40, false, cfg) {
		t.Fatal("should be trusted when all thresholds met")
	}
	if EvaluateTrusted(false, 60, f64(0.97), 40, false, cfg) {
		t.Fatal("not verified -> not trusted")
	}
	if EvaluateTrusted(true, 10, f64(0.99), 40, false, cfg) {
		t.Fatal("below min released -> not trusted")
	}
	if EvaluateTrusted(true, 60, f64(0.80), 40, false, cfg) {
		t.Fatal("below min completion -> not trusted")
	}
	if EvaluateTrusted(true, 60, f64(0.99), 5, false, cfg) {
		t.Fatal("too new -> not trusted")
	}
}
