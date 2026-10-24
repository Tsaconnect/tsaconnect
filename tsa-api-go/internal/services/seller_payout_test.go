package services

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

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

// codeMailer keeps the last code it was asked to send.
type codeMailer struct {
	code string
	err  error
}

var sixDigits = regexp.MustCompile(`\b\d{6}\b`)

func (m *codeMailer) Send(_, _, _, _, message, _, _ string) error {
	m.code = sixDigits.FindString(message)
	return m.err
}

func sellerPayoutSvc(t *testing.T, banks BankDirectory) (*SellerPayoutService, *codeMailer) {
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
	if err := db.Exec(`CREATE TABLE seller_payout_changes (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL UNIQUE, payload TEXT NOT NULL, code_hash TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0, expires_at DATETIME NOT NULL, created_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	// Saving a payout account also points the owner's payment merchant at it.
	if err := db.Exec(`CREATE TABLE payment_merchants (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'merchant',
		settlement_bank_code TEXT, settlement_account_number TEXT, settlement_account_name TEXT,
		created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, name TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	dir := stubPayoutInstitutions{
		"NGN": {{Code: "058", Name: "GTBank", Type: PaycrestInstitutionBank}},
		"KES": {{Code: "MPESA", Name: "M-Pesa", Type: PaycrestInstitutionMobileMoney}},
	}
	mail := &codeMailer{}
	svc := NewSellerPayoutService(db, dir, banks, mail)
	svc.identity = func(context.Context, uuid.UUID) (bool, string, error) { return true, "Ada Obi", nil }
	svc.businessNames = func(context.Context, uuid.UUID) ([]string, error) { return nil, nil }
	return svc, mail
}

// seller adds a user with an email and returns their id.
func seller(t *testing.T, s *SellerPayoutService) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := s.db.Exec(`INSERT INTO users (id, email, name) VALUES (?, 'ada@example.com', 'Ada')`, id.String()).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// setWithCode is the whole guarded change: request, then confirm with the emailed code.
func setWithCode(t *testing.T, s *SellerPayoutService, mail *codeMailer, uid uuid.UUID, in SetSellerPayoutInput) (*models.SellerPayoutMethod, error) {
	t.Helper()
	p, err := s.Request(context.Background(), uid, in)
	if err != nil {
		return nil, err
	}
	return s.Confirm(context.Background(), uid, p.ID, mail.code)
}

func TestSellerPayout_DefaultsToWallet(t *testing.T) {
	s, _ := sellerPayoutSvc(t, nil)
	p, err := s.Resolve(context.Background(), uuid.New())
	if err != nil || p.Kind != models.SellerPayoutWallet || p.Method != nil {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestSellerPayout_NGNBankNameComesFromTheBank(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	m, err := setWithCode(t, s, mail, uid, SetSellerPayoutInput{
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

// One payout account: saving it (after the emailed code) moves the owner's P2P
// settlement to it too, and nobody else's.
func TestSellerPayout_SavingTheAccountMovesTheOwnersPaymentMerchant(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	other := uuid.New()
	for _, owner := range []uuid.UUID{uid, other} {
		if err := s.db.Exec(`INSERT INTO payment_merchants (id, user_id, kind, settlement_bank_code, settlement_account_number, settlement_account_name)
			VALUES (?, ?, 'merchant', '044', '0000000001', 'OLD')`, uuid.New().String(), owner.String()).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := setWithCode(t, s, mail, uid, SetSellerPayoutInput{
		Currency: "ngn", InstitutionCode: "058", AccountNumber: "0123456789",
	}); err != nil {
		t.Fatal(err)
	}
	read := func(owner uuid.UUID) string {
		var out string
		s.db.Raw(`SELECT settlement_bank_code || ':' || settlement_account_number || ':' || settlement_account_name FROM payment_merchants WHERE user_id = ?`,
			owner.String()).Scan(&out)
		return out
	}
	if got := read(uid); got != "058:0123456789:ADA OBI" {
		t.Errorf("owner's payment merchant = %q, want the saved payout account", got)
	}
	if got := read(other); got != "044:0000000001:OLD" {
		t.Errorf("another merchant changed: %q", got)
	}
}

// Saving a naira bank account hands it to TSA Pay; a failure there is for an
// operator and never undoes the saved account; a non-bank account is not offered.
func TestSellerPayout_SavedBankAccountIsHandedToTSAPay(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	var gotBank, gotAccount string
	calls := 0
	s.FollowWith(func(_ context.Context, user uuid.UUID, bank, account string) error {
		calls++
		gotBank, gotAccount = bank, account
		if user != uid {
			t.Errorf("followed for %s, want %s", user, uid)
		}
		return errors.New("tsa pay down")
	})
	m, err := setWithCode(t, s, mail, uid, SetSellerPayoutInput{Currency: "ngn", InstitutionCode: "058", AccountNumber: "0123456789"})
	if err != nil || m.AccountNumber != "0123456789" {
		t.Fatalf("a TSA Pay failure must not undo the saved account: %v %+v", err, m)
	}
	if calls != 1 || gotBank != "058" || gotAccount != "0123456789" {
		t.Fatalf("follow called %d times with %q %q", calls, gotBank, gotAccount)
	}
	if p, _ := s.Resolve(context.Background(), uid); p.Method == nil || p.Method.AccountNumber != "0123456789" {
		t.Fatalf("the account was not kept: %+v", p)
	}

	for _, other := range []*models.SellerPayoutMethod{
		{UserID: uid, Kind: models.SellerPayoutMobileMoney, Currency: "NGN", Verified: true},
		{UserID: uid, Kind: PaycrestInstitutionBank, Currency: "GHS", Verified: true},
		{UserID: uid, Kind: PaycrestInstitutionBank, Currency: "NGN", Verified: false},
	} {
		s.followTSAPay(context.Background(), other)
	}
	if calls != 1 {
		t.Fatalf("only a verified naira bank account reaches TSA Pay settlement (calls = %d)", calls)
	}
}

// A stolen login that knows the password still cannot redirect payouts.
func TestSellerPayout_NothingChangesBeforeTheCode(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	ctx := context.Background()
	p, err := s.Request(ctx, uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Resolve(ctx, uid); got.Kind != models.SellerPayoutWallet {
		t.Fatalf("saved before the code: %+v", got)
	}
	if _, err := s.Confirm(ctx, uid, p.ID, "000000"); !errors.Is(err, ErrSellerPayoutBadCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := s.Confirm(ctx, uuid.New(), p.ID, mail.code); !errors.Is(err, ErrSellerPayoutNoPending) {
		t.Fatalf("another user's code: %v", err)
	}
	if _, err := s.Confirm(ctx, uid, p.ID, mail.code); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if _, err := s.Confirm(ctx, uid, p.ID, mail.code); !errors.Is(err, ErrSellerPayoutNoPending) {
		t.Fatalf("a used code applied twice: %v", err)
	}
}

func TestSellerPayout_ExpiredAndExhaustedCodes(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	ctx := context.Background()
	in := SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}

	p, _ := s.Request(ctx, uid, in)
	s.now = func() time.Time { return time.Now().Add(EmailCodeTTL + time.Minute) }
	if _, err := s.Confirm(ctx, uid, p.ID, mail.code); !errors.Is(err, ErrSellerPayoutNoPending) {
		t.Fatalf("expired: %v", err)
	}
	s.now = time.Now

	p, _ = s.Request(ctx, uid, in)
	for i := 0; i < EmailCodeAttempts; i++ {
		_, _ = s.Confirm(ctx, uid, p.ID, "000000")
	}
	if _, err := s.Confirm(ctx, uid, p.ID, mail.code); !errors.Is(err, ErrSellerPayoutNoPending) {
		t.Fatalf("right code after the attempts ran out: %v", err)
	}
}

func TestSellerPayout_ReplacesAndClears(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	ctx := context.Background()
	if _, err := setWithCode(t, s, mail, uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}); err != nil {
		t.Fatal(err)
	}
	m, err := setWithCode(t, s, mail, uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0987654321"})
	if err != nil || m.AccountNumber != "0987654321" {
		t.Fatalf("got %+v, %v", m, err)
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
		{"a typed name is not verified", stubPayoutBanks{}, SetSellerPayoutInput{Currency: "KES", InstitutionCode: "MPESA", AccountNumber: "254712345678", AccountName: "Wanjiru K"}, ErrSellerPayoutUnverified},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := sellerPayoutSvc(t, c.banks)
			uid := seller(t, s)
			if _, err := s.Request(ctx, uid, c.in); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if p, _ := s.Resolve(ctx, uid); p.Kind != models.SellerPayoutWallet {
				t.Fatalf("a refused request must leave the wallet: %+v", p)
			}
		})
	}
}

func TestSellerPayout_NeedsKYCAndAMailer(t *testing.T) {
	ctx := context.Background()
	in := SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}

	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	s.identity = func(context.Context, uuid.UUID) (bool, string, error) { return false, "", nil }
	if _, err := s.Request(ctx, uid, in); !errors.Is(err, ErrSellerPayoutKYC) {
		t.Fatalf("unverified user: %v", err)
	}

	s.identity = func(context.Context, uuid.UUID) (bool, string, error) { return true, "Ada Obi", nil }
	mail.err = errors.New("smtp down")
	if _, err := s.Request(ctx, uid, in); !errors.Is(err, ErrSellerPayoutCode) {
		t.Fatalf("mail failure: %v", err)
	}
	var left int64
	s.db.Model(&models.SellerPayoutChange{}).Count(&left)
	if left != 0 {
		t.Fatalf("a code that could not be sent left %d change(s) waiting", left)
	}

	s.mailer = nil
	if _, err := s.Request(ctx, uid, in); !errors.Is(err, ErrSellerPayoutCode) {
		t.Fatalf("no mailer: %v", err)
	}
}

func TestSellerPayout_ClearDropsAWaitingChange(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	ctx := context.Background()
	p, _ := s.Request(ctx, uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"})
	if err := s.Clear(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ctx, uid, p.ID, mail.code); !errors.Is(err, ErrSellerPayoutNoPending) {
		t.Fatalf("a cleared seller's waiting code still applied: %v", err)
	}
}

func TestSellerPayout_ReRequestReplacesTheWaitingChange(t *testing.T) {
	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, s)
	ctx := context.Background()
	first, _ := s.Request(ctx, uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"})
	oldCode := mail.code
	second, err := s.Request(ctx, uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0987654321"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ctx, uid, first.ID, oldCode); !errors.Is(err, ErrSellerPayoutNoPending) {
		t.Fatalf("the replaced change still confirms: %v", err)
	}
	if m, err := s.Confirm(ctx, uid, second.ID, mail.code); err != nil || m.AccountNumber != "0987654321" {
		t.Fatalf("got %+v, %v", m, err)
	}
}

// Money goes only to the seller's own account or their verified business's.
func TestSellerPayout_AccountMustBeTheirsOrTheirBusiness(t *testing.T) {
	ctx := context.Background()
	in := SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}

	s, mail := sellerPayoutSvc(t, stubPayoutBanks{name: "EMEKA OKAFOR"}) // identity is "Ada Obi"
	uid := seller(t, s)
	if _, err := s.Request(ctx, uid, in); !errors.Is(err, ErrSellerPayoutNameMismatch) {
		t.Fatalf("someone else's account: %v", err)
	}
	var waiting int64
	s.db.Model(&models.SellerPayoutChange{}).Count(&waiting)
	if waiting != 0 {
		t.Fatal("a refused account left a change waiting")
	}

	s.businessNames = func(context.Context, uuid.UUID) ([]string, error) {
		return []string{"Okafor Emeka Ventures", "Emeka Okafor"}, nil
	}
	if _, err := setWithCode(t, s, mail, uid, in); err != nil {
		t.Fatalf("the verified business's account: %v", err)
	}

	// A rename review that removes the match between request and confirm.
	s2, mail2 := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid2 := seller(t, s2)
	p, err := s2.Request(ctx, uid2, in)
	if err != nil {
		t.Fatal(err)
	}
	s2.identity = func(context.Context, uuid.UUID) (bool, string, error) { return true, "Someone Else", nil }
	if _, err := s2.Confirm(ctx, uid2, p.ID, mail2.code); !errors.Is(err, ErrSellerPayoutNameMismatch) {
		t.Fatalf("a name that stopped matching before confirm: %v", err)
	}
}
