package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Guarded owner actions: a merchant owner's changes that a stolen login would
// make to take money or impersonate the merchant are applied only after the
// owner enters the code emailed to them. Requested with the account password
// (checked by the handler), stored as a PayOwnerAction, applied by
// ConfirmOwnerAction.

var (
	// ErrPayCodeUnavailable: no code can be sent (no mailer, or no email on the account).
	ErrPayCodeUnavailable = errors.New("we can't send you a confirmation code right now")
	// ErrPayActionNoPending: nothing waiting — never requested, expired, used up or superseded.
	ErrPayActionNoPending = errors.New("there is no change waiting to be confirmed")
	// ErrPayCodeInvalid: the code does not match.
	ErrPayCodeInvalid = errors.New("that code isn't right")
	// ErrPayActionBusy: another request of the same kind kept winning the race
	// to store its action.
	ErrPayActionBusy = errors.New("another change is being requested")
)

// PayOwnerActionPending tells the owner a code is on its way, and where.
type PayOwnerActionPending struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"`
	ExpiresAt time.Time `json:"expiresAt"`
	SentTo    string    `json:"sentTo"`
}

// PayOwnerActionResult is what a confirmed action produced. Key and the webhook
// secrets are returned exactly once, here.
type PayOwnerActionResult struct {
	Kind              string
	Merchant          *models.PayMerchant
	Mode              string
	Key               string
	SecretAction      string
	WebhookSecretLive string
	WebhookSecretTest string
}

// PayEndpointsChange is where the merchant is told about payments and may send
// customers back to, and where its live key may be used from. AllowedIPs nil
// leaves the allowlist as it is (a client that predates it never clears it);
// empty clears it.
type PayEndpointsChange struct {
	WebhookURL      string   `json:"webhookUrl"`
	TestWebhookURL  string   `json:"testWebhookUrl"`
	ReturnDomains   []string `json:"returnDomains"`
	AllowedIPs      []string `json:"allowedIps"`
	ExpectedVersion *int     `json:"expectedVersion"`
}

type payKeysPayload struct {
	Mode string `json:"mode"`
}

type paySecretPayload struct {
	Action string `json:"action"` // "reveal" | "roll"
}

// SetMailer lets owners confirm guarded actions; without it they cannot.
func (s *PayMerchantService) SetMailer(m CodeMailer) { s.mailer = m }

// RequestKeyRoll asks to roll the live or test API key.
func (s *PayMerchantService) RequestKeyRoll(ctx context.Context, ownerID uuid.UUID, mode string) (*PayOwnerActionPending, error) {
	if mode != models.PayModeLive && mode != models.PayModeTest {
		return nil, fmt.Errorf("%w: mode must be live or test", ErrPayMerchantInvalid)
	}
	m, err := s.activeOwnedMerchant(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	return s.requestOwnerAction(ctx, ownerID, m, models.PayOwnerActionKeys, payKeysPayload{Mode: mode},
		fmt.Sprintf("roll your %s API key (the current %s key stops working when you confirm)", mode, mode))
}

// RequestWebhookSecret asks to reveal or roll the webhook signing secret.
func (s *PayMerchantService) RequestWebhookSecret(ctx context.Context, ownerID uuid.UUID, action string) (*PayOwnerActionPending, error) {
	if action != "reveal" && action != "roll" {
		return nil, fmt.Errorf("%w: action must be reveal or roll", ErrPayMerchantInvalid)
	}
	m, err := s.activeOwnedMerchant(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	return s.requestOwnerAction(ctx, ownerID, m, models.PayOwnerActionWebhookSecret, paySecretPayload{Action: action},
		action+" your webhook signing secret")
}

// RequestEndpointsChange validates the new webhook URLs and return domains now
// (so a bad URL is refused before a code is sent) and asks to apply them.
func (s *PayMerchantService) RequestEndpointsChange(ctx context.Context, ownerID uuid.UUID, ch PayEndpointsChange) (*PayOwnerActionPending, error) {
	m, err := s.activeOwnedMerchant(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if _, err := s.prepare(ctx, m, endpointsUpdate(ch, m.Version), nil); err != nil {
		return nil, err
	}
	ch.ExpectedVersion = nil // the action's BaseVersion carries it from here
	return s.requestOwnerAction(ctx, ownerID, m, models.PayOwnerActionEndpoints, ch,
		"change where TSA Pay sends your webhooks, may return your customers and accepts your live API key from")
}

// endpointsUpdate is the admin edit an endpoints change amounts to: both webhook
// URLs, the whole return-domain list and the allowlist when ch carries one,
// against version unless ch names one.
func endpointsUpdate(ch PayEndpointsChange, version int) PayMerchantUpdate {
	web, test := strings.TrimSpace(ch.WebhookURL), strings.TrimSpace(ch.TestWebhookURL)
	domains := ch.ReturnDomains
	if domains == nil {
		domains = []string{}
	}
	if ch.ExpectedVersion != nil {
		version = *ch.ExpectedVersion
	}
	return PayMerchantUpdate{WebhookURL: &web, TestWebhookURL: &test, ReturnDomains: domains, AllowedIPs: ch.AllowedIPs, ExpectedVersion: &version}
}

// activeOwnedMerchant is the owner's merchant, refused while suspended.
func (s *PayMerchantService) activeOwnedMerchant(ctx context.Context, ownerID uuid.UUID) (*models.PayMerchant, error) {
	m, err := s.GetOrProvisionByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if m.Status == models.PayMerchantSuspended {
		return nil, ErrPayMerchantSuspended
	}
	return m, nil
}

// requestOwnerAction stores the action (replacing any older one of its kind) and
// emails its code. A code that could not be sent leaves nothing waiting.
func (s *PayMerchantService) requestOwnerAction(ctx context.Context, ownerID uuid.UUID, m *models.PayMerchant, kind string, payload any, doing string) (*PayOwnerActionPending, error) {
	if s.mailer == nil {
		return nil, ErrPayCodeUnavailable
	}
	var owner struct {
		Email sql.NullString
		Name  sql.NullString
	}
	if err := s.db.WithContext(ctx).Table("users").Select("email", "name").Where("id = ?", ownerID).Limit(1).Scan(&owner).Error; err != nil {
		return nil, err
	}
	email := strings.TrimSpace(owner.Email.String)
	if email == "" {
		return nil, ErrPayCodeUnavailable
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	code, err := NewEmailCode()
	if err != nil {
		return nil, err
	}
	action := models.PayOwnerAction{ID: uuid.New(), MerchantID: m.ID, Kind: kind, OwnerID: ownerID, Payload: string(body),
		BaseVersion: m.Version, ExpiresAt: s.now().Add(EmailCodeTTL)}
	action.CodeHash = EmailCodeHash(action.ID, code)
	if err := s.replaceOwnerAction(ctx, &action); err != nil {
		return nil, err
	}
	message := fmt.Sprintf("Your code to %s for %s is %s. It expires in %d minutes.\n\n"+
		"If you didn't ask for this, don't share the code: change your password and contact support.",
		doing, m.BusinessName, code, int(EmailCodeTTL/time.Minute))
	if err := s.mailer.Send(email, owner.Name.String, "Your TSA Pay confirmation code", "Confirm this change", message, "", ""); err != nil {
		log.Printf("[tsa-pay] %s code for merchant %s could not be sent: %v", kind, m.ID, err)
		s.dropOwnerAction(ctx, action.ID)
		return nil, ErrPayCodeUnavailable
	}
	return &PayOwnerActionPending{ID: action.ID, Kind: kind, ExpiresAt: action.ExpiresAt, SentTo: MaskEmail(email)}, nil
}

// replaceOwnerAction stores a, replacing any waiting action of its kind. A
// concurrent request of the same kind can insert between the delete and the
// insert; that is retried once, then refused as busy rather than a 500.
func (s *PayMerchantService) replaceOwnerAction(ctx context.Context, a *models.PayOwnerAction) error {
	var err error
	for try := 0; try < 2; try++ {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("merchant_id = ? AND kind = ?", a.MerchantID, a.Kind).Delete(&models.PayOwnerAction{}).Error; err != nil {
				return err
			}
			return tx.Create(a).Error
		})
		if !isUniqueViolation(err) {
			return err
		}
	}
	return ErrPayActionBusy
}

// ConfirmOwnerAction applies the owner's waiting action with code. The attempt
// is spent before the code is compared, and the action is claimed (deleted)
// before it is applied, so it applies at most once.
func (s *PayMerchantService) ConfirmOwnerAction(ctx context.Context, ownerID, actionID uuid.UUID, code string) (*PayOwnerActionResult, error) {
	m, err := s.activeOwnedMerchant(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	var a models.PayOwnerAction
	if err := s.db.WithContext(ctx).Where("id = ? AND merchant_id = ? AND owner_id = ?", actionID, m.ID, ownerID).First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPayActionNoPending
		}
		return nil, err
	}
	if !s.now().Before(a.ExpiresAt) {
		s.dropOwnerAction(ctx, a.ID)
		return nil, ErrPayActionNoPending
	}
	// The action is claimed (deleted) before it is applied: of two confirms
	// racing with the right code, only the Claimed one goes on — so a key is
	// minted once. An apply error after the claim uses the action up and the
	// owner starts again: deliberate, so nothing can ever be applied twice.
	outcome, err := RedeemEmailCode(ctx, s.db, &models.PayOwnerAction{}, a.ID, a.Attempts, a.CodeHash, code)
	if err != nil {
		return nil, err
	}
	switch outcome {
	case EmailCodeClaimed:
	case EmailCodeWrong:
		return nil, ErrPayCodeInvalid
	default: // exhausted, or another confirm claimed it first
		return nil, ErrPayActionNoPending
	}
	// Settlement and endpoints are checked against BaseVersion by update(); keys
	// and the webhook secret don't bump the version, but a change in between still
	// means the owner confirmed something they no longer see.
	if m.Version != a.BaseVersion {
		return nil, ErrPayMerchantStale
	}
	return s.applyOwnerAction(ctx, ownerID, m, &a)
}

func (s *PayMerchantService) applyOwnerAction(ctx context.Context, ownerID uuid.UUID, m *models.PayMerchant, a *models.PayOwnerAction) (*PayOwnerActionResult, error) {
	out := &PayOwnerActionResult{Kind: a.Kind}
	switch a.Kind {
	case models.PayOwnerActionSettlement:
		updated, err := s.confirmSettlementByOwner(ctx, ownerID, m, a)
		if err != nil {
			return nil, err
		}
		out.Merchant = updated
	case models.PayOwnerActionBusinessName:
		updated, err := s.confirmBusinessNameByOwner(ctx, ownerID, m, a)
		if err != nil {
			return nil, err
		}
		out.Merchant = updated
	case models.PayOwnerActionEndpoints:
		var ch PayEndpointsChange
		if err := json.Unmarshal([]byte(a.Payload), &ch); err != nil {
			return nil, err
		}
		updated, _, err := s.update(ctx, ownerID, m.ID, endpointsUpdate(ch, a.BaseVersion), nil)
		if err != nil {
			return nil, err
		}
		out.Merchant = updated
	case models.PayOwnerActionKeys:
		var p payKeysPayload
		if err := json.Unmarshal([]byte(a.Payload), &p); err != nil {
			return nil, err
		}
		scope := PayKeysTest
		if p.Mode == models.PayModeLive {
			scope = PayKeysLive
		}
		keys, err := s.IssueKeys(ctx, ownerID, m.ID, scope)
		if err != nil {
			return nil, err
		}
		out.Mode = p.Mode
		out.Key = keys.TestKey
		if p.Mode == models.PayModeLive {
			out.Key = keys.LiveKey
		}
	case models.PayOwnerActionWebhookSecret:
		var p paySecretPayload
		if err := json.Unmarshal([]byte(a.Payload), &p); err != nil {
			return nil, err
		}
		out.SecretAction = p.Action
		if p.Action == "roll" {
			keys, err := s.IssueKeys(ctx, ownerID, m.ID, PayKeysWebhook)
			if err != nil {
				return nil, err
			}
			out.WebhookSecretLive, out.WebhookSecretTest = keys.WebhookSecretLive, keys.WebhookSecretTest
		} else {
			out.WebhookSecretLive = s.WebhookSecret(m, models.PayModeLive)
			out.WebhookSecretTest = s.WebhookSecret(m, models.PayModeTest)
			if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return s.writeAudit(tx, m.ID, ownerID, models.PayAuditActionMerchantKeys, map[string]any{"scope": "webhook_secret_revealed"})
			}); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("%w: unknown action %q", ErrPayMerchantInvalid, a.Kind)
	}
	return out, nil
}

// dropOwnerAction ends one waiting action. Best effort: a row left behind is
// stale by version or expires on its own.
func (s *PayMerchantService) dropOwnerAction(ctx context.Context, id uuid.UUID) {
	if err := s.db.WithContext(ctx).Where("id = ?", id).Delete(&models.PayOwnerAction{}).Error; err != nil {
		log.Printf("[tsa-pay] could not clear owner action %s: %v", id, err)
	}
}
