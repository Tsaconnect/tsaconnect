package main

import (
	"context"
	"log"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
)

// runLegalNameBackfill is the one-time repair for users who are verified but
// hold no legal name (the Aug 2026 Didit outage, and admin overrides from
// before an override had to carry a name). The money-in gate refuses all of
// them. Users Didit cannot answer for are listed; their name has to be set by
// hand from the document (see deploy/one-off/2026-10-13-set-verified-legal-name.sql).
func runLegalNameBackfill(cfg *config.Config) {
	result, err := handlers.BackfillAllVerifiedNames(context.Background(), cfg)
	if err != nil {
		log.Fatalf("[backfill-legal-names] %v", err)
	}
	log.Printf("[backfill-legal-names] scanned=%d updated=%d unresolved=%d no_document=%d failed=%d",
		result.Scanned, result.Updated, len(result.UnresolvedUserIDs), len(result.NoDocumentUserIDs), result.Failed)
	if len(result.NoDocumentUserIDs) > 0 {
		log.Printf("[backfill-legal-names] approved by override but never submitted an ID document: %s",
			strings.Join(result.NoDocumentUserIDs, ","))
	}
	if len(result.UnresolvedUserIDs) > 0 {
		log.Printf("[backfill-legal-names] no Didit name for user ids: %s",
			strings.Join(result.UnresolvedUserIDs, ","))
	}
}
