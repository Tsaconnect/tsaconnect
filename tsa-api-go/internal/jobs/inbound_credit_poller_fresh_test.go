package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type flakyIngestor struct{ err error }

func (f *flakyIngestor) RunOnce(context.Context) (services.IngestResult, error) {
	return services.IngestResult{}, f.err
}

func TestInboundCreditPoller_LastIngestOKOnlyOnSuccess(t *testing.T) {
	ing := &flakyIngestor{err: errors.New("statement 503")}
	w := NewInboundCreditPoller(ing, 0)
	_ = w.Run(context.Background())
	if !w.LastIngestOK().IsZero() {
		t.Fatal("a failed fetch counted as fresh")
	}
	ing.err = nil
	_ = w.Run(context.Background())
	if w.LastIngestOK().IsZero() {
		t.Fatal("a successful fetch was not recorded")
	}
}
