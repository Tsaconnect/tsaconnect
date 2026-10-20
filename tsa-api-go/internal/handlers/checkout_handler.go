package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// CheckoutHandler handles checkout and order escrow endpoints.
type CheckoutHandler struct {
	Config            *config.Config
	BlockchainService *services.BlockchainService
	EscrowService     *services.EscrowService
	EventBus          *events.Bus
	// otcService prices MCGP; feeRates reads the escrow's fee and gas. Both are
	// interfaces so tests can stand in for the chain; nil means unavailable.
	otcService mcgpPricer
	feeRates   escrowRateReader
	// walletBalances reads the buyer's pay-token balance on Sonic mainnet.
	walletBalances walletBalanceReader
	// spendable pays MCGP orders from Spendable MCGP (checkout_spendable.go);
	// nil leaves MCGP checkout unavailable.
	spendable orderSpendablePayer
	// funding pays a checkout with any token the buyer holds, swapped into the
	// order token in her own wallet first (checkout_funding.go); nil leaves it off.
	funding marketplaceFunding
	// FeeService is the fee/TP engine (SP3). Optional/nil-safe: wired from
	// main.go. Used via settleFeeEarnings.
	FeeService *services.FeeDistributionService
}

// NewCheckoutHandler creates a new CheckoutHandler.
func NewCheckoutHandler(cfg *config.Config, bs *services.BlockchainService, es *services.EscrowService, bus *events.Bus, otc *services.OTCService) *CheckoutHandler {
	ch := &CheckoutHandler{
		Config:            cfg,
		BlockchainService: bs,
		EscrowService:     es,
		EventBus:          bus,
	}
	// Assign only non-nil pointers: a nil *T in an interface is not nil.
	if otc != nil {
		ch.otcService = otc
	}
	if es != nil {
		ch.feeRates = es
	}
	if bs != nil {
		ch.walletBalances = bs
	}
	return ch
}

// walletBalanceReader is the balance read a checkout needs. *BlockchainService
// fills it; its code check keeps a mis-set token address reading as unknown
// rather than as a zero balance that would refuse every checkout.
type walletBalanceReader interface {
	DepositBalance(chainKey, token, address string) (*big.Int, error)
}

// walletBalance reads the pay-token balance of the wallet the escrow will
// pull from, or nil when it cannot be read — an unknown balance never blocks a
// checkout, the chain still decides at escrow time. Callers read it before
// opening a DB transaction, so the RPC never holds a pooled connection.
func (ch *CheckoutHandler) walletBalance(wallet, token string) *big.Int {
	if wallet == "" {
		return nil
	}
	// MCGP is paid from Spendable MCGP, never from the wallet's ERC-20 MCGP,
	// so that is the balance a shortfall is measured against.
	if token == "MCGP" {
		if !ch.spendableEnabled() {
			return nil
		}
		// Warm the cached operator caps here, off the DB transaction, so the
		// per-order cap check made inside it (refuseSpendableLimit) never
		// waits on an RPC while holding a pooled connection.
		ch.spendable.WarmLimits(context.Background())
		balance, err := ch.spendable.Balance(wallet)
		if err != nil {
			log.Printf("[checkout] spendable MCGP balance read for %s failed (%v); not pre-checking", wallet, err)
			return nil
		}
		return balance
	}
	if ch.walletBalances == nil {
		return nil
	}
	balance, err := ch.walletBalances.DepositBalance("sonic", token, wallet)
	if err != nil {
		log.Printf("[checkout] balance read for %s %s failed (%v); not pre-checking", wallet, token, err)
		return nil
	}
	return balance
}

// balanceReadBudget bounds how long a checkout waits on the balance read; past
// it the balance is unknown, which never blocks.
const balanceReadBudget = 3 * time.Second

// ratesAndBalance resolves the pricing rates and, alongside them, the buyer's
// balance of token — both chain reads, neither needing the other, and both
// done before a DB transaction opens. A slow balance read is abandoned, never
// waited on past balanceReadBudget or a failed pricing read.
func (ch *CheckoutHandler) ratesAndBalance(ctx context.Context, token, wallet string) (pricingRates, *big.Int, *cartPricingError) {
	read := make(chan *big.Int, 1)
	go func() { read <- ch.walletBalance(wallet, strings.ToUpper(token)) }()
	rates, perr := ch.resolvePricingRates(ctx, token)
	if perr != nil {
		return rates, nil, perr
	}
	select {
	case balance := <-read:
		return rates, balance, nil
	case <-time.After(balanceReadBudget):
		log.Printf("[checkout] balance read for %s %s exceeded %s; not pre-checking", wallet, token, balanceReadBudget)
		return rates, nil, nil
	case <-ctx.Done():
		return rates, nil, nil
	}
}

// refuseShortfall answers 400 insufficient_balance, rolling tx back, when a
// known balance cannot cover total: the escrow pulls the whole total and
// reverts on a shortfall, which the buyer would otherwise meet only after
// signing, and paying gas for, an approve.
func refuseShortfall(c *gin.Context, tx *gorm.DB, token string, total, balance *big.Int) bool {
	short := shortfallMessage(token, total, balance)
	if short == "" {
		return false
	}
	tx.Rollback()
	utils.ErrorResponseWithCode(c, http.StatusBadRequest, short, "insufficient_balance")
	return true
}

// shortfallMessage is the buyer-facing refusal when a known balance is below
// total, and "" otherwise.
func shortfallMessage(token string, total, balance *big.Int) string {
	if balance == nil || balance.Cmp(total) >= 0 {
		return ""
	}
	decimals := tokenDecimals(token)
	if token == "MCGP" {
		return fmt.Sprintf("Not enough Spendable MCGP. This order needs %s MCGP and your Spendable MCGP balance is %s MCGP.",
			formatTokenBalance(total, decimals), formatTokenBalance(balance, decimals))
	}
	return fmt.Sprintf("Not enough %s in your wallet. This order needs %s %s and your wallet holds %s %s.",
		token, formatTokenBalance(total, decimals), token, formatTokenBalance(balance, decimals), token)
}

// mcgpPricer is the OTC read that prices MCGP in USD.
type mcgpPricer interface {
	GetBuyPricePerToken() (*big.Int, error)
}

// escrowRateReader is what pricing needs from the escrow: the fee rate and the
// flat gas fee createOrder will apply. *services.EscrowService implements it.
type escrowRateReader interface {
	PlatformFeeBPS(ctx context.Context, tokenAddr string) (int, error)
	GasFeeWei(ctx context.Context, tokenAddr string) (*big.Int, error)
}

// orderEscrowContract returns the contract address that the order's escrow lives on.
// Falls back to current config for orders predating the per-order column (backfill window).
func (ch *CheckoutHandler) orderEscrowContract(order *models.Order) string {
	if order.EscrowContractAddress != "" {
		return order.EscrowContractAddress
	}
	return ch.Config.ProductEscrowAddress
}

// --- Shipping zone detection ---

const (
	ShippingZoneSameCity      = "same_city"
	ShippingZoneSameState     = "same_state"
	ShippingZoneSameCountry   = "same_country"
	ShippingZoneInternational = "international"
)

type shippingOrigin struct {
	City    string
	State   string
	Country string
}

func normalizeLocationComponent(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func normalizeStateComponent(value string) string {
	normalized := normalizeLocationComponent(value)
	for _, suffix := range []string{" state", " province", " region"} {
		if strings.HasSuffix(normalized, suffix) {
			return strings.TrimSpace(strings.TrimSuffix(normalized, suffix))
		}
	}
	if normalized == "federal capital territory" {
		return "fct"
	}
	return normalized
}

func parseProductLocation(location string) shippingOrigin {
	parts := strings.Split(location, ",")
	trimmed := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			trimmed = append(trimmed, part)
		}
	}

	origin := shippingOrigin{}
	if len(trimmed) > 0 {
		origin.City = trimmed[0]
	}
	if len(trimmed) > 1 {
		origin.State = trimmed[1]
	}
	if len(trimmed) > 2 {
		origin.Country = trimmed[2]
	}

	return origin
}

func resolveShippingOrigin(product *models.Product, seller *models.User) shippingOrigin {
	origin := parseProductLocation(product.Location)
	if seller != nil {
		if origin.City == "" {
			origin.City = seller.City
		}
		if origin.State == "" {
			origin.State = seller.State
		}
		if origin.Country == "" {
			origin.Country = seller.Country
		}
	}
	return origin
}

// DetectShippingZone compares buyer and seller locations.
func DetectShippingZone(buyerCity, buyerState, buyerCountry, sellerCity, sellerState, sellerCountry string) string {
	bc := normalizeLocationComponent(buyerCountry)
	sc := normalizeLocationComponent(sellerCountry)
	bs := normalizeStateComponent(buyerState)
	ss := normalizeStateComponent(sellerState)
	bci := normalizeLocationComponent(buyerCity)
	sci := normalizeLocationComponent(sellerCity)

	if bc == "" || sc == "" {
		return ShippingZoneSameCountry
	}

	if bc != sc {
		return ShippingZoneInternational
	}
	if bs != ss {
		return ShippingZoneSameCountry
	}
	if bci != sci {
		return ShippingZoneSameState
	}
	return ShippingZoneSameCity
}

// GetShippingRate returns the shipping cost for a product in a given zone.
func GetShippingRate(product *models.Product, zone string) float64 {
	switch zone {
	case ShippingZoneSameCity:
		return product.ShippingSameCity
	case ShippingZoneSameState:
		return product.ShippingSameState
	case ShippingZoneSameCountry:
		return product.ShippingSameCountry
	case ShippingZoneInternational:
		return product.ShippingInternational
	default:
		return product.ShippingInternational
	}
}

// --- Service call-out / booking tier resolution ---

// GetCalloutRate returns the STANDARD-mode call-out fee for a service at a given
// area tier. "residential" is always free; the remaining tiers reuse the product
// shipping columns (relabelled "Call-out fee" in the service UI).
func GetCalloutRate(service *models.Product, tier string) float64 {
	if tier == models.BookingTierResidential {
		return 0
	}
	return GetShippingRate(service, tier)
}

// resolveServiceTier maps the auto-detected shipping zone (same_city/state/
// country/international) to a service area tier, honoring a buyer-DECLARED finer
// tier when it is geographically plausible. The buyer may only refine DOWN within
// proximity the server can sanity-check: "residential" only refines "same_city",
// and "same_lga" (booking only) refines same_city/same_state. Implausible
// declarations are ignored (charge the detected zone); the provider dispute path
// is the backstop for the residual cases the server can't verify (design §4.2).
func resolveServiceTier(detectedZone, declared string, booking bool) string {
	switch declared {
	case models.BookingTierResidential:
		if detectedZone == ShippingZoneSameCity {
			return models.BookingTierResidential
		}
	case models.BookingTierSameLGA:
		if booking && (detectedZone == ShippingZoneSameCity || detectedZone == ShippingZoneSameState) {
			return models.BookingTierSameLGA
		}
	}
	if booking && detectedZone == ShippingZoneInternational {
		return models.BookingTierOutsideCountry
	}
	return detectedZone
}

// GetBookingFee returns the admin-set merged call-out+booking fare (USD) for a
// booking-mode category at the resolved tier, or 0 if no row exists.
func GetBookingFee(db *gorm.DB, categoryID uuid.UUID, tier string) float64 {
	var bf models.CategoryBookingFee
	if err := db.Where("category_id = ? AND tier = ?", categoryID, tier).First(&bf).Error; err != nil {
		return 0
	}
	return bf.AmountUSD
}

// GetServiceTierFee returns the merchant-set distance fee (USD) for an
// order/booking service at the given tier. Unlike GetCalloutRate (contact /
// legacy standard), residential is a PRICEABLE tier here (reads
// ShippingResidential) — the PM's new booking/delivery fee schedule prices every
// tier including "same residential area" (design DV3/F6).
func GetServiceTierFee(service *models.Product, tier string) float64 {
	switch tier {
	case models.BookingTierResidential:
		return service.ShippingResidential
	case models.BookingTierSameCity, models.BookingTierSameLGA:
		// The merchant sets five columns (residential/city/state/country/intl);
		// "same_lga" is a refinement of the city tier (PM grouped "same city/LGA").
		return service.ShippingSameCity
	case models.BookingTierSameState:
		return service.ShippingSameState
	case models.BookingTierSameCountry:
		return service.ShippingSameCountry
	case models.BookingTierOutsideCountry:
		return service.ShippingInternational
	default:
		// Detected zones (same_state/same_country/international) also land here via
		// GetShippingRate, which maps them correctly. Guards against a tier without
		// a dedicated column silently falling through to the most expensive rate.
		return GetShippingRate(service, tier)
	}
}

// slotBookedCountTx returns the total party-size reserved for a (product, slot)
// within a transaction, counting only bookings whose order is not cancelled or
// refunded so a cancel/refund frees capacity automatically (design §4/F4).
func slotBookedCountTx(tx *gorm.DB, productID uuid.UUID, slotStart time.Time) int {
	var total *int
	tx.Model(&models.ServiceBooking{}).
		Joins("JOIN orders ON orders.id = service_bookings.order_id").
		Where("service_bookings.product_id = ? AND service_bookings.slot_start = ?", productID, slotStart).
		Where("orders.status NOT IN ?", []string{models.OrderStatusCancelled, models.OrderStatusRefunded}).
		Select("COALESCE(SUM(service_bookings.party_size),0)").
		Scan(&total)
	if total == nil {
		return 0
	}
	return *total
}

// slotWithinAvailability reports whether [slotStart, slotStart+duration] falls
// inside a configured weekly availability window, and returns the matched
// window's start-minute (for grid-alignment checks). windowStart is -1 when no
// window covers the slot.
//
// slotStart MUST already be expressed in the service's own timezone: the stored
// windows are wall-clock there, so both the weekday and the time-of-day read
// below are only meaningful in that zone.
func slotWithinAvailability(tx *gorm.DB, productID uuid.UUID, slotStart time.Time, durationMin int) (ok bool, windowStart int) {
	dow := int(slotStart.Weekday())
	slotStartMin := slotStart.Hour()*60 + slotStart.Minute()
	slotEndMin := slotStartMin + durationMin
	var windows []models.ServiceAvailability
	tx.Where("product_id = ? AND day_of_week = ?", productID, dow).Find(&windows)
	for _, w := range windows {
		ws, ok1 := parseHHMM(w.StartTime)
		we, ok2 := parseHHMM(w.EndTime)
		if ok1 && ok2 && slotStartMin >= ws && slotEndMin <= we {
			return true, ws
		}
	}
	return false, -1
}

// parseHHMM parses "HH:MM" into minutes-since-midnight.
func parseHHMM(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// priceMenuSelection recomputes the total (USD) for an Order-type service from
// the ServiceMenuItem rows in the DB — never trusting client-sent prices — and
// returns the captured line items. Every requested item must belong to the
// product and be available (F3). Returns a non-empty errMsg on any problem.
func (ch *CheckoutHandler) priceMenuSelection(tx *gorm.DB, productID uuid.UUID, lines []menuLineRequest) (float64, []models.OrderMenuSelection, string) {
	if len(lines) == 0 {
		return 0, nil, "Select at least one item to order"
	}
	qtyByID := map[uuid.UUID]int{}
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		id, err := uuid.Parse(l.ItemID)
		if err != nil {
			return 0, nil, "Invalid menu item selected"
		}
		if l.Quantity <= 0 {
			return 0, nil, "Item quantity must be at least 1"
		}
		if _, dup := qtyByID[id]; !dup {
			ids = append(ids, id)
		}
		qtyByID[id] += l.Quantity
	}

	var items []models.ServiceMenuItem
	if err := tx.Where("product_id = ? AND id IN ? AND is_available = ?", productID, ids, true).
		Find(&items).Error; err != nil {
		return 0, nil, "Failed to price the order"
	}
	if len(items) != len(ids) {
		return 0, nil, "One or more selected items are unavailable"
	}

	var total float64
	sel := make([]models.OrderMenuSelection, 0, len(items))
	for _, it := range items {
		qty := qtyByID[it.ID]
		total += it.PriceUSD * float64(qty)
		sel = append(sel, models.OrderMenuSelection{
			ItemID: it.ID, Name: it.Name, Quantity: qty, UnitPriceUSD: it.PriceUSD,
		})
	}
	return total, sel, ""
}

// reserveBookingSlot validates a Booking-type service's requested slot (within an
// availability window, capacity not exceeded) and returns an unsaved
// ServiceBooking (OrderID is set by the caller after the order is created).
// Returns a non-empty errMsg on any problem.
func (ch *CheckoutHandler) reserveBookingSlot(tx *gorm.DB, buyerID uuid.UUID, product *models.Product, profile *models.ServiceProfile, req bookingSlotRequest) (*models.ServiceBooking, string) {
	partySize := req.PartySize
	if partySize <= 0 {
		partySize = 1
	}
	duration := profile.SlotDurationMinutes
	if duration <= 0 {
		return nil, fmt.Sprintf("%s is not configured for booking yet", product.Name)
	}
	slotStart := req.SlotStart.UTC()
	slotEnd := slotStart.Add(time.Duration(duration) * time.Minute)

	// The instant is what gets stored and compared for capacity, but the
	// availability windows are wall-clock in the service's own zone — so the
	// weekday and time-of-day checks below have to read the slot there.
	local := slotStart.In(profile.Location())

	// Reject slots in the past (a booking must be for a future time).
	if !slotStart.After(time.Now().UTC()) {
		return nil, fmt.Sprintf("%s is in the past", formatSlot(local))
	}

	within, windowStart := slotWithinAvailability(tx, product.ID, local, duration)
	if !within {
		return nil, fmt.Sprintf("%s is not available at %s", product.Name, formatSlot(local))
	}
	// Grid alignment: the slot must sit on the duration grid measured from the
	// window start, otherwise a buyer could send offset starts (10:00, 10:01, …)
	// that each count as a distinct slot and bypass per-slot capacity entirely.
	slotStartMin := local.Hour()*60 + local.Minute()
	if (slotStartMin-windowStart)%duration != 0 {
		return nil, fmt.Sprintf("%s is not a valid slot time", formatSlot(local))
	}

	if profile.MaxCapacityPerSlot > 0 {
		// Serialize concurrent bookings for the SAME slot so the read-then-write
		// capacity check can't be raced into overbooking. Postgres transaction-
		// scoped advisory lock keyed on (product, slot); auto-released at commit/
		// rollback. No-op on SQLite (tests) — single-writer there anyway.
		if tx.Dialector.Name() == "postgres" {
			lockKey := fmt.Sprintf("svcbooking:%s:%d", product.ID.String(), slotStart.Unix())
			tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", lockKey)
		}
		booked := slotBookedCountTx(tx, product.ID, slotStart)
		if booked+partySize > profile.MaxCapacityPerSlot {
			return nil, fmt.Sprintf("The %s slot is full", formatSlot(local))
		}
	}
	return &models.ServiceBooking{
		ID:        uuid.New(),
		ProductID: product.ID,
		BuyerID:   buyerID,
		SlotStart: slotStart,
		SlotEnd:   slotEnd,
		PartySize: partySize,
		Status:    models.ServiceBookingStatusReserved,
	}, ""
}

// resolveServiceTypeAndProfile loads a service product's category-derived type
// (falling back to the legacy ServiceFeeMode) and its ServiceProfile.
func resolveServiceTypeAndProfile(tx *gorm.DB, product *models.Product) (string, models.ServiceProfile) {
	var cat models.Category
	if product.CategoryID != nil {
		if err := tx.First(&cat, "id = ?", *product.CategoryID).Error; err != nil {
			log.Printf("service pricing: category %s load failed for service %s: %v", *product.CategoryID, product.ID, err)
		}
	}
	serviceType := models.EffectiveServiceType(cat)
	var profile models.ServiceProfile
	tx.Where("product_id = ?", product.ID).First(&profile)
	return serviceType, profile
}

// rejectsInstantBuy reports whether a listing must NOT be bought directly on the
// instant-checkout paths: a request-type booking requires a booking request the
// provider approves (and, for on_request, prices) before payment. Without this,
// a buyer could escrow the base fare and skip approval — and, for on_request,
// skip the provider naming the price. The approval→pay path (PayBookingRequest)
// does not call this, so it can still complete a request booking.
func rejectsInstantBuy(serviceType string, profile *models.ServiceProfile) bool {
	return serviceType == models.ServiceTypeBooking && profile.BookingType == models.BookingTypeRequest
}

// serviceContactGatePaid reports whether the buyer has paid the one-time
// contact-reveal fee for a contact-type service (the checkout gate).
func serviceContactGatePaid(tx *gorm.DB, buyerID, productID uuid.UUID) bool {
	var n int64
	tx.Model(&models.ServiceContactPayment{}).
		Where("caller_id = ? AND service_id = ?", buyerID, productID).Count(&n)
	return n > 0
}

// resolveServicePricing computes the escrow charge (ProductAmount=priceUSD,
// ShippingAmount=calloutUSD) for a service line item by its type, plus the
// resolved area zone, any recorded menu selections, and an unsaved booking to
// attach after the order row is created. Shared by the cart checkout and the
// single-service checkout so the pricing/capacity rules live in one place.
// A non-empty errMsg means reject the order with 400.
func (ch *CheckoutHandler) resolveServicePricing(
	tx *gorm.DB, buyerID uuid.UUID, product *models.Product, serviceType string,
	profile *models.ServiceProfile, detectedZone, declared string,
	menuLines []menuLineRequest, slotReq *bookingSlotRequest,
) (priceUSD, calloutUSD float64, zone string, menuSel []models.OrderMenuSelection, booking *models.ServiceBooking, errMsg string) {
	zone = resolveServiceTier(detectedZone, declared, serviceType == models.ServiceTypeBooking)

	switch serviceType {
	case models.ServiceTypeOrder:
		sum, sel, perr := ch.priceMenuSelection(tx, product.ID, menuLines)
		if perr != "" {
			return 0, 0, zone, nil, nil, perr
		}
		priceUSD = sum
		menuSel = sel
		calloutUSD = GetServiceTierFee(product, zone) // residential priceable (F6)

	case models.ServiceTypeBooking:
		fee := GetServiceTierFee(product, zone)
		if fee <= 0 && product.CategoryID != nil {
			fee = GetBookingFee(tx, *product.CategoryID, zone) // legacy admin fare
		}
		priceUSD = fee + profile.AccessFeeUSD
		calloutUSD = profile.CalloutFeeUSD
		if profile.SlotDurationMinutes > 0 {
			if slotReq == nil || slotReq.SlotStart.IsZero() {
				return 0, 0, zone, nil, nil, fmt.Sprintf("Pick a time slot for %s", product.Name)
			}
			b, berr := ch.reserveBookingSlot(tx, buyerID, product, profile, *slotReq)
			if berr != "" {
				return 0, 0, zone, nil, nil, berr
			}
			booking = b
		}

	default: // contact
		if profile.AccessFeeUSD > 0 {
			priceUSD = profile.AccessFeeUSD
			calloutUSD = profile.CalloutFeeUSD
		} else if product.Price > 0 {
			priceUSD = product.Price
			calloutUSD = GetCalloutRate(product, zone)
		} else {
			priceUSD = GetCalloutRate(product, zone)
		}
	}

	// A buyer who unlocked the listing's promotion pays the promo price on the
	// item price; the call-out/shipping part stays as quoted.
	factor, ferr := services.NewPromoService(tx).PriceFactor(context.Background(), product, buyerID)
	if ferr != nil {
		return 0, 0, zone, nil, nil, "Failed to resolve promotion"
	}
	priceUSD *= factor

	if priceUSD+calloutUSD <= 0 {
		return 0, 0, zone, nil, nil, fmt.Sprintf("%s has no payable amount for your selection", product.Name)
	}
	return priceUSD, calloutUSD, zone, menuSel, booking, ""
}

// --- Fee calculation (big.Int) ---

// splitSellerFee turns a listing price into the escrowed productAmount and the
// platform fee, sized so the two add up to the price. ProductEscrow pulls
// productAmount + productAmount*bps/10000 from the buyer and releases only
// productAmount to the seller, so this is how the seller — not the buyer —
// pays the fee: the buyer pays the listing price, the seller nets price - fee.
// When bps does not divide evenly the buyer pays up to one base unit less than
// the price, never more.
func splitSellerFee(priceWei *big.Int, bps int) (productAmount, fee *big.Int) {
	feeOn := func(amount *big.Int) *big.Int { return escrowFeeOn(amount, bps) }
	if bps <= 0 {
		return new(big.Int).Set(priceWei), big.NewInt(0)
	}
	// Largest P with P + fee(P) <= price. The division gives P or P-1.
	p := new(big.Int).Mul(priceWei, big.NewInt(10000))
	p.Div(p, big.NewInt(int64(10000+bps)))
	for {
		next := new(big.Int).Add(p, big.NewInt(1))
		if new(big.Int).Add(next, feeOn(next)).Cmp(priceWei) > 0 {
			break
		}
		p = next
	}
	return p, feeOn(p)
}

// escrowFeeOn is the platform fee ProductEscrow computes on an escrowed
// productAmount: amount * bps / 10000, rounded down.
func escrowFeeOn(amount *big.Int, bps int) *big.Int {
	f := new(big.Int).Mul(amount, big.NewInt(int64(bps)))
	return f.Div(f, big.NewInt(10000))
}

// pricingRates are the chain-side rates one checkout prices with. They are
// read once, before any DB transaction opens, so a slow RPC never holds a
// pooled connection and every line in a cart prices at the same rates.
type pricingRates struct {
	token    string
	decimals int
	// feeBPS is the platform fee rate the escrow applies (0 for MCGP).
	feeBPS int
	// gasFee is the flat per-order fee the escrow adds, in token base units.
	gasFee *big.Int
	// pricePerMCGP is the OTC buy price of 1 MCGP in USD (6dp); nil for stables.
	pricePerMCGP *big.Int
}

// usdToWei converts a USD amount to the token's base units: 1:1 for the
// stables, through the OTC buy price for MCGP.
func (r pricingRates) usdToWei(usd float64) (*big.Int, error) {
	if r.pricePerMCGP != nil {
		// (usd * 1e6 * 1e18) / pricePerMCGP
		usd6, err := toWei(floatToDecimalStr(usd, 6), 6)
		if err != nil {
			return nil, err
		}
		w := new(big.Int).Mul(usd6, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
		return w.Div(w, r.pricePerMCGP), nil
	}
	return toWei(floatToDecimalStr(usd, r.decimals), r.decimals)
}

// usdPerToken is the USD value of one token, for display only.
func (r pricingRates) usdPerToken() float64 {
	if r.pricePerMCGP == nil {
		return 1
	}
	v, _ := new(big.Float).Quo(new(big.Float).SetInt(r.pricePerMCGP), big.NewFloat(1e6)).Float64()
	return v
}

// resolvePricingRates reads the rates an order in token is priced and charged
// with. The fee and gas come from the escrow itself — one rate for every order
// it holds, products and services alike, MCGP zeroed — so the recorded fee and
// the approve amount always equal what createOrder pulls. If the chain cannot
// be read they fall back to the fee_config replica, which the confirmed
// on-chain flow keeps in step. A rate the escrow would refuse stops pricing.
func (ch *CheckoutHandler) resolvePricingRates(ctx context.Context, token string) (pricingRates, *cartPricingError) {
	token = strings.ToUpper(token)
	r := pricingRates{token: token, decimals: tokenDecimals(token), gasFee: big.NewInt(0)}
	if token == "MCGP" {
		if ch.otcService == nil {
			return r, &cartPricingError{http.StatusInternalServerError, "Failed to get MCGP exchange rate"}
		}
		p, err := ch.otcService.GetBuyPricePerToken()
		if err != nil || p == nil || p.Sign() == 0 {
			return r, &cartPricingError{http.StatusInternalServerError, "Failed to get MCGP exchange rate"}
		}
		r.pricePerMCGP = p
		return r, nil
	}

	addr := ""
	if ch.BlockchainService != nil {
		addr = ch.BlockchainService.TokenAddress("sonic", token)
	}
	feeRead, gasRead := false, false
	if ch.feeRates != nil {
		bps, err := ch.feeRates.PlatformFeeBPS(ctx, addr)
		switch {
		case errors.Is(err, services.ErrEscrowFeeAboveMax):
			log.Printf("[checkout] ALERT escrow_fee_above_max=1 token=%s", token)
			return r, &cartPricingError{http.StatusServiceUnavailable, "Checkout is temporarily unavailable: the platform fee is misconfigured."}
		case err != nil:
			log.Printf("[checkout] escrow fee read for %s failed (%v); using fee_config", token, err)
		default:
			r.feeBPS, feeRead = bps, true
		}
		gas, err := ch.feeRates.GasFeeWei(ctx, addr)
		if err != nil {
			log.Printf("[checkout] escrow gas read for %s failed (%v); using fee_config", token, err)
		} else {
			r.gasFee, gasRead = gas, true
		}
	}
	if !feeRead {
		if bps, exempt := services.FeeRate(models.FeatureProductMerchant, token, nil); !exempt {
			r.feeBPS = bps
		}
	}
	if !gasRead {
		r.gasFee = configGasFeeWei(token)
	}
	return r, nil
}

// configGasFeeWei is the fee_config replica's flat product gas fee in token
// base units — the fallback when the escrow cannot be read.
func configGasFeeWei(token string) *big.Int {
	gasFeeUSD, exempt := services.FeeFlatUSD(models.FeatureProduct, token, nil)
	if exempt {
		return big.NewInt(0)
	}
	gasFeeWei, ok := new(big.Int).SetString(fmt.Sprintf("%.0f", gasFeeUSD*math.Pow10(tokenDecimals(token))), 10)
	if !ok {
		return big.NewInt(0)
	}
	return gasFeeWei
}

// tokenDecimals returns the number of decimals for a given token symbol.
func tokenDecimals(token string) int {
	switch strings.ToUpper(token) {
	case "USDC", "USDT":
		return 6
	default:
		return 18
	}
}

// weiToUSDFloat converts a wei string amount back to a USD float value.
func weiToUSDFloat(weiStr string, token string) float64 {
	wei, ok := new(big.Int).SetString(weiStr, 10)
	if !ok || wei.Sign() == 0 {
		return 0
	}
	decimals := tokenDecimals(token)
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	fWei := new(big.Float).SetInt(wei)
	fDiv := new(big.Float).SetInt(divisor)
	result, _ := new(big.Float).Quo(fWei, fDiv).Float64()
	return result
}

// toWei converts a decimal string amount (e.g. "10.5") to a big.Int in the smallest unit.
// Uses string-based parsing to avoid float64 precision loss.
func toWei(amountStr string, decimals int) (*big.Int, error) {
	// Split on decimal point
	parts := strings.Split(amountStr, ".")
	intPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	} else if len(parts) > 2 {
		return nil, fmt.Errorf("invalid decimal amount: %s", amountStr)
	}

	// Pad or truncate fractional part to match decimals
	if len(fracPart) > decimals {
		fracPart = fracPart[:decimals]
	} else {
		for len(fracPart) < decimals {
			fracPart += "0"
		}
	}

	// Combine into a single integer string
	combined := intPart + fracPart

	result, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %s", amountStr)
	}
	return result, nil
}

// floatToDecimalStr converts a float64 to a string with enough precision.
func floatToDecimalStr(f float64, decimals int) string {
	return strconv.FormatFloat(f, 'f', decimals, 64)
}

// isValidTransition checks whether a status transition is allowed.
func isValidTransition(currentStatus, newStatus string) bool {
	return isValidTransitionForKind(currentStatus, newStatus, models.OrderKindProduct)
}

func isValidTransitionForKind(currentStatus, newStatus, kind string) bool {
	for _, s := range models.ValidNextStatusesForKind(currentStatus, kind) {
		if s == newStatus {
			return true
		}
	}
	return false
}

// --- Request/Response types ---

type createOrderRequest struct {
	Token        string `json:"token"`
	BuyerCity    string `json:"buyerCity"`
	BuyerState   string `json:"buyerState"`
	BuyerCountry string `json:"buyerCountry"`
	// ServiceTiers optionally maps a service product ID → the buyer-declared area
	// tier ("residential", "same_lga", …) for that line item, letting the buyer's
	// "provider is in my area" / LGA declaration reach the server. Honored only
	// when geographically plausible (resolveServiceTier); absent → detected zone.
	ServiceTiers map[string]string `json:"serviceTiers"`
	// MenuSelections maps an Order-type service product ID → the picked menu items.
	// The server recomputes the price from ServiceMenuItem rows in the DB and never
	// trusts a client-sent price (design §5/F2/F3).
	MenuSelections map[string][]menuLineRequest `json:"menuSelections"`
	// BookingSlots maps a Booking-type service product ID → the reserved slot.
	BookingSlots map[string]bookingSlotRequest `json:"bookingSlots"`
	// ExpectedTotal is the quote total (token base units, gas included) the
	// buyer confirmed. When set, order creation refuses a different total with
	// 409 code "price_changed". Optional for older clients.
	ExpectedTotal string `json:"expectedTotal"`
	// DeliveryAddressID picks one of the buyer's saved addresses. When set, its
	// location replaces BuyerCity/State/Country for pricing, and its recipient,
	// phone and street are snapshotted onto each order for the seller.
	DeliveryAddressID *uuid.UUID `json:"deliveryAddressId"`
	// FundWithSwap: the buyer will pay this checkout with another token,
	// swapped into this one in her own wallet first (checkout_funding.go), so
	// her balance of it is expected to be short now. The shortfall refusal is
	// skipped — the funding is then sized from these very orders — and nothing
	// else changes: the escrow still pulls from her wallet and still reverts
	// on a short balance.
	FundWithSwap bool `json:"fundWithSwap"`
}

type menuLineRequest struct {
	ItemID   string `json:"itemId"`
	Quantity int    `json:"quantity"`
}

type bookingSlotRequest struct {
	SlotStart time.Time `json:"slotStart"` // RFC3339
	PartySize int       `json:"partySize"`
}

type submitEscrowRequest struct {
	ApproveTxHash string `json:"approveTxHash" binding:"required"`
	EscrowTxHash  string `json:"escrowTxHash" binding:"required"`
}

type deliverRequest struct {
	// DeliveryProofURL is required for PRODUCT orders (proof of shipment/delivery)
	// and optional for SERVICE orders ("mark service rendered" — there is nothing
	// to ship). Enforced per-kind in MarkDelivered rather than via binding.
	DeliveryProofURL string `json:"deliveryProofUrl"`
}

type submitConfirmRequest struct {
	TxHash string `json:"txHash" binding:"required"`
}

type adminResolveRequest struct {
	RefundBuyer bool `json:"refundBuyer"`
}

// --- Endpoints ---

// CreateOrderFromCart handles POST /api/orders — creates orders from cart grouped by seller.
// pricedCartLine is one cart item priced for a pay token: exactly the amounts
// an order for it carries. CreateOrderFromCart and QuoteCart both price through
// priceCartItems, so the total a buyer confirms is the total the escrow pulls.
type pricedCartLine struct {
	item           models.CartItem
	product        models.Product
	seller         models.User
	isService      bool
	kind           string
	quantity       int
	zone           string
	menuSelections []models.OrderMenuSelection
	booking        *models.ServiceBooking
	productAmount  *big.Int
	shippingAmount *big.Int
	platformFee    *big.Int
}

// orderTotal is what the order records as TotalAmount (escrowed on-chain).
func (l pricedCartLine) orderTotal() *big.Int {
	t := new(big.Int).Add(l.productAmount, l.shippingAmount)
	return t.Add(t, l.platformFee)
}

type cartPricingError struct {
	status int
	msg    string
}

// buyerLocation is the request's delivery location, falling back per field to
// the buyer's profile.
func buyerLocation(req createOrderRequest, user *models.User) (city, state, country string) {
	city, state, country = req.BuyerCity, req.BuyerState, req.BuyerCountry
	if city == "" {
		city = user.City
	}
	if state == "" {
		state = user.State
	}
	if country == "" {
		country = user.Country
	}
	return city, state, country
}

// priceCartItems validates and prices every cart item for token inside tx. It
// writes nothing: stock is checked here and decremented by the caller.
func (ch *CheckoutHandler) priceCartItems(tx *gorm.DB, user *models.User, items []models.CartItem, req createOrderRequest, rates pricingRates) ([]pricedCartLine, *cartPricingError) {
	buyerCity, buyerState, buyerCountry := buyerLocation(req, user)
	token := rates.token

	// Group items by seller
	type sellerGroup struct {
		sellerID uuid.UUID
		items    []models.CartItem
	}
	groups := make(map[string]*sellerGroup)
	for _, item := range items {
		key := item.Seller.String()
		if groups[key] == nil {
			groups[key] = &sellerGroup{sellerID: item.Seller}
		}
		groups[key].items = append(groups[key].items, item)
	}

	var lines []pricedCartLine
	for _, group := range groups {
		for _, item := range group.items {
			var product models.Product
			if err := tx.First(&product, "id = ?", item.Product).Error; err != nil {
				return nil, &cartPricingError{http.StatusBadRequest, fmt.Sprintf("Product %s not found", item.Product)}
			}
			// Every pay token: an order with yourself moves no value but still
			// costs escrow/operator transactions and can farm fee rewards.
			if product.UserID == user.ID {
				return nil, &cartPricingError{http.StatusBadRequest, fmt.Sprintf("You can't buy your own listing (%s). Remove it from your cart.", product.Name)}
			}

			// Re-checked at order time, not just at add-to-cart: the listing or its
			// seller can be suspended while it sits in someone's cart, and that
			// must stop the purchase rather than escrow funds against a listing
			// nobody can fulfil.
			var listingSeller models.User
			if err := tx.First(&listingSeller, "id = ?", product.UserID).Error; err != nil || !product.IsVisibleToPublic(&listingSeller) {
				return nil, &cartPricingError{http.StatusBadRequest, fmt.Sprintf("%s is no longer available for purchase", product.Name)}
			}

			// Listing kind: a service skips stock + shipping. The three service
			// types (contact/order/booking) branch on the category's ServiceType.
			line := pricedCartLine{item: item, product: product, kind: models.OrderKindProduct, quantity: item.Quantity}
			line.isService = product.Type == models.ProductTypeService
			serviceType := ""
			var serviceProfile models.ServiceProfile

			if line.isService {
				line.kind = models.OrderKindService
				line.quantity = 1 // services have no inventory/quantity

				serviceType, serviceProfile = resolveServiceTypeAndProfile(tx, &product)

				// Contact-fee gate applies ONLY to contact-type services: the buyer
				// must have paid the one-time contact-reveal fee before engaging.
				// Order/booking services are NOT contact-gated (F1).
				if serviceType == models.ServiceTypeContact && !serviceContactGatePaid(tx, user.ID, product.ID) {
					return nil, &cartPricingError{http.StatusForbidden, fmt.Sprintf("Pay the contact fee for %s before engaging it", product.Name)}
				}
				if rejectsInstantBuy(serviceType, &serviceProfile) {
					return nil, &cartPricingError{http.StatusBadRequest, fmt.Sprintf("%s requires a booking request — submit a request and wait for the provider to approve before paying.", product.Name)}
				}
			} else if product.Stock < item.Quantity {
				return nil, &cartPricingError{http.StatusBadRequest, fmt.Sprintf("Insufficient stock for %s (available: %d, requested: %d)", product.Name, product.Stock, item.Quantity)}
			}

			// Get seller for zone detection
			if err := tx.First(&line.seller, "id = ?", group.sellerID).Error; err != nil {
				return nil, &cartPricingError{http.StatusBadRequest, "Seller not found"}
			}

			origin := resolveShippingOrigin(&product, &line.seller)
			detectedZone := DetectShippingZone(buyerCity, buyerState, buyerCountry, origin.City, origin.State, origin.Country)

			// Resolve the two USD charge components: priceUSD lands in
			// ProductAmount (platform fee applies, must be > 0 on-chain);
			// calloutUSD lands in ShippingAmount (pass-through to the merchant).
			var priceUSD, calloutUSD float64
			line.zone = detectedZone

			if line.isService {
				declared := ""
				if req.ServiceTiers != nil {
					declared = req.ServiceTiers[product.ID.String()]
				}
				var slotReq *bookingSlotRequest
				if req.BookingSlots != nil {
					if s, ok := req.BookingSlots[product.ID.String()]; ok {
						slotReq = &s
					}
				}
				p, co, z, sel, booking, perr := ch.resolveServicePricing(
					tx, user.ID, &product, serviceType, &serviceProfile,
					detectedZone, declared, req.MenuSelections[product.ID.String()], slotReq,
				)
				if perr != "" {
					return nil, &cartPricingError{http.StatusBadRequest, perr}
				}
				priceUSD, calloutUSD, line.zone, line.menuSelections, line.booking = p, co, z, sel, booking
			} else {
				factor, ferr := services.NewPromoService(tx).PriceFactor(context.Background(), &product, user.ID)
				if ferr != nil {
					return nil, &cartPricingError{http.StatusInternalServerError, "Failed to resolve promotion"}
				}
				priceUSD = product.Price * factor * float64(item.Quantity)
				calloutUSD = GetShippingRate(&product, line.zone) * float64(item.Quantity)
			}

			priceWei, err := rates.usdToWei(priceUSD)
			if err != nil {
				return nil, &cartPricingError{http.StatusInternalServerError, "Failed to convert product amount"}
			}
			line.productAmount, line.platformFee = splitSellerFee(priceWei, rates.feeBPS)
			if line.shippingAmount, err = rates.usdToWei(calloutUSD); err != nil {
				return nil, &cartPricingError{http.StatusInternalServerError, "Failed to convert shipping amount"}
			}

			// The on-chain createOrder requires productAmount > 0. A sub-unit
			// amount (e.g. a tiny fare divided by a high MCGP price) can round to
			// zero wei here and would revert at escrow time — reject up front with
			// a clear error. Guards products and services alike.
			if line.productAmount.Sign() <= 0 {
				return nil, &cartPricingError{http.StatusBadRequest, fmt.Sprintf("%s resolves to a zero on-chain amount for %s", product.Name, token)}
			}

			lines = append(lines, line)
		}
	}
	return lines, nil
}

// cartCheckout is the validated input shared by QuoteCart and CreateOrderFromCart.
type cartCheckout struct {
	user  *models.User
	req   createOrderRequest
	token string
	cart  models.Cart
	items []models.CartItem
	// delivery is the saved address the buyer picked; nil when none was sent.
	delivery *models.DeliveryAddress
}

// loadCartCheckout authenticates, binds and validates a cart checkout request
// and loads the caller's non-empty active cart, writing the error response and
// returning false when any step fails.
func loadCartCheckout(c *gin.Context) (*cartCheckout, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, false
	}
	var req createOrderRequest
	// Bound with the body kept, so a route that also reads its own fields from
	// the same body (the funding cart quote) can bind it again.
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return nil, false
	}
	if strings.TrimSpace(req.Token) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "token is required")
		return nil, false
	}
	token := strings.ToUpper(req.Token)
	if token != "USDC" && token != "USDT" && token != "MCGP" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported token. Use USDC, USDT, or MCGP")
		return nil, false
	}
	var cart models.Cart
	if err := config.DB.Where("user_id = ? AND status = ?", user.ID, "active").First(&cart).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "No active cart found")
		return nil, false
	}
	items := cart.GetItems()
	if len(items) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cart is empty")
		return nil, false
	}
	var delivery *models.DeliveryAddress
	if req.DeliveryAddressID != nil {
		addr, err := findUserDeliveryAddress(config.DB, user.ID, *req.DeliveryAddressID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponse(c, http.StatusNotFound, "Delivery address not found")
			return nil, false
		}
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load delivery address")
			return nil, false
		}
		delivery = addr
		req.BuyerCity, req.BuyerState, req.BuyerCountry = addr.City, addr.State, addr.Country
	}
	return &cartCheckout{user: user, req: req, token: token, cart: cart, items: items, delivery: delivery}, true
}

// orderPull is what the escrow pulls from the buyer's wallet for one order:
// its TotalAmount plus the escrow's gas fee.
func orderPull(order *models.Order, gasFee *big.Int) *big.Int {
	total, _ := new(big.Int).SetString(order.TotalAmount, 10)
	if total == nil {
		total = new(big.Int)
	}
	return total.Add(total, gasFee)
}

// cartTotal is everything the buyer's wallet pays for lines: each order's
// TotalAmount plus the escrow's gas fee per order.
func cartTotal(lines []pricedCartLine, gasFee *big.Int) *big.Int {
	total := new(big.Int)
	for _, l := range lines {
		total.Add(total, l.orderTotal())
		total.Add(total, gasFee)
	}
	return total
}

func (ch *CheckoutHandler) CreateOrderFromCart(c *gin.Context) {
	in, ok := loadCartCheckout(c)
	if !ok {
		return
	}
	user, req, token, cart, items := in.user, in.req, in.token, in.cart, in.items
	buyerCity, buyerState, buyerCountry := buyerLocation(req, user)
	if ch.refuseMcgpUnavailable(c, token) {
		return
	}

	rates, balance, perr := ch.ratesAndBalance(c.Request.Context(), token, user.WalletAddress)
	if perr != nil {
		utils.ErrorResponse(c, perr.status, perr.msg)
		return
	}

	// A checkout funded by swap takes stock with no funds behind it yet, so it
	// is earned: her proven wallet (the one the swap is paid from and into),
	// and no other funded checkout of hers in progress (re-checked under a lock
	// inside the transaction below).
	fundBySwap := req.FundWithSwap && token != "MCGP" && ch.FundingEnabled()
	if fundBySwap {
		if _, ok := requireProvenWallet(c, user); !ok {
			return
		}
		if err := ch.funding.CanStartCheckout(c.Request.Context(), user.ID); err != nil {
			fundingError(c, err)
			return
		}
	}

	// Wrap everything in a DB transaction
	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	lines, perr := ch.priceCartItems(tx, user, items, req, rates)
	if perr != nil {
		tx.Rollback()
		utils.ErrorResponse(c, perr.status, perr.msg)
		return
	}

	// The buyer confirmed a quoted total; if anything re-priced since (MCGP
	// rate, fee rate, stock, shipping), stop before any order exists rather
	// than have the wallet sign an amount they never saw.
	total := cartTotal(lines, rates.gasFee)
	if req.ExpectedTotal != "" && total.String() != req.ExpectedTotal {
		tx.Rollback()
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"The total changed since you confirmed it. Review the new amount before paying.", "price_changed")
		return
	}

	// Refused before any order takes stock — unless the buyer is funding the
	// checkout by swap, which sizes itself from these orders once they exist.
	if !fundBySwap && refuseShortfall(c, tx, token, total, balance) {
		return
	}
	if fundBySwap {
		// At most one funded checkout awaiting payment per buyer: the check
		// above was read-then-act, so it is repeated under her user row's lock.
		if err := lockUserRow(tx, user.ID); err != nil {
			tx.Rollback()
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create order")
			return
		}
		var pending int64
		if err := tx.Model(&models.Order{}).
			Where("buyer_id = ? AND fund_with_swap = ? AND status = ?", user.ID, true, models.OrderStatusPendingPayment).
			Count(&pending).Error; err != nil {
			tx.Rollback()
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create order")
			return
		}
		if pending > 0 {
			tx.Rollback()
			fundingError(c, services.ErrFundingInProgress)
			return
		}
	}
	// Each order is one Spendable MCGP debit, so each must fit the caps.
	for _, line := range lines {
		if ch.refuseSpendableLimit(c, tx, token, line.orderTotal()) {
			return
		}
	}

	// Determine buyer upline
	buyerUpline := ""
	if user.ReferredBy != nil {
		var referrer models.User
		if err := tx.First(&referrer, "id = ?", *user.ReferredBy).Error; err == nil {
			buyerUpline = referrer.WalletAddress
		}
	}

	// One id for every order this checkout creates: a checkout funded by swap
	// is paid as one group (services/marketplace_funding.go).
	groupID := uuid.New()
	var orders []models.Order
	for _, line := range lines {
		if !line.isService {
			// Conditional decrement: two cart lines for the same product were each
			// checked against the starting stock, so the row itself must refuse
			// to go negative.
			res := tx.Model(&models.Product{}).
				Where("id = ? AND stock >= ?", line.product.ID, line.item.Quantity).
				Update("stock", gorm.Expr("stock - ?", line.item.Quantity))
			if res.Error != nil {
				tx.Rollback()
				utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update product stock")
				return
			}
			if res.RowsAffected == 0 {
				tx.Rollback()
				utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Insufficient stock for %s", line.product.Name))
				return
			}
		}

		order := models.Order{
			ID:              uuid.New(),
			BuyerID:         user.ID,
			SellerID:        line.seller.ID,
			ProductID:       line.item.Product,
			Kind:            line.kind,
			Quantity:        line.quantity,
			Token:           token,
			ProductAmount:   line.productAmount.String(),
			ShippingAmount:  line.shippingAmount.String(),
			PlatformFee:     line.platformFee.String(),
			TotalAmount:     line.orderTotal().String(),
			ShippingZone:    line.zone,
			ShippingCity:    buyerCity,
			ShippingState:   buyerState,
			ShippingCountry: buyerCountry,
			BuyerUpline:     buyerUpline,
			Status:          models.OrderStatusPendingPayment,
			CheckoutGroupID: &groupID,
			FundWithSwap:    fundBySwap,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		if in.delivery != nil && !line.isService {
			order.ShippingRecipientName = in.delivery.RecipientName
			order.ShippingPhone = in.delivery.Phone
			order.ShippingStreet = in.delivery.Street
		}

		// Persist Order-type menu selections in the dedicated column (not Notes,
		// which ship/refund handlers overwrite — F2).
		order.SetMenuSelections(line.menuSelections)

		// Carry the buyer's product spec selections + purchase note onto the
		// order so the merchant knows how to arrange the purchase. PRODUCT items
		// only — service items reuse item.Notes to encode tier meta (serviceCart).
		if !line.isService {
			order.PurchaseNote = line.item.Notes
			if len(line.item.SelectedAttributes) > 0 {
				attrs := make([]models.OrderAttribute, 0, len(line.item.SelectedAttributes))
				for _, a := range line.item.SelectedAttributes {
					attrs = append(attrs, models.OrderAttribute{Name: a.Name, Value: a.Value})
				}
				order.SetSelectedAttributes(attrs)
			}
		}

		if err := tx.Create(&order).Error; err != nil {
			tx.Rollback()
			log.Printf("Failed to create order: %v", err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create order")
			return
		}

		// A booking-type order reserves its slot in the same transaction.
		if line.booking != nil {
			line.booking.OrderID = order.ID
			if err := tx.Create(line.booking).Error; err != nil {
				tx.Rollback()
				log.Printf("Failed to create service booking: %v", err)
				utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reserve slot")
				return
			}
		}

		orders = append(orders, order)
	}

	// Mark cart as converted
	now := time.Now()
	cart.Status = "converted"
	cart.ConvertedAt = &now
	if err := tx.Save(&cart).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update cart")
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to commit transaction")
		return
	}

	for i := range orders {
		order := &orders[i]
		shortID := order.ID.String()[:8]
		ch.EventBus.Publish(events.Event{
			Type:    events.OrderPlaced,
			UserID:  order.BuyerID,
			Title:   "Order Placed",
			Message: fmt.Sprintf("Your order #%s has been placed. Complete payment to proceed.", shortID),
			Data: map[string]interface{}{
				"orderId": order.ID.String(),
				"status":  order.Status,
				"role":    "buyer",
			},
		})
		ch.EventBus.Publish(events.Event{
			Type:    events.OrderPlaced,
			UserID:  order.SellerID,
			Title:   "New Order Received",
			Message: fmt.Sprintf("You have received a new order #%s. Awaiting buyer payment.", shortID),
			Data: map[string]interface{}{
				"orderId": order.ID.String(),
				"status":  order.Status,
				"role":    "seller",
			},
		})
	}

	utils.SuccessResponse(c, http.StatusCreated, "Orders created successfully", gin.H{
		"orders":          orders,
		"checkoutGroupId": groupID,
	})
}

// priceActiveCart prices the loaded cart exactly as order creation will — the
// one read QuoteCart and the funding cart quote share — writing the error
// response and returning false when it cannot.
func (ch *CheckoutHandler) priceActiveCart(c *gin.Context, in *cartCheckout) ([]pricedCartLine, pricingRates, bool) {
	if ch.refuseMcgpUnavailable(c, in.token) {
		return nil, pricingRates{}, false
	}
	rates, perr := ch.resolvePricingRates(c.Request.Context(), in.token)
	if perr != nil {
		utils.ErrorResponse(c, perr.status, perr.msg)
		return nil, pricingRates{}, false
	}
	// Pricing reads inside a transaction that is always rolled back, so service
	// pricing sees the same consistent view it gets at order creation.
	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return nil, pricingRates{}, false
	}
	defer tx.Rollback()
	lines, perr := ch.priceCartItems(tx, in.user, in.items, in.req, rates)
	if perr != nil {
		utils.ErrorResponse(c, perr.status, perr.msg)
		return nil, pricingRates{}, false
	}
	return lines, rates, true
}

// cartQuoteLine is one order-to-be as the buyer will pay it, in the token's
// base units (decimal strings). Total = product + shipping + platform fee +
// gas fee: everything the approve/escrow pair pulls from the buyer's wallet.
type cartQuoteLine struct {
	ProductID      string `json:"productId"`
	Name           string `json:"name"`
	SellerID       string `json:"sellerId"`
	SellerName     string `json:"sellerName"`
	Quantity       int    `json:"quantity"`
	Kind           string `json:"kind"`
	ShippingZone   string `json:"shippingZone"`
	ProductAmount  string `json:"productAmount"`
	ShippingAmount string `json:"shippingAmount"`
	PlatformFee    string `json:"platformFee"`
	GasFee         string `json:"gasFee"`
	Total          string `json:"total"`
}

type cartQuoteTotals struct {
	ProductAmount  string `json:"productAmount"`
	ShippingAmount string `json:"shippingAmount"`
	PlatformFee    string `json:"platformFee"`
	GasFee         string `json:"gasFee"`
	Total          string `json:"total"`
}

// QuoteCart handles POST /api/orders/quote: prices the active cart for a pay
// token exactly as CreateOrderFromCart would, without creating orders or
// touching stock or booking slots. The app's confirm step shows this, so the
// amount a buyer confirms is the amount the escrow pulls. Same body as
// CreateOrderFromCart.
func (ch *CheckoutHandler) QuoteCart(c *gin.Context) {
	in, ok := loadCartCheckout(c)
	if !ok {
		return
	}
	token := in.token
	lines, rates, ok := ch.priceActiveCart(c, in)
	if !ok {
		return
	}

	gasFee := rates.gasFee
	sum := struct{ product, shipping, fee, gas, total *big.Int }{
		new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int),
	}
	out := make([]cartQuoteLine, 0, len(lines))
	for _, l := range lines {
		lineTotal := new(big.Int).Add(l.orderTotal(), gasFee)
		sellerName := l.seller.Name
		if sellerName == "" {
			sellerName = l.seller.Username
		}
		out = append(out, cartQuoteLine{
			ProductID:      l.product.ID.String(),
			Name:           l.product.Name,
			SellerID:       l.seller.ID.String(),
			SellerName:     sellerName,
			Quantity:       l.quantity,
			Kind:           l.kind,
			ShippingZone:   l.zone,
			ProductAmount:  l.productAmount.String(),
			ShippingAmount: l.shippingAmount.String(),
			PlatformFee:    l.platformFee.String(),
			GasFee:         gasFee.String(),
			Total:          lineTotal.String(),
		})
		sum.product.Add(sum.product, l.productAmount)
		sum.shipping.Add(sum.shipping, l.shippingAmount)
		sum.fee.Add(sum.fee, l.platformFee)
		sum.gas.Add(sum.gas, gasFee)
		sum.total.Add(sum.total, lineTotal)
	}

	utils.SuccessResponse(c, http.StatusOK, "Quote ready", gin.H{
		"token":    token,
		"decimals": tokenDecimals(token),
		// USD value of one token, from the same read the amounts were sized
		// with, so the app's "≈ local currency" matches the charge.
		"usdPerToken": rates.usdPerToken(),
		"lines":       out,
		"totals": cartQuoteTotals{
			ProductAmount:  sum.product.String(),
			ShippingAmount: sum.shipping.String(),
			PlatformFee:    sum.fee.String(),
			GasFee:         sum.gas.String(),
			Total:          sum.total.String(),
		},
	})
}

type createServiceOrderRequest struct {
	Token          string              `json:"token" binding:"required"`
	ProductID      string              `json:"productId" binding:"required"`
	BuyerCity      string              `json:"buyerCity"`
	BuyerState     string              `json:"buyerState"`
	BuyerCountry   string              `json:"buyerCountry"`
	ServiceTier    string              `json:"serviceTier"`
	MenuSelections []menuLineRequest   `json:"menuSelections"`
	BookingSlot    *bookingSlotRequest `json:"bookingSlot"`
	// Answers are the buyer's auto-generated booking-form answers, keyed by the
	// service's FieldConfig field keys (unified booking engine). Validated against
	// the config and stored on the order for the merchant to fulfil.
	Answers map[string]interface{} `json:"answers"`
}

// CreateServiceOrder creates a SINGLE service order directly (no cart) for the
// order/booking/contact "Pay now" flows, which carry per-item menu selections /
// booking slots the shared cart route cannot thread. It reuses the exact same
// service pricing + capacity rules as the cart checkout (resolveServicePricing).
func (ch *CheckoutHandler) CreateServiceOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req createServiceOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "token and productId are required")
		return
	}
	token := strings.ToUpper(req.Token)
	if token != "USDC" && token != "USDT" && token != "MCGP" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported token. Use USDC, USDT, or MCGP")
		return
	}
	if ch.refuseMcgpUnavailable(c, token) {
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	buyerCity, buyerState, buyerCountry := req.BuyerCity, req.BuyerState, req.BuyerCountry
	if buyerCity == "" {
		buyerCity = user.City
	}
	if buyerState == "" {
		buyerState = user.State
	}
	if buyerCountry == "" {
		buyerCountry = user.Country
	}

	rates, balance, perr := ch.ratesAndBalance(c.Request.Context(), token, user.WalletAddress)
	if perr != nil {
		utils.ErrorResponse(c, perr.status, perr.msg)
		return
	}

	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var product models.Product
	if err := tx.First(&product, "id = ?", productID).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		return
	}
	if product.Type != models.ProductTypeService {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusBadRequest, "Not a service listing")
		return
	}
	if product.UserID == user.ID {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusBadRequest, "You can't book or pay for your own service")
		return
	}
	var seller models.User
	if err := tx.First(&seller, "id = ?", product.UserID).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusBadRequest, "Seller not found")
		return
	}

	// Same guard as the cart path: a suspended or deleted service — or one whose
	// provider's account is suspended — cannot be booked.
	if !product.IsVisibleToPublic(&seller) {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusBadRequest, "This service is no longer available")
		return
	}

	serviceType, profile := resolveServiceTypeAndProfile(tx, &product)
	if serviceType == models.ServiceTypeContact && !serviceContactGatePaid(tx, user.ID, product.ID) {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusForbidden, fmt.Sprintf("Pay the contact fee for %s before engaging it", product.Name))
		return
	}
	if rejectsInstantBuy(serviceType, &profile) {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("%s requires a booking request — submit a request and wait for the provider to approve before paying.", product.Name))
		return
	}

	order, errMsg, status := ch.placeServiceOrder(
		tx, rates, user, &seller, &product, serviceType, &profile,
		token, req.ServiceTier, buyerCity, buyerState, buyerCountry,
		req.MenuSelections, req.BookingSlot, req.Answers, nil,
	)
	if errMsg != "" {
		tx.Rollback()
		utils.ErrorResponse(c, status, errMsg)
		return
	}
	if refuseShortfall(c, tx, token, orderPull(order, rates.gasFee), balance) {
		return
	}
	if ch.refuseSpendableLimit(c, tx, token, orderPull(order, rates.gasFee)) {
		return
	}
	if err := tx.Commit().Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to commit transaction")
		return
	}

	shortID := order.ID.String()[:8]
	ch.EventBus.Publish(events.Event{
		Type: events.OrderPlaced, UserID: order.BuyerID, Title: "Order Placed",
		Message: fmt.Sprintf("Your service order #%s has been placed. Complete payment to proceed.", shortID),
		Data:    map[string]interface{}{"orderId": order.ID.String(), "status": order.Status, "role": "buyer"},
	})
	ch.EventBus.Publish(events.Event{
		Type: events.OrderPlaced, UserID: order.SellerID, Title: "New Service Order",
		Message: fmt.Sprintf("You have a new service order #%s. Awaiting buyer payment.", shortID),
		Data:    map[string]interface{}{"orderId": order.ID.String(), "status": order.Status, "role": "seller"},
	})

	utils.SuccessResponse(c, http.StatusCreated, "Order created successfully", gin.H{"orders": []models.Order{*order}})
}

// placeServiceOrder runs the service pricing → on-chain amount conversion →
// Order (+ optional ServiceBooking) creation for a single service inside an open
// transaction, and returns the created order. It does NOT commit or publish
// events — the caller owns those. Shared by the inline "Pay now" checkout
// (CreateServiceOrder) and the pay-a-booking-request conversion so the money
// path lives in one place. On failure it returns a non-empty errMsg + HTTP
// status and leaves the tx open for the caller to roll back.
func (ch *CheckoutHandler) placeServiceOrder(
	tx *gorm.DB, rates pricingRates, buyer, seller *models.User, product *models.Product,
	serviceType string, profile *models.ServiceProfile,
	token, serviceTier, buyerCity, buyerState, buyerCountry string,
	menuLines []menuLineRequest, slotReq *bookingSlotRequest, answers map[string]interface{},
	priceOverrideUSD *float64,
) (*models.Order, string, int) {
	// Validate the auto-generated booking-form answers against the merchant's
	// field config (unified booking engine). No config → no validation (legacy).
	if err := models.ValidateAnswers(profile.GetFieldConfig(), answers); err != nil {
		return nil, err.Error(), http.StatusBadRequest
	}

	origin := resolveShippingOrigin(product, seller)
	detectedZone := DetectShippingZone(buyerCity, buyerState, buyerCountry, origin.City, origin.State, origin.Country)

	var priceUSD, calloutUSD float64
	var zone string
	var menuSel []models.OrderMenuSelection
	var booking *models.ServiceBooking
	if priceOverrideUSD != nil {
		// Provider-quoted price (price-on-request): the quote is the all-in
		// service price; no tier call-out is added and there's no slot to reserve.
		priceUSD = *priceOverrideUSD
		zone = detectedZone
	} else {
		var perr string
		priceUSD, calloutUSD, zone, menuSel, booking, perr = ch.resolveServicePricing(
			tx, buyer.ID, product, serviceType, profile, detectedZone, serviceTier, menuLines, slotReq,
		)
		if perr != "" {
			return nil, perr, http.StatusBadRequest
		}
	}

	priceWei, err := rates.usdToWei(priceUSD)
	if err != nil {
		return nil, "Failed to convert product amount", http.StatusInternalServerError
	}
	shippingAmountWei, err := rates.usdToWei(calloutUSD)
	if err != nil {
		return nil, "Failed to convert shipping amount", http.StatusInternalServerError
	}
	productAmountWei, platformFeeWei := splitSellerFee(priceWei, rates.feeBPS)
	if productAmountWei == nil || productAmountWei.Sign() <= 0 {
		return nil, fmt.Sprintf("%s resolves to a zero on-chain amount for %s", product.Name, token), http.StatusBadRequest
	}

	totalAmountWei := new(big.Int).Add(productAmountWei, shippingAmountWei)
	totalAmountWei.Add(totalAmountWei, platformFeeWei)

	buyerUpline := ""
	if buyer.ReferredBy != nil {
		var referrer models.User
		if err := tx.First(&referrer, "id = ?", *buyer.ReferredBy).Error; err == nil {
			buyerUpline = referrer.WalletAddress
		}
	}

	order := models.Order{
		ID:              uuid.New(),
		BuyerID:         buyer.ID,
		SellerID:        product.UserID,
		ProductID:       product.ID,
		Kind:            models.OrderKindService,
		Quantity:        1,
		Token:           token,
		ProductAmount:   productAmountWei.String(),
		ShippingAmount:  shippingAmountWei.String(),
		PlatformFee:     platformFeeWei.String(),
		TotalAmount:     totalAmountWei.String(),
		ShippingZone:    zone,
		ShippingCity:    buyerCity,
		ShippingState:   buyerState,
		ShippingCountry: buyerCountry,
		BuyerUpline:     buyerUpline,
		Status:          models.OrderStatusPendingPayment,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	order.SetMenuSelections(menuSel)
	order.SetServiceAnswers(answers)

	if err := tx.Create(&order).Error; err != nil {
		log.Printf("Failed to create service order: %v", err)
		return nil, "Failed to create order", http.StatusInternalServerError
	}
	if booking != nil {
		booking.OrderID = order.ID
		if err := tx.Create(booking).Error; err != nil {
			log.Printf("Failed to create service booking: %v", err)
			return nil, "Failed to reserve slot", http.StatusInternalServerError
		}
	}
	return &order, "", 0
}

// orderSystemFeeUSD computes the USD-denominated system fee for TP distribution.
// For MCGP orders, the buyer pays no platform fee or gas fee, so TP distributes 0.
// This is the LEGACY (v1) bundle: 2% merchant platform fee + $0.10 gas fee.
func orderSystemFeeUSD(order *models.Order) float64 {
	// Product gas flat — documented-GLOBAL (chainID nil), Sonic-only flow (§6).
	gasFeeUSD, _ := services.FeeFlatUSD(models.FeatureProduct, order.Token, nil)
	return orderMerchantFeeUSD(order) + gasFeeUSD
}

// orderMerchantFeeUSD is the 2% MERCHANT platform fee in USD ONLY — the carved
// fee the v2 engine settles under FeatureProductMerchant. It excludes the buyer's
// stablecoin gas (a separate 100%-system fee that must not generate TP/cashback).
// For MCGP orders order.PlatformFee is zero, so this returns 0.
func orderMerchantFeeUSD(order *models.Order) float64 {
	return weiToUSDFloat(order.PlatformFee, order.Token)
}

// getOrderForEscrow is a shared helper for prepare-approve and prepare-escrow.
func (ch *CheckoutHandler) getOrderForEscrow(c *gin.Context) (*models.Order, *models.User, *models.User, string, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, nil, nil, "", false
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return nil, nil, nil, "", false
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return nil, nil, nil, "", false
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can prepare escrow")
		return nil, nil, nil, "", false
	}

	// MCGP is paid from Spendable MCGP (pay-spendable), never by depositing
	// wallet MCGP into the escrow.
	if refuseLegacyMcgpFunding(c, &order) {
		return nil, nil, nil, "", false
	}

	if !isValidTransitionForKind(order.Status, models.OrderStatusEscrowed, order.Kind) {
		// Coded so the app stops offering a retry that can never succeed.
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			fmt.Sprintf("Cannot prepare escrow: order is in %s status", order.Status), "order_not_payable")
		return nil, nil, nil, "", false
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return nil, nil, nil, "", false
	}

	var seller models.User
	if err := config.DB.First(&seller, "id = ?", order.SellerID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Seller not found")
		return nil, nil, nil, "", false
	}
	if seller.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Seller has no wallet address")
		return nil, nil, nil, "", false
	}

	tokenAddr := ch.BlockchainService.TokenAddress("sonic", order.Token)
	if tokenAddr == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Token %s not configured", order.Token))
		return nil, nil, nil, "", false
	}

	// The buyer is paying now: restart the unpaid-order clock, so the expiry
	// cannot cancel an order whose escrow tx is about to be sent or mined.
	if err := services.TouchUnpaidOrder(config.DB, order.ID); err != nil {
		log.Printf("[checkout] touch order %s failed: %v", order.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare payment")
		return nil, nil, nil, "", false
	}

	return &order, user, &seller, tokenAddr, true
}

// repriceToEscrowFee keeps a pending order payable when the escrow's fee rate
// changed after the order was created. createOrder computes the fee from the
// CURRENT rate, so a stored fee at an older rate makes the approve short (the
// escrow reverts, on every retry) or long (the stored fee no longer matches
// what the escrow holds). The listing price the buyer agreed to —
// productAmount + stored fee — is re-split at the current rate, so the buyer
// pays the same and only the seller/fee split moves. Orders already escrowed
// are left alone.
func repriceToEscrowFee(db *gorm.DB, order *models.Order, bps int) error {
	if order.Status != models.OrderStatusPendingPayment {
		return nil
	}
	product, ok1 := new(big.Int).SetString(order.ProductAmount, 10)
	storedFee, ok2 := new(big.Int).SetString(order.PlatformFee, 10)
	shipping, ok3 := new(big.Int).SetString(order.ShippingAmount, 10)
	if !ok1 || !ok2 || !ok3 {
		return fmt.Errorf("order %s has non-numeric amounts", order.ID)
	}
	if escrowFeeOn(product, bps).Cmp(storedFee) == 0 {
		return nil
	}
	listing := new(big.Int).Add(product, storedFee)
	newProduct, newFee := splitSellerFee(listing, bps)
	if newProduct.Sign() <= 0 {
		return fmt.Errorf("order %s re-prices to a zero product amount", order.ID)
	}
	total := new(big.Int).Add(newProduct, shipping)
	total.Add(total, newFee)
	log.Printf("[checkout] re-priced order %s to %d bps: product %s→%s fee %s→%s",
		order.ID, bps, order.ProductAmount, newProduct, order.PlatformFee, newFee)
	updates := map[string]interface{}{
		"product_amount": newProduct.String(),
		"platform_fee":   newFee.String(),
		"total_amount":   total.String(),
	}
	if err := db.Model(order).Updates(updates).Error; err != nil {
		return err
	}
	order.ProductAmount, order.PlatformFee, order.TotalAmount = newProduct.String(), newFee.String(), total.String()
	return nil
}

// PrepareApprove handles POST /api/orders/:id/prepare-approve — returns the ERC20 approve tx.
func (ch *CheckoutHandler) PrepareApprove(c *gin.Context) {
	order, user, _, tokenAddr, ok := ch.getOrderForEscrow(c)
	if !ok {
		return
	}

	rates, perr := ch.resolvePricingRates(c.Request.Context(), order.Token)
	if perr != nil {
		utils.ErrorResponse(c, perr.status, perr.msg)
		return
	}
	if err := repriceToEscrowFee(config.DB, order, rates.feeBPS); err != nil {
		log.Printf("Failed to re-price order %s to the escrow fee: %v", order.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare approve transaction")
		return
	}

	// The escrow pulls product + shipping + platformFee + its gas fee, so the
	// approve covers exactly that, with the gas read from the escrow itself.
	approveAmount := orderPull(order, rates.gasFee)

	client := ch.BlockchainService.ClientForChain("sonic")
	if client == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Blockchain service unavailable")
		return
	}

	approveTxBytes, err := client.PrepareERC20Approve(tokenAddr, user.WalletAddress, ch.Config.ProductEscrowAddress, approveAmount)
	if err != nil {
		log.Printf("Failed to prepare approve tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare approve transaction")
		return
	}

	var approveTx map[string]interface{}
	if err := json.Unmarshal(approveTxBytes, &approveTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode approve transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Approve transaction prepared", gin.H{
		"approveTx": approveTx,
	})
}

// PrepareEscrow handles POST /api/orders/:id/prepare-escrow — returns the createOrder tx.
// Must be called AFTER the approve tx has been broadcast so gas estimation succeeds.
func (ch *CheckoutHandler) PrepareEscrow(c *gin.Context) {
	order, user, seller, tokenAddr, ok := ch.getOrderForEscrow(c)
	if !ok {
		return
	}

	productAmount, _ := new(big.Int).SetString(order.ProductAmount, 10)
	shippingAmount, _ := new(big.Int).SetString(order.ShippingAmount, 10)

	contractOrderID := services.GenerateOrderID(order.ID)
	upline := order.BuyerUpline
	if upline == "" {
		upline = "0x0000000000000000000000000000000000000000"
	}

	createOrderTxBytes, err := ch.EscrowService.PrepareCreateOrder(
		contractOrderID,
		user.WalletAddress,
		seller.WalletAddress,
		tokenAddr,
		productAmount,
		shippingAmount,
		upline,
	)
	if err != nil {
		log.Printf("Failed to prepare createOrder tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare escrow transaction")
		return
	}

	var createOrderTx map[string]interface{}
	if err := json.Unmarshal(createOrderTxBytes, &createOrderTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode escrow transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Escrow transaction prepared", gin.H{
		"createOrderTx":   createOrderTx,
		"contractOrderId": fmt.Sprintf("0x%x", contractOrderID),
	})
}

// SubmitEscrow handles POST /api/orders/:id/submit-escrow
func (ch *CheckoutHandler) SubmitEscrow(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req submitEscrowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "approveTxHash and escrowTxHash are required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can submit escrow")
		return
	}
	// An order claimed by a Spendable MCGP payment must never also be marked
	// paid by an escrow deposit. If one did reach the escrow (an old app racing
	// the claim), it is a second payment for the order: page a human.
	if order.IsSpendableMCGP() && req.EscrowTxHash != "" && ch.EscrowService != nil {
		if _, err := ch.EscrowService.VerifyEscrowCreated(req.EscrowTxHash, services.GenerateOrderID(order.ID)); err == nil {
			log.Printf("[checkout] ALERT order_mcgp_double_funded=1 order=%s escrow_tx=%s — escrow deposit for an order paid from Spendable MCGP; refund the escrow", order.ID, req.EscrowTxHash)
		}
	}
	if refuseSpendableOnChainStep(c, &order, "Its payment is confirmed automatically.") {
		return
	}

	switch {
	case order.Status == models.OrderStatusEscrowed:
		// Already recorded — by an earlier submit whose response was lost, or
		// by the unpaid-order sweep. A retry proving the same escrow succeeds.
		created, err := ch.EscrowService.VerifyEscrowCreated(req.EscrowTxHash, services.GenerateOrderID(order.ID))
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Cannot submit escrow: order is in escrowed status")
			return
		}
		if order.EscrowTxHash == "" {
			if err := config.DB.Model(&models.Order{}).
				Where("id = ? AND (escrow_tx_hash IS NULL OR escrow_tx_hash = '')", order.ID).
				Updates(map[string]interface{}{"approve_tx_hash": req.ApproveTxHash, "escrow_tx_hash": req.EscrowTxHash}).Error; err != nil {
				log.Printf("[checkout] record tx hashes for order %s failed: %v", order.ID, err)
			}
		}
		expires := time.Now().Add(services.EscrowWindow)
		if order.EscrowExpiresAt != nil {
			expires = *order.EscrowExpiresAt
		}
		respondEscrowSubmitted(c, order.ID, created.OrderID, expires)
		return

	case order.Status == models.OrderStatusCancelled && order.EscrowTxHash == "":
		// A payment that reached escrow after the order was cancelled is not
		// revived — its stock may be resold, and the cancel may be the
		// seller's. It is surfaced so the buyer is refunded.
		if _, err := ch.EscrowService.VerifyEscrowCreated(req.EscrowTxHash, services.GenerateOrderID(order.ID)); err == nil {
			log.Printf("[checkout] ALERT paid_after_cancel=1 order=%s escrow_tx=%s: payment reached escrow after the order was cancelled", order.ID, req.EscrowTxHash)
			utils.ErrorResponseWithCode(c, http.StatusConflict,
				"Your payment reached escrow after this order was cancelled. Contact support to have it refunded.", "order_not_payable")
			return
		}
	}
	if !isValidTransitionForKind(order.Status, models.OrderStatusEscrowed, order.Kind) {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			fmt.Sprintf("Cannot submit escrow: order is in %s status", order.Status), "order_not_payable")
		return
	}

	// Reporting a payment is payment activity: keep the unpaid-order expiry off
	// an order whose escrow tx may still be confirming.
	if err := services.TouchUnpaidOrder(config.DB, order.ID); err != nil {
		log.Printf("[checkout] touch order %s failed: %v", order.ID, err)
	}

	// Verify approve tx
	approveReceipt := ch.BlockchainService.GetTransactionReceipt(req.ApproveTxHash, "sonic")
	switch approveReceipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify approve transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Approve transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Approve transaction failed on-chain")
		return
	}

	// Verify escrow tx and extract OrderCreated event
	created, err := ch.EscrowService.VerifyEscrowCreated(req.EscrowTxHash, services.GenerateOrderID(order.ID))
	if err != nil {
		log.Printf("Failed to verify escrow tx: %v", err)
		utils.ErrorResponse(c, http.StatusBadRequest, "Escrow transaction verification failed: "+err.Error())
		return
	}

	expires := time.Now().Add(services.EscrowWindow)
	updates := services.EscrowedOrderUpdates(created.OrderID, created.ProductAmount, created.ShippingAmount,
		created.PlatformFee, ch.EscrowService.CurrentEscrowAddress(), expires)
	updates["approve_tx_hash"] = req.ApproveTxHash
	updates["escrow_tx_hash"] = req.EscrowTxHash
	if err := config.DB.Transaction(func(tx *gorm.DB) error {
		return services.MarkOrderEscrowed(tx, order.ID, updates)
	}); errors.Is(err, services.ErrOrderNotPayable) {
		// Recorded or cancelled while the receipts were being verified; a retry
		// lands in the matching branch above.
		utils.ErrorResponse(c, http.StatusConflict, "This order changed while your payment was being confirmed. Tap Try again.")
		return
	} else if err != nil {
		log.Printf("[checkout] mark order %s escrowed failed: %v", order.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	services.NotifyOrderEscrowed(ch.EventBus, &order, expires)
	respondEscrowSubmitted(c, order.ID, created.OrderID, expires)
}

func respondEscrowSubmitted(c *gin.Context, orderID uuid.UUID, contractOrderID [32]byte, expires time.Time) {
	utils.SuccessResponse(c, http.StatusOK, "Escrow submitted successfully", gin.H{
		"orderId":         orderID,
		"status":          models.OrderStatusEscrowed,
		"contractOrderId": fmt.Sprintf("0x%x", contractOrderID),
		"escrowExpiresAt": expires,
	})
}

// shipRequest optional tracking info when marking order as shipped.
type shipRequest struct {
	TrackingNumber string `json:"trackingNumber"`
	Notes          string `json:"notes"`
}

// MarkShipped handles POST /api/orders/:id/ship
// Seller marks an escrowed order as shipped. Optional tracking number and notes.
func (ch *CheckoutHandler) MarkShipped(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req shipRequest
	// Body is optional — ignore bind errors so seller can ship without tracking info
	_ = c.ShouldBindJSON(&req)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can mark as shipped")
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if order.Kind == models.OrderKindService {
		utils.ErrorResponse(c, http.StatusBadRequest, "Services are not shipped — use 'Mark service rendered' instead")
		return
	}

	if !isValidTransitionForKind(order.Status, models.OrderStatusShipped, order.Kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot mark as shipped: order is in %s status", order.Status))
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":            models.OrderStatusShipped,
		"seller_shipped_at": now,
	}
	if strings.TrimSpace(req.TrackingNumber) != "" {
		updates["tracking_number"] = strings.TrimSpace(req.TrackingNumber)
	}
	if strings.TrimSpace(req.Notes) != "" {
		updates["notes"] = strings.TrimSpace(req.Notes)
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	shortOrderID := order.ID.String()[:8]
	shipMsg := fmt.Sprintf("Your order #%s has been shipped by the seller.", shortOrderID)
	if tn := strings.TrimSpace(req.TrackingNumber); tn != "" {
		shipMsg = fmt.Sprintf("Your order #%s has been shipped. Tracking: %s", shortOrderID, tn)
	}
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderShipped,
		UserID:  order.BuyerID,
		Title:   "Order Shipped",
		Message: shipMsg,
		Data: map[string]interface{}{
			"orderId":        order.ID.String(),
			"status":         models.OrderStatusShipped,
			"trackingNumber": req.TrackingNumber,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Order marked as shipped", gin.H{
		"orderId":         order.ID,
		"status":          models.OrderStatusShipped,
		"sellerShippedAt": now,
		"trackingNumber":  req.TrackingNumber,
	})
}

// MarkDelivered handles POST /api/orders/:id/deliver
func (ch *CheckoutHandler) MarkDelivered(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req deliverRequest
	_ = c.ShouldBindJSON(&req)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can mark as delivered")
		return
	}

	// Products require delivery proof; services ("rendered") do not.
	if order.Kind != models.OrderKindService && req.DeliveryProofURL == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "deliveryProofUrl is required")
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if !isValidTransitionForKind(order.Status, models.OrderStatusDelivered, order.Kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot mark as delivered: order is in %s status", order.Status))
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":              models.OrderStatusDelivered,
		"delivery_proof_url":  req.DeliveryProofURL,
		"seller_delivered_at": now,
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		log.Printf("MarkDelivered reload error: %v", err)
	}

	deliveredTitle := "Order Delivered"
	deliveredMsg := fmt.Sprintf("Your order %s has been marked as delivered by the seller", order.ID)
	if order.Kind == models.OrderKindService {
		deliveredTitle = "Service Rendered"
		deliveredMsg = fmt.Sprintf("The provider marked service order %s as rendered. Confirm to release payment.", order.ID)
	}
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderDelivered,
		UserID:  order.BuyerID,
		Title:   deliveredTitle,
		Message: deliveredMsg,
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusDelivered,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Order marked as delivered", order)
}

// refundDecisionRequest optional note when approving or rejecting a refund.
type refundDecisionRequest struct {
	Notes string `json:"notes"`
}

// ApproveRefund handles POST /api/orders/:id/approve-refund
// Merchant consents to refund. Order stays in refund_requested; admin executes on-chain.
// Sets merchant_approved_refund=true so admin dashboard knows.
func (ch *CheckoutHandler) ApproveRefund(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req refundDecisionRequest
	_ = c.ShouldBindJSON(&req)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can approve a refund")
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if order.Status != models.OrderStatusRefundRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot approve refund: order is in %s status, must be %s", order.Status, models.OrderStatusRefundRequested))
		return
	}

	updates := map[string]interface{}{
		"merchant_approved_refund": true,
	}
	if strings.TrimSpace(req.Notes) != "" {
		updates["notes"] = strings.TrimSpace(req.Notes)
	}
	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderRefundApproved,
		UserID:  order.BuyerID,
		Title:   "Refund Approved by Seller",
		Message: fmt.Sprintf("The seller has approved your refund request for order %s. An admin will finalize the refund shortly.", order.ID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  order.Status,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Refund approved by seller", gin.H{
		"orderId":                order.ID,
		"status":                 order.Status,
		"merchantApprovedRefund": true,
	})
}

// RejectRefund handles POST /api/orders/:id/reject-refund
// Merchant disputes the refund request. Status returns to 'delivered' and the buyer
// can re-request or admin can manually resolve.
func (ch *CheckoutHandler) RejectRefund(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req refundDecisionRequest
	_ = c.ShouldBindJSON(&req)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can reject a refund")
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if order.Status != models.OrderStatusRefundRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot reject refund: order is in %s status, must be %s", order.Status, models.OrderStatusRefundRequested))
		return
	}

	if !isValidTransitionForKind(order.Status, models.OrderStatusDelivered, order.Kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot transition back to delivered")
		return
	}

	updates := map[string]interface{}{
		"status":                   models.OrderStatusDelivered,
		"merchant_approved_refund": false,
	}
	if strings.TrimSpace(req.Notes) != "" {
		updates["notes"] = strings.TrimSpace(req.Notes)
	}
	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderRefundRejected,
		UserID:  order.BuyerID,
		Title:   "Refund Rejected",
		Message: fmt.Sprintf("The seller has rejected your refund request for order %s. Contact support if you disagree.", order.ID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusDelivered,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Refund rejected by seller", gin.H{
		"orderId": order.ID,
		"status":  models.OrderStatusDelivered,
	})
}

// PrepareConfirm handles POST /api/orders/:id/prepare-confirm
func (ch *CheckoutHandler) PrepareConfirm(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can confirm receipt")
		return
	}

	if refuseSpendableOnChainStep(c, &order, "Update the app and confirm it again.") {
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if !isValidTransitionForKind(order.Status, models.OrderStatusCompleted, order.Kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot confirm receipt: order is in %s status", order.Status))
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	contractOrderID := services.GenerateOrderID(order.ID)
	confirmTxBytes, err := ch.EscrowService.PrepareConfirmReceipt(contractOrderID, user.WalletAddress, ch.orderEscrowContract(&order))
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare confirm transaction")
		return
	}

	var confirmTx map[string]interface{}
	if err := json.Unmarshal(confirmTxBytes, &confirmTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode confirm transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Confirm transaction prepared", gin.H{
		"confirmTx": confirmTx,
	})
}

// SubmitConfirm handles POST /api/orders/:id/submit-confirm
func (ch *CheckoutHandler) SubmitConfirm(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req submitConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash is required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can confirm receipt")
		return
	}

	if refuseSpendableOnChainStep(c, &order, "Update the app and confirm it again.") {
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if !isValidTransitionForKind(order.Status, models.OrderStatusCompleted, order.Kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot confirm receipt: order is in %s status", order.Status))
		return
	}

	// Verify tx
	receipt := ch.BlockchainService.GetTransactionReceipt(req.TxHash, "sonic")
	switch receipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify confirm transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Confirm transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Confirm transaction failed on-chain")
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":             models.OrderStatusCompleted,
		"release_tx_hash":    req.TxHash,
		"buyer_confirmed_at": now,
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	ch.settleCompletedOrderFees(&order, now)

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCompleted,
		UserID:  order.SellerID,
		Title:   "Order Confirmed by Buyer",
		Message: fmt.Sprintf("The buyer confirmed receipt for order #%s. Funds have been released to your wallet.", order.ID.String()[:8]),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusCompleted,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Order completed", gin.H{
		"orderId":          order.ID,
		"status":           models.OrderStatusCompleted,
		"buyerConfirmedAt": now,
	})
}

// RequestRefund handles POST /api/orders/:id/request-refund
func (ch *CheckoutHandler) RequestRefund(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can request a refund")
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	switch order.Status {
	case models.OrderStatusEscrowed:
		if order.IsSpendableMCGP() {
			// Before delivery the escrow refunds instantly; so does the hold.
			if _, err := ch.refundSpendableOrder(c.Request.Context(), &order,
				map[string]interface{}{"status": models.OrderStatusRefunded}, false, true); err != nil {
				respondSpendableErr(c, order.ID, err)
				return
			}
			ch.EventBus.Publish(events.Event{
				Type:    events.OrderRefunded,
				UserID:  order.SellerID,
				Title:   "Order Refunded",
				Message: fmt.Sprintf("The buyer refunded order #%s before it was delivered.", order.ID.String()[:8]),
				Data:    map[string]interface{}{"orderId": order.ID.String(), "status": models.OrderStatusRefunded},
			})
			utils.SuccessResponse(c, http.StatusOK, "Refunded to your Spendable MCGP", gin.H{
				"orderId": order.ID,
				"status":  models.OrderStatusRefunded,
			})
			return
		}
		// Before delivery — prepare unsigned requestRefund tx
		contractOrderID := services.GenerateOrderID(order.ID)
		refundTxBytes, err := ch.EscrowService.PrepareRequestRefund(contractOrderID, user.WalletAddress, ch.orderEscrowContract(&order))
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare refund transaction")
			return
		}
		var refundTx map[string]interface{}
		if err := json.Unmarshal(refundTxBytes, &refundTx); err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode refund transaction")
			return
		}

		utils.SuccessResponse(c, http.StatusOK, "Refund transaction prepared", gin.H{
			"refundTx": refundTx,
		})

	case models.OrderStatusDelivered:
		// After delivery — update to refund_requested for admin review
		if err := config.DB.Model(&order).Update("status", models.OrderStatusRefundRequested).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
			return
		}
		ch.EventBus.Publish(events.Event{
			Type:    events.OrderRefunded,
			UserID:  user.ID,
			Title:   "Refund Requested",
			Message: fmt.Sprintf("Your refund request for order %s is under admin review", order.ID),
			Data: map[string]interface{}{
				"orderId": order.ID.String(),
				"status":  models.OrderStatusRefundRequested,
			},
		})

		utils.SuccessResponse(c, http.StatusOK, "Refund requested for admin review", gin.H{
			"orderId": order.ID,
			"status":  models.OrderStatusRefundRequested,
		})

	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Refund can only be requested when order is escrowed or delivered")
	}
}

// CancelOrder handles POST /api/orders/:id/cancel.
// - Buyer may cancel directly while the order is in pending_payment (no on-chain escrow yet).
// - Seller may cancel in pending_payment or escrowed (escrowed cancels require an on-chain tx).
// - Once shipped/delivered, neither party may cancel; they must go through refund_requested.
func (ch *CheckoutHandler) CancelOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	isBuyer := order.BuyerID == user.ID
	isSeller := order.SellerID == user.ID
	if !isBuyer && !isSeller {
		utils.ErrorResponse(c, http.StatusForbidden, "You are not a party to this order")
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}

	if !isValidTransitionForKind(order.Status, models.OrderStatusCancelled, order.Kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot cancel: order is in %s status", order.Status))
		return
	}

	// Buyer-initiated cancel is only allowed before payment escrow.
	if isBuyer && !isSeller && order.Status != models.OrderStatusPendingPayment {
		utils.ErrorResponse(c, http.StatusForbidden, "Buyers can only cancel before payment. Please request a refund instead.")
		return
	}

	// Pending-payment cancels: mark cancelled AND restore the reserved stock
	// in a single transaction so cancelling an unpaid order doesn't burn inventory.
	if order.Status == models.OrderStatusPendingPayment {
		tx := config.DB.Begin()
		if tx.Error != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
			return
		}
		cancelled, err := services.CancelUnpaidOrder(tx, &order)
		if err != nil {
			tx.Rollback()
			log.Printf("CancelOrder error for order %s: %v", order.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to cancel order")
			return
		}
		if !cancelled {
			tx.Rollback()
			utils.ErrorResponse(c, http.StatusConflict, "This order is no longer awaiting payment")
			return
		}
		if err := tx.Commit().Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to commit transaction")
			return
		}

		role := "seller"
		if isBuyer {
			role = "buyer"
		}
		ch.EventBus.Publish(events.Event{
			Type:    events.OrderRefunded,
			UserID:  order.BuyerID,
			Title:   "Order Cancelled",
			Message: fmt.Sprintf("Order #%s was cancelled (by %s).", order.ID.String()[:8], role),
			Data: map[string]interface{}{
				"orderId":     order.ID.String(),
				"status":      models.OrderStatusCancelled,
				"cancelledBy": role,
			},
		})
		ch.EventBus.Publish(events.Event{
			Type:    events.OrderRefunded,
			UserID:  order.SellerID,
			Title:   "Order Cancelled",
			Message: fmt.Sprintf("Order #%s was cancelled (by %s).", order.ID.String()[:8], role),
			Data: map[string]interface{}{
				"orderId":     order.ID.String(),
				"status":      models.OrderStatusCancelled,
				"cancelledBy": role,
			},
		})

		utils.SuccessResponse(c, http.StatusOK, "Order cancelled", gin.H{
			"orderId": order.ID,
			"status":  models.OrderStatusCancelled,
		})
		return
	}

	if order.IsSpendableMCGP() {
		// The seller cancels a paid order: the hold goes back to the buyer.
		if _, err := ch.refundSpendableOrder(c.Request.Context(), &order,
			map[string]interface{}{"status": models.OrderStatusCancelled}, false, true); err != nil {
			respondSpendableErr(c, order.ID, err)
			return
		}
		for _, uid := range []uuid.UUID{order.BuyerID, order.SellerID} {
			ch.EventBus.Publish(events.Event{
				Type:    events.OrderRefunded,
				UserID:  uid,
				Title:   "Order Cancelled",
				Message: fmt.Sprintf("Order #%s was cancelled by the seller. The MCGP is returned to the buyer's Spendable balance.", order.ID.String()[:8]),
				Data:    map[string]interface{}{"orderId": order.ID.String(), "status": models.OrderStatusCancelled, "cancelledBy": "seller"},
			})
		}
		utils.SuccessResponse(c, http.StatusOK, "Order cancelled", gin.H{
			"orderId": order.ID,
			"status":  models.OrderStatusCancelled,
		})
		return
	}

	// Escrowed cancel requires on-chain tx, seller only.
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	contractOrderID := services.GenerateOrderID(order.ID)
	cancelTxBytes, err := ch.EscrowService.PrepareCancelOrder(contractOrderID, user.WalletAddress, ch.orderEscrowContract(&order))
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare cancel transaction")
		return
	}

	var cancelTx map[string]interface{}
	if err := json.Unmarshal(cancelTxBytes, &cancelTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode cancel transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Cancel transaction prepared", gin.H{
		"cancelTx": cancelTx,
	})
}

// GetUserOrders handles GET /api/orders
func (ch *CheckoutHandler) GetUserOrders(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	role := c.Query("role")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Order{})

	switch role {
	case "buyer":
		query = query.Where("buyer_id = ?", user.ID)
	case "seller":
		query = query.Where("seller_id = ?", user.ID)
	default:
		query = query.Where("buyer_id = ? OR seller_id = ?", user.ID, user.ID)
	}

	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var orders []models.Order
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch orders")
		return
	}

	enriched := enrichOrdersWithPreview(orders)

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	utils.SuccessResponse(c, http.StatusOK, "Orders retrieved", gin.H{
		"orders": enriched,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// orderPreview is an enriched order with summaries of related entities.
// Kept as a flat struct to preserve all model fields in JSON output.
type orderPreview struct {
	models.Order
	Buyer   *userPreview    `json:"buyer,omitempty"`
	Seller  *userPreview    `json:"seller,omitempty"`
	Product *productPreview `json:"product,omitempty"`
}

type userPreview struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
}

type productPreview struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	ImageURL string  `json:"imageUrl,omitempty"`
}

// enrichOrdersWithPreview loads related buyer/seller/product summaries in a single
// query each to avoid N+1 issues. Returns orders with nested previews or unchanged
// if the related entity can't be found.
func enrichOrdersWithPreview(orders []models.Order) []orderPreview {
	if len(orders) == 0 {
		return []orderPreview{}
	}

	buyerIDs := make(map[uuid.UUID]struct{})
	sellerIDs := make(map[uuid.UUID]struct{})
	productIDs := make(map[uuid.UUID]struct{})
	for _, o := range orders {
		buyerIDs[o.BuyerID] = struct{}{}
		sellerIDs[o.SellerID] = struct{}{}
		productIDs[o.ProductID] = struct{}{}
	}

	toSlice := func(m map[uuid.UUID]struct{}) []uuid.UUID {
		out := make([]uuid.UUID, 0, len(m))
		for id := range m {
			out = append(out, id)
		}
		return out
	}

	var users []models.User
	allUserIDs := append(toSlice(buyerIDs), toSlice(sellerIDs)...)
	if len(allUserIDs) > 0 {
		_ = config.DB.Where("id IN ?", allUserIDs).Find(&users).Error
	}
	userByID := make(map[uuid.UUID]*userPreview, len(users))
	for i := range users {
		u := &users[i]
		userByID[u.ID] = &userPreview{
			ID:       u.ID.String(),
			Name:     strings.TrimSpace(u.Name),
			Username: u.Username,
			Email:    u.Email,
		}
	}

	var products []models.Product
	if len(productIDs) > 0 {
		// Unscoped: an order's product may since have been deleted, and the order
		// still has to render its name, price and image. Keeping order history
		// readable is the whole reason deletion is soft.
		_ = config.DB.Unscoped().Where("id IN ?", toSlice(productIDs)).Find(&products).Error
	}
	productByID := make(map[uuid.UUID]*productPreview, len(products))
	for i := range products {
		p := &products[i]
		productByID[p.ID] = &productPreview{
			ID:       p.ID.String(),
			Name:     p.Name,
			Price:    p.Price,
			ImageURL: firstProductImageURL(p),
		}
	}

	out := make([]orderPreview, len(orders))
	for i, o := range orders {
		out[i] = orderPreview{
			Order:   o,
			Buyer:   userByID[o.BuyerID],
			Seller:  userByID[o.SellerID],
			Product: productByID[o.ProductID],
		}
	}
	return out
}

// firstProductImageURL extracts the first non-empty image URL from a product's images JSON.
func firstProductImageURL(p *models.Product) string {
	if len(p.Images) == 0 {
		return ""
	}
	var imgs []map[string]interface{}
	if err := json.Unmarshal(p.Images, &imgs); err != nil {
		return ""
	}
	for _, img := range imgs {
		if u, ok := img["url"].(string); ok && u != "" {
			return u
		}
	}
	return ""
}

// GetOrderDetail handles GET /api/orders/:id
func (ch *CheckoutHandler) GetOrderDetail(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	// Check access: buyer, seller, or admin
	if order.BuyerID != user.ID && order.SellerID != user.ID && !hasAdminPrivilege(c, user) {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied")
		return
	}

	enriched := enrichOrdersWithPreview([]models.Order{order})
	utils.SuccessResponse(c, http.StatusOK, "Order retrieved", enriched[0])
}

// GetShippingEstimate handles GET /api/orders/shipping-estimate
func (ch *CheckoutHandler) GetShippingEstimate(c *gin.Context) {
	productIDStr := c.Query("productId")
	buyerCity := c.Query("buyerCity")
	buyerState := c.Query("buyerState")
	buyerCountry := c.Query("buyerCountry")

	if productIDStr == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "productId is required")
		return
	}

	productID, err := uuid.Parse(productIDStr)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var product models.Product
	if err := config.DB.First(&product, "id = ?", productID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Get seller location
	var seller models.User
	if err := config.DB.First(&seller, "id = ?", product.UserID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Seller not found")
		return
	}

	origin := resolveShippingOrigin(&product, &seller)
	zone := DetectShippingZone(
		buyerCity,
		buyerState,
		buyerCountry,
		origin.City,
		origin.State,
		origin.Country,
	)
	shippingCost := GetShippingRate(&product, zone)

	utils.SuccessResponse(c, http.StatusOK, "Shipping estimate", gin.H{
		"zone":         zone,
		"shippingCost": shippingCost,
		"currency":     "USD",
	})
}

// AdminResolveDispute handles POST /api/admin/orders/:id/resolve.
// Accepts orders that are refund_requested, cancel_requested, or disputed.
// This call does NOT change any state — it only returns an unsigned resolve tx.
// The admin must sign & broadcast, then POST the resulting tx hash to
// /api/admin/orders/:id/submit-resolve so the backend can verify the on-chain
// confirmation and only THEN clear the dispute fields.
func (ch *CheckoutHandler) AdminResolveDispute(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req adminResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	allowedStatus := order.Status == models.OrderStatusRefundRequested ||
		order.Status == models.OrderStatusCancelRequested
	isDisputed := order.DisputedAt != nil
	if !allowedStatus && !isDisputed {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot resolve: order must be refund_requested, cancel_requested, or disputed (current: %s)", order.Status))
		return
	}

	if order.IsSpendableMCGP() {
		ch.adminResolveSpendable(c, user, &order, req.RefundBuyer)
		return
	}

	// Orders that predate on-chain escrow have nothing for the escrow contract
	// to resolve. Pending_payment disputes (if any sneak in) would fail here.
	if order.ContractOrderID == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Order has no on-chain escrow to resolve")
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Admin wallet address required")
		return
	}

	// Gate on the order's escrow contract isAdmin view. Source of truth for
	// "can this wallet call adminResolve" is the contract that holds the funds —
	// admin sets can differ across deployments after a contract migration.
	escrowAddr := ch.orderEscrowContract(&order)
	isAdmin, err := ch.EscrowService.IsAdmin(user.WalletAddress, escrowAddr)
	if err != nil {
		log.Printf("AdminResolveDispute IsAdmin check failed admin=%s contract=%s: %v", user.WalletAddress, escrowAddr, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not verify admin authorization")
		return
	}
	if !isAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, fmt.Sprintf(
			"Connected wallet (%s) is not registered as an escrow admin on %s",
			user.WalletAddress, escrowAddr,
		))
		return
	}

	contractOrderID := services.GenerateOrderID(order.ID)
	resolveTxBytes, err := ch.EscrowService.PrepareAdminResolve(contractOrderID, req.RefundBuyer, user.WalletAddress, escrowAddr)
	if err != nil {
		log.Printf("AdminResolveDispute PrepareAdminResolve failed order=%s admin=%s refundBuyer=%v: %v",
			order.ID, user.WalletAddress, req.RefundBuyer, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare resolve transaction")
		return
	}

	var resolveTx map[string]interface{}
	if err := json.Unmarshal(resolveTxBytes, &resolveTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode resolve transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Resolve transaction prepared", gin.H{
		"resolveTx":   resolveTx,
		"refundBuyer": req.RefundBuyer,
	})
}

// adminSubmitResolveBody is the request for SubmitAdminResolve.
type adminSubmitResolveBody struct {
	TxHash      string `json:"txHash" binding:"required"`
	RefundBuyer bool   `json:"refundBuyer"`
}

// SubmitAdminResolve handles POST /api/admin/orders/:id/submit-resolve.
// Admin sends the broadcast tx hash for the resolve tx prepared by AdminResolveDispute.
// Only after on-chain confirmation do we flip the status, clear dispute flags,
// and notify the parties. If the admin abandons signing, nothing here runs and
// the order stays frozen under dispute — which is the correct behavior.
func (ch *CheckoutHandler) SubmitAdminResolve(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body adminSubmitResolveBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash is required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if refuseSpendableOnChainStep(c, &order, "Resolve it with /resolve, which moves the funds directly.") {
		return
	}

	allowedStatus := order.Status == models.OrderStatusRefundRequested ||
		order.Status == models.OrderStatusCancelRequested
	isDisputed := order.DisputedAt != nil
	if !allowedStatus && !isDisputed {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot submit resolve: order is in %s status", order.Status))
		return
	}

	receipt := ch.BlockchainService.GetTransactionReceipt(body.TxHash, "sonic")
	switch receipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify resolve transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Resolve transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Resolve transaction failed on-chain")
		return
	}

	var finalStatus string
	if body.RefundBuyer {
		finalStatus = models.OrderStatusRefunded
	} else {
		finalStatus = models.OrderStatusCompleted
	}

	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return
	}
	orderUpdates := map[string]interface{}{
		"status":          finalStatus,
		"release_tx_hash": body.TxHash,
	}
	if isDisputed {
		orderUpdates["disputed_at"] = nil
		orderUpdates["dispute_reason"] = ""
		orderUpdates["dispute_raised_by"] = ""
	}
	if err := tx.Model(&order).Updates(orderUpdates).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}
	// Restore stock if the buyer was refunded (item never reached them or is being returned).
	// Services hold no inventory, so there is nothing to restore.
	if body.RefundBuyer {
		if err := services.RestoreOrderStock(tx, &order); err != nil {
			tx.Rollback()
			log.Printf("SubmitAdminResolve stock restore error for order %s: %v", order.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to restore stock")
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to commit transaction")
		return
	}

	ch.notifyOrderResolved(&order, finalStatus, body.RefundBuyer, body.TxHash)

	// Ensure admin action is attributed (currently unused but useful for audit logs).
	_ = user

	utils.SuccessResponse(c, http.StatusOK, "Dispute resolved", gin.H{
		"orderId":     order.ID,
		"status":      finalStatus,
		"refundBuyer": body.RefundBuyer,
	})
}

// adminDismissBody is the request for DismissRequest. `notes` is optional.
type adminDismissBody struct {
	Notes string `json:"notes"`
}

// DismissRequest handles POST /api/admin/orders/:id/dismiss-request.
// Admin-only. Reverts an off-chain refund/cancel request (and/or clears a
// raised dispute) without touching the escrow contract — use when the request
// was raised in error (e.g., buyer changed their mind after receiving the
// product) and both parties are OK proceeding with the normal flow.
//
// Reversion rules (pure DB transitions, no on-chain tx):
//
//	refund_requested  →  delivered (if sellerDeliveredAt set) or shipped
//	                     (if sellerShippedAt set) or escrowed
//	cancel_requested  →  escrowed
//	disputed only     →  status unchanged; dispute fields cleared
//
// Does NOT touch stock. Does NOT fire release_tx_hash. After dismissal, the
// normal flow resumes (buyer can confirm receipt, seller can ship, etc.).
func (ch *CheckoutHandler) DismissRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body adminDismissBody
	_ = c.ShouldBindJSON(&body)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	isDisputed := order.DisputedAt != nil
	hasRefundRequest := order.Status == models.OrderStatusRefundRequested
	hasCancelRequest := order.Status == models.OrderStatusCancelRequested
	if !isDisputed && !hasRefundRequest && !hasCancelRequest {
		utils.ErrorResponse(c, http.StatusBadRequest, "Nothing to dismiss: order has no active request or dispute")
		return
	}

	// Determine reverted status for active requests.
	var revertedStatus string
	switch {
	case hasRefundRequest:
		switch {
		case order.SellerDeliveredAt != nil:
			revertedStatus = models.OrderStatusDelivered
		case order.SellerShippedAt != nil:
			revertedStatus = models.OrderStatusShipped
		default:
			revertedStatus = models.OrderStatusEscrowed
		}
	case hasCancelRequest:
		revertedStatus = models.OrderStatusEscrowed
	default:
		// Dispute-only: leave status where it is.
		revertedStatus = order.Status
	}

	updates := map[string]interface{}{}
	if revertedStatus != order.Status {
		updates["status"] = revertedStatus
	}
	if hasRefundRequest {
		updates["merchant_approved_refund"] = false
	}
	if hasCancelRequest {
		updates["merchant_approved_cancel"] = false
		updates["cancel_reason"] = ""
		updates["cancel_requested_at"] = nil
	}
	if isDisputed {
		updates["disputed_at"] = nil
		updates["dispute_reason"] = ""
		updates["dispute_raised_by"] = ""
	}
	if notes := strings.TrimSpace(body.Notes); notes != "" {
		updates["notes"] = notes
	}

	if len(updates) > 0 {
		if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
			log.Printf("DismissRequest update error for order %s: %v", order.ID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to dismiss request")
			return
		}
	}

	shortID := order.ID.String()[:8]
	var summary string
	switch {
	case hasRefundRequest:
		summary = fmt.Sprintf("Admin dismissed the refund request on order #%s. You can proceed with the normal flow.", shortID)
	case hasCancelRequest:
		summary = fmt.Sprintf("Admin dismissed the cancel request on order #%s. The order is active again.", shortID)
	default:
		summary = fmt.Sprintf("Admin cleared the dispute on order #%s. You can proceed with the normal flow.", shortID)
	}

	// Notify both parties so whoever raised the request/dispute knows it was dismissed.
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderDisputeResolved,
		UserID:  order.BuyerID,
		Title:   "Request Dismissed",
		Message: summary,
		Data: map[string]interface{}{
			"orderId":     order.ID.String(),
			"status":      revertedStatus,
			"dismissedBy": "admin",
		},
	})
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderDisputeResolved,
		UserID:  order.SellerID,
		Title:   "Request Dismissed",
		Message: summary,
		Data: map[string]interface{}{
			"orderId":     order.ID.String(),
			"status":      revertedStatus,
			"dismissedBy": "admin",
		},
	})

	_ = user

	utils.SuccessResponse(c, http.StatusOK, "Request dismissed", gin.H{
		"orderId": order.ID,
		"status":  revertedStatus,
	})
}

// --- Cancel-request workflow ---

type requestCancelBody struct {
	Reason string `json:"reason"`
}

type rejectCancelBody struct {
	Notes string `json:"notes"`
}

type raiseDisputeBody struct {
	Reason string `json:"reason" binding:"required"`
}

type submitCancelBody struct {
	TxHash string `json:"txHash" binding:"required"`
}

// RequestCancelOrder handles POST /api/orders/:id/request-cancel.
// Buyer requests cancellation of an escrowed order.
func (ch *CheckoutHandler) RequestCancelOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body requestCancelBody
	_ = c.ShouldBindJSON(&body)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can request a cancel")
		return
	}
	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}
	if order.Status != models.OrderStatusEscrowed {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cancel can only be requested while the order is escrowed. Use request-refund once shipped.")
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":              models.OrderStatusCancelRequested,
		"cancel_reason":       strings.TrimSpace(body.Reason),
		"cancel_requested_at": now,
	}
	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	shortID := order.ID.String()[:8]
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCancelRequested,
		UserID:  order.BuyerID,
		Title:   "Cancel Request Submitted",
		Message: fmt.Sprintf("Your cancel request for order #%s has been submitted. The seller will review it shortly.", shortID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusCancelRequested,
			"reason":  body.Reason,
		},
	})
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCancelRequested,
		UserID:  order.SellerID,
		Title:   "Buyer Wants to Cancel",
		Message: fmt.Sprintf("The buyer requested to cancel order #%s. Reason: %s. Please approve or reject.", shortID, body.Reason),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusCancelRequested,
			"reason":  body.Reason,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Cancel request submitted", gin.H{
		"orderId":           order.ID,
		"status":            models.OrderStatusCancelRequested,
		"cancelRequestedAt": now,
	})
}

// ApproveCancelOrder handles POST /api/orders/:id/approve-cancel.
// Seller approves the buyer's cancel request. Returns an unsigned on-chain cancel tx.
// Seller must sign/broadcast and then call SubmitCancelOrder with the tx hash.
func (ch *CheckoutHandler) ApproveCancelOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can approve a cancel request")
		return
	}
	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}
	if order.Status != models.OrderStatusCancelRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot approve cancel: order is in %s status", order.Status))
		return
	}
	if order.IsSpendableMCGP() {
		// No cancel tx to sign: approving returns the hold to the buyer now.
		if _, err := ch.refundSpendableOrder(c.Request.Context(), &order, map[string]interface{}{
			"status":                   models.OrderStatusCancelled,
			"merchant_approved_cancel": true,
		}, false, true); err != nil {
			respondSpendableErr(c, order.ID, err)
			return
		}
		ch.publishCancelApproved(&order, "")
		utils.SuccessResponse(c, http.StatusOK, "Order cancelled", gin.H{
			"orderId":   order.ID,
			"status":    models.OrderStatusCancelled,
			"cancelled": true,
		})
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	contractOrderID := services.GenerateOrderID(order.ID)
	cancelTxBytes, err := ch.EscrowService.PrepareCancelOrder(contractOrderID, user.WalletAddress, ch.orderEscrowContract(&order))
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare cancel transaction")
		return
	}

	var cancelTx map[string]interface{}
	if err := json.Unmarshal(cancelTxBytes, &cancelTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode cancel transaction")
		return
	}

	if err := config.DB.Model(&order).Update("merchant_approved_cancel", true).Error; err != nil {
		log.Printf("ApproveCancelOrder flag update error: %v", err)
	}

	utils.SuccessResponse(c, http.StatusOK, "Cancel transaction prepared", gin.H{
		"cancelTx": cancelTx,
		"orderId":  order.ID,
	})
}

// SubmitCancelOrder handles POST /api/orders/:id/submit-cancel.
// Seller submits the broadcast tx hash for the approved cancel; status → cancelled.
func (ch *CheckoutHandler) SubmitCancelOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body submitCancelBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash is required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can submit the cancel tx")
		return
	}
	if refuseSpendableOnChainStep(c, &order, "Approving the cancel request refunds it.") {
		return
	}
	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}
	if order.Status != models.OrderStatusCancelRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot submit cancel: order is in %s status", order.Status))
		return
	}

	receipt := ch.BlockchainService.GetTransactionReceipt(body.TxHash, "sonic")
	switch receipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify cancel transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Cancel transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Cancel transaction failed on-chain")
		return
	}

	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return
	}
	if err := tx.Model(&order).Updates(map[string]interface{}{
		"status":          models.OrderStatusCancelled,
		"release_tx_hash": body.TxHash,
	}).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}
	if err := services.RestoreOrderStock(tx, &order); err != nil {
		tx.Rollback()
		log.Printf("SubmitCancelOrder stock restore error for order %s: %v", order.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to restore stock")
		return
	}
	if err := tx.Commit().Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to commit transaction")
		return
	}

	ch.publishCancelApproved(&order, body.TxHash)

	utils.SuccessResponse(c, http.StatusOK, "Order cancelled", gin.H{
		"orderId": order.ID,
		"status":  models.OrderStatusCancelled,
	})
}

// publishCancelApproved tells both parties an approved cancel returned the
// buyer's funds. txHash is empty for a Spendable MCGP order.
func (ch *CheckoutHandler) publishCancelApproved(order *models.Order, txHash string) {
	shortID := order.ID.String()[:8]
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCancelApproved,
		UserID:  order.BuyerID,
		Title:   "Cancel Approved",
		Message: fmt.Sprintf("Your cancel request for order #%s was approved. Funds have been returned.", shortID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusCancelled,
			"txHash":  txHash,
		},
	})
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCancelApproved,
		UserID:  order.SellerID,
		Title:   "Order Cancelled",
		Message: fmt.Sprintf("You approved the cancellation of order #%s. Funds returned to buyer.", shortID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusCancelled,
		},
	})
}

// RejectCancelOrder handles POST /api/orders/:id/reject-cancel.
// Seller rejects the buyer's cancel request; order returns to escrowed.
func (ch *CheckoutHandler) RejectCancelOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body rejectCancelBody
	_ = c.ShouldBindJSON(&body)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can reject a cancel request")
		return
	}
	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}
	if order.Status != models.OrderStatusCancelRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot reject cancel: order is in %s status", order.Status))
		return
	}

	updates := map[string]interface{}{
		"status":                   models.OrderStatusEscrowed,
		"merchant_approved_cancel": false,
	}
	if notes := strings.TrimSpace(body.Notes); notes != "" {
		updates["notes"] = notes
	}
	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	shortID := order.ID.String()[:8]
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCancelRejected,
		UserID:  order.BuyerID,
		Title:   "Cancel Request Rejected",
		Message: fmt.Sprintf("The seller rejected your cancel request for order #%s. The order remains active. You can raise a dispute if you disagree.", shortID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusEscrowed,
			"notes":   body.Notes,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Cancel request rejected", gin.H{
		"orderId": order.ID,
		"status":  models.OrderStatusEscrowed,
	})
}

// RaiseDispute handles POST /api/orders/:id/dispute.
// Either buyer or seller can escalate to admin. Dispute freezes mutation endpoints.
func (ch *CheckoutHandler) RaiseDispute(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body raiseDisputeBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "A reason is required")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) < 10 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Please describe the issue in at least 10 characters")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	var role string
	switch user.ID {
	case order.BuyerID:
		role = "buyer"
	case order.SellerID:
		role = "seller"
	default:
		utils.ErrorResponse(c, http.StatusForbidden, "You are not a party to this order")
		return
	}

	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is already under dispute")
		return
	}

	switch order.Status {
	case models.OrderStatusCompleted, models.OrderStatusRefunded, models.OrderStatusCancelled:
		utils.ErrorResponse(c, http.StatusBadRequest, "Disputes cannot be raised on closed orders")
		return
	case models.OrderStatusPendingPayment:
		// No on-chain escrow exists yet for AdminResolveDispute to act on.
		// The buyer (or seller) can cancel directly while pending_payment.
		utils.ErrorResponse(c, http.StatusBadRequest, "Dispute can only be raised after payment is escrowed. Cancel the order directly if payment hasn't been made yet.")
		return
	}

	now := time.Now()
	if err := config.DB.Model(&order).Updates(map[string]interface{}{
		"disputed_at":       now,
		"dispute_reason":    reason,
		"dispute_raised_by": role,
	}).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to raise dispute")
		return
	}

	shortID := order.ID.String()[:8]
	var otherParty uuid.UUID
	if role == "buyer" {
		otherParty = order.SellerID
	} else {
		otherParty = order.BuyerID
	}
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderDisputed,
		UserID:  otherParty,
		Title:   "Order Escalated to Admin",
		Message: fmt.Sprintf("Order #%s has been escalated to admin by the %s. Reason: %s. An admin will review shortly.", shortID, role, reason),
		Data: map[string]interface{}{
			"orderId":  order.ID.String(),
			"raisedBy": role,
			"reason":   reason,
		},
	})
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderDisputed,
		UserID:  user.ID,
		Title:   "Dispute Submitted",
		Message: fmt.Sprintf("Your dispute for order #%s has been submitted. An admin will review it shortly.", shortID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"reason":  reason,
		},
	})

	// Notify all admins / super-admins
	var admins []models.User
	if err := config.DB.Where("role IN (?)", []string{models.RoleAdmin, models.RoleSuperAdmin}).Find(&admins).Error; err == nil {
		for _, a := range admins {
			ch.EventBus.Publish(events.Event{
				Type:    events.OrderDisputed,
				UserID:  a.ID,
				Title:   "New Dispute Requires Review",
				Message: fmt.Sprintf("Order #%s has a new dispute raised by the %s: %s", shortID, role, reason),
				Data: map[string]interface{}{
					"orderId":  order.ID.String(),
					"raisedBy": role,
					"reason":   reason,
				},
			})
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Dispute raised", gin.H{
		"orderId":    order.ID,
		"disputedAt": now,
	})
}

// GetDisputedOrders handles GET /api/admin/orders/disputed.
func (ch *CheckoutHandler) GetDisputedOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Order{}).Where("disputed_at IS NOT NULL")

	var total int64
	query.Count(&total)

	var orders []models.Order
	if err := query.Order("disputed_at ASC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch disputed orders")
		return
	}

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	utils.SuccessResponse(c, http.StatusOK, "Disputed orders fetched", gin.H{
		"orders": orders,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// GetAllOrders handles GET /api/admin/orders — admin paginated order list.
func (ch *CheckoutHandler) GetAllOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	search := c.Query("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Order{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if search != "" {
		query = query.Where("id::text ILIKE ?", "%"+search+"%")
	}

	var total int64
	query.Count(&total)

	var orders []models.Order
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch orders")
		return
	}

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	utils.SuccessResponse(c, http.StatusOK, "Orders fetched successfully", gin.H{
		"orders": orders,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}
