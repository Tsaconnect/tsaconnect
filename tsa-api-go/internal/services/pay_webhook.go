package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	neturl "net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
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

// PayWebhookDefaultBatch is the row budget a worker tick passes to
// DeliverDue. It is exactly merchants-per-tick x per-merchant-per-tick, so
// the fairness fetch can always take a full slice from every merchant it
// picks: a smaller budget would starve the merchants sorted last, a larger
// one would never be spent.
const PayWebhookDefaultBatch = payWebhookMerchantsPerTick * payWebhookPerMerchantPerTick

// payWebhookConcurrency bounds how many merchants' deliveries run at once.
// Deliveries of ONE merchant never overlap (see DeliverDue); the bound only
// keeps a busy tick from opening an unbounded number of outbound connections.
const payWebhookConcurrency = 8

// payWebhookRecordTimeout bounds the detached write that records a delivery
// attempt's outcome after its request was sent.
const payWebhookRecordTimeout = 5 * time.Second

// PayWebhookRetention is how long a finished (delivered or given-up)
// delivery, with its full payload, is kept for the admin delivery log before
// PurgeOld may delete it.
const PayWebhookRetention = 90 * 24 * time.Hour

// PayWebhookPurgeBatch bounds how many rows one PurgeOld call deletes so a
// large backlog is worked off over several ticks instead of one long write.
const PayWebhookPurgeBatch = 500

// payWebhookBodyReadLimit bounds how much of a webhook endpoint's response
// body is read — enough to see an error message, not enough for a
// misbehaving endpoint to make delivery expensive.
const payWebhookBodyReadLimit = 64 * 1024

// ErrPayWebhookDeliveryNotFound is returned by Resend when no delivery
// matches the given id.
var ErrPayWebhookDeliveryNotFound = errors.New("webhook delivery not found")

// ErrPayDeliveryNotFound is the owner routes' name for the same refusal:
// ResendForMerchant answers it for another merchant's delivery, one in the
// other mode, and one that doesn't exist alike.
var ErrPayDeliveryNotFound = ErrPayWebhookDeliveryNotFound

// ErrPayWebhookNoURL: a delivered event can't be sent anew because the
// merchant has no webhook URL for its mode.
var ErrPayWebhookNoURL = errors.New("no webhook URL is set for this mode")

// PayWebhookSender delivers TSA Pay webhook events on a schedule, signing
// each payload with the receiving merchant's per-mode secret and never
// retrying a delivery the schedule has already given up on.
type PayWebhookSender struct {
	db        *gorm.DB
	merchants *PayMerchantService
	client    *http.Client
	now       func() time.Time
	payments  *PayPaymentService // nil until SetPayments: a delivered event can't be sent anew
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
//
// Delivery of the fetched rows is concurrent across merchants and sequential
// within one — see deliverRows.
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

	return s.deliverRows(ctx, rows)
}

// deliverRows sends rows through at most payWebhookConcurrency workers. Each
// merchant's rows go to a single worker in fetch order, so a merchant's own
// events keep their order and it is never hit by two requests at once, while
// different merchants proceed in parallel — a hanging endpoint costs one
// worker, not the whole tick. It returns the total delivered; a cancelled
// ctx stops new deliveries from starting (in-flight ones finish), and the
// first hard error (not an HTTP failure) is returned once in-flight work has
// finished.
func (s *PayWebhookSender) deliverRows(ctx context.Context, rows []models.PayWebhookDelivery) (int, error) {
	var order []uuid.UUID
	byMerchant := make(map[uuid.UUID][]*models.PayWebhookDelivery)
	for i := range rows {
		d := &rows[i]
		if _, seen := byMerchant[d.MerchantID]; !seen {
			order = append(order, d.MerchantID)
		}
		byMerchant[d.MerchantID] = append(byMerchant[d.MerchantID], d)
	}

	work := make(chan []*models.PayWebhookDelivery)
	var (
		delivered atomic.Int64
		stopped   atomic.Bool
		skipped   atomic.Bool
		errOnce   sync.Once
		firstErr  error
		wg        sync.WaitGroup
	)
	fail := func(err error) { errOnce.Do(func() { firstErr = err }) }

	// deliverGroup sends one merchant's rows in order. A panic inside a worker
	// goroutine would take the whole process down (the scheduler's recover
	// only covers its own goroutine), so it is contained here: it aborts this
	// merchant's group and is reported as the tick's error, while other
	// merchants carry on. The row that panicked is recorded as a failed
	// attempt, so it walks the retry schedule (and eventually fails) instead
	// of being fetched first and panicking every tick, and a panic after the
	// request went out cannot make the merchant receive it again every tick.
	deliverGroup := func(group []*models.PayWebhookDelivery) {
		var current *models.PayWebhookDelivery
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[tsa-pay] webhook delivery panicked: %v\n%s", r, debug.Stack())
				fail(fmt.Errorf("webhook delivery panic: %v", r))
				if current != nil {
					s.recordPanickedAttempt(ctx, current)
				}
			}
		}()
		for _, d := range group {
			if stopped.Load() {
				return
			}
			if ctx.Err() != nil {
				skipped.Store(true)
				return
			}
			current = d
			ok, err := s.deliverOne(ctx, d)
			current = nil
			if err != nil {
				fail(err)
				stopped.Store(true)
				return
			}
			if ok {
				delivered.Add(1)
			}
		}
	}

	workers := payWebhookConcurrency
	if len(order) < workers {
		workers = len(order)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A worker keeps draining work after a stop so the feeder below
			// never blocks on a channel nobody reads.
			for group := range work {
				deliverGroup(group)
			}
		}()
	}
	for _, id := range order {
		work <- byMerchant[id]
	}
	close(work)
	wg.Wait()

	switch {
	case firstErr != nil:
		return int(delivered.Load()), firstErr
	case skipped.Load():
		return int(delivered.Load()), ctx.Err()
	}
	return int(delivered.Load()), nil
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
		// The URL can never be requested, so retrying is pointless; the error
		// itself repeats the URL (query token included), so it is not kept.
		return s.finalizeNoCall(ctx, d, "webhook url is not a valid request url")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(PayWebhookSignatureHeader, sig)
	req.Header.Set("X-TSA-Event", d.Event)
	req.Header.Set("X-TSA-Delivery", d.ID.String())

	attempts := d.Attempts + 1
	resp, err := s.client.Do(req)
	if err != nil {
		// client.Do wraps the failure in a *url.Error that repeats the full
		// URL, query token included; keep only the underlying cause.
		if ctx.Err() != nil {
			// Cancelled mid-request: the outcome is unknown, so leave the
			// row as it was (no retry step consumed) for the next tick.
			return false, ctx.Err()
		}
		var urlErr *neturl.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
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

// recordPanickedAttempt books a delivery that panicked as a failed attempt.
// It must not panic itself: it runs inside the recover of deliverGroup.
func (s *PayWebhookSender) recordPanickedAttempt(ctx context.Context, d *models.PayWebhookDelivery) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[tsa-pay] recording a panicked webhook delivery failed: %v", r)
		}
	}()
	if _, err := s.recordAttempt(ctx, d, d.Attempts+1, 0, "internal error while delivering", false); err != nil {
		log.Printf("[tsa-pay] recording a panicked webhook delivery failed: %v", err)
	}
}

// recordAttempt writes the outcome of one delivery attempt, guarded on the
// row's attempts count not having changed since it was read (WHERE id = ?
// AND attempts = <old>) — a defence against two senders racing the same
// delivery.
func (s *PayWebhookSender) recordAttempt(ctx context.Context, d *models.PayWebhookDelivery, attempts, statusCode int, lastErr string, success bool) (bool, error) {
	// The request already reached the merchant, so its outcome is recorded
	// even if the tick's ctx was cancelled meanwhile — otherwise the row
	// would look undelivered and be sent again next tick.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), payWebhookRecordTimeout)
	defer cancel()
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

// PurgeOld deletes up to limit finished deliveries — delivered or given up
// more than retention ago, measured from that terminal timestamp rather than
// created_at so a row an admin resent and that only just succeeded is not
// purged for its age — and returns how many went. A delivery still owed to a
// merchant (neither timestamp set) is never touched, however old.
func (s *PayWebhookSender) PurgeOld(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	cutoff := s.now().Add(-retention)
	ids, err := s.purgeableIDs(ctx, cutoff, limit)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return s.deleteFinished(ctx, ids, cutoff)
}

const payWebhookPurgePredicate = "(delivered_at < ? OR failed_at < ?)"

// purgeableIDs selects up to limit ids of deliveries finished before cutoff.
// Ids are selected first and deleted by id so the same statements run on
// SQLite and Postgres.
func (s *PayWebhookSender) purgeableIDs(ctx context.Context, cutoff time.Time, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.db.WithContext(ctx).Model(&models.PayWebhookDelivery{}).
		Where(payWebhookPurgePredicate, cutoff, cutoff).
		Limit(limit).
		Pluck("id", &ids).Error
	return ids, err
}

// deleteFinished deletes ids, re-asserting the finished-before-cutoff
// predicate in the statement itself: an admin Resend between the select and
// this delete clears both timestamps, and that now-pending row must survive.
func (s *PayWebhookSender) deleteFinished(ctx context.Context, ids []uuid.UUID, cutoff time.Time) (int64, error) {
	res := s.db.WithContext(ctx).
		Where("id IN ? AND "+payWebhookPurgePredicate, ids, cutoff, cutoff).
		Delete(&models.PayWebhookDelivery{})
	return res.RowsAffected, res.Error
}

// Resend requeues a delivery for immediate redelivery — an admin's "try
// again" on a webhook delivery that gave up (or, defensively, any other
// one): attempts and the last outcome are cleared and next_attempt_at is
// set to now, exactly as a freshly-enqueued delivery would be. It never
// calls the merchant's endpoint itself; the next DeliverDue tick does.
func (s *PayWebhookSender) Resend(ctx context.Context, deliveryID uuid.UUID) error {
	n, err := s.requeue(s.db.WithContext(ctx).Where("id = ?", deliveryID))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPayWebhookDeliveryNotFound
	}
	return nil
}

// requeue clears the last outcome of the deliveries q matches and makes them
// due now, as freshly enqueued ones are. It returns how many it changed.
func (s *PayWebhookSender) requeue(q *gorm.DB) (int64, error) {
	now := s.now()
	res := q.Model(&models.PayWebhookDelivery{}).
		Updates(map[string]any{
			"attempts":         0,
			"last_status_code": 0,
			"last_error":       "",
			"delivered_at":     nil,
			"failed_at":        nil,
			"next_attempt_at":  &now,
		})
	return res.RowsAffected, res.Error
}

// SetPayments wires the payment service a delivered event is rebuilt from
// (the payment as it is now). Unset, ResendForMerchant can only retry an
// undelivered event.
func (s *PayWebhookSender) SetPayments(p *PayPaymentService) { s.payments = p }

// ResendForMerchant is an owner's "send it again" on one of their own
// webhook deliveries. ErrPayDeliveryNotFound unless the delivery belongs to
// merchantID in mode. One not yet delivered (pending a retry, or given up)
// is retried in place. One already delivered (even meanwhile) is never
// changed: its event is queued as a new delivery of the payment as it is now,
// under a new id. Either way, the delivery to be sent is returned.
func (s *PayWebhookSender) ResendForMerchant(ctx context.Context, merchantID uuid.UUID, mode string, deliveryID uuid.UUID) (models.PayWebhookDelivery, error) {
	var d models.PayWebhookDelivery
	err := s.db.WithContext(ctx).Where("id = ? AND merchant_id = ? AND mode = ?", deliveryID, merchantID, mode).First(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.PayWebhookDelivery{}, ErrPayDeliveryNotFound
	}
	if err != nil {
		return models.PayWebhookDelivery{}, err
	}
	if d.DeliveredAt == nil {
		// Only while it is still undelivered: the worker can deliver it between
		// the read above and this update, and a delivered record is never
		// changed. Delivered meanwhile, it is sent anew below.
		n, err := s.requeue(s.db.WithContext(ctx).Where("id = ? AND delivered_at IS NULL", d.ID))
		if err != nil {
			return models.PayWebhookDelivery{}, err
		}
		if n > 0 {
			var fresh models.PayWebhookDelivery
			err := s.db.WithContext(ctx).First(&fresh, "id = ?", d.ID).Error
			return fresh, err
		}
	}
	if s.payments == nil {
		return models.PayWebhookDelivery{}, errors.New("tsa-pay: webhook redelivery is not wired (SetPayments)")
	}
	return s.payments.redeliver(ctx, &d)
}
