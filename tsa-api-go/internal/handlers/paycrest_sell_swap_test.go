package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// paycrest_sell_swap_test.go — Task 12: GET /api/paycrest/sell-swap-quote.
//
// The route itself is thin, and the two things it must not get wrong are
// both about money going to the right place: the USDC receiver is the wallet
// reconciled against the account (never a free query value), and a route we
// refuse to price reads as "not right now", not as a broken server.

// stubSellQuoter implements the handler's sellQuoter port. The real quoter
// calls LiFi and Paycrest; nothing here should reach either.
type stubSellQuoter struct {
	last services.SellQuoteInput
	res  *services.SellSwapQuote
	err  error
}

func (s *stubSellQuoter) Quote(_ context.Context, in services.SellQuoteInput) (*services.SellSwapQuote, error) {
	s.last = in
	if s.err != nil {
		return nil, s.err
	}
	return s.res, nil
}

const sellSwapUserWallet = "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"
const sellSwapOtherWallet = "0x000000000000000000000000000000000000dEaD"

// doSellSwapQuote calls the handler with a signed-in user in context, the way
// the auth middleware leaves it — the same shape the other paycrest handler
// tests use instead of mounting a router.
func doSellSwapQuote(t *testing.T, h *PaycrestHandler, user *models.User, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/paycrest/sell-swap-quote?"+rawQuery, nil)
	c.Set("user", user)
	h.SellSwapQuote(c)
	return w
}

func TestSellSwapQuote_UsesTheReconciledWalletAndMapsTooCostly(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)
	// Another active account already holds the address the next request will
	// claim, which is what makes callerWallet refuse it.
	insertTestUser(t, db, &models.User{ID: uuid.New(), Username: "someone_else",
		WalletAddress: sellSwapOtherWallet, AccountStatus: models.AccountStatusActive})

	quoter := &stubSellQuoter{err: services.ErrSellRouteTooCostly}
	h := NewPaycrestHandler(nil, nil, nil).WithSellQuoter(quoter)

	q := "fromChainId=56&fromToken=0x0000000000000000000000000000000000000000&fromAmount=1&fromAddress=" +
		sellSwapUserWallet + "&walletAddress=" + sellSwapUserWallet
	w := doSellSwapQuote(t, h, user, q)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
	if quoter.last.WalletAddress != sellSwapUserWallet {
		t.Fatalf("receiver = %q, want the reconciled wallet", quoter.last.WalletAddress)
	}

	q = "fromChainId=56&fromToken=0x0000000000000000000000000000000000000000&fromAmount=1&fromAddress=" +
		sellSwapUserWallet + "&walletAddress=" + sellSwapOtherWallet
	w = doSellSwapQuote(t, h, user, q)
	if w.Code != http.StatusConflict {
		t.Fatalf("a mismatched walletAddress must be refused by callerWallet (409), got %d", w.Code)
	}
}

// An unpriceable sell is an outage, not a user error: 503 so the app can say
// "try again" rather than send her looking for a different token.
func TestSellSwapQuote_UnavailableIs503(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)

	h := NewPaycrestHandler(nil, nil, nil).WithSellQuoter(&stubSellQuoter{err: services.ErrSellUnavailable})
	w := doSellSwapQuote(t, h, user,
		"fromChainId=56&fromToken=0x0000000000000000000000000000000000000000&fromAmount=1&fromAddress="+sellSwapUserWallet)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

// With no quoter wired (RAMP_CONVERSION_SELL off) the route is not registered
// at all; the handler still has to fail closed if it is ever reached.
func TestSellSwapQuote_DormantWithoutAQuoter(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)

	h := NewPaycrestHandler(nil, nil, nil)
	if h.SellQuoteEnabled() {
		t.Fatal("sell quoting must be off until a quoter is wired")
	}
	w := doSellSwapQuote(t, h, user,
		"fromChainId=56&fromToken=0x0000000000000000000000000000000000000000&fromAmount=1&fromAddress="+sellSwapUserWallet)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// Required parameters are named, not defaulted: a missing amount priced as
// zero would quote a sell of nothing.
func TestSellSwapQuote_RequiresTheSourceFields(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)

	quoter := &stubSellQuoter{res: &services.SellSwapQuote{}}
	h := NewPaycrestHandler(nil, nil, nil).WithSellQuoter(quoter)
	for _, q := range []string{
		"fromToken=0x0000000000000000000000000000000000000000&fromAmount=1&fromAddress=" + sellSwapUserWallet,
		"fromChainId=56&fromAmount=1&fromAddress=" + sellSwapUserWallet,
		"fromChainId=56&fromToken=0x0000000000000000000000000000000000000000&fromAddress=" + sellSwapUserWallet,
		"fromChainId=56&fromToken=0x0000000000000000000000000000000000000000&fromAmount=1",
	} {
		if w := doSellSwapQuote(t, h, user, q); w.Code != http.StatusBadRequest {
			t.Fatalf("%q: status = %d, want 400", q, w.Code)
		}
	}
}
