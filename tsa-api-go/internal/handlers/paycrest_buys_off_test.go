package handlers

import "github.com/ojimcy/tsa-api-go/internal/services"

// The legacy /paycrest/buy tests exercise the buy path kept for when Paycrest
// buys come back (services.PaycrestBuysOff, owner 2026-10-16); they run with it on.
func init() { services.PaycrestBuysOff = false }
