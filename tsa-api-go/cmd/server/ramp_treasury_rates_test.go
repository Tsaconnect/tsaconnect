package main

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Settlement prices through the same source, so it must be wired without Paycrest.
func TestWireTreasuryMarketRate_WorksWithoutPaycrest(t *testing.T) {
	it := &services.ITService{}
	if r := wireTreasuryMarketRate(it, nil, 400); r == nil {
		t.Fatal("no rate source built without Paycrest")
	}
	if wireTreasuryMarketRate(nil, nil, 400) == nil {
		t.Fatal("a nil IT service must still yield a source")
	}
}
