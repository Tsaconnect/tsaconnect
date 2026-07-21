package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// CompanyCustomerParams are the details registered for the company Flutterwave
// customer when one must be minted.
type CompanyCustomerParams struct {
	Name  string
	Email string
	Phone string
}

// ResolveFlutterwaveCompanyCustomer returns the FW customer id that parents all
// virtual accounts, minting one lazily so operators don't have to find it in the
// dashboard:
//
//  1. an explicit env override (customerIDEnv) wins — the escape hatch;
//  2. else a customer previously minted for THIS baseURL is reused;
//  3. else create a company customer via the API and persist it, keyed by
//     baseURL so a sandbox↔live switch never reuses a cross-environment id.
//
// Returns "" (no error) when FW isn't configured, leaving VA provisioning
// dormant. Only ever hits the API on the first boot of a new environment.
func ResolveFlutterwaveCompanyCustomer(
	ctx context.Context, db *gorm.DB, client *FlutterwaveClient,
	baseURL, customerIDEnv string, params CompanyCustomerParams,
) (string, error) {
	if id := strings.TrimSpace(customerIDEnv); id != "" {
		return id, nil
	}
	if client == nil || !client.IsEnabled() {
		return "", nil
	}

	// (2) reuse the persisted id for this environment.
	var row models.FlutterwaveCustomer
	err := db.WithContext(ctx).Where("base_url = ?", baseURL).First(&row).Error
	if err == nil {
		return row.CustomerID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("resolve fw customer: load: %w", err)
	}

	// (3) mint one and persist.
	email := strings.TrimSpace(params.Email)
	if email == "" {
		return "", errors.New("resolve fw customer: company email required to create one (set FLUTTERWAVE_COMPANY_EMAIL)")
	}
	id, err := client.CreateCustomer(ctx, FWCustomerRequest{
		Email:         email,
		Name:          &FWName{First: strings.TrimSpace(params.Name)},
		Phone:         strings.TrimSpace(params.Phone),
		MetaReference: "company-" + baseURL,
	})
	if err != nil {
		return "", fmt.Errorf("resolve fw customer: create: %w", err)
	}
	if err := db.WithContext(ctx).Create(&models.FlutterwaveCustomer{
		BaseURL: baseURL, CustomerID: id, Email: email,
	}).Error; err != nil {
		// The customer already exists at FW; log rather than fail boot. Worst
		// case a later boot re-mints (harmless — VAs attach to whichever id).
		log.Printf("[fw] company customer %s created but persist failed: %v", id, err)
	}
	return id, nil
}
