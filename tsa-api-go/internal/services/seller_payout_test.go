package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type stubPayoutInstitutions map[string][]PaycrestPayoutOption

func (f stubPayoutInstitutions) Institutions(_ context.Context, fiat string) ([]PaycrestPayoutOption, error) {
	return f[fiat], nil
}

type stubPayoutBanks struct {
	name string
	err  error
}

func (f stubPayoutBanks) ListBanks(context.Context) ([]NombaBank, error) { return nil, nil }
func (f stubPayoutBanks) Resolve(_ context.Context, _, acct string) (NombaLookupResult, error) {
	return NombaLookupResult{AccountNumber: acct, AccountName: f.name}, f.err
}

func sellerPayoutSvc(t *testing.T, banks BankDirectory) *SellerPayoutService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE seller_payout_methods (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, kind TEXT NOT NULL, currency TEXT NOT NULL,
		institution_code TEXT NOT NULL, institution_name TEXT NOT NULL, account_number TEXT NOT NULL,
		account_name TEXT NOT NULL, verified INTEGER NOT NULL DEFAULT 0, created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX uq_spm_user ON seller_payout_methods(user_id)`).Error; err != nil {
		t.Fatal(err)
	}
	dir := stubPayoutInstitutions{
		"NGN": {{Code: "058", Name: "GTBank", Type: PaycrestInstitutionBank}},
		"KES": {{Code: "MPESA", Name: "M-Pesa", Type: PaycrestInstitutionMobileMoney}},
	}
	return NewSellerPayoutService(db, dir, banks)
}

func TestSellerPayout_DefaultsToWallet(t *testing.T) {
	s := sellerPayoutSvc(t, nil)
	p, err := s.Resolve(context.Background(), uuid.New())
	if err != nil || p.Kind != models.SellerPayoutWallet || p.Method != nil {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestSellerPayout_NGNBankNameComesFromTheBank(t *testing.T) {
	s := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := uuid.New()
	m, err := s.Set(context.Background(), uid, SetSellerPayoutInput{
		Currency: "ngn", InstitutionCode: "058", AccountNumber: "0123456789", AccountName: "Typed Name",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.AccountName != "ADA OBI" || !m.Verified || m.Kind != models.SellerPayoutBank {
		t.Fatalf("got %+v", m)
	}
	p, _ := s.Resolve(context.Background(), uid)
	if p.Kind != models.SellerPayoutBank || p.Method.InstitutionName != "GTBank" {
		t.Fatalf("resolve: %+v", p)
	}
}

func TestSellerPayout_ReplacesAndClears(t *testing.T) {
	s := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := uuid.New()
	ctx := context.Background()
	if _, err := s.Set(ctx, uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Set(ctx, uid, SetSellerPayoutInput{Currency: "KES", InstitutionCode: "MPESA", AccountNumber: "254712345678", AccountName: "Wanjiru K"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != models.SellerPayoutMobileMoney || m.Verified || m.Currency != "KES" {
		t.Fatalf("got %+v", m)
	}
	if err := s.Clear(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Resolve(ctx, uid); p.Kind != models.SellerPayoutWallet {
		t.Fatalf("after clear: %+v", p)
	}
}

func TestSellerPayout_Refusals(t *testing.T) {
	ctx := context.Background()
	uid := uuid.New()
	cases := []struct {
		name  string
		banks BankDirectory
		in    SetSellerPayoutInput
		want  error
	}{
		{"unknown institution", stubPayoutBanks{name: "X"}, SetSellerPayoutInput{InstitutionCode: "999", AccountNumber: "0123456789"}, ErrSellerPayoutInstitution},
		{"short nuban", stubPayoutBanks{name: "X"}, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "123"}, ErrPaycrestAccountShape},
		{"bank cannot name it", stubPayoutBanks{err: ErrNombaAccountNotFound}, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}, ErrSellerPayoutAccount},
		{"no name directory", nil, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}, ErrSellerPayoutUnavailable},
		{"momo needs a phone number", stubPayoutBanks{}, SetSellerPayoutInput{Currency: "KES", InstitutionCode: "MPESA", AccountNumber: "12", AccountName: "A"}, ErrPaycrestAccountShape},
		{"momo needs a name", stubPayoutBanks{}, SetSellerPayoutInput{Currency: "KES", InstitutionCode: "MPESA", AccountNumber: "254712345678"}, ErrSellerPayoutAccount},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sellerPayoutSvc(t, c.banks)
			if _, err := s.Set(ctx, uid, c.in); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if p, _ := s.Resolve(ctx, uid); p.Kind != models.SellerPayoutWallet {
				t.Fatalf("a refused set must leave the wallet: %+v", p)
			}
		})
	}
}
