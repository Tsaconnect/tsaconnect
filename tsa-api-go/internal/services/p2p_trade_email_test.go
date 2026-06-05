package services

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestHumanizeTokenAmount(t *testing.T) {
	cases := []struct {
		base     string
		decimals int
		want     string
	}{
		{"25000000000000000000", 18, "25"},   // 25 USDT on BSC (18 dec)
		{"25000000", 6, "25"},                // 25 on Sonic (6 dec)
		{"1500000", 6, "1.5"},                // trims trailing zeros
		{"1234560000000000000", 18, "1.23456"},
		{"0", 18, "0"},
		{"100", 0, "100"},
		{"notanumber", 6, "notanumber"}, // unparseable → raw
	}
	for _, c := range cases {
		if got := humanizeTokenAmount(c.base, c.decimals); got != c.want {
			t.Errorf("humanizeTokenAmount(%q,%d) = %q, want %q", c.base, c.decimals, got, c.want)
		}
	}
}

func mkTrade(buyer, seller uuid.UUID) *models.P2PTrade {
	return &models.P2PTrade{
		BuyerID:      buyer,
		SellerID:     seller,
		Token:        "USDT",
		ChainID:      97, // BSC testnet → 18 decimals
		TokenAmount:  "25000000000000000000",
		FiatAmount:   "34647.37",
		FiatCurrency: "NGN",
	}
}

func TestBuildP2PTradeEmail_MilestonesAndRoles(t *testing.T) {
	buyer := uuid.New()
	seller := uuid.New()
	trade := mkTrade(buyer, seller)

	milestones := []string{
		events.P2PTradeOpened, events.P2PTradePaid, events.P2PTradeReleased,
		events.P2PTradeCancelled, events.P2PTradeDisputed, events.P2PTradeResolved,
		events.P2PTradeExpired,
	}
	for _, evt := range milestones {
		for _, party := range []uuid.UUID{buyer, seller} {
			c, ok := buildP2PTradeEmail(events.Event{Type: evt, UserID: party}, trade, "https://app.example.com")
			if !ok {
				t.Fatalf("%s for party: expected an email, got send=false", evt)
			}
			if c.Subject == "" || c.Body == "" {
				t.Errorf("%s: empty subject/body", evt)
			}
			// Humanized amount + fiat must appear (not raw base units).
			if !strings.Contains(c.Body, "25 USDT") {
				t.Errorf("%s: body missing humanized amount: %q", evt, c.Body)
			}
			if !strings.Contains(c.Body, "34647.37 NGN") {
				t.Errorf("%s: body missing fiat: %q", evt, c.Body)
			}
			if strings.Contains(c.Body, "25000000000000000000") {
				t.Errorf("%s: body leaked raw base units: %q", evt, c.Body)
			}
			// CTA present + points at the trade when a link base is configured.
			if c.CTAText != "View trade" || !strings.Contains(c.CTAURL, "/p2p/trade/"+trade.ID.String()) {
				t.Errorf("%s: bad CTA %q / %q", evt, c.CTAText, c.CTAURL)
			}
		}
	}
}

func TestBuildP2PTradeEmail_RoleTailoring(t *testing.T) {
	buyer := uuid.New()
	seller := uuid.New()
	trade := mkTrade(buyer, seller)

	// On 'paid', the SELLER is the one who must act (confirm + release).
	sellerPaid, _ := buildP2PTradeEmail(events.Event{Type: events.P2PTradePaid, UserID: seller}, trade, "")
	if !strings.Contains(strings.ToLower(sellerPaid.Subject), "confirm") {
		t.Errorf("seller 'paid' subject should prompt confirm/release, got %q", sellerPaid.Subject)
	}
	buyerPaid, _ := buildP2PTradeEmail(events.Event{Type: events.P2PTradePaid, UserID: buyer}, trade, "")
	if buyerPaid.Subject == sellerPaid.Subject {
		t.Errorf("buyer and seller 'paid' emails should differ")
	}

	// On 'opened', the BUYER is told to pay within 30 minutes.
	buyerOpened, _ := buildP2PTradeEmail(events.Event{Type: events.P2PTradeOpened, UserID: buyer}, trade, "")
	if !strings.Contains(buyerOpened.Body, "30 minutes") {
		t.Errorf("buyer 'opened' body should mention the 30-minute window, got %q", buyerOpened.Body)
	}
}

func TestBuildP2PTradeEmail_NoCTAWithoutLinkBase(t *testing.T) {
	buyer := uuid.New()
	seller := uuid.New()
	c, _ := buildP2PTradeEmail(events.Event{Type: events.P2PTradeReleased, UserID: buyer}, mkTrade(buyer, seller), "")
	if c.CTAText != "" || c.CTAURL != "" {
		t.Errorf("expected no CTA when link base empty, got %q / %q", c.CTAText, c.CTAURL)
	}
}

func TestBuildP2PTradeEmail_SkipsNonMilestonesAndNonParties(t *testing.T) {
	buyer := uuid.New()
	seller := uuid.New()
	trade := mkTrade(buyer, seller)

	// Transient 'releasing' and chat are NOT email milestones.
	if _, ok := buildP2PTradeEmail(events.Event{Type: events.P2PTradeReleasing, UserID: buyer}, trade, ""); ok {
		t.Error("p2p.trade.releasing should not send email")
	}
	if _, ok := buildP2PTradeEmail(events.Event{Type: "p2p.message.new", UserID: buyer}, trade, ""); ok {
		t.Error("chat messages should not send email")
	}
	// A user who is neither buyer nor seller gets nothing.
	if _, ok := buildP2PTradeEmail(events.Event{Type: events.P2PTradeReleased, UserID: uuid.New()}, trade, ""); ok {
		t.Error("non-party should not receive a trade email")
	}
	// Nil trade is safe.
	if _, ok := buildP2PTradeEmail(events.Event{Type: events.P2PTradeReleased, UserID: buyer}, nil, ""); ok {
		t.Error("nil trade should not send email")
	}
}

func TestBuildP2PTradeEmail_DisputePrepareSkippedButConfirmEmails(t *testing.T) {
	buyer := uuid.New()
	seller := uuid.New()
	trade := mkTrade(buyer, seller)

	// Prepare-time dispute emit ("sign to submit") carries a "role" marker and
	// must NOT email — the dispute isn't on-chain yet.
	prepare := events.Event{Type: events.P2PTradeDisputed, UserID: buyer, Data: map[string]any{"tradeId": trade.ID, "role": "buyer"}}
	if _, ok := buildP2PTradeEmail(prepare, trade, ""); ok {
		t.Error("dispute prepare emit (has role marker) should not email")
	}

	// On-chain confirm emit (no role key) emails.
	confirm := events.Event{Type: events.P2PTradeDisputed, UserID: seller, Data: map[string]any{"tradeId": trade.ID}}
	if _, ok := buildP2PTradeEmail(confirm, trade, ""); !ok {
		t.Error("confirmed dispute emit should email")
	}

	// Expired ALSO carries a "role" key (watchdog) but is a different event —
	// the prepare-skip must not swallow it.
	expired := events.Event{Type: events.P2PTradeExpired, UserID: buyer, Data: map[string]any{"tradeId": trade.ID, "role": "buyer"}}
	if _, ok := buildP2PTradeEmail(expired, trade, ""); !ok {
		t.Error("expired emit should email even though it carries a role key")
	}
}
