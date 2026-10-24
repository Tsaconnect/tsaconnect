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
	"unicode"
	"unicode/utf8"

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
	// MainnetChainIDs is every id MainnetChainNameForID resolves, ascending.
	MainnetChainIDs() []int64
	// TokenAddress is the configured mainnet address of a symbol on a chain
	// ("" when there is none) — how an owner's crypto payout is held to the
	// stablecoins the platform lists.
	TokenAddress(chain, symbol string) string
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
	// ErrPayAccountUnverifiable wraps (inside ErrPayMerchantInvalid) a bank
	// account the name enquiry could not resolve. The provider's own words are
	// for the log and an admin, never for an owner.
	ErrPayAccountUnverifiable = errors.New("the bank account could not be verified")
	// ErrPayMerchantAmbiguous: a user owns more than one merchant, which nothing
	// should ever produce; the owner routes refuse rather than guess which.
	ErrPayMerchantAmbiguous = errors.New("more than one merchant account belongs to this user")
	// ErrPayBanksUnavailable: no bank directory is configured, so banks can be
	// neither listed nor resolved.
	ErrPayBanksUnavailable = errors.New("the bank directory is unavailable")
)

// payOwnerSettlementTokens are what an owner may be paid in on a chain: the
// stablecoins the platform lists there. Anything else is an admin's call.
var payOwnerSettlementTokens = []string{"USDC", "USDT"}

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

// A live key's allowlist: at most this many entries, and no range wider than
// these — a /8 lets in whole providers and protects nothing.
const (
	maxPayAllowedIPs     = 20
	minPayAllowedIPv4Len = 16
	minPayAllowedIPv6Len = 48
)

// paySharedEgress are ranges many unrelated parties send from, so listing one
// would let anyone there use the key: Cloudflare's edge (our own proxy — the
// same ranges as deploy/nginx-cloudflare-realip.conf, which a test holds them
// to) and its Workers egress, carrier-grade NAT, and the Class E space
// Cloudflare's Pseudo-IPv4 hands out.
var paySharedEgress = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
		"100.64.0.0/10", // carrier-grade NAT
		"240.0.0.0/4",   // Class E: Cloudflare Pseudo-IPv4
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// payBenchmarkNet (RFC 2544) is global unicast to netip but never on the
// internet, so no request comes from it.
var payBenchmarkNet = netip.MustParsePrefix("198.18.0.0/15")

// PaySharedEgress reports whether ip is in a range many parties send from
// (see paySharedEgress). Seeing a live-key request "from" one means the
// real-client chain is broken — most likely Cloudflare added edge ranges that
// nginx does not trust yet — not that a merchant should list it.
func PaySharedEgress(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range paySharedEgress {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// normalizePayAllowedIPs checks an owner-typed allowlist and returns its stored
// form: each entry an IP or CIDR range, canonical (a range masked to its
// network, a single-host range stored as the address), deduplicated, in order.
// Private, loopback and other non-public addresses are refused: a request
// never reaches us from one, so listing it is always a mistake — usually the
// server's internal address instead of the one it is seen from. So are
// shared-egress ranges (PaySharedEgress).
func normalizePayAllowedIPs(in []string) (string, error) {
	// Counted raw, before deduplicating: the raw list is what a pending owner
	// action stores, so one address repeated 100k times must be refused too.
	if len(in) > maxPayAllowedIPs {
		return "", fmt.Errorf("%w: at most %d allowed IPs", ErrPayMerchantInvalid, maxPayAllowedIPs)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		var p netip.Prefix
		var err error
		if strings.Contains(e, "/") {
			p, err = netip.ParsePrefix(e)
		} else {
			var a netip.Addr
			if a, err = netip.ParseAddr(e); err == nil && a.Zone() == "" {
				p, err = a.Prefix(a.BitLen())
			} else if err == nil {
				err = errors.New("zoned address") // Prefix would silently drop the zone
			}
		}
		if err != nil {
			return "", fmt.Errorf("%w: allowed IP %q is not an IP address or CIDR range", ErrPayMerchantInvalid, e)
		}
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return "", fmt.Errorf("%w: allowed range %q is too wide", ErrPayMerchantInvalid, e)
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		p = p.Masked()
		a := p.Addr()
		if !a.IsGlobalUnicast() || a.IsPrivate() || payBenchmarkNet.Overlaps(p) {
			return "", fmt.Errorf("%w: allowed IP %q is not a public address — list the address your server's requests come from", ErrPayMerchantInvalid, e)
		}
		for _, shared := range paySharedEgress {
			if shared.Overlaps(p) {
				return "", fmt.Errorf("%w: allowed IP %q is shared by many senders (a proxy, cloud edge or carrier NAT), so it would let anyone there use your key", ErrPayMerchantInvalid, e)
			}
		}
		if (a.Is4() && p.Bits() < minPayAllowedIPv4Len) || (a.Is6() && p.Bits() < minPayAllowedIPv6Len) {
			return "", fmt.Errorf("%w: allowed range %q is too wide (at most /%d for IPv4, /%d for IPv6)", ErrPayMerchantInvalid, e, minPayAllowedIPv4Len, minPayAllowedIPv6Len)
		}
		canon := p.String()
		if p.IsSingleIP() {
			canon = a.String()
		}
		if !seen[canon] {
			seen[canon] = true
			out = append(out, canon)
		}
	}
	return strings.Join(out, ","), nil
}

// payAccountNumberRE matches a 10-digit NUBAN.
var payAccountNumberRE = regexp.MustCompile(`^[0-9]{10}$`)

// PayMerchantUpdate is a partial edit to a merchant's settings: only non-nil
// fields are applied. ReturnDomains and AllowedIPs are the exceptions — nil
// means unchanged, but a non-nil (possibly empty) slice replaces the list, so
// either can be cleared.
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
	AllowedIPs              []string `json:"allowedIps"`    // nil = unchanged
	// LiveEnabled switches the merchant's live key on or off. Admin only: no
	// owner route builds an update carrying it.
	LiveEnabled *bool `json:"liveEnabled"`
	// ExpectedVersion, when set, is the merchant version the editor loaded; a
	// different current version refuses the edit as stale.
	ExpectedVersion *int `json:"expectedVersion"`
}

// Sensitive reports whether an update touches where money or events go —
// settlement destination, crypto target, webhook endpoints, the domains
// allowed to receive a checkout redirect, or where the live key may be used
// from — whether the merchant may take real money at all (the live switch), or
// the business name (what customers are shown, and what a payout account may
// be named). Those need a super admin; a fee-share or status change does not.
// An owner's own rename goes through the review queue instead.
func (u PayMerchantUpdate) Sensitive() bool {
	return u.BusinessName != nil || u.SettlementMode != nil || u.BankCode != nil || u.AccountNumber != nil ||
		u.ChainID != nil || u.TokenAddress != nil || u.TokenSymbol != nil || u.WalletAddress != nil ||
		u.WebhookURL != nil || u.TestWebhookURL != nil || u.ReturnDomains != nil || u.AllowedIPs != nil || u.LiveEnabled != nil
}

// PayKeyScope selects which of a merchant's credentials IssueKeys rotates or
// RevokeKeys switches off (RevokeKeys refuses PayKeysAll).
type PayKeyScope string

const (
	PayKeysAll     PayKeyScope = "all" // live + test keys + both webhook secrets at once; only tests seed with it
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

// PayMerchantView is the admin-facing read shape: the parsed return-domain and
// allowed-IP lists plus whether a live/test key currently exists, without ever
// including the hash (already excluded from models.PayMerchant's own JSON).
type PayMerchantView struct {
	models.PayMerchant
	ReturnDomains []string `json:"returnDomains"`
	AllowedIPs    []string `json:"allowedIps"`
	HasLiveKey    bool     `json:"hasLiveKey"`
	HasTestKey    bool     `json:"hasTestKey"`
}

// NewPayMerchantView is m in the admin read shape.
func NewPayMerchantView(m models.PayMerchant) PayMerchantView {
	v := PayMerchantView{
		PayMerchant:   m,
		ReturnDomains: m.ReturnDomainList(),
		AllowedIPs:    m.AllowedIPList(),
		HasLiveKey:    m.LiveKeyHash != nil,
		HasTestKey:    m.TestKeyHash != nil,
	}
	if v.AllowedIPs == nil {
		v.AllowedIPs = []string{}
	}
	return v
}

// PayMerchantService provisions and manages TSA Pay merchants.
type PayMerchantService struct {
	db     *gorm.DB
	banks  BankDirectory    // nil → settlement can't be verified, so it stays unset
	chains payChainRegistry // nil → crypto settlement refused
	mailer CodeMailer       // nil → owners cannot confirm a guarded action
	// reserved are words an owner's business name may not contain (nil =
	// DefaultReservedBusinessNames).
	reserved []string
	master   []byte
	now      func() time.Time
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
//
// The merchant is created live: approving the application is the review, so an
// approved merchant takes real payments at once. A super admin can still
// switch it off.
func (s *PayMerchantService) CreateFromRequest(ctx context.Context, req *models.MerchantRequest) (*models.PayMerchant, error) {
	var existing models.PayMerchant
	err := s.db.WithContext(ctx).Where("merchant_request_id = ?", req.ID).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	domain, err := payRequestDomain(req.Website)
	if err != nil {
		return nil, err
	}
	// Checked at submit and approval too; this is the resume path for requests
	// made before business names were held to the rule.
	name, err := s.normalizeBusinessName(req.BusinessName)
	if err != nil {
		return nil, err
	}

	m := &models.PayMerchant{
		MerchantRequestID:       req.ID,
		UserID:                  req.UserID,
		BusinessName:            name,
		WebsiteDomain:           domain,
		ReturnDomains:           domain,
		Status:                  models.PayMerchantActive,
		LiveEnabled:             true,
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

// payRequestDomain is the hostname of an application's website, which becomes
// the merchant's website domain and first return domain. A blank website (a
// business with no site, paid through links) is no domain — and no return
// domain, so no return URL is accepted until the owner adds one.
//
// The submit handler validates and trims the website, but CreateFromRequest
// is also the resume path for requests approved before that validation
// existed (or edited directly), so it trims defensively too — url.Parse
// errors (returns a nil *url.URL) on a whitespace-padded string, and a
// malformed Website must return an error, never a nil-pointer dereference
// on u.Hostname().
func payRequestDomain(website string) (string, error) {
	raw := strings.TrimSpace(website)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("pay merchant: invalid website %q: %w", website, err)
	}
	domain := strings.ToLower(u.Hostname())
	// The domain becomes the merchant's initial return-domain list, which is
	// split on commas: a hostname like "a.com,evil.com" (which url.Parse
	// accepts) would smuggle a second domain past the super-admin-only
	// return-domain edit, so a malformed one is refused here too.
	if !ValidPayDomain(domain) {
		return "", fmt.Errorf("%w: website %q is not a valid domain", ErrPayMerchantInvalid, website)
	}
	return domain, nil
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
	page, limit = payPage(page, limit)

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
		views[i] = NewPayMerchantView(m)
	}
	return views, total, nil
}

// Update applies a partial edit to a merchant's settings. The resulting
// (not just the changed) merchant is validated as a whole, and nothing is
// persisted unless every check passes — a refused update never partially
// lands. Settlement changes are verified against the bank/chain the money
// actually needs to reach before they're accepted.
//
// It returns the merchant as it now is and as it was immediately before this
// edit — the exact version the compare-and-set replaced — so a caller can act on
// what really changed without a racy read of its own.
func (s *PayMerchantService) Update(ctx context.Context, actor uuid.UUID, id uuid.UUID, in PayMerchantUpdate) (updated, before *models.PayMerchant, err error) {
	return s.update(ctx, actor, id, in, nil)
}

// PaySettlementChange is where a merchant asks to be paid: the only settings a
// merchant may change about their own account. A naira destination names a
// bank account; a crypto one names a token on a chain and a wallet. The symbol
// is never taken from the caller — it is the listed stablecoin at that address.
type PaySettlementChange struct {
	SettlementMode string `json:"settlementMode"`
	BankCode       string `json:"bankCode"`
	AccountNumber  string `json:"accountNumber"`
	ChainID        int64  `json:"chainId"`
	TokenAddress   string `json:"tokenAddress"`
	WalletAddress  string `json:"walletAddress"`
	// ExpectedVersion is the merchant version the owner was looking at: a change
	// made from a stale screen is refused rather than silently applied.
	ExpectedVersion *int `json:"expectedVersion"`
}

// PaySettlementResult is what an owner's change came to: waiting on an emailed
// code (Pending), or a no-op because the destination was already this.
type PaySettlementResult struct {
	Merchant *models.PayMerchant
	Pending  *PayOwnerActionPending
}

// paySettlementPayload is a payout change as it waits on its code: the
// destination as the request validated it and, for a bank account, the name the
// bank resolved it to — the name the owner was shown and the code's email named.
type paySettlementPayload struct {
	// SettlementMode is empty on a crypto change requested before bank changes
	// also needed a code.
	SettlementMode string `json:"settlementMode,omitempty"`
	BankCode       string `json:"bankCode,omitempty"`
	AccountNumber  string `json:"accountNumber,omitempty"`
	AccountName    string `json:"accountName,omitempty"`
	ChainID        int64  `json:"chainId,omitempty"`
	TokenAddress   string `json:"tokenAddress,omitempty"`
	WalletAddress  string `json:"walletAddress,omitempty"`
}

// newPaySettlementPayload is the waiting form of a validated destination.
func newPaySettlementPayload(u *models.PayMerchant) paySettlementPayload {
	if u.SettlementMode == models.PaySettlementFiat {
		return paySettlementPayload{SettlementMode: u.SettlementMode, BankCode: u.BankCode, AccountNumber: u.AccountNumber, AccountName: u.AccountName}
	}
	return paySettlementPayload{SettlementMode: u.SettlementMode, ChainID: u.ChainID, TokenAddress: u.TokenAddress, WalletAddress: u.WalletAddress}
}

// change is the owner's change the payload holds, made against version.
func (p paySettlementPayload) change(version int) PaySettlementChange {
	mode := p.SettlementMode
	if mode == "" {
		mode = models.PaySettlementCrypto
	}
	return PaySettlementChange{SettlementMode: mode, BankCode: p.BankCode, AccountNumber: p.AccountNumber,
		ChainID: p.ChainID, TokenAddress: p.TokenAddress, WalletAddress: p.WalletAddress, ExpectedVersion: &version}
}

// paySettlementDoing is what the code's email says the owner is confirming.
func paySettlementDoing(u *models.PayMerchant) string {
	if u.SettlementMode == models.PaySettlementFiat {
		return fmt.Sprintf("be paid to the bank account %s (%s)", payLast4(u.AccountNumber), u.AccountName)
	}
	return fmt.Sprintf("be paid in %s to the wallet %s", u.TokenSymbol, PayShortAddress(u.WalletAddress))
}

// GetByOwner returns the merchant a user owns, or ErrPayMerchantNotFound. A
// user has at most one — a merchant is provisioned from their one approved
// payment_acceptance request — and two is refused, never guessed between.
func (s *PayMerchantService) GetByOwner(ctx context.Context, userID uuid.UUID) (*models.PayMerchant, error) {
	var ms []models.PayMerchant
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Limit(2).Find(&ms).Error; err != nil {
		return nil, err
	}
	switch len(ms) {
	case 0:
		return nil, ErrPayMerchantNotFound
	case 1:
		return &ms[0], nil
	default:
		return nil, ErrPayMerchantAmbiguous
	}
}

// GetOrProvisionByOwner is GetByOwner for an approved merchant, who needs no
// second application for TSA Pay: the merchant approval is the review, so a
// merchant with a verified identity gets the TSA Pay account on first use,
// provisioned from their approved merchant request (settlement unset until they
// choose a destination in Payment Settings). Anyone else — not a merchant, not
// verified, or no usable request — gets exactly what GetByOwner says.
func (s *PayMerchantService) GetOrProvisionByOwner(ctx context.Context, userID uuid.UUID) (*models.PayMerchant, error) {
	m, err := s.GetByOwner(ctx, userID)
	if !errors.Is(err, ErrPayMerchantNotFound) {
		return m, err
	}
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPayMerchantNotFound
		}
		return nil, err
	}
	if user.Role != models.RoleMerchant {
		return nil, ErrPayMerchantNotFound
	}
	verified, err := IsKYCVerified(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if !verified {
		return nil, ErrPayMerchantNotFound
	}
	// A TSA Pay application of her own, in any state, is decided by its own
	// approval: provisioning here would bypass a rejection or leave a second
	// merchant row beside the one that approval creates.
	var applied int64
	if err := s.db.WithContext(ctx).Model(&models.MerchantRequest{}).
		Where("user_id = ? AND business_type = ?", userID, models.BusinessTypePaymentAcceptance).
		Count(&applied).Error; err != nil {
		return nil, err
	}
	if applied > 0 {
		return nil, ErrPayMerchantNotFound
	}
	var req models.MerchantRequest
	err = s.db.WithContext(ctx).
		Where("user_id = ? AND status = ? AND business_type IN ?", userID, models.MerchantRequestStatusApproved,
			[]string{models.BusinessTypeGeneralProducts, models.BusinessTypeDigitalProducts,
				models.BusinessTypeServiceProvider, models.BusinessTypeP2PMerchant}).
		// A marketplace business's name is the one she chose for selling; a
		// payment-merchant row is the fallback.
		Order("CASE WHEN business_type = 'p2p_merchant' THEN 1 ELSE 0 END, created_at DESC").First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPayMerchantNotFound
	}
	if err != nil {
		return nil, err
	}
	// A marketplace request carries no TSA Pay website or settlement. Settlement
	// starts from the merchant's payout account when they have a naira bank one
	// (CreateFromRequest re-checks the name at the bank), else it is left blank
	// for them to choose in Payment Settings.
	req.Website = ""
	req.SettlementBankCode, req.SettlementAccountNumber, req.SettlementAccountName = "", "", ""
	account, err := MerchantBankAccount(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if account != nil {
		req.SettlementBankCode, req.SettlementAccountNumber, req.SettlementAccountName =
			account.InstitutionCode, account.AccountNumber, account.AccountName
	}
	created, err := s.CreateFromRequest(ctx, &req)
	if err != nil {
		// Not "no merchant": a name that breaks the TSA Pay rule maps to its own
		// refusal in payOwnerFail, anything else to a server error.
		log.Printf("[tsa-pay] provisioning merchant %s from request %s failed: %v", userID, req.ID, err)
		return nil, err
	}
	return created, nil
}

// FollowPayoutAccount points a merchant's TSA Pay bank settlement at their
// payout account, so changing the payout account changes it here too. The owner
// just changed it through its own guarded flow (password and emailed code), so
// this goes through the same audited update as any settlement edit — the bank
// re-resolves the name, the version moves, and the payout cool-off starts —
// but asks for nothing more. It deliberately leaves alone a merchant with no
// TSA Pay account, a suspended one, and one settling in crypto, which is a
// choice the payout account cannot make for them. Nothing to do is not an error.
func (s *PayMerchantService) FollowPayoutAccount(ctx context.Context, ownerID uuid.UUID, bankCode, accountNumber string) error {
	m, err := s.GetByOwner(ctx, ownerID)
	if errors.Is(err, ErrPayMerchantNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.Status != models.PayMerchantActive || m.SettlementMode == models.PaySettlementCrypto {
		return nil
	}
	if m.SettlementMode == models.PaySettlementFiat && m.BankCode == bankCode && m.AccountNumber == accountNumber {
		return nil
	}
	in, err := s.ownerUpdate(m, PaySettlementChange{SettlementMode: models.PaySettlementFiat, BankCode: bankCode, AccountNumber: accountNumber})
	if err != nil {
		return err
	}
	_, _, err = s.update(ctx, ownerID, m.ID, in, nil)
	return err
}

// UpdateSettlementByOwner is a merchant changing where they are paid. It is the
// admin Update with more rules, because the caller is the account an attacker
// would take over to redirect a merchant's money:
//   - only the settlement destination can change — never the merchant's name,
//     status, limits, webhooks or return domains — and not while suspended;
//   - a bank account may be in any name (a store often has no account of its
//     own — owner 2026-10-17), but it must resolve, and the owner sees the
//     bank's name for it before saving;
//   - a crypto destination is only ever a listed stablecoin;
//   - either is applied only once the owner enters the code emailed to them (a
//     guarded owner action — see ConfirmOwnerAction). Everything above is
//     checked first, so a change that would be refused sends no code;
//   - a change to the destination it already has writes and announces nothing.
//
// Everything else — resolving the account, the chain and token rules, the
// version check and the audit row (with the owner as its actor) — is Update's.
func (s *PayMerchantService) UpdateSettlementByOwner(ctx context.Context, ownerID uuid.UUID, ch PaySettlementChange) (*PaySettlementResult, error) {
	m, err := s.activeOwnedMerchant(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	in, err := s.ownerUpdate(m, ch)
	if err != nil {
		return nil, err
	}
	edit, err := s.prepare(ctx, m, in, nil)
	if err != nil {
		return nil, err
	}
	if sameSettlement(&edit.before, &edit.updated) {
		return &PaySettlementResult{Merchant: m}, nil
	}
	u := &edit.updated
	pending, err := s.requestOwnerAction(ctx, ownerID, m, models.PayOwnerActionSettlement, newPaySettlementPayload(u), paySettlementDoing(u))
	if err != nil {
		return nil, err
	}
	return &PaySettlementResult{Merchant: m, Pending: pending}, nil
}

// confirmSettlementByOwner applies a payout change the owner confirmed with the
// emailed code, through Update against the version it was requested at and to
// the rules it was requested under. A bank account must still resolve to the
// name the owner was shown.
func (s *PayMerchantService) confirmSettlementByOwner(ctx context.Context, ownerID uuid.UUID, m *models.PayMerchant, a *models.PayOwnerAction) (*models.PayMerchant, error) {
	var p paySettlementPayload
	if err := json.Unmarshal([]byte(a.Payload), &p); err != nil {
		return nil, err
	}
	in, err := s.ownerUpdate(m, p.change(a.BaseVersion))
	if err != nil {
		return nil, err
	}
	updated, _, err := s.update(ctx, ownerID, m.ID, in, func(u *models.PayMerchant) error {
		if u.SettlementMode == models.PaySettlementFiat && u.AccountName != p.AccountName {
			return fmt.Errorf("%w: %w: the account no longer resolves to the name that was confirmed", ErrPayMerchantInvalid, ErrPayAccountUnverifiable)
		}
		return nil
	})
	return updated, err
}

// ownerUpdate is the admin edit an owner's change amounts to: the settlement
// fields of one mode and nothing else. A crypto token must be a listed
// stablecoin on its chain, and its symbol is the listing's.
func (s *PayMerchantService) ownerUpdate(m *models.PayMerchant, ch PaySettlementChange) (PayMerchantUpdate, error) {
	mode := strings.TrimSpace(ch.SettlementMode)
	version := m.Version
	if ch.ExpectedVersion != nil {
		version = *ch.ExpectedVersion
	}
	in := PayMerchantUpdate{SettlementMode: &mode, ExpectedVersion: &version}
	switch mode {
	case models.PaySettlementFiat:
		in.BankCode, in.AccountNumber = &ch.BankCode, &ch.AccountNumber
	case models.PaySettlementCrypto:
		symbol, err := s.listedStablecoin(ch.ChainID, ch.TokenAddress)
		if err != nil {
			return in, err
		}
		token, wallet := strings.TrimSpace(ch.TokenAddress), strings.TrimSpace(ch.WalletAddress)
		in.ChainID, in.TokenAddress, in.TokenSymbol, in.WalletAddress = &ch.ChainID, &token, &symbol, &wallet
	default:
		return in, fmt.Errorf("%w: settlement must be %q or %q", ErrPayMerchantInvalid, models.PaySettlementFiat, models.PaySettlementCrypto)
	}
	return in, nil
}

// listedStablecoin is the symbol of token when it is one of
// payOwnerSettlementTokens on chainID.
func (s *PayMerchantService) listedStablecoin(chainID int64, token string) (string, error) {
	if s.chains == nil {
		return "", fmt.Errorf("%w: no chain registry configured for crypto settlement", ErrPayMerchantInvalid)
	}
	return payListedStablecoin(s.chains, chainID, token)
}

// payListedStablecoin is the symbol of token when it is one of
// payOwnerSettlementTokens as listed on chainID — what an owner may be paid in,
// and what a refund may be sized in and paid with.
func payListedStablecoin(chains payChainRegistry, chainID int64, token string) (string, error) {
	chain, ok := chains.MainnetChainNameForID(chainID)
	if !ok {
		return "", fmt.Errorf("%w: chain id %d is not a supported settlement chain", ErrPayMerchantInvalid, chainID)
	}
	for _, symbol := range payOwnerSettlementTokens {
		if listed := chains.TokenAddress(chain, symbol); listed != "" && sameTokenAddress(listed, token) {
			return symbol, nil
		}
	}
	return "", fmt.Errorf("%w: you can be paid in %s on %s", ErrPayMerchantInvalid, strings.Join(payOwnerSettlementTokens, " or "), chain)
}

// PaySettlementOption is one stablecoin an owner may be paid in: exactly what
// listedStablecoin accepts, so a picker built from these can only offer what a
// change will take.
type PaySettlementOption struct {
	ChainID      int64  `json:"chainId"`
	Chain        string `json:"chain"`
	Symbol       string `json:"symbol"`
	TokenAddress string `json:"tokenAddress"`
}

// SettlementOptions is every (chain, stablecoin) an owner may be paid in,
// ordered by chain id then symbol. None without a chain registry.
func (s *PayMerchantService) SettlementOptions() []PaySettlementOption {
	out := []PaySettlementOption{}
	if s.chains == nil {
		return out
	}
	for _, id := range s.chains.MainnetChainIDs() {
		chain, ok := s.chains.MainnetChainNameForID(id)
		if !ok {
			continue
		}
		for _, symbol := range payOwnerSettlementTokens { // already in symbol order
			if addr := s.chains.TokenAddress(chain, symbol); addr != "" {
				out = append(out, PaySettlementOption{ChainID: id, Chain: chain, Symbol: symbol, TokenAddress: addr})
			}
		}
	}
	return out
}

// Banks is the bank directory an owner picks a payout bank from — the same one
// a settlement change is resolved against.
func (s *PayMerchantService) Banks(ctx context.Context) ([]NombaBank, error) {
	if s.banks == nil {
		return nil, ErrPayBanksUnavailable
	}
	return s.banks.ListBanks(ctx)
}

// ResolveBank is the name enquiry an owner checks an account with before
// saving it.
func (s *PayMerchantService) ResolveBank(ctx context.Context, bankCode, accountNumber string) (NombaLookupResult, error) {
	if s.banks == nil {
		return NombaLookupResult{}, ErrPayBanksUnavailable
	}
	return s.banks.Resolve(ctx, bankCode, accountNumber)
}

// sameSettlement reports whether two merchants are paid to the same place.
func sameSettlement(a, b *models.PayMerchant) bool {
	if a.SettlementMode != b.SettlementMode {
		return false
	}
	switch a.SettlementMode {
	case models.PaySettlementFiat:
		return a.BankCode == b.BankCode && a.AccountNumber == b.AccountNumber
	case models.PaySettlementCrypto:
		return a.ChainID == b.ChainID && sameTokenAddress(a.TokenAddress, b.TokenAddress) && strings.EqualFold(a.WalletAddress, b.WalletAddress)
	}
	return true
}

// PayShortAddress is a wallet as the owner should compare it: its first six and
// last four characters, never only a suffix a lookalike address could share.
func PayShortAddress(a string) string {
	a = strings.TrimSpace(a)
	if len(a) <= 12 {
		return a
	}
	return a[:6] + "…" + a[len(a)-4:]
}

// update is Update with an optional check on the resulting merchant, run after
// validatePayMerchant (which resolves a bank account's name) and before
// anything is written: a refusal from check persists nothing. It returns the
// merchant after the edit and as it was before it.
func (s *PayMerchantService) update(ctx context.Context, actor uuid.UUID, id uuid.UUID, in PayMerchantUpdate, check func(*models.PayMerchant) error) (*models.PayMerchant, *models.PayMerchant, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	edit, err := s.prepare(ctx, m, in, check)
	if err != nil {
		return nil, nil, err
	}
	updated, err := s.commit(ctx, actor, edit, in.Sensitive())
	if err != nil {
		return nil, nil, err
	}
	return updated, &edit.before, nil
}

// payEdit is a validated edit of one merchant that has not been written.
type payEdit struct {
	before, updated models.PayMerchant
	// changes holds only the columns the edit supplied, so a concurrent edit to
	// any other column is never overwritten with a stale copy.
	changes map[string]interface{}
}

// prepare applies in to m in memory and validates the result, check included.
func (s *PayMerchantService) prepare(ctx context.Context, m *models.PayMerchant, in PayMerchantUpdate, check func(*models.PayMerchant) error) (*payEdit, error) {
	if in.ExpectedVersion != nil && *in.ExpectedVersion != m.Version {
		return nil, ErrPayMerchantStale
	}

	before := *m
	updated := *m
	changes := map[string]interface{}{}

	if in.BusinessName != nil {
		// A super admin's rename is held to the same rule as an owner's.
		name, err := s.normalizeBusinessName(*in.BusinessName)
		if err != nil {
			return nil, err
		}
		updated.BusinessName = name
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
	if in.LiveEnabled != nil {
		updated.LiveEnabled = *in.LiveEnabled
		changes["live_enabled"] = updated.LiveEnabled
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
	if in.AllowedIPs != nil {
		ips, err := normalizePayAllowedIPs(in.AllowedIPs)
		if err != nil {
			return nil, err
		}
		updated.AllowedIPs = ips
		changes["allowed_ips"] = updated.AllowedIPs
	}

	if err := s.validatePayMerchant(ctx, &before, &updated); err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(&updated); err != nil {
			return nil, err
		}
	}
	if updated.AccountName != before.AccountName {
		changes["account_name"] = updated.AccountName // set by the name enquiry
	}
	if !sameSettlement(&before, &updated) {
		// Outstanding payments wait a day before they follow it (payoutCoolOff).
		now := s.now()
		updated.PayoutChangedAt = &now
		changes["payout_changed_at"] = &now
	}
	return &payEdit{before: before, updated: updated, changes: changes}, nil
}

// commit writes a prepared edit: compare-and-set on the version it was read at,
// with its audit row in the same transaction. sensitive logs the settlement and
// endpoint fields it touched.
func (s *PayMerchantService) commit(ctx context.Context, actor uuid.UUID, e *payEdit, sensitive bool) (*models.PayMerchant, error) {
	before, updated, changes, id := e.before, e.updated, e.changes, e.before.ID
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		won, err := s.casUpdate(tx, id, before.Version, changes)
		if err != nil {
			return err
		}
		if !won {
			return ErrPayMerchantStale
		}
		// An admin renaming the merchant directly answers any rename the
		// owner still has waiting.
		if before.BusinessName != updated.BusinessName {
			if err := supersedePendingBusinessNames(tx, id, "superseded by an admin edit of the business name"); err != nil {
				return err
			}
			// The owner's other merchant records follow, as they do for an approved rename.
			if _, err := renameOwnersBusiness(tx, before.UserID, updated.BusinessName); err != nil {
				return err
			}
		}
		return s.writeAudit(tx, id, actor, models.PayAuditActionMerchantUpdate, payAuditChanges(&before, &updated))
	})
	if err != nil {
		return nil, err
	}
	updated.Version = before.Version + 1

	if sensitive {
		log.Printf("[tsa-pay] AUDIT merchant=%s actor=%s fields=settlement liveEnabled=%t->%t bank=%s->%s account=%s->%s wallet=%s->%s webhookUrl=%q->%q testWebhookUrl=%q->%q returnDomains=%q->%q allowedIPs=%q->%q",
			id, actor, before.LiveEnabled, updated.LiveEnabled,
			payLast4(before.BankCode), payLast4(updated.BankCode),
			payLast4(before.AccountNumber), payLast4(updated.AccountNumber),
			before.WalletAddress, updated.WalletAddress,
			PayRedactURL(before.WebhookURL), PayRedactURL(updated.WebhookURL),
			PayRedactURL(before.TestWebhookURL), PayRedactURL(updated.TestWebhookURL),
			before.ReturnDomains, updated.ReturnDomains,
			before.AllowedIPs, updated.AllowedIPs,
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
		// Resolved on every way INTO fiat, not only when the account changes: bank
		// fields saved while unconfigured (or kept from an earlier fiat spell)
		// were never verified for this settlement, and a two-step edit — account
		// first, then the mode — would otherwise go live on an unverified account.
		if before.SettlementMode != models.PaySettlementFiat || updated.AccountName == "" ||
			before.BankCode != updated.BankCode || before.AccountNumber != updated.AccountNumber {
			if s.banks == nil {
				return fmt.Errorf("%w: no bank directory configured to resolve the account", ErrPayMerchantInvalid)
			}
			res, err := s.banks.Resolve(ctx, updated.BankCode, updated.AccountNumber)
			if err != nil {
				return fmt.Errorf("%w: %w: %v", ErrPayMerchantInvalid, ErrPayAccountUnverifiable, err)
			}
			updated.AccountName = strings.TrimSpace(res.AccountName)
		}
		if updated.AccountName == "" {
			return fmt.Errorf("%w: the bank account resolved to no account name", ErrPayMerchantInvalid)
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
			out[field] = map[string]any{"before": PayRedactURL(b), "after": PayRedactURL(a)}
		}
	}
	addURL("webhookUrl", before.WebhookURL, after.WebhookURL)
	addURL("testWebhookUrl", before.TestWebhookURL, after.TestWebhookURL)
	add("returnDomains", before.ReturnDomains, after.ReturnDomains)
	add("allowedIps", before.AllowedIPs, after.AllowedIPs)
	add("liveEnabled", before.LiveEnabled, after.LiveEnabled)
	return out
}

// writeAudit records one audit event inside tx, so it commits or rolls back
// with the change it describes.
func (s *PayMerchantService) writeAudit(tx *gorm.DB, merchantID, actor uuid.UUID, action models.PayAuditAction, details any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return tx.Create(&models.PayAuditEvent{MerchantID: merchantID, ActorID: actor, Action: action, Details: string(raw)}).Error
}

// PayRedactURL makes a merchant-supplied URL safe for an audit row or log
// line: only scheme and host are kept. Userinfo is dropped, and the path,
// query and fragment — where a webhook token lives (Slack-style hooks carry
// it in the path) — become "/…". Empty stays empty; anything that is not an absolute URL is
// "[unparseable url]" rather than echoed back.
func PayRedactURL(raw string) string {
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
		return s.writeAudit(tx, id, actor, models.PayAuditActionMerchantKeys, map[string]any{"scope": scope})
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

// payRevokeReasonMax bounds the reason an admin gives for a revoke: it is
// stored in the audit row and sent to the owner by email and push.
const payRevokeReasonMax = 280

// payRevokeLinkRe spots a link or web address. The revoke notice reaches the
// owner as a trusted, unmutable TSA Connect email right after telling them to
// make a new key — a place a rogue admin could plant a phishing link.
var payRevokeLinkRe = regexp.MustCompile(`(?i)://|\bwww\.|\b[a-z0-9-]{2,}\.[a-z]{2,}\b`)

// PayRevokeReason normalises an admin's revoke reason to one line and refuses
// one the owner should not be shown: empty, too long, carrying invisible or
// direction-changing characters, or a link.
func PayRevokeReason(raw string) (string, error) {
	reason := strings.Join(strings.Fields(raw), " ")
	switch {
	case reason == "":
		return "", fmt.Errorf("%w: give a reason — the merchant is told it", ErrPayMerchantInvalid)
	case utf8.RuneCountInString(reason) > payRevokeReasonMax:
		return "", fmt.Errorf("%w: keep the reason to %d characters", ErrPayMerchantInvalid, payRevokeReasonMax)
	case strings.IndexFunc(reason, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0:
		return "", fmt.Errorf("%w: the reason has hidden characters — retype it", ErrPayMerchantInvalid)
	case payRevokeLinkRe.MatchString(reason):
		return "", fmt.Errorf("%w: leave links and web addresses out of the reason — the merchant is emailed it", ErrPayMerchantInvalid)
	}
	return reason, nil
}

// RevokeKeys switches off one of a merchant's credentials — the live or test
// API key, or both webhook signing secrets — without minting a replacement,
// so no admin ever holds a working merchant secret. The owner creates a new
// key, or reveals the re-keyed webhook secret, on the web app, behind their
// password and an emailed code. reason is required and audited.
//
// Like IssueKeys it leaves the merchant's version alone: bumping it would
// stale every pending owner settlement/endpoints change and the admin's own
// open edit form. A pending owner key roll is left to run too — confirming it
// mints the replacement the revoke notice asks for.
func (s *PayMerchantService) RevokeKeys(ctx context.Context, actor, id uuid.UUID, scope PayKeyScope, reason string) (*models.PayMerchant, error) {
	reason, err := PayRevokeReason(reason)
	if err != nil {
		return nil, err
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.revoke(ctx, actor, m, scope, reason)
}

// revoke applies RevokeKeys to the credential as m read it, and only to that
// one: a key the owner rolled, or secrets another admin re-keyed, since the
// read makes it ErrPayMerchantStale — so a revoke never kills a replacement
// it was not about, nor tells the owner twice about one action.
func (s *PayMerchantService) revoke(ctx context.Context, actor uuid.UUID, m *models.PayMerchant, scope PayKeyScope, reason string) (*models.PayMerchant, error) {
	out := *m
	var guard string
	var guardValue any
	var updates map[string]interface{}
	switch scope {
	case PayKeysLive:
		if m.LiveKeyHash == nil {
			return nil, fmt.Errorf("%w: this merchant has no live key to revoke", ErrPayMerchantInvalid)
		}
		guard, guardValue = "live_key_hash = ?", *m.LiveKeyHash
		updates = map[string]interface{}{"live_key_hash": nil, "live_key_prefix": ""}
		out.LiveKeyHash, out.LiveKeyPrefix = nil, ""
	case PayKeysTest:
		if m.TestKeyHash == nil {
			return nil, fmt.Errorf("%w: this merchant has no test key to revoke", ErrPayMerchantInvalid)
		}
		guard, guardValue = "test_key_hash = ?", *m.TestKeyHash
		updates = map[string]interface{}{"test_key_hash": nil, "test_key_prefix": ""}
		out.TestKeyHash, out.TestKeyPrefix = nil, ""
	case PayKeysWebhook:
		// The secrets are derived from this version, so bumping it is the revoke.
		guard, guardValue = "webhook_secret_version = ?", m.WebhookSecretVersion
		updates = map[string]interface{}{"webhook_secret_version": m.WebhookSecretVersion + 1}
		out.WebhookSecretVersion++
	default:
		return nil, fmt.Errorf("%w: revoke the live key, the test key or the webhook secrets", ErrPayMerchantInvalid)
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.PayMerchant{}).Where("id = ?", m.ID).Where(guard, guardValue).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrPayMerchantStale
		}
		return s.writeAudit(tx, m.ID, actor, models.PayAuditActionMerchantKeysRevoked, map[string]any{"scope": scope, "reason": reason})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[tsa-pay] AUDIT merchant=%s actor=%s keys revoked scope=%s", m.ID, actor, scope)
	return &out, nil
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
