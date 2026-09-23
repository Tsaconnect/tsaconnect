package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// payChainRegistry resolves a chain ID to its name for crypto settlement — and
// only for a chain a delivery can be confirmed on (see MainnetChainNameForID):
// a merchant we can never prove a delivery for must not be configurable. Kept
// narrow (rather than depending on the full BlockchainService) so this service
// can be constructed and tested without a live chain registry.
type payChainRegistry interface {
	MainnetChainNameForID(id int64) (string, bool)
}

// ErrPayMerchantNotFound is returned when no PayMerchant matches a lookup.
var ErrPayMerchantNotFound = errors.New("merchant not found")

var (
	// ErrPayMerchantInvalid is wrapped with the specific reason a Update
	// (or the settlement/key work it does) refused a merchant's settings.
	ErrPayMerchantInvalid = errors.New("invalid merchant settings")
	// ErrPayKeyInvalid is returned by Authenticate for any key that isn't a
	// live hash match — malformed, unknown, or rotated out. Never reveals
	// which of those it was, so a probing caller learns nothing.
	ErrPayKeyInvalid = errors.New("invalid API key")
	// ErrPayMerchantSuspended is returned by Authenticate for an otherwise
	// valid key belonging to a suspended merchant.
	ErrPayMerchantSuspended = errors.New("this merchant account is suspended")
	// ErrPayAuthUnavailable is returned by Authenticate when the key lookup
	// itself failed (database down), so callers answer 503 — a good key must
	// not read as a bad one during an outage.
	ErrPayAuthUnavailable = errors.New("authentication is temporarily unavailable")
	// ErrPayMerchantStale is returned by Update when the merchant changed
	// since the caller read it — an explicit expectedVersion mismatch, or a
	// concurrent writer winning the compare-and-set.
	ErrPayMerchantStale = errors.New("this merchant was changed by someone else — reload and try again")
)

// Approval defaults for a payment_acceptance merchant request. An admin can
// change these later (Task 5's merchant-settings path).
const (
	payMerchantDefaultFeeSharePercent = 100
	payMerchantDefaultMinAmountKobo   = 100_000
	payMerchantDefaultMaxAmountKobo   = 500_000_000
)

// payReturnDomainRE matches one lowercase hostname label sequence — the
// stored form of a return domain (no scheme, no path, no port).
var payReturnDomainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// ValidPayDomain reports whether host is a well-formed lowercase DNS name
// usable as a return domain: at least two labels of at most 63 characters,
// and not an IP literal
// (which the label regexp alone would accept).
func ValidPayDomain(host string) bool {
	if len(host) > 253 || !payReturnDomainRE.MatchString(host) {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) > 63 {
			return false
		}
	}
	_, err := netip.ParseAddr(host)
	return err != nil
}

// payAccountNumberRE matches a 10-digit NUBAN.
var payAccountNumberRE = regexp.MustCompile(`^[0-9]{10}$`)

// PayMerchantUpdate is a partial edit to a merchant's settings: only non-nil
// fields are applied. ReturnDomains is the one exception — nil means
// unchanged, but a non-nil (possibly empty) slice replaces the list, so a
// merchant's return domains can be cleared.
type PayMerchantUpdate struct {
	BusinessName            *string  `json:"businessName"`
	Status                  *string  `json:"status"`
	SettlementMode          *string  `json:"settlementMode"`
	BankCode                *string  `json:"bankCode"`
	AccountNumber           *string  `json:"accountNumber"`
	ChainID                 *int64   `json:"chainId"`
	TokenAddress            *string  `json:"tokenAddress"`
	TokenSymbol             *string  `json:"tokenSymbol"`
	WalletAddress           *string  `json:"walletAddress"`
	CustomerFeeSharePercent *int     `json:"customerFeeSharePercent"`
	MinAmountKobo           *int64   `json:"minAmount"`
	MaxAmountKobo           *int64   `json:"maxAmount"`
	WebhookURL              *string  `json:"webhookUrl"`
	TestWebhookURL          *string  `json:"testWebhookUrl"`
	ReturnDomains           []string `json:"returnDomains"` // nil = unchanged
	// ExpectedVersion, when set, is the merchant version the editor loaded; a
	// different current version refuses the edit as stale.
	ExpectedVersion *int `json:"expectedVersion"`
}

// Sensitive reports whether an update touches where money or events go —
// settlement destination, crypto target, webhook endpoints, or the domains
// allowed to receive a checkout redirect. Those need a super admin; a
// business-name typo fix or a fee-share/status change does not.
func (u PayMerchantUpdate) Sensitive() bool {
	return u.SettlementMode != nil || u.BankCode != nil || u.AccountNumber != nil ||
		u.ChainID != nil || u.TokenAddress != nil || u.TokenSymbol != nil || u.WalletAddress != nil ||
		u.WebhookURL != nil || u.TestWebhookURL != nil || u.ReturnDomains != nil
}

// PayKeyScope selects which of a merchant's credentials IssueKeys rotates.
type PayKeyScope string

const (
	PayKeysAll     PayKeyScope = "all" // first issue: live + test keys + both webhook secrets
	PayKeysLive    PayKeyScope = "live"
	PayKeysTest    PayKeyScope = "test"
	PayKeysWebhook PayKeyScope = "webhook" // bumps the version: both webhook secrets change
)

// PayMerchantKeys carries newly minted credentials. Each field is set only
// when that scope minted or derived it — this is the ONE time a raw key or
// webhook secret is ever available; nothing stores them.
type PayMerchantKeys struct {
	LiveKey           string `json:"liveKey,omitempty"`
	TestKey           string `json:"testKey,omitempty"`
	WebhookSecretLive string `json:"webhookSecretLive,omitempty"`
	WebhookSecretTest string `json:"webhookSecretTest,omitempty"`
}

// PayMerchantView is the admin-facing read shape: the parsed return-domain
// list plus whether a live/test key currently exists, without ever
// including the hash (already excluded from models.PayMerchant's own JSON).
type PayMerchantView struct {
	models.PayMerchant
	ReturnDomains []string `json:"returnDomains"`
	HasLiveKey    bool     `json:"hasLiveKey"`
	HasTestKey    bool     `json:"hasTestKey"`
}

// PayMerchantService provisions and manages TSA Pay merchants.
type PayMerchantService struct {
	db     *gorm.DB
	banks  BankDirectory    // nil → settlement can't be verified, so it stays unset
	chains payChainRegistry // nil → crypto settlement refused
	master []byte
	now    func() time.Time
}

// NewPayMerchantService constructs a PayMerchantService. banks and chains may
// be nil — TSA Pay stays dormant-friendly: a nil bank directory just leaves
// settlement unconfigured until an admin sets it, rather than failing boot.
func NewPayMerchantService(db *gorm.DB, banks BankDirectory, chains payChainRegistry, master []byte, now func() time.Time) *PayMerchantService {
	return &PayMerchantService{db: db, banks: banks, chains: chains, master: master, now: now}
}

// CreateFromRequest provisions a PayMerchant from an approved
// payment_acceptance MerchantRequest. It is idempotent: calling it again for
// a request that already provisioned a merchant (e.g. a retried approval)
// returns the existing row instead of erroring or inserting a duplicate.
//
// Settlement is only set when the applicant supplied a complete bank/account
// and the account resolves via the name-enquiry: the account name stored is
// always the resolver's, never the applicant-typed one. An unverifiable or
// incomplete settlement destination is left unset for an admin to configure.
func (s *PayMerchantService) CreateFromRequest(ctx context.Context, req *models.MerchantRequest) (*models.PayMerchant, error) {
	var existing models.PayMerchant
	err := s.db.WithContext(ctx).Where("merchant_request_id = ?", req.ID).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// The submit handler validates and trims the website, but CreateFromRequest
	// is also the resume path for requests approved before that validation
	// existed (or edited directly), so it trims defensively too — url.Parse
	// errors (returns a nil *url.URL) on a whitespace-padded string, and a
	// malformed Website must return an error, never a nil-pointer dereference
	// on u.Hostname().
	u, err := url.Parse(strings.TrimSpace(req.Website))
	if err != nil {
		return nil, fmt.Errorf("pay merchant: invalid website %q: %w", req.Website, err)
	}
	domain := strings.ToLower(u.Hostname())
	// The domain becomes the merchant's initial return-domain list, which is
	// split on commas: a hostname like "a.com,evil.com" (which url.Parse
	// accepts) would smuggle a second domain past the super-admin-only
	// return-domain edit, so a malformed one is refused here too.
	if !ValidPayDomain(domain) {
		return nil, fmt.Errorf("%w: website %q is not a valid domain", ErrPayMerchantInvalid, req.Website)
	}

	m := &models.PayMerchant{
		MerchantRequestID:       req.ID,
		UserID:                  req.UserID,
		BusinessName:            req.BusinessName,
		WebsiteDomain:           domain,
		ReturnDomains:           domain,
		Status:                  models.PayMerchantActive,
		CustomerFeeSharePercent: payMerchantDefaultFeeSharePercent,
		MinAmountKobo:           payMerchantDefaultMinAmountKobo,
		MaxAmountKobo:           payMerchantDefaultMaxAmountKobo,
		WebhookSecretVersion:    1,
	}

	if req.SettlementBankCode != "" && req.SettlementAccountNumber != "" && req.SettlementAccountName != "" && s.banks != nil {
		if res, resolveErr := s.banks.Resolve(ctx, req.SettlementBankCode, req.SettlementAccountNumber); resolveErr == nil {
			m.SettlementMode = models.PaySettlementFiat
			m.BankCode = req.SettlementBankCode
			m.AccountNumber = req.SettlementAccountNumber
			m.AccountName = res.AccountName
		}
		// A failed resolve (including ErrNombaAccountNotFound) leaves settlement
		// unset rather than trusting the applicant-typed account name.
	}

	if err := s.db.WithContext(ctx).Create(m).Error; err != nil {
		if isUniqueViolation(err) {
			var again models.PayMerchant
			if reErr := s.db.WithContext(ctx).Where("merchant_request_id = ?", req.ID).First(&again).Error; reErr == nil {
				return &again, nil
			}
		}
		return nil, err
	}

	return m, nil
}

// Get returns one merchant by id, or ErrPayMerchantNotFound.
func (s *PayMerchantService) Get(ctx context.Context, id uuid.UUID) (*models.PayMerchant, error) {
	var m models.PayMerchant
	if err := s.db.WithContext(ctx).First(&m, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPayMerchantNotFound
		}
		return nil, err
	}
	return &m, nil
}

// List returns a page of merchants, newest first, as the admin console's
// read shape (parsed return domains, key presence booleans, no hashes).
func (s *PayMerchantService) List(ctx context.Context, page, limit int) ([]PayMerchantView, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&models.PayMerchant{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var merchants []models.PayMerchant
	if err := s.db.WithContext(ctx).Order("created_at desc").Offset((page - 1) * limit).Limit(limit).Find(&merchants).Error; err != nil {
		return nil, 0, err
	}

	views := make([]PayMerchantView, len(merchants))
	for i, m := range merchants {
		views[i] = PayMerchantView{
			PayMerchant:   m,
			ReturnDomains: m.ReturnDomainList(),
			HasLiveKey:    m.LiveKeyHash != nil,
			HasTestKey:    m.TestKeyHash != nil,
		}
	}
	return views, total, nil
}

// Update applies a partial edit to a merchant's settings. The resulting
// (not just the changed) merchant is validated as a whole, and nothing is
// persisted unless every check passes — a refused update never partially
// lands. Settlement changes are verified against the bank/chain the money
// actually needs to reach before they're accepted.
func (s *PayMerchantService) Update(ctx context.Context, actor uuid.UUID, id uuid.UUID, in PayMerchantUpdate) (*models.PayMerchant, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if in.ExpectedVersion != nil && *in.ExpectedVersion != m.Version {
		return nil, ErrPayMerchantStale
	}

	before := *m
	updated := *m
	// changes holds only the columns this request supplied, so a concurrent
	// edit to any other column is never overwritten with a stale copy.
	changes := map[string]interface{}{}

	if in.BusinessName != nil {
		updated.BusinessName = strings.TrimSpace(*in.BusinessName)
		changes["business_name"] = updated.BusinessName
	}
	if in.Status != nil {
		updated.Status = *in.Status
		changes["status"] = updated.Status
	}
	if in.SettlementMode != nil {
		updated.SettlementMode = *in.SettlementMode
		changes["settlement_mode"] = updated.SettlementMode
	}
	if in.BankCode != nil {
		updated.BankCode = strings.TrimSpace(*in.BankCode)
		changes["bank_code"] = updated.BankCode
	}
	if in.AccountNumber != nil {
		updated.AccountNumber = strings.TrimSpace(*in.AccountNumber)
		changes["account_number"] = updated.AccountNumber
	}
	if in.ChainID != nil {
		updated.ChainID = *in.ChainID
		changes["chain_id"] = updated.ChainID
	}
	if in.TokenAddress != nil {
		updated.TokenAddress = strings.TrimSpace(*in.TokenAddress)
		changes["token_address"] = updated.TokenAddress
	}
	if in.TokenSymbol != nil {
		updated.TokenSymbol = strings.TrimSpace(*in.TokenSymbol)
		changes["token_symbol"] = updated.TokenSymbol
	}
	if in.WalletAddress != nil {
		updated.WalletAddress = strings.TrimSpace(*in.WalletAddress)
		changes["wallet_address"] = updated.WalletAddress
	}
	if in.CustomerFeeSharePercent != nil {
		updated.CustomerFeeSharePercent = *in.CustomerFeeSharePercent
		changes["customer_fee_share_percent"] = updated.CustomerFeeSharePercent
	}
	if in.MinAmountKobo != nil {
		updated.MinAmountKobo = *in.MinAmountKobo
		changes["min_amount_kobo"] = updated.MinAmountKobo
	}
	if in.MaxAmountKobo != nil {
		updated.MaxAmountKobo = *in.MaxAmountKobo
		changes["max_amount_kobo"] = updated.MaxAmountKobo
	}
	if in.WebhookURL != nil {
		updated.WebhookURL = strings.TrimSpace(*in.WebhookURL)
		changes["webhook_url"] = updated.WebhookURL
	}
	if in.TestWebhookURL != nil {
		updated.TestWebhookURL = strings.TrimSpace(*in.TestWebhookURL)
		changes["test_webhook_url"] = updated.TestWebhookURL
	}
	if in.ReturnDomains != nil {
		domains := make([]string, 0, len(in.ReturnDomains))
		for _, d := range in.ReturnDomains {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" {
				continue
			}
			if !ValidPayDomain(d) {
				return nil, fmt.Errorf("%w: invalid return domain %q", ErrPayMerchantInvalid, d)
			}
			domains = append(domains, d)
		}
		updated.ReturnDomains = strings.Join(domains, ",")
		changes["return_domains"] = updated.ReturnDomains
	}

	if err := s.validatePayMerchant(ctx, &before, &updated); err != nil {
		return nil, err
	}
	if updated.AccountName != before.AccountName {
		changes["account_name"] = updated.AccountName // set by the name enquiry
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		won, err := s.casUpdate(tx, id, before.Version, changes)
		if err != nil {
			return err
		}
		if !won {
			return ErrPayMerchantStale
		}
		return s.writeAudit(tx, id, actor, "merchant.update", payAuditChanges(&before, &updated))
	})
	if err != nil {
		return nil, err
	}
	updated.Version = before.Version + 1

	if in.Sensitive() {
		log.Printf("[tsa-pay] AUDIT merchant=%s actor=%s fields=settlement bank=%s->%s account=%s->%s wallet=%s->%s webhookUrl=%q->%q testWebhookUrl=%q->%q returnDomains=%q->%q",
			id, actor,
			payLast4(before.BankCode), payLast4(updated.BankCode),
			payLast4(before.AccountNumber), payLast4(updated.AccountNumber),
			before.WalletAddress, updated.WalletAddress,
			payRedactURL(before.WebhookURL), payRedactURL(updated.WebhookURL),
			payRedactURL(before.TestWebhookURL), payRedactURL(updated.TestWebhookURL),
			before.ReturnDomains, updated.ReturnDomains,
		)
	}

	return &updated, nil
}

// casUpdate writes changes to a merchant only if it is still at version, and
// bumps the version in the same statement. false means another writer got
// there first (or the row is gone) and nothing was written.
func (s *PayMerchantService) casUpdate(db *gorm.DB, id uuid.UUID, version int, changes map[string]interface{}) (bool, error) {
	changes["version"] = gorm.Expr("version + 1")
	res := db.Model(&models.PayMerchant{}).Where("id = ? AND version = ?", id, version).Updates(changes)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// validatePayMerchant checks the RESULTING merchant (not just the changed
// fields) is a settings combination TSA Pay can operate on. Every refusal is
// wrapped with ErrPayMerchantInvalid so callers can errors.Is against one
// sentinel regardless of the specific reason.
func (s *PayMerchantService) validatePayMerchant(ctx context.Context, before, updated *models.PayMerchant) error {
	if updated.Status != models.PayMerchantActive && updated.Status != models.PayMerchantSuspended {
		return fmt.Errorf("%w: status must be active or suspended", ErrPayMerchantInvalid)
	}
	if updated.CustomerFeeSharePercent < 0 || updated.CustomerFeeSharePercent > 100 {
		return fmt.Errorf("%w: customer fee share must be 0-100", ErrPayMerchantInvalid)
	}
	if !(updated.MinAmountKobo > 0 && updated.MinAmountKobo <= updated.MaxAmountKobo && updated.MaxAmountKobo <= MaxPayAmountKobo) {
		return fmt.Errorf("%w: amount bounds must satisfy 0 < min <= max <= %d kobo", ErrPayMerchantInvalid, MaxPayAmountKobo)
	}
	if updated.WebhookURL != "" {
		if err := ValidPayWebhookURL(updated.WebhookURL); err != nil {
			return fmt.Errorf("%w: webhook url: %v", ErrPayMerchantInvalid, err)
		}
	}
	if updated.TestWebhookURL != "" {
		if err := ValidPayWebhookURL(updated.TestWebhookURL); err != nil {
			return fmt.Errorf("%w: test webhook url: %v", ErrPayMerchantInvalid, err)
		}
	}

	switch updated.SettlementMode {
	case "":
		// unconfigured — allowed
	case models.PaySettlementFiat:
		if updated.BankCode == "" || !payAccountNumberRE.MatchString(updated.AccountNumber) {
			return fmt.Errorf("%w: fiat settlement needs a bank code and a 10-digit account number", ErrPayMerchantInvalid)
		}
		if before.BankCode != updated.BankCode || before.AccountNumber != updated.AccountNumber {
			if s.banks == nil {
				return fmt.Errorf("%w: no bank directory configured to resolve the account", ErrPayMerchantInvalid)
			}
			res, err := s.banks.Resolve(ctx, updated.BankCode, updated.AccountNumber)
			if err != nil {
				return fmt.Errorf("%w: could not resolve bank account: %v", ErrPayMerchantInvalid, err)
			}
			updated.AccountName = res.AccountName
		}
	case models.PaySettlementCrypto:
		if s.chains == nil {
			return fmt.Errorf("%w: no chain registry configured for crypto settlement", ErrPayMerchantInvalid)
		}
		if _, ok := s.chains.MainnetChainNameForID(updated.ChainID); !ok {
			return fmt.Errorf("%w: chain id %d is not a supported settlement chain", ErrPayMerchantInvalid, updated.ChainID)
		}
		if !common.IsHexAddress(updated.TokenAddress) {
			return fmt.Errorf("%w: invalid token address", ErrPayMerchantInvalid)
		}
		if strings.EqualFold(updated.TokenAddress, evmNativeAddress) || strings.EqualFold(updated.TokenAddress, evmNativeAliasAddress) {
			return fmt.Errorf("%w: the merchant's own token on its own chain is not offered in v1", ErrPayMerchantInvalid)
		}
		if !common.IsHexAddress(updated.WalletAddress) || strings.EqualFold(updated.WalletAddress, evmNativeAddress) {
			return fmt.Errorf("%w: invalid settlement wallet address", ErrPayMerchantInvalid)
		}
		if l := len(updated.TokenSymbol); l < 1 || l > 16 {
			return fmt.Errorf("%w: token symbol must be 1-16 characters", ErrPayMerchantInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown settlement mode %q", ErrPayMerchantInvalid, updated.SettlementMode)
	}
	return nil
}

// payAuditChanges lists the audited fields that differ between before and
// after as {field: {before, after}}. Only non-secret fields are audited, and
// the account number only as its last four digits.
func payAuditChanges(before, after *models.PayMerchant) map[string]map[string]any {
	out := map[string]map[string]any{}
	add := func(field string, b, a any) {
		if b != a {
			out[field] = map[string]any{"before": b, "after": a}
		}
	}
	add("businessName", before.BusinessName, after.BusinessName)
	add("status", before.Status, after.Status)
	add("settlementMode", before.SettlementMode, after.SettlementMode)
	add("bankCode", before.BankCode, after.BankCode)
	add("accountNumber", payLast4(before.AccountNumber), payLast4(after.AccountNumber))
	add("chainId", before.ChainID, after.ChainID)
	add("tokenAddress", before.TokenAddress, after.TokenAddress)
	add("tokenSymbol", before.TokenSymbol, after.TokenSymbol)
	add("walletAddress", before.WalletAddress, after.WalletAddress)
	add("customerFeeSharePercent", before.CustomerFeeSharePercent, after.CustomerFeeSharePercent)
	add("minAmount", before.MinAmountKobo, after.MinAmountKobo)
	add("maxAmount", before.MaxAmountKobo, after.MaxAmountKobo)
	// Compared raw (a query-only change is still a change) but recorded
	// redacted: webhook URLs can carry a token in the query string.
	addURL := func(field, b, a string) {
		if b != a {
			out[field] = map[string]any{"before": payRedactURL(b), "after": payRedactURL(a)}
		}
	}
	addURL("webhookUrl", before.WebhookURL, after.WebhookURL)
	addURL("testWebhookUrl", before.TestWebhookURL, after.TestWebhookURL)
	add("returnDomains", before.ReturnDomains, after.ReturnDomains)
	return out
}

// writeAudit records one audit event inside tx, so it commits or rolls back
// with the change it describes.
func (s *PayMerchantService) writeAudit(tx *gorm.DB, merchantID, actor uuid.UUID, action string, details any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return tx.Create(&models.PayAuditEvent{MerchantID: merchantID, ActorID: actor, Action: action, Details: string(raw)}).Error
}

// payRedactURL makes a merchant-supplied URL safe for an audit row or log
// line: only scheme and host are kept. Userinfo is dropped, and the path,
// query and fragment — where a webhook token lives (Slack-style hooks carry
// it in the path) — become "/…". Empty stays empty; anything that is not an absolute URL is
// "[unparseable url]" rather than echoed back.
func payRedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "[unparseable url]"
	}
	out := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		out += "/…"
	}
	return out
}

// payLast4 returns the last 4 characters of a settlement identifier for an
// audit log — enough to spot-check a change without logging the full bank
// account number.
func payLast4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

// IssueKeys mints and/or rotates a merchant's credentials per scope. A raw
// key or webhook secret is returned exactly once, here — nothing else ever
// has it again; only its hash (keys) or nothing at all (webhook secrets,
// which are derived on demand by WebhookSecret) is stored.
func (s *PayMerchantService) IssueKeys(ctx context.Context, actor uuid.UUID, id uuid.UUID, scope PayKeyScope) (*PayMerchantKeys, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}

	out := &PayMerchantKeys{}
	updates := map[string]interface{}{}
	bumpWebhook := false

	if scope == PayKeysAll || scope == PayKeysLive {
		raw, prefix, hash, err := GeneratePayKey(models.PayModeLive)
		if err != nil {
			return nil, err
		}
		updates["live_key_hash"] = hash
		updates["live_key_prefix"] = prefix
		out.LiveKey = raw
	}
	if scope == PayKeysAll || scope == PayKeysTest {
		raw, prefix, hash, err := GeneratePayKey(models.PayModeTest)
		if err != nil {
			return nil, err
		}
		updates["test_key_hash"] = hash
		updates["test_key_prefix"] = prefix
		out.TestKey = raw
	}
	if scope == PayKeysAll || scope == PayKeysWebhook {
		updates["webhook_secret_version"] = gorm.Expr("webhook_secret_version + 1")
		bumpWebhook = true
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("%w: unknown key scope %q", ErrPayMerchantInvalid, scope)
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.PayMerchant{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return s.writeAudit(tx, id, actor, "merchant.keys", map[string]any{"scope": scope})
	})
	if err != nil {
		return nil, err
	}

	if bumpWebhook {
		fresh, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out.WebhookSecretLive = s.WebhookSecret(fresh, models.PayModeLive)
		out.WebhookSecretTest = s.WebhookSecret(fresh, models.PayModeTest)
	}

	log.Printf("[tsa-pay] AUDIT merchant=%s actor=%s keys scope=%s", id, actor, scope)
	return out, nil
}

// Authenticate resolves the raw API key a merchant integration sent to the
// merchant it belongs to and which mode ("live"/"test") it authorizes,
// refusing a malformed/unknown key or a suspended merchant's key alike.
func (s *PayMerchantService) Authenticate(ctx context.Context, rawKey string) (*models.PayMerchant, string, error) {
	mode, ok := PayKeyMode(rawKey)
	if !ok {
		return nil, "", ErrPayKeyInvalid
	}
	column := "live_key_hash"
	if mode == models.PayModeTest {
		column = "test_key_hash"
	}

	var m models.PayMerchant
	if err := s.db.WithContext(ctx).Where(column+" = ?", HashPayKey(rawKey)).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrPayKeyInvalid
		}
		log.Printf("[tsa-pay] authenticate: key lookup failed: %v", err)
		return nil, "", ErrPayAuthUnavailable
	}
	if m.Status == models.PayMerchantSuspended {
		return nil, "", ErrPayMerchantSuspended
	}
	return &m, mode, nil
}

// WebhookSecret returns the current signing secret for m's webhooks of the
// given mode — derived from the master key, never stored.
func (s *PayMerchantService) WebhookSecret(m *models.PayMerchant, mode string) string {
	return DerivePayWebhookSecret(s.master, m.ID, mode, m.WebhookSecretVersion)
}
