package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// PayWebhookSchedule is the backoff schedule a failed webhook delivery is
// retried on: 1m, 5m, 30m, 2h, 6h, 24h. After the last attempt on this
// schedule fails, the delivery is given up on (failed_at set).
var PayWebhookSchedule = []time.Duration{
	time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour,
}

// payWebhookPerMerchantPerTick caps how many deliveries a single merchant
// gets in one DeliverDue call, so a merchant with a large backlog (or a
// webhook endpoint that never answers within its own turn) cannot starve
// every other merchant's events out of the same tick.
const payWebhookPerMerchantPerTick = 2

// payWebhookMerchantsPerTick caps how many distinct merchants' due
// deliveries are even considered in one DeliverDue call. It bounds the
// first-stage query's cost when the number of merchants with a backlog is
// itself large; it is not the fairness mechanism (that's every merchant up
// to this cap getting a fetch, regardless of how earliest-due rows sort).
const payWebhookMerchantsPerTick = 25

// payWebhookBodyReadLimit bounds how much of a webhook endpoint's response
// body is read — enough to see an error message, not enough for a
// misbehaving endpoint to make delivery expensive.
const payWebhookBodyReadLimit = 64 * 1024

// ErrPayWebhookDeliveryNotFound is returned by Resend when no delivery
// matches the given id.
var ErrPayWebhookDeliveryNotFound = errors.New("webhook delivery not found")

// PayWebhookSender delivers TSA Pay webhook events on a schedule, signing
// each payload with the receiving merchant's per-mode secret and never
// retrying a delivery the schedule has already given up on.
type PayWebhookSender struct {
	db        *gorm.DB
	merchants *PayMerchantService
	client    *http.Client
	now       func() time.Time
}

// NewPayWebhookSender constructs a PayWebhookSender. client is injected so
// tests can point deliveries at an httptest server without the production
// dialer's public-address/port-443 restriction getting in the way — see
// NewPayWebhookHTTPClient for the client production actually uses.
func NewPayWebhookSender(db *gorm.DB, merchants *PayMerchantService, client *http.Client, now func() time.Time) *PayWebhookSender {
	return &PayWebhookSender{db: db, merchants: merchants, client: client, now: now}
}

// NewPayWebhookHTTPClient builds the HTTP client production delivery uses:
// a 5s overall timeout, redirects never followed (a 3xx counts as a failed
// attempt), no proxy, and a dialer that refuses to connect to anything but
// a public IP address on port 443 — checked at dial time, AFTER DNS
// resolution, which is what defeats a webhook host that resolves
// differently on the second lookup (DNS rebinding) than it did when the
// merchant saved the URL (ValidPayWebhookURL, checked only once, at save
// time).
func NewPayWebhookHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 3 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !payPublicIP(ip) || port != "443" {
				return fmt.Errorf("tsa-pay webhook: refusing %s", address)
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: 3 * time.Second,
		MaxIdleConns:        20,
	}
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// DeliverDue sends webhook deliveries due now (delivered_at IS NULL AND
// failed_at IS NULL AND next_attempt_at <= now), up to limit rows fetched —
// but at most payWebhookPerMerchantPerTick actually delivered per merchant
// per call. A merchant whose backlog exceeds that cap simply waits for the
// next tick; its undelivered rows are untouched, so nothing is lost, only
// deferred.
//
// The fetch is fair across merchants: it first picks the distinct merchants
// with due rows (live mode first, then earliest next_attempt_at, capped at
// payWebhookMerchantsPerTick), then fetches each of those merchants' own
// due rows separately, capped per merchant at payWebhookPerMerchantPerTick
// and by the overall limit budget. A single global "ORDER BY next_attempt_at
// LIMIT" would let one merchant's backlog — if it happens to sort earliest —
// fill the whole batch and starve every other merchant out of the tick,
// since skipped rows keep the same next_attempt_at and get re-fetched
// (and re-dominated) next tick too.
func (s *PayWebhookSender) DeliverDue(ctx context.Context, limit int) (int, error) {
	now := s.now()
	const dueClause = "delivered_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?"

	var merchantIDs []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.PayWebhookDelivery{}).
		Where(dueClause, now).
		Group("merchant_id").
		Order("MIN(CASE WHEN mode = 'live' THEN 0 ELSE 1 END) ASC, MIN(next_attempt_at) ASC").
		Limit(payWebhookMerchantsPerTick).
		Pluck("merchant_id", &merchantIDs).Error; err != nil {
		return 0, err
	}

	var rows []models.PayWebhookDelivery
	for _, merchantID := range merchantIDs {
		if limit <= 0 {
			break
		}
		fetchLimit := payWebhookPerMerchantPerTick
		if limit < fetchLimit {
			fetchLimit = limit
		}
		var merchantRows []models.PayWebhookDelivery
		if err := s.db.WithContext(ctx).
			Where(dueClause+" AND merchant_id = ?", now, merchantID).
			Order("CASE WHEN mode = 'live' THEN 0 ELSE 1 END, next_attempt_at ASC").
			Limit(fetchLimit).
			Find(&merchantRows).Error; err != nil {
			return 0, err
		}
		rows = append(rows, merchantRows...)
		limit -= len(merchantRows)
	}

	delivered := 0
	perMerchant := make(map[uuid.UUID]int, len(rows))
	for i := range rows {
		if ctx.Err() != nil {
			return delivered, ctx.Err()
		}
		d := &rows[i]
		if perMerchant[d.MerchantID] >= payWebhookPerMerchantPerTick {
			continue
		}
		perMerchant[d.MerchantID]++

		ok, err := s.deliverOne(ctx, d)
		if err != nil {
			return delivered, err
		}
		if ok {
			delivered++
		}
	}
	return delivered, nil
}

// deliverOne attempts one delivery of d and records the outcome. It never
// returns an error for a failed HTTP call — only for something that keeps
// the row from being read/written at all — so one bad endpoint can't stop
// the rest of the tick.
func (s *PayWebhookSender) deliverOne(ctx context.Context, d *models.PayWebhookDelivery) (bool, error) {
	m, err := s.merchants.Get(ctx, d.MerchantID)
	if err != nil {
		if errors.Is(err, ErrPayMerchantNotFound) {
			// The merchant is gone; nothing will ever be able to deliver
			// this. Give up on it rather than retrying forever.
			return s.finalizeNoCall(ctx, d, "merchant not found")
		}
		return false, err
	}

	url := m.WebhookURLFor(d.Mode)
	switch {
	case url == "":
		return s.finalizeNoCall(ctx, d, "no webhook url configured for this mode")
	case !strings.HasPrefix(url, "https://"):
		return s.finalizeNoCall(ctx, d, "webhook url must be https")
	}

	ts := s.now().Unix()
	sig := SignPayWebhook(s.merchants.WebhookSecret(m, d.Mode), ts, []byte(d.Payload))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(d.Payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(PayWebhookSignatureHeader, sig)
	req.Header.Set("X-TSA-Event", d.Event)
	req.Header.Set("X-TSA-Delivery", d.ID.String())

	attempts := d.Attempts + 1
	resp, err := s.client.Do(req)
	if err != nil {
		return s.recordAttempt(ctx, d, attempts, 0, truncate(err.Error(), 255), false)
	}
	defer resp.Body.Close()
	// Best-effort drain, capped, so keep-alive can reuse the connection
	// without letting a misbehaving endpoint make delivery expensive; a
	// read failure here doesn't change the outcome already decided by the
	// status code.
	_, _ = io.CopyN(io.Discard, resp.Body, payWebhookBodyReadLimit)

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	errText := ""
	if !success {
		errText = truncate(fmt.Sprintf("http %d", resp.StatusCode), 255)
	}
	return s.recordAttempt(ctx, d, attempts, resp.StatusCode, errText, success)
}

// recordAttempt writes the outcome of one delivery attempt, guarded on the
// row's attempts count not having changed since it was read (WHERE id = ?
// AND attempts = <old>) — a defence against two senders racing the same
// delivery.
func (s *PayWebhookSender) recordAttempt(ctx context.Context, d *models.PayWebhookDelivery, attempts, statusCode int, lastErr string, success bool) (bool, error) {
	now := s.now()
	fields := map[string]any{
		"attempts":         attempts,
		"last_status_code": statusCode,
		"last_error":       lastErr,
	}
	switch {
	case success:
		fields["delivered_at"] = &now
		fields["next_attempt_at"] = nil
	case attempts <= len(PayWebhookSchedule):
		next := now.Add(PayWebhookSchedule[attempts-1])
		fields["next_attempt_at"] = &next
	default:
		fields["failed_at"] = &now
		fields["next_attempt_at"] = nil
	}

	res := s.db.WithContext(ctx).Model(&models.PayWebhookDelivery{}).
		Where("id = ? AND attempts = ?", d.ID, d.Attempts).
		Updates(fields)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// Someone else already recorded this attempt; not this call's win.
		return false, nil
	}
	return success, nil
}

// finalizeNoCall marks a delivery failed WITHOUT ever calling its URL — for
// a stored URL that fails the pre-check (empty, or not https). Guarded the
// same way recordAttempt is.
func (s *PayWebhookSender) finalizeNoCall(ctx context.Context, d *models.PayWebhookDelivery, reason string) (bool, error) {
	now := s.now()
	res := s.db.WithContext(ctx).Model(&models.PayWebhookDelivery{}).
		Where("id = ? AND attempts = ?", d.ID, d.Attempts).
		Updates(map[string]any{
			"failed_at":       &now,
			"next_attempt_at": nil,
			"last_error":      truncate(reason, 255),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return false, nil
}

// Resend requeues a delivery for immediate redelivery — an admin's "try
// again" on a webhook delivery that gave up (or, defensively, any other
// one): attempts and the last outcome are cleared and next_attempt_at is
// set to now, exactly as a freshly-enqueued delivery would be. It never
// calls the merchant's endpoint itself; the next DeliverDue tick does.
func (s *PayWebhookSender) Resend(ctx context.Context, deliveryID uuid.UUID) error {
	now := s.now()
	res := s.db.WithContext(ctx).Model(&models.PayWebhookDelivery{}).
		Where("id = ?", deliveryID).
		Updates(map[string]any{
			"attempts":         0,
			"last_status_code": 0,
			"last_error":       "",
			"delivered_at":     nil,
			"failed_at":        nil,
			"next_attempt_at":  &now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPayWebhookDeliveryNotFound
	}
	return nil
}
