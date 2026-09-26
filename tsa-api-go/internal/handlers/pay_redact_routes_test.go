package handlers

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// The merchant's bank must not reach a paying customer on ANY route that
// returns her attempt — the pay routes, and the ordinary sell routes the app
// uses to resume one. An ordinary sale's own records keep her real details.

const (
	ordinaryAccount = "5550001111"
	ordinaryName    = "ADA OKAFOR"
	ordinaryBank    = "044"
)

// assertMerchantBankHidden fails if any of the merchant's details is in body.
func assertMerchantBankHidden(t *testing.T, where, body string) {
	t.Helper()
	for _, secret := range []string{merchantAccount, merchantAccountName} {
		if strings.Contains(body, secret) {
			t.Fatalf("%s: %q reached a paying customer: %s", where, secret, body)
		}
	}
	if strings.Contains(body, `"payoutBankCode":"`+merchantBankCode) || strings.Contains(body, `"bankCode":"`+merchantBankCode) {
		t.Fatalf("%s: the merchant's bank code reached a paying customer: %s", where, body)
	}
}

func assertOrdinaryBankKept(t *testing.T, where, body string) {
	t.Helper()
	for _, want := range []string{ordinaryAccount, ordinaryName} {
		if !strings.Contains(body, want) {
			t.Fatalf("%s: an ordinary sale lost %q: %s", where, want, body)
		}
	}
}

func seedRouteOrder(t *testing.T, db *gorm.DB, user *models.User, payment *uuid.UUID, status string) *models.PaycrestOrderRecord {
	t.Helper()
	o := &models.PaycrestOrderRecord{UserID: user.ID, Direction: models.PaycrestDirectionSell, Status: status,
		Reference: "PCS-" + uuid.NewString()[:8], FiatCode: "NGN", FiatAmount: "50000.00", PayPaymentID: payment,
		PayoutBankCode: ordinaryBank, PayoutAccount: ordinaryAccount, PayoutAccountName: ordinaryName, DepositAddress: "0xdep"}
	if payment != nil {
		o.PayoutBankCode, o.PayoutAccount, o.PayoutAccountName = merchantBankCode, merchantAccount, merchantAccountName
	}
	if err := db.Create(o).Error; err != nil {
		t.Fatal(err)
	}
	return o
}

func callOrderRoute(t *testing.T, handle gin.HandlerFunc, user *models.User, id uuid.UUID, method string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/api/paycrest/orders", nil)
	if id != uuid.Nil {
		c.Params = gin.Params{{Key: "id", Value: id.String()}}
	}
	c.Set("user", user)
	handle(c)
	return w
}

func TestPaycrestOrderRoutes_MaskAPayPaymentsOrderAndLeaveOrdinaryOrdersAlone(t *testing.T) {
	h, db, _ := newPaycrestGateHandler(t)
	user := seedHandlerUserAt(t, db, models.VerificationStatusVerified)
	payment := uuid.New()
	for name, tc := range map[string]struct {
		handle gin.HandlerFunc
		method string
	}{
		"get":    {h.GetOrder, http.MethodGet},
		"claim":  {h.ClaimPayment, http.MethodPost},
		"cancel": {h.Cancel, http.MethodPost},
	} {
		t.Run(name, func(t *testing.T) {
			pay := seedRouteOrder(t, db, user, &payment, models.PaycrestStatusAwaitingDeposit)
			w := callOrderRoute(t, tc.handle, user, pay.ID, tc.method)
			if w.Code != http.StatusOK {
				t.Fatalf("pay order: %d %s", w.Code, w.Body.String())
			}
			assertMerchantBankHidden(t, name+" (pay order)", w.Body.String())

			plain := seedRouteOrder(t, db, user, nil, models.PaycrestStatusAwaitingDeposit)
			w = callOrderRoute(t, tc.handle, user, plain.ID, tc.method)
			if w.Code != http.StatusOK {
				t.Fatalf("ordinary order: %d %s", w.Code, w.Body.String())
			}
			assertOrdinaryBankKept(t, name+" (ordinary order)", w.Body.String())
		})
	}
}

func TestPaycrestOrderRoutes_TheListLeavesPayPaymentsOrdersOut(t *testing.T) {
	h, db, _ := newPaycrestGateHandler(t)
	user := seedHandlerUserAt(t, db, models.VerificationStatusVerified)
	payment := uuid.New()
	pay := seedRouteOrder(t, db, user, &payment, models.PaycrestStatusRefunded)
	plain := seedRouteOrder(t, db, user, nil, models.PaycrestStatusSettled)
	w := callOrderRoute(t, h.ListOrders, user, uuid.Nil, http.MethodGet)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	// A payment is listed as a payment (GET /pay/history), never as her sale.
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Data) != 1 {
		t.Fatalf("err %v body %s", err, w.Body.String())
	}
	raw, _ := json.Marshal(body.Data[0])
	if body.Data[0]["id"] != plain.ID.String() || body.Data[0]["id"] == pay.ID.String() {
		t.Fatalf("unexpected row %s", raw)
	}
	assertOrdinaryBankKept(t, "list (ordinary order)", string(raw))
}

func TestSellIntentRoutes_MaskAPayIntentAndOrderAndLeaveOrdinarySalesAlone(t *testing.T) {
	payment := uuid.New()
	payIntent := &models.RampSellIntent{ID: uuid.New(), PayPaymentID: &payment, Status: models.SellIntentSwapping,
		BankCode: merchantBankCode, AccountNumber: merchantAccount, AccountName: merchantAccountName}
	plainIntent := &models.RampSellIntent{ID: uuid.New(), Status: models.SellIntentSwapping,
		BankCode: ordinaryBank, AccountNumber: ordinaryAccount, AccountName: ordinaryName}
	payOrder := &models.PaycrestOrderRecord{ID: uuid.New(), PayPaymentID: &payment,
		PayoutBankCode: merchantBankCode, PayoutAccount: merchantAccount, PayoutAccountName: merchantAccountName}
	plainOrder := &models.PaycrestOrderRecord{ID: uuid.New(),
		PayoutBankCode: ordinaryBank, PayoutAccount: ordinaryAccount, PayoutAccountName: ordinaryName}

	open := func(in *models.RampSellIntent) string {
		h := (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{resumable: in})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/paycrest/sell-intents/open", nil)
		c.Set("user", &models.User{ID: uuid.New()})
		h.OpenSellIntent(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	assertMerchantBankHidden(t, "open (pay intent)", open(payIntent))
	assertOrdinaryBankKept(t, "open (ordinary intent)", open(plainIntent))

	assertMerchantBankHidden(t, "order (pay)", openOrderRequest(t, &stubSellIntents{order: payOrder}, "").Body.String())
	assertOrdinaryBankKept(t, "order (ordinary)", openOrderRequest(t, &stubSellIntents{order: plainOrder}, "").Body.String())

	// The masked view is a copy: the record the service holds still has the real bank.
	if payIntent.AccountNumber != merchantAccount || payOrder.PayoutAccount != merchantAccount {
		t.Fatal("a route changed the record it was handed")
	}
}

// A pay-linked record with no bank on it (an ended sale) still answers, blanked.
func TestPayRecordViews_AreOnlyAppliedToPayLinkedRecords(t *testing.T) {
	payment := uuid.New()
	o := &models.PaycrestOrderRecord{PayoutAccount: merchantAccount}
	if got := orderForCaller(o); got != o {
		t.Fatal("an ordinary order must be returned as is")
	}
	o.PayPaymentID = &payment
	if got := orderForCaller(o); got.PayoutAccount == merchantAccount {
		t.Fatal("a pay order was not masked")
	}
	in := &models.RampSellIntent{AccountNumber: merchantAccount}
	if got := intentForCaller(in); got != in {
		t.Fatal("an ordinary intent must be returned as is")
	}
	in.PayPaymentID = &payment
	if got := intentForCaller(in); got.AccountNumber == merchantAccount {
		t.Fatal("a pay intent was not masked")
	}
	if orderForCaller(nil) != nil || intentForCaller(nil) != nil {
		t.Fatal("nil in, nil out")
	}
	rows := ordersForCaller([]models.PaycrestOrderRecord{*o, {PayoutAccount: ordinaryAccount}})
	if rows[0].PayoutAccount == merchantAccount || rows[1].PayoutAccount != ordinaryAccount {
		t.Fatalf("rows = %+v", rows)
	}
}

// ---- B2 / B3 ----

// The intent order route answers an order that may still be open exactly as the
// pay start routes do — before rate_moved, which it also wraps when a short
// order could not be cancelled, or the app would auto-retry against it.
func TestOpenSellIntentOrder_AnOrderThatMayStillExistIsPayAttemptUncertain(t *testing.T) {
	for name, err := range map[string]error{
		"a short order that could not be cancelled": fmt.Errorf("%w: %w", services.ErrSellAttemptMayExist, services.ErrSellRateMoved),
		"an order that could not be recorded":       fmt.Errorf("%w: db down", services.ErrSellAttemptMayExist),
	} {
		t.Run(name, func(t *testing.T) {
			w := openOrderRequest(t, &stubSellIntents{err: err}, "")
			got := payDecode(t, w.Body.Bytes())
			if w.Code != http.StatusConflict || got["code"] != "pay_attempt_uncertain" || got["message"] != payAttemptUncertainMessage {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

// The 409 an ordinary sell gets while a sale is open carries that open sale: for a
// customer paying a merchant it is a pay intent, whose bank is the merchant's.
func TestCreateSellIntent_TheSaleInProgressAnswerMasksAPayIntentsBank(t *testing.T) {
	user := &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet}
	payment := uuid.New()
	payIntent := &models.RampSellIntent{ID: uuid.New(), PayPaymentID: &payment, Status: models.SellIntentSwapping,
		BankCode: merchantBankCode, AccountNumber: merchantAccount, AccountName: merchantAccountName}
	plainIntent := &models.RampSellIntent{ID: uuid.New(), Status: models.SellIntentSwapping,
		BankCode: ordinaryBank, AccountNumber: ordinaryAccount, AccountName: ordinaryName}
	for name, tc := range map[string]struct {
		open *models.RampSellIntent
		ok   func(t *testing.T, where, body string)
	}{"pay": {payIntent, assertMerchantBankHidden}, "ordinary": {plainIntent, assertOrdinaryBankKept}} {
		t.Run(name, func(t *testing.T) {
			h := (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{err: services.ErrSellIntentOpen, open: tc.open})
			w := postSellIntent(t, h, user, sellIntentBody())
			if w.Code != http.StatusConflict {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			tc.ok(t, "409 sale in progress", w.Body.String())
		})
	}
}

// A guard for every response the paycrest and pay handlers write: the records
// that carry a pay payment's bank (RampSellIntent, PaycrestOrderRecord) may only
// reach a response through the caller views. It reads the handlers' source, so a
// new route or a new error branch that serialises a raw record fails here.
//
// go/types would be exact but drags the whole dependency graph into a unit test,
// so records are found by data flow lite: a variable is a record when it is
// assigned from a call to a method whose declared result is one (collected from
// the services package and the handlers' own port interfaces), or when it has one
// of the names the handlers have always used for them. A record is only flagged
// when it is passed to the response as itself — bare, or inside a gin.H / slice
// literal; passed through any call (a mask, or View(p)) it is transformed. The
// canaries below prove each way of getting one past the guard is caught.
var rawRecordNames = map[string]bool{"intent": true, "open": true, "order": true, "row": true, "rows": true, "resumable": true}

var maskFuncs = map[string]bool{"intentForCaller": true, "orderForCaller": true, "ordersForCaller": true, "payIntentView": true, "payOrderView": true}

// recordReturningMethods is every method name whose declared result mentions a
// pay-bank-bearing record, from the services package and the handler files.
func recordReturningMethods(t *testing.T, handlerFiles []string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	mentions := func(fl *ast.FieldList, src []byte, fset *token.FileSet) bool {
		if fl == nil {
			return false
		}
		for _, f := range fl.List {
			txt := string(src[fset.Position(f.Type.Pos()).Offset:fset.Position(f.Type.End()).Offset])
			if strings.Contains(txt, "PaycrestOrderRecord") || strings.Contains(txt, "RampSellIntent") {
				return true
			}
		}
		return false
	}
	scan := func(path string) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.FuncDecl:
				if v.Recv != nil && mentions(v.Type.Results, src, fset) {
					out[v.Name.Name] = true
				}
			case *ast.InterfaceType:
				for _, m := range v.Methods.List {
					if ft, ok := m.Type.(*ast.FuncType); ok && len(m.Names) == 1 && mentions(ft.Results, src, fset) {
						out[m.Names[0].Name] = true
					}
				}
			}
			return true
		})
	}
	svc, _ := filepath.Glob("../services/*.go")
	for _, f := range append(svc, handlerFiles...) {
		if !strings.HasSuffix(f, "_test.go") {
			scan(f)
		}
	}
	return out
}

// rawRecordResponses returns "file:line name" for every raw record passed to a
// response call in src, and how many response calls it saw.
func rawRecordResponses(t *testing.T, filename string, src any, methods map[string]bool) (found []string, responses int) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	isResponse := func(call *ast.CallExpr) bool {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		switch sel.Sel.Name {
		case "SuccessResponse", "ErrorResponse", "ErrorResponseWithCode", "JSON":
			return true
		}
		return false
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// The variables of this function that hold a record.
		vars := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && methods[sel.Sel.Name] {
				// The record is the first result of every such method.
				if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
					vars[id.Name] = true
				}
			}
			return true
		})
		// Bare identifiers only: a call (a mask, View(p), len(x)) transforms it.
		var direct func(n ast.Node)
		direct = func(n ast.Node) {
			switch v := n.(type) {
			case *ast.Ident:
				if vars[v.Name] || rawRecordNames[v.Name] {
					found = append(found, fmt.Sprintf("%s:%d %s", filename, fset.Position(v.Pos()).Line, v.Name))
				}
			case *ast.CompositeLit:
				for _, e := range v.Elts {
					direct(e)
				}
			case *ast.KeyValueExpr:
				direct(v.Value)
			case *ast.UnaryExpr:
				direct(v.X)
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isResponse(call) {
				responses++
				for _, arg := range call.Args {
					direct(arg)
				}
			}
			return true
		})
	}
	return found, responses
}

func TestEveryResponseThatCarriesARecordGoesThroughACallerView(t *testing.T) {
	files, _ := filepath.Glob("paycrest*.go")
	files = append(files, "pay_handler.go")
	var handlerFiles []string
	for _, name := range files {
		if !strings.HasSuffix(name, "_test.go") {
			handlerFiles = append(handlerFiles, name)
		}
	}
	methods := recordReturningMethods(t, handlerFiles)
	for _, m := range []string{"OpenOrder", "Resumable", "ClaimPayment", "CreateSell", "PayDirect", "Get"} {
		if !methods[m] {
			t.Fatalf("the guard did not learn that %s returns a record: %v", m, methods)
		}
	}
	total := 0
	for _, name := range handlerFiles {
		found, n := rawRecordResponses(t, name, nil, methods)
		total += n
		for _, f := range found {
			t.Errorf("a response carries a raw record (use intentForCaller/orderForCaller/ordersForCaller): %s", f)
		}
	}
	if total < 10 {
		t.Fatalf("only %d response calls found: the guard is not reading the handlers", total)
	}

	// Canaries: every way past the guard that a future branch might take.
	bypass := map[string]string{
		"a named variable":           `open, _ := h.sellIntents.Open(ctx, id); c.JSON(409, gin.H{"intent": open})`,
		"a short name":               `o, err := h.orders.Get(ctx, uid, id); utils.SuccessResponse(c, 200, "x", o)`,
		"an unusual name":            `sale, err := h.sellIntents.Resumable(ctx, uid); utils.SuccessResponse(c, 200, "x", gin.H{"sale": sale})`,
		"a result named res":         `res, err := h.orders.ClaimPayment(ctx, uid, id); utils.SuccessResponse(c, 200, "x", res)`,
		"a slice":                    `list, err := h.orders.List(ctx, uid, 5); utils.SuccessResponse(c, 200, "x", gin.H{"orders": list})`,
		"an error response":          `existing, _ := h.sellIntents.Get(ctx, uid, id); utils.ErrorResponseWithCode(c, 409, "m", "x", existing)`,
		"the pay start's own record": `order, err := h.checkout.PayDirect(ctx, u, id, in); utils.SuccessResponse(c, 201, "x", gin.H{"order": order})`,
	}
	for name, body := range bypass {
		src := "package handlers\nfunc f(h *H, c *gin.Context) {\n" + body + "\n}"
		if found, _ := rawRecordResponses(t, "canary.go", src, methods); len(found) != 1 {
			t.Errorf("canary %q: %v, want exactly one raw record flagged", name, found)
		}
	}
	// And what is allowed stays allowed.
	allowed := map[string]string{
		"masked":         `open, _ := h.sellIntents.Open(ctx, id); c.JSON(409, gin.H{"intent": intentForCaller(open)})`,
		"transformed":    `p, _ := h.payments.GetForMerchant(ctx, m, mode, id); utils.SuccessResponse(c, 200, "x", h.payments.View(p))`,
		"a field of one": `o, _ := h.orders.Get(ctx, uid, id); utils.SuccessResponse(c, 200, "x", gin.H{"id": o.ID})`,
	}
	for name, body := range allowed {
		src := "package handlers\nfunc f(h *H, c *gin.Context) {\n" + body + "\n}"
		if found, _ := rawRecordResponses(t, "ok.go", src, methods); len(found) != 0 {
			t.Errorf("%q flagged: %v", name, found)
		}
	}
}

// A pay order's sender fee is chosen per order to take the deposit's cushion:
// an internal figure the payer was never quoted, so her view carries neither
// the percent nor the amount. The record itself is not changed.
func TestPayOrderView_HidesOurPerOrderFee(t *testing.T) {
	o := &models.PaycrestOrderRecord{SenderFee: "0.0082", SenderFeePercent: "1.1171", Rate: "1347.43", TokenAmount: "0.742221"}
	v := payOrderView(o)
	if v.SenderFee != "" || v.SenderFeePercent != "" {
		t.Fatalf("payer view carries our fee: %q / %q%%", v.SenderFee, v.SenderFeePercent)
	}
	if v.Rate != "1347.43" || v.TokenAmount != "0.742221" {
		t.Fatalf("payer view lost what she needs to pay: %+v", v)
	}
	if o.SenderFee != "0.0082" || o.SenderFeePercent != "1.1171" {
		t.Fatal("payOrderView changed the record it was given")
	}
}
