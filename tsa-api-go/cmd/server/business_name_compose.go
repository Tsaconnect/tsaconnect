package main

import (
	"context"
	"log"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// runBusinessNameCompose is the one-time step that adds each unverified
// merchant's owner legal name to their business name ("Ojay store - Ademu
// Emmanuel") and makes every shown name unique. Without apply it only lists what
// it would do. Merchants whose owner has no verified legal name, or whose
// composed name is taken, are listed and left as they are.
func runBusinessNameCompose(apply bool) {
	ctx := context.Background()
	if err := services.FillBusinessNameKeys(ctx, config.DB); err != nil {
		log.Fatalf("[compose-business-names] keys: %v", err)
	}
	items, err := services.ComposeBusinessNames(ctx, config.DB, !apply)
	if err != nil {
		log.Fatalf("[compose-business-names] %v", err)
	}
	changed, skipped := 0, 0
	for _, it := range items {
		switch {
		case it.Skipped != "":
			skipped++
			log.Printf("[compose-business-names] skip   %s  %q  (%s)", it.MerchantID, it.From, it.Skipped)
		default:
			changed++
			log.Printf("[compose-business-names] rename %s  %q -> %q", it.MerchantID, it.From, it.To)
		}
	}
	mode := "DRY RUN — nothing changed; run again with -apply"
	if apply {
		mode = "applied"
	}
	log.Printf("[compose-business-names] %s: %d to rename, %d skipped", mode, changed, skipped)
}
