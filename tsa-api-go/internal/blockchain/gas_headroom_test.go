package blockchain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimumUsefulRisePercent is the base-fee rise a prepared transaction must
// survive, independent of whatever gasPriceHeadroomPercent happens to be.
//
// It exists so the headroom cannot be quietly tuned down to a value that
// reproduces the original bug while every test stays green: the production
// rejection that prompted this code was only a 0.46% rise, so a test built
// around those literal numbers passes at ANY positive headroom and proves
// nothing. 9% is the floor the retry in the app is sized to complement.
const minimumUsefulRisePercent = 9

func TestApplyGasHeadroomSurvivesTheMinimumUsefulBaseFeeRise(t *testing.T) {
	suggested := big.NewInt(20_000_000_000) // 20 gwei

	priced := applyGasHeadroom(suggested)

	risen := new(big.Int).Mul(suggested, big.NewInt(100+minimumUsefulRisePercent))
	risen.Div(risen, big.NewInt(100))
	if priced.Cmp(risen) < 0 {
		t.Fatalf("priced at %s, rejected by a base fee of %s (a %d%% rise); headroom is too small",
			priced, risen, minimumUsefulRisePercent)
	}
}

// The rejection a user actually hit on Arbitrum: prepared at 20006000 wei, and
// by the time they broadcast the base fee was 20098000.
func TestApplyGasHeadroomCoversTheProductionRejection(t *testing.T) {
	priced := applyGasHeadroom(big.NewInt(20006000))

	if priced.Cmp(big.NewInt(20098000)) < 0 {
		t.Fatalf("still below the base fee that rejected the real transaction: %s", priced)
	}
}

func TestApplyGasHeadroomDoesNotMutateInput(t *testing.T) {
	suggested := big.NewInt(1_000_000_000)
	applyGasHeadroom(suggested)

	if suggested.Cmp(big.NewInt(1_000_000_000)) != 0 {
		t.Fatalf("input mutated: %s", suggested)
	}
}

func TestApplyGasHeadroomHandlesNil(t *testing.T) {
	if got := applyGasHeadroom(nil); got == nil || got.Sign() != 0 {
		t.Fatalf("expected zero for a nil price, got %v", got)
	}
}

func TestApplyGasHeadroomScales(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero stays zero", 0, 0},
		{"one gwei", 1_000_000_000, 1_100_000_000},
		// Picked so the headroom is visible despite integer truncation:
		// 15 * 110 / 100 = 16.5, truncated to 16. A value like 3 would round
		// back down to 3 and be indistinguishable from no headroom at all.
		{"truncates rather than rounds up", 15, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyGasHeadroom(big.NewInt(tc.in)); got.Cmp(big.NewInt(tc.want)) != 0 {
				t.Fatalf("applyGasHeadroom(%d) = %s, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// rawAccessor is the opt-out: it returns what the chain says, with no headroom,
// and a transaction built with it is priced at a base fee that has already moved
// by the time the user signs.
const rawAccessor = "SuggestGasPriceRaw"

// Every transaction builder must take the buffered default. This is a
// structural test rather than a behavioural one because the builders reach the
// network for a nonce and a gas estimate before they price anything, so there
// is no seam to stub — and reverting any single call site to the raw accessor
// otherwise leaves the whole package green, which is exactly how this shipped
// half-fixed the first time.
//
// Precedent: TestAutoMigrateHasNoDestructiveDDL guards a rule the same way.
func TestTransactionBuildersDoNotUseTheRawGasPrice(t *testing.T) {
	// The one legitimate caller: reporting a price, not signing one.
	allowed := map[string]string{
		"client.go":             "declares it, and SuggestGasPrice is the one caller that adds the headroom",
		"blockchain_service.go": "GetGasPrices reports the chain's own price for display",
	}

	for _, dir := range []string{".", "../services"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != rawAccessor {
					return true
				}
				if why, ok := allowed[name]; ok {
					t.Logf("%s uses %s as expected: %s", name, rawAccessor, why)
					return true
				}
				t.Errorf("%s calls %s — a transaction priced this way is rejected once the base "+
					"fee moves. Use SuggestGasPrice, or add this file to the allow-list with a reason.", path, rawAccessor)
				return true
			})
		}
	}
}

// The buffered accessor is only the default if the builders actually take it.
func TestTransactionBuildersPriceWithHeadroom(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "token.go", nil, 0)
	if err != nil {
		t.Fatalf("parse token.go: %v", err)
	}

	builders := map[string]bool{
		"PrepareNativeTransfer": false,
		"PrepareERC20Transfer":  false,
		"PrepareERC20Approve":   false,
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, want := builders[fn.Name.Name]; !want {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SuggestGasPrice" {
				builders[fn.Name.Name] = true
			}
			return true
		})
	}

	for name, found := range builders {
		if !found {
			t.Errorf("%s does not price via SuggestGasPrice — it will build transactions at a stale base fee", name)
		}
	}
}
