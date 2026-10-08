package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// providerSite is one place that branches on an IT intent's provider.
// refuses seeds an intent for `provider` in `status` and reports whether the
// site refused it without acting.
type providerSite struct {
	name    string
	status  string
	refuses func(t *testing.T, s *ITService, userID uuid.UUID, intent *models.ITIntent) bool
}

// claimParks is the P4 park on every claim branch: the money is claimed into
// buy_awaiting_rate, never priced, and the reason names the missing terms.
func claimParks(s *ITService, i *models.ITIntent, parkOnBadRate bool, ch PayInChannel) bool {
	ref := "chg_" + i.ID.String()[:8]
	ok, err := s.claimAndPriceStableBuy(context.Background(), i, ref, i.AmountMinor, time.Now(), parkOnBadRate, ch)
	var got models.ITIntent
	s.db.First(&got, "id = ?", i.ID)
	return ok && err == nil && got.Status == models.ITStatusBuyAwaitingRate && got.PaymentRef == ref &&
		got.AssetAmount == i.AssetAmount && strings.Contains(got.FailureReason, "has no terms")
}

// unrouted turns a seeded intent into one opened before routing (no honour
// window), so a claim takes the non-routed branch.
func unrouted(t *testing.T, s *ITService, i *models.ITIntent) *models.ITIntent {
	t.Helper()
	if err := s.db.Model(i).Update("rate_expires_at", nil).Error; err != nil {
		t.Fatalf("clear rate_expires_at: %v", err)
	}
	i.RateExpiresAt = nil
	if IsRoutedStableBuy(i) {
		t.Fatal("intent is still routed")
	}
	return i
}

var providerSites = []providerSite{
	{"ConvertStableBuy", models.ITStatusPaymentReceived, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		conv := &stubConverter{}
		s.SetStableConverter(conv, "m")
		return errors.Is(s.ConvertStableBuy(context.Background(), i.ID), ErrUnknownITProvider) && len(conv.converted) == 0
	}},
	{"DeliverStableBuy", models.ITStatusDelivering, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		d := &stubDeliverer{id: "x"}
		s.SetStableDeliverer(d)
		return errors.Is(s.DeliverStableBuy(context.Background(), i.ID), ErrUnknownITProvider) && d.calls == 0
	}},
	{"FinalizeBuyConversion", models.ITStatusConverting, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		s.db.Model(i).Update("conversion_id", "trf_c")
		ct := &countingTransfers{}
		s.SetStableTransfers(ct)
		return errors.Is(s.FinalizeBuyConversion(context.Background(), i.ID), ErrUnknownITProvider) && ct.calls == 0
	}},
	{"FinalizeBuyDelivery", models.ITStatusDelivering, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		s.db.Model(i).Update("delivery_id", "trf_d")
		ct := &countingTransfers{}
		s.SetStableTransfers(ct)
		return errors.Is(s.FinalizeBuyDelivery(context.Background(), i.ID), ErrUnknownITProvider) && ct.calls == 0
	}},
	{"PendingStableBuys", models.ITStatusPaymentReceived, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		a, b, c, d, err := s.PendingStableBuys(context.Background())
		return err == nil && !slices.Contains(slices.Concat(a, b, c, d), i.ID)
	}},
	{"RepriceParkedStableBuy", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		_, err := s.RepriceParkedStableBuy(context.Background(), i.ID)
		return errors.Is(err, ErrUnknownITProvider)
	}},
	{"priceRoutedArrival", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		a, err := s.priceRoutedArrival(context.Background(), i, i.AmountMinor, time.Now(), PayInCentral)
		return a == nil && errors.Is(err, ErrUnknownITProvider)
	}},
	{"quoteRoutedLiveFor", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		q, err := s.quoteRoutedLiveFor(context.Background(), i, big.NewInt(1_000_000), PayInCentral)
		return q == nil && errors.Is(err, ErrUnknownITProvider)
	}},
	{"routedMetric", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		return routedMetric(i) == "unknown"
	}},
	{"quoteForIntent", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		q, err := s.quoteForIntent(context.Background(), i, "1000000")
		return q == nil && errors.Is(err, ErrUnknownITProvider)
	}},
	{"quoteForIntent/non-routed", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		q, err := s.quoteForIntent(context.Background(), unrouted(t, s, i), "1000000")
		return q == nil && errors.Is(err, ErrUnknownITProvider)
	}},
	{"routedDeliveryAmounts", models.ITStatusDelivering, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		send, hold := s.routedDeliveryAmounts(i, big.NewInt(100))
		return send.Int64() == 100 && hold.Int64() == 100 // never Flutterwave's send fee
	}},
	// P5: outside its park an unknown provider is never refunded (the park
	// itself is refundable: TestMarkStableBuyRefunded_UnknownProviderParkRefundable).
	{"MarkStableBuyRefunded", models.ITStatusDelivering, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		_, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), i.ID, "note")
		var got models.ITIntent
		s.db.First(&got, "id = ?", i.ID)
		return errors.Is(err, ErrITBadState) && got.Status == models.ITStatusDelivering
	}},
	// P4: a payment for an unknown provider parks, never prices, on both rails
	// and on both claim branches (review M1: the non-routed one is checked first).
	{"claimAndPriceStableBuy/routed-central", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		return claimParks(s, i, false, PayInCentral)
	}},
	{"claimAndPriceStableBuy/routed-VA", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		return claimParks(s, i, true, PayInVA)
	}},
	{"claimAndPriceStableBuy/non-routed-central", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		return claimParks(s, unrouted(t, s, i), false, PayInCentral)
	}},
	{"claimAndPriceStableBuy/non-routed-VA", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		return claimParks(s, unrouted(t, s, i), true, PayInVA)
	}},
	{"DeliverParkedTreasuryBuy", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		_, err := s.DeliverParkedTreasuryBuy(context.Background(), uuid.New(), i.ID, "note")
		return errors.Is(err, ErrITBadState)
	}},
	{"GetStableBuyForUser", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, userID uuid.UUID, i *models.ITIntent) bool {
		for _, p := range knownITProviders {
			if _, err := s.GetStableBuyForUser(context.Background(), userID, i.ID, p); !errors.Is(err, ErrStableIntentNotFound) {
				return false
			}
		}
		return true
	}},
}

// Spec §12.3 / S45: every site refuses a provider without terms. C1 moves
// "tsa_pool" out of this list when it gives the pool its own terms.
func TestProviderBranches_Exhaustive(t *testing.T) {
	for _, provider := range []string{"tsa_pool", "bogus"} {
		for _, site := range providerSites {
			t.Run(provider+"/"+site.name, func(t *testing.T) {
				s, userID := newRoutedBuyService(t)
				if err := s.db.AutoMigrate(&models.ITIntentAuditLog{}); err != nil {
					t.Fatalf("migrate audit: %v", err)
				}
				intent := seedProviderIntent(t, s, userID, provider, site.status)
				if !site.refuses(t, s, userID, intent) {
					t.Fatalf("%s acted on an intent with provider %q", site.name, provider)
				}
			})
		}
	}
	for _, p := range knownITProviders {
		if _, err := providerTerms(&models.ITIntent{Provider: p}); err != nil {
			t.Fatalf("known provider %q lost its terms: %v", p, err)
		}
	}
}

// ---------------------------------------------------------------------------
// P7 source guard. The behavioural table above is the proof; this is the
// tripwire that keeps a new branch from appearing outside providerTerms. It is
// type-checked, so it follows the provider value through fields, locals,
// helper parameters and helper returns, not only through models.ITIntentProvider.
// ---------------------------------------------------------------------------

const (
	lintModelsPath   = "github.com/ojimcy/tsa-api-go/internal/models"
	lintServicesPath = "github.com/ojimcy/tsa-api-go/internal/services"
)

// lintFile is one parsed source file of a type-checked package.
type lintFile struct {
	file *ast.File
	path string
}

// providerLint follows IT-intent provider values ("tainted") through one
// type-checked package.
type providerLint struct {
	fset    *token.FileSet
	info    *types.Info
	files   []lintFile
	tainted map[types.Object]bool // locals, parameters, package vars holding a provider
	sources map[*types.Func]bool  // functions that return a provider
	decls   map[*types.Func]*ast.FuncDecl
}

func newProviderLint(fset *token.FileSet, info *types.Info, files []lintFile) *providerLint {
	l := &providerLint{fset: fset, info: info, files: files,
		tainted: map[types.Object]bool{}, sources: map[*types.Func]bool{}, decls: map[*types.Func]*ast.FuncDecl{}}
	for _, f := range files {
		for _, d := range f.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				if fn, ok := info.Defs[fd.Name].(*types.Func); ok {
					l.decls[fn] = fd
				}
			}
		}
	}
	l.propagate()
	return l
}

// isProviderField: models.ITIntent.Provider, or itProviderTerms.provider.
func isProviderField(obj types.Object) bool {
	v, ok := obj.(*types.Var)
	if !ok || !v.IsField() || v.Pkg() == nil {
		return false
	}
	owner, name := "", ""
	switch v.Pkg().Path() {
	case lintModelsPath:
		owner, name = "ITIntent", "Provider"
	case lintServicesPath:
		owner, name = "itProviderTerms", "provider"
	default:
		return false
	}
	if v.Name() != name {
		return false
	}
	tn, ok := v.Pkg().Scope().Lookup(owner).(*types.TypeName)
	if !ok {
		return false
	}
	st, ok := tn.Type().Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		if st.Field(i) == v {
			return true
		}
	}
	return false
}

// isProviderConst: a models.ITProvider* constant.
func (l *providerLint) isProviderConst(e ast.Expr) bool {
	sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	c, ok := l.info.Uses[sel.Sel].(*types.Const)
	return ok && c.Pkg() != nil && c.Pkg().Path() == lintModelsPath && strings.HasPrefix(c.Name(), "ITProvider")
}

func (l *providerLint) callee(call *ast.CallExpr) *types.Func {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		fn, _ := l.info.Uses[f].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		fn, _ := l.info.Uses[f.Sel].(*types.Func)
		return fn
	}
	return nil
}

func funcPkg(fn *types.Func) string {
	if fn == nil || fn.Pkg() == nil {
		return ""
	}
	return fn.Pkg().Path()
}

// isTainted: does e hold an IT intent's provider?
func (l *providerLint) isTainted(e ast.Expr) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		obj := l.info.Uses[x]
		if obj == nil {
			obj = l.info.Defs[x]
		}
		return obj != nil && l.tainted[obj]
	case *ast.SelectorExpr:
		if sel, ok := l.info.Selections[x]; ok {
			return isProviderField(sel.Obj())
		}
		obj := l.info.Uses[x.Sel] // pkg.Var
		return obj != nil && l.tainted[obj]
	case *ast.BinaryExpr:
		return x.Op == token.ADD && (l.isTainted(x.X) || l.isTainted(x.Y))
	case *ast.CallExpr:
		if tv, ok := l.info.Types[x.Fun]; ok && tv.IsType() { // string(p)
			return len(x.Args) == 1 && l.isTainted(x.Args[0])
		}
		fn := l.callee(x)
		if fn == nil {
			return false
		}
		if funcPkg(fn) == lintModelsPath && fn.Name() == "ITIntentProvider" {
			return true
		}
		if l.sources[fn] {
			return true
		}
		// strings.ToLower(p), strings.TrimSpace(p): still the provider.
		if p := funcPkg(fn); p == "strings" || p == "bytes" {
			res := fn.Type().(*types.Signature).Results()
			if res.Len() == 1 && types.Identical(res.At(0).Type(), types.Typ[types.String]) {
				return slices.ContainsFunc(x.Args, l.isTainted)
			}
		}
	}
	return false
}

func (l *providerLint) taint(e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	if !ok || id.Name == "_" {
		return false
	}
	obj := l.info.Defs[id]
	if obj == nil {
		obj = l.info.Uses[id]
	}
	if obj == nil || l.tainted[obj] {
		return false
	}
	l.tainted[obj] = true
	return true
}

// propagate runs to a fixed point: assignments, call arguments into the
// package's own parameters, and returns out of the package's own functions.
func (l *providerLint) propagate() {
	for changed := true; changed; {
		changed = false
		for _, f := range l.files {
			ast.Inspect(f.file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.AssignStmt:
					if len(x.Lhs) == len(x.Rhs) {
						for i := range x.Lhs {
							if l.isTainted(x.Rhs[i]) && l.taint(x.Lhs[i]) {
								changed = true
							}
						}
					}
				case *ast.ValueSpec:
					if len(x.Names) == len(x.Values) {
						for i := range x.Names {
							if l.isTainted(x.Values[i]) && l.taint(x.Names[i]) {
								changed = true
							}
						}
					}
				case *ast.CallExpr:
					fn := l.callee(x)
					if fn == nil || l.decls[fn] == nil {
						return true
					}
					params := fn.Type().(*types.Signature).Params()
					for i, a := range x.Args {
						if i < params.Len() && l.isTainted(a) && !l.tainted[params.At(i)] {
							l.tainted[params.At(i)] = true
							changed = true
						}
					}
				case *ast.FuncDecl:
					fn, _ := l.info.Defs[x.Name].(*types.Func)
					if fn == nil || x.Body == nil || l.sources[fn] || fn.Type().(*types.Signature).Results().Len() != 1 {
						return true
					}
					ast.Inspect(x.Body, func(m ast.Node) bool {
						if _, lit := m.(*ast.FuncLit); lit {
							return false
						}
						if r, ok := m.(*ast.ReturnStmt); ok && len(r.Results) == 1 && l.isTainted(r.Results[0]) {
							l.sources[fn] = true
							changed = true
						}
						return true
					})
				}
				return true
			})
		}
	}
}

var (
	sqlProviderExclusion = regexp.MustCompile(`(?i)\bprovider\s*(<>|!=|not\s+in\b)`)
	sqlProviderInline    = regexp.MustCompile(`(?i)\bprovider\s*(==?|in)\s*\(?\s*'`)
	sqlProviderCase      = regexp.MustCompile(`(?i)\bcase\s+(when\s+)?[\w.]*provider\b`)
	sqlProviderBound     = regexp.MustCompile(`(?i)\bprovider\s*(=|in)\s*\(?\s*\?`)
)

// mentionsITIntent: does the function work on it_intents rows?
func (l *providerLint) mentionsITIntent(body ast.Node) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if lit, ok := n.(*ast.BasicLit); ok && strings.Contains(lit.Value, "it_intents") {
			found = true
		}
		e, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		t := l.info.TypeOf(e)
		for t != nil {
			switch u := t.(type) {
			case *types.Pointer:
				t = u.Elem()
				continue
			case *types.Slice:
				t = u.Elem()
				continue
			case *types.Named:
				if o := u.Obj(); o.Pkg() != nil && o.Pkg().Path() == lintModelsPath && o.Name() == "ITIntent" {
					found = true
				}
			}
			break
		}
		return true
	})
	return found
}

// boundProviderFindings checks a query on it_intents whose provider is bound
// to a placeholder: equality only with a models.ITProvider* constant (a
// positive match never selects an unknown provider), IN only with the list
// providerTerms derives.
func (l *providerLint) boundProviderFindings(call *ast.CallExpr) []string {
	if len(call.Args) == 0 {
		return nil
	}
	lit, ok := ast.Unparen(call.Args[0]).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil
	}
	query, err := strconv.Unquote(lit.Value)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range sqlProviderBound.FindAllStringSubmatchIndex(query, -1) {
		op := strings.ToLower(query[m[2]:m[3]])
		argIdx := 1 + strings.Count(query[:m[0]], "?")
		pos := l.fset.Position(lit.Pos()).String()
		if argIdx >= len(call.Args) {
			out = append(out, pos+": provider bound to an argument the guard cannot see")
			continue
		}
		arg := ast.Unparen(call.Args[argIdx])
		switch {
		case op == "=" && l.isProviderConst(arg):
		case op == "in" && isCallTo(arg, "flutterwaveFulfilledProviders"):
		default:
			out = append(out, pos+": it_intents provider filter not built from providerTerms")
		}
	}
	return out
}

func isCallTo(e ast.Expr, name string) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := ast.Unparen(c.Fun).(*ast.Ident)
	return ok && id.Name == name
}

// findings returns every provider branch outside it_provider_terms.go.
func (l *providerLint) findings() []string {
	var out []string
	for _, f := range l.files {
		if filepath.Base(f.path) == "it_provider_terms.go" {
			continue
		}
		at := func(n ast.Node, what string) { out = append(out, l.fset.Position(n.Pos()).String()+": "+what) }
		isProvider := func(e ast.Expr) bool { return l.isTainted(e) || l.isProviderConst(e) }
		for _, d := range f.file.Decls {
			itScoped := false
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				if fn, _ := l.info.Defs[fd.Name].(*types.Func); funcPkg(fn) == lintModelsPath && fn.Name() == "ITIntentProvider" {
					continue // the one legacy "" fallback providerTerms reads through
				}
				itScoped = l.mentionsITIntent(fd)
			}
			var stack []ast.Node
			// itStatement: the nearest statement around the node works on
			// it_intents rows (other tables have a provider column too).
			itStatement := func() bool {
				for i := len(stack) - 1; i >= 0; i-- {
					if st, ok := stack[i].(ast.Stmt); ok {
						if _, block := st.(*ast.BlockStmt); !block {
							return l.mentionsITIntent(st)
						}
					}
				}
				return false
			}
			ast.Inspect(d, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				switch x := n.(type) {
				case *ast.BinaryExpr:
					if (x.Op == token.EQL || x.Op == token.NEQ) && (isProvider(x.X) || isProvider(x.Y)) {
						at(x, "direct provider comparison")
					}
				case *ast.SwitchStmt:
					if x.Tag != nil && isProvider(x.Tag) {
						at(x, "switch on the provider")
					}
				case *ast.CaseClause:
					if slices.ContainsFunc(x.List, l.isProviderConst) {
						at(x, "case on a provider constant")
					}
				case *ast.IndexExpr:
					if l.isTainted(x.Index) {
						at(x, "lookup keyed by the provider")
					}
				case *ast.CallExpr:
					fn := l.callee(x)
					switch funcPkg(fn) {
					case "strings", "bytes", "slices", "maps", "sort", "reflect":
						res := fn.Type().(*types.Signature).Results()
						if res.Len() == 1 && !types.Identical(res.At(0).Type(), types.Typ[types.String]) &&
							slices.ContainsFunc(x.Args, isProvider) {
							at(x, "provider compared through "+funcPkg(fn)+"."+fn.Name())
						}
					}
					if itScoped {
						out = append(out, l.boundProviderFindings(x)...)
					}
				case *ast.BasicLit:
					if x.Kind != token.STRING {
						break
					}
					itQuery := strings.Contains(x.Value, "it_intents") || itStatement()
					switch {
					case sqlProviderExclusion.MatchString(x.Value):
						at(x, "provider exclusion in SQL")
					case itQuery && sqlProviderInline.MatchString(x.Value):
						at(x, "provider value inlined in it_intents SQL")
					case itQuery && sqlProviderCase.MatchString(x.Value):
						at(x, "CASE on the provider in it_intents SQL")
					}
				}
				return true
			})
		}
	}
	return out
}

// lintPackage is one `go list -json` row.
type lintPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Export     string
	DepOnly    bool
}

// lintWorld is the module's packages plus export data for every dependency,
// listed once per test binary.
var lintWorld = sync.OnceValues(func() ([]lintPackage, error) {
	cmd := exec.Command("go", "list", "-export", "-deps",
		"-json=ImportPath,Dir,GoFiles,CgoFiles,Export,DepOnly", "./internal/...", "./cmd/...")
	cmd.Dir = filepath.Join("..", "..") // tsa-api-go
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, stderr.String())
	}
	var pkgs []lintPackage
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p lintPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
})

// lintChecker type-checks sources against the listed export data.
func lintChecker(t *testing.T) (*token.FileSet, types.Importer, []lintPackage) {
	t.Helper()
	pkgs, err := lintWorld()
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	exports := map[string]string{}
	for _, p := range pkgs {
		exports[p.ImportPath] = p.Export
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		if f := exports[path]; f != "" {
			return os.Open(f)
		}
		return nil, fmt.Errorf("no export data for %q", path)
	})
	return fset, imp, pkgs
}

func lintCheck(fset *token.FileSet, imp types.Importer, path string, files []lintFile) (*providerLint, error) {
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	asts := make([]*ast.File, len(files))
	for i, f := range files {
		asts[i] = f.file
	}
	if _, err := (&types.Config{Importer: imp}).Check(path, fset, asts, info); err != nil {
		return nil, err
	}
	return newProviderLint(fset, info, files), nil
}

// P7: nothing outside it_provider_terms.go branches on a provider by itself.
func TestProviderBranches_NoDirectComparisons(t *testing.T) {
	fset, imp, pkgs := lintChecker(t)
	var findings []string
	checked := 0
	for _, p := range pkgs {
		if p.DepOnly {
			continue
		}
		if len(p.CgoFiles) > 0 {
			t.Fatalf("%s has cgo files the guard cannot type-check", p.ImportPath)
		}
		var files []lintFile
		for _, name := range p.GoFiles {
			path := filepath.Join(p.Dir, name)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			files = append(files, lintFile{file: f, path: path})
		}
		l, err := lintCheck(fset, imp, p.ImportPath, files)
		if err != nil {
			t.Fatalf("type-check %s: %v", p.ImportPath, err)
		}
		findings = append(findings, l.findings()...)
		checked++
	}
	if checked < 10 {
		t.Fatalf("guard checked %d packages; the listing is broken", checked)
	}
	if len(findings) > 0 {
		t.Fatalf("branch on an IT intent's provider through providerTerms (it_provider_terms.go):\n%s", strings.Join(findings, "\n"))
	}
}

func lintSynthetic(t *testing.T, body string) []string {
	t.Helper()
	fset, imp, _ := lintChecker(t)
	src := `package x
import (
	"slices"
	"strings"
	"gorm.io/gorm"
	"github.com/ojimcy/tsa-api-go/internal/models"
)
var _ = slices.Contains[[]string]
var _ = strings.EqualFold
var _ *gorm.DB
var _ models.ITIntent
` + body
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	l, err := lintCheck(fset, imp, "example.com/x", []lintFile{{file: f, path: "synthetic.go"}})
	if err != nil {
		t.Fatalf("type-check: %v\n%s", err, body)
	}
	return l.findings()
}

// The guard is not vacuous: each pattern it forbids is caught, including the
// ones the first, syntax-only draft missed (review M4).
func TestProviderLint_FlagsSyntheticComparisons(t *testing.T) {
	forbidden := map[string]string{
		"constant comparison": `func a(i *models.ITIntent) bool { return models.ITIntentProvider(i) == models.ITProviderTreasury }`,
		"switch on helper":    `func a(i *models.ITIntent) { switch models.ITIntentProvider(i) { } }`,
		"SQL exclusion":       `const q = "provider <> ?"`,
		"field vs literal":    `func a(i *models.ITIntent) bool { return i.Provider == "tsa_pool" }`,
		"field by value":      `func a(i models.ITIntent) bool { return i.Provider != "" }`,
		"local copy":          `func a(i *models.ITIntent) bool { p := models.ITIntentProvider(i); return p == "treasury" }`,
		"var copy":            `func a(i *models.ITIntent) bool { var p string = i.Provider; q := strings.ToLower(p); return q == "x" }`,
		"switch init":         `func a(i *models.ITIntent) int { switch p := i.Provider; p { case "treasury": return 1 }; return 0 }`,
		"tagless switch":      `func a(i *models.ITIntent) int { switch { case i.Provider == "x": return 1 }; return 0 }`,
		"case on constant":    `func a(s string) int { switch s { case models.ITProviderFlutterwave: return 1 }; return 0 }`,
		"EqualFold":           `func a(i *models.ITIntent) bool { return strings.EqualFold(i.Provider, "TREASURY") }`,
		"slices.Contains":     `func a(i *models.ITIntent) bool { return slices.Contains([]string{"treasury"}, i.Provider) }`,
		"map lookup":          `var m = map[string]bool{}; func a(i *models.ITIntent) bool { return m[i.Provider] }`,
		"helper parameter":    `func is(p, want string) bool { return p == want }; func a(i *models.ITIntent) bool { return is(i.Provider, "treasury") }`,
		"helper return":       `func prov(i *models.ITIntent) string { return i.Provider }; func a(i *models.ITIntent) bool { return prov(i) != "flutterwave" }`,
		"SQL inline value":    `func a(db *gorm.DB) { db.Model(&models.ITIntent{}).Where("provider = 'treasury'") }`,
		"SQL inline, raw":     `const q = "SELECT id FROM it_intents WHERE provider IN ('', 'flutterwave')"`,
		"SQL NOT IN":          `const q = "status = ? AND provider NOT IN ?"`,
		"SQL CASE":            `func a(db *gorm.DB) { var rows []models.ITIntent; db.Select("CASE provider WHEN 'treasury' THEN 1 END").Find(&rows) }`,
		"SQL IN, own list": `func a(db *gorm.DB) { var rows []models.ITIntent
			db.Where("provider IN ?", []string{"", "flutterwave"}).Find(&rows) }`,
		"SQL =, literal": `func a(db *gorm.DB) { db.Model(&models.ITIntent{}).Where("status = ? AND provider = ?", "x", "tsa_pool") }`,
	}
	for name, body := range forbidden {
		t.Run(name, func(t *testing.T) {
			if got := lintSynthetic(t, body); len(got) == 0 {
				t.Fatalf("guard missed a provider branch:\n%s", body)
			}
		})
	}
	// The original three patterns are three findings, one each.
	if got := lintSynthetic(t, `
func a(i *models.ITIntent) bool { return models.ITIntentProvider(i) == models.ITProviderTreasury }
func b(i *models.ITIntent) { switch models.ITIntentProvider(i) { } }
const q = "provider <> ?"
`); len(got) != 3 {
		t.Fatalf("findings = %v, want 3 (comparison, switch, SQL)", got)
	}
}

// What the guard allows: reading the value (logs, audit, API views), writing
// it, and positive it_intents filters built from the constants or the terms.
func TestProviderLint_AllowsNonBranchingUses(t *testing.T) {
	allowed := `
type view struct{ Provider string }
func flutterwaveFulfilledProviders() []string { return nil }
func a(i *models.ITIntent) view { return view{Provider: models.ITIntentProvider(i)} }
func b(i *models.ITIntent) string { return "provider=" + i.Provider }
func c(userID string) *models.ITIntent { return &models.ITIntent{Provider: models.ITProviderTreasury} }
func d(db *gorm.DB, userID string) { db.Model(&models.ITIntent{}).Where("user_id = ? AND provider = ? AND status NOT IN ?", userID, models.ITProviderTreasury, []string{"x"}) }
func e(db *gorm.DB) { var rows []models.ITIntent; db.Where("provider IN ?", flutterwaveFulfilledProviders()).Find(&rows) }
func f(db *gorm.DB) { db.Table("webhook_events").Where("provider IN ?", []string{"nomba"}) }
type order struct{ Provider string }
func g(o order) bool { return o.Provider == "paystack" }
func h(db *gorm.DB) { db.AutoMigrate(&models.ITIntent{}); db.Exec("UPDATE p2p_payment_methods SET provider = 'opay' WHERE provider = 'bank_transfer'") }
`
	if got := lintSynthetic(t, allowed); len(got) != 0 {
		t.Fatalf("guard flagged a non-branching use: %v", got)
	}
}
