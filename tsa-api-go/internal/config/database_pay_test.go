package config

import (
	"os"
	"strings"
	"testing"
)

func TestAutoMigrate_IncludesTSAPayModels(t *testing.T) {
	src, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"&models.PayMerchant{}", "&models.PayPayment{}", "&models.PayWebhookDelivery{}", "&models.PayAuditEvent{}"} {
		if !strings.Contains(string(src), m) {
			t.Fatalf("AutoMigrate is missing %s", m)
		}
	}
}
