package main

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
)

func TestBuildTSAPay_DormantUnlessEnabledAndKeyed(t *testing.T) {
	sched := jobs.NewScheduler()
	if h := buildTSAPay(&config.Config{TSAPayEnabled: false}, nil, nil, nil, sched); h != nil {
		t.Fatal("disabled TSA Pay built a handler")
	}
	t.Setenv("PAYCREST_SENDER_FEE_PERCENT", "1")
	if h := buildTSAPay(&config.Config{TSAPayEnabled: true, TSAPayWebhookMasterKey: "abcd"}, nil, nil, nil, sched); h != nil {
		t.Fatal("a short master key must leave TSA Pay dormant")
	}
	if sched.Count() != 0 {
		t.Fatal("a dormant TSA Pay registered workers")
	}
}
