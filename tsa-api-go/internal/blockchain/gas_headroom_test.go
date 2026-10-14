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

// The backstop, not a supported input: SuggestGasPrice raises an error before a
// nil can reach here. This only pins that it cannot panic.
func TestApplyGasHeadroomDoesNotPanicOnNil(t *testing.T) {
	if got := applyGasHeadroom(nil); got == nil || got.Sign() != 0 {
		t.Fatalf("expected a zero rather than a panic for a nil price, got %v", got)
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

// Directories that build transactions. Handlers are included because one of
// them does (reward_distribution_handler.go), which the first version of this
// test missed entirely.
var txBuildingDirs = []string{".", "../services", "../handlers"}

// Every transaction builder must take the buffered default. This is a
// structural test rather than a behavioural one because the builders reach the
// network for a nonce and a gas estimate before they price anything, so there
// is no seam to stub — and reverting any single call site to the raw accessor
// otherwise leaves the whole package green, which is exactly how this shipped
// half-fixed the first time.
//
// Precedent: TestAutoMigrateHasNoDestructiveDDL guards a rule the same way.
func TestTransactionBuildersDoNotUseTheRawGasPrice(t *testing.T) {
	// Keyed on PATH, not base name: two directories can hold a client.go, and
	// an allow-list that matches on file name alone would exempt both.
	allowed := map[string]string{
		"client.go":                         "declares it, and SuggestGasPrice is the one caller that adds the headroom",
		"../services/blockchain_service.go": "GetGasPrices reports the chain's own price for display",
	}

	forEachGoFile(t, func(t *testing.T, path string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != rawAccessor {
				return true
			}
			if why, ok := allowed[path]; ok {
				t.Logf("%s uses %s as expected: %s", path, rawAccessor, why)
				return true
			}
			t.Errorf("%s calls %s — a transaction priced this way is rejected once the base "+
				"fee moves. Use SuggestGasPrice, or add this path to the allow-list with a reason.", path, rawAccessor)
			return true
		})
	})
}

// unpricedBuilders are transactions built for a named chain but with no
// GasPrice. They sidestep the headroom rather than opting out of it, so the
// check above cannot see them: there is no raw accessor to catch. This one
// predates the work and is left alone, but it is pinned so the set cannot
// quietly grow — a new unpriced builder is a new instance of the bug this file
// exists for.
//
// Both entries hand the client a tx with no nonce either, so the signer resolves
// nonce and gas together against the live RPC at signing time. That is not the
// bug this file guards — it is fresher than a prepare-time snapshot, since there
// is no window for the base fee to move between pricing and signing. They are
// listed so the shape stays deliberate: a chain-named tx that omits gas but
// supplies a nonce would be neither design, and would fail here.
//
//   - reward_distribution_handler.go: admin setMerkleRoot, signed in the console
//   - reward_merkle_service.go: the user's reward claim, priced by
//     tsa-app/services/rewardsClaim.ts from provider.getFeeData()
//   - fee_rate_intent_service.go: FeePlanTx, which tsa-admin either signs
//     through the browser wallet (use-fee-signer.ts) or exports as a Gnosis
//     Safe batch (buildFeeSafeBatch) — both supply their own gas
var unpricedBuilders = map[string]bool{
	"../handlers/reward_distribution_handler.go": true,
	"../services/reward_merkle_service.go":       true,
	"../services/fee_rate_intent_service.go":     true,
}

// ChainID is the marker for "this is meant to be signed and broadcast". A
// literal without it is a partial: the five dev-only stubs in services/ return
// To/Value/Data when no RPC client is configured, and hard-error in production
// rather than being signed. Pricing those would be meaningless, so the rule is
// scoped to transactions that name a chain.
func TestNoNewUnpricedTransactionBuilders(t *testing.T) {
	found := map[string]bool{}

	forEachGoFile(t, func(t *testing.T, path string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isUnsignedTxLiteral(lit) {
				return true
			}
			keys := literalKeys(lit)
			if keys["ChainID"] && !keys["GasPrice"] {
				found[path] = true
			}
			return true
		})
	})

	for path := range found {
		if !unpricedBuilders[path] {
			t.Errorf("%s builds an UnsignedTx for a specific chain with no GasPrice, so it is signed "+
				"at whatever the client guesses and bypasses the headroom entirely. Set GasPrice from SuggestGasPrice.", path)
		}
	}
	for path := range unpricedBuilders {
		if !found[path] {
			t.Errorf("%s now prices its transaction — drop it from unpricedBuilders so the rule "+
				"stays tight.", path)
		}
	}
}

// Recognised by NAME or by SHAPE, because neither alone is enough.
//
// Name matching is a suffix, not equality: services/reward_merkle_service.go
// declares its own blockchainUnsignedTx mirror to avoid importing the
// blockchain package, and an exact match walked straight past it — a
// chain-named, unpriced builder in a directory this test already scanned,
// reported green. But a suffix only catches mirrors that happen to be named
// after the original, so a shape match backs it up: To + Value + Data is an
// unsigned transaction whatever its type is called.
func isUnsignedTxLiteral(lit *ast.CompositeLit) bool {
	switch t := lit.Type.(type) {
	case *ast.Ident: // UnsignedTx{...} or a local mirror of it
		if strings.HasSuffix(t.Name, "UnsignedTx") {
			return true
		}
	case *ast.SelectorExpr: // blockchain.UnsignedTx{...}
		if strings.HasSuffix(t.Sel.Name, "UnsignedTx") {
			return true
		}
	}
	return hasKeys(lit, "To", "Value", "Data")
}

// hasKeys reports whether a composite literal sets every named field.
func hasKeys(lit *ast.CompositeLit, names ...string) bool {
	set := literalKeys(lit)
	for _, name := range names {
		if !set[name] {
			return false
		}
	}
	return true
}

func literalKeys(lit *ast.CompositeLit) map[string]bool {
	set := make(map[string]bool, len(lit.Elts))
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok {
			set[key.Name] = true
		}
	}
	return set
}

func forEachGoFile(t *testing.T, fn func(*testing.T, string, *ast.File)) {
	t.Helper()
	for _, dir := range txBuildingDirs {
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
			fn(t, path, file)
		}
	}
}

// The buffered accessor is only the default if the builders actually take it:
// PrepareContractCall prices with SuggestGasPrice, and every Prepare* builder
// in token.go goes through it.
func TestTransactionBuildersPriceWithHeadroom(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "token.go", nil, 0)
	if err != nil {
		t.Fatalf("parse token.go: %v", err)
	}

	calls := map[string]string{
		"PrepareContractCall":   "SuggestGasPrice",
		"PrepareNativeTransfer": "PrepareContractCall",
		"PrepareERC20Transfer":  "PrepareContractCall",
		"PrepareERC20Approve":   "PrepareContractCall",
	}
	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		want, ok := calls[fn.Name.Name]
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == want {
				found[fn.Name.Name] = true
			}
			return true
		})
	}

	for name, want := range calls {
		if !found[name] {
			t.Errorf("%s does not call %s — it will build transactions at a stale base fee", name, want)
		}
	}
}
