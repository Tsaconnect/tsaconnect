package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// FeeQuoteHandler serves GET /api/it/quote — the itemized deposit/withdrawal
// fee breakdown (PM fee spec, batch 11) the app shows before the user
// confirms. Quote-only: it never moves money.
//
// Always registered (unlike the other /api/it routes): the calculator prices
// the provider fee from the config fallback when Flutterwave is dormant, so
// the endpoint has no hard dependency on any credential.
type FeeQuoteHandler struct {
	svc *services.FeeQuoteService
	// fx supplies NGN/USD for converting the flat USD platform fee on
	// stablecoin flows quoted in NGN. Optional — without it those specific
	// quotes 503 (mirrors GET /api/it/rate when the FX feed is down); MCGP
	// and USD quotes are unaffected.
	fx services.FxProvider
}

func NewFeeQuoteHandler(svc *services.FeeQuoteService, fx services.FxProvider) *FeeQuoteHandler {
	return &FeeQuoteHandler{svc: svc, fx: fx}
}

// GetQuote handles GET /api/it/quote?flow=&asset=&chain=&amount=&currency=.
//
//	flow      deposit | withdraw                         (required)
//	asset     MCGP | USDC | USDT                         (required)
//	chain     wallet chain key, e.g. "polygon"           (required for
//	          stablecoin withdrawals — prices the 3% vs 5% swap split)
//	amount    positive decimal in currency, max 2dp      (required)
//	currency  NGN | USD                                  (default NGN)
func (h *FeeQuoteHandler) GetQuote(c *gin.Context) {
	asset := services.FeeAsset(strings.ToUpper(strings.TrimSpace(c.Query("asset"))))
	in := services.FeeQuoteInput{
		Flow:     services.FeeFlow(c.Query("flow")),
		Asset:    asset,
		Chain:    c.Query("chain"),
		Amount:   c.Query("amount"),
		Currency: strings.ToUpper(strings.TrimSpace(c.DefaultQuery("currency", "NGN"))),
	}

	// The flat platform fee is specified in USD; NGN quotes on stablecoin
	// flows need the live NGN/USD rate to fold it into the total. Resolved
	// here (not in the calculator) so the service stays pure.
	var fxMeta gin.H
	if asset.IsStable() && in.Currency != "USD" {
		if h.fx == nil {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee pricing is temporarily unavailable")
			return
		}
		ngnPerUsd, stale, err := h.fx.NgnPerUsd(c.Request.Context())
		if err != nil {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee pricing is temporarily unavailable")
			return
		}
		minorPerUSD, err := services.MinorPerUSDFromRate(ngnPerUsd)
		if err != nil {
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee pricing is temporarily unavailable")
			return
		}
		in.FxMinorPerUSD = minorPerUSD
		// Quote-only display tolerates a stale rate but flags it, mirroring
		// GET /api/it/rate; transactional paths must re-check freshness.
		fxMeta = gin.H{"ngnPerUsd": ngnPerUsd, "stale": stale}
	}

	quote, err := h.svc.Quote(c.Request.Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrFeeQuoteBadFlow),
			errors.Is(err, services.ErrFeeQuoteBadAsset),
			errors.Is(err, services.ErrFeeQuoteBadAmount),
			errors.Is(err, services.ErrFeeQuoteBadCurrency),
			errors.Is(err, services.ErrFeeQuoteNeedChain):
			utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		case errors.Is(err, services.ErrFeeQuoteNeedFx):
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee pricing is temporarily unavailable")
		default:
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to compute fee quote")
		}
		return
	}

	data := gin.H{"quote": quote}
	if fxMeta != nil {
		data["fx"] = fxMeta
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee quote", data)
}
