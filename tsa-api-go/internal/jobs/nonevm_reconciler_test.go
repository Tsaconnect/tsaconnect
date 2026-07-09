package jobs

import (
	"testing"
	"time"
)

func TestNonEVMReconcilerMeta(t *testing.T) {
	r := NewNonEVMReconciler(nil, nil)
	if r.Name() == "" {
		t.Fatal("Name empty")
	}
	if r.Interval() <= 0 {
		t.Fatalf("bad interval %v", r.Interval())
	}
	if r.Interval() != 45*time.Second {
		t.Fatalf("want 45s got %v", r.Interval())
	}
}
