package services

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// Review m8 on Plan A / S8, S39, generalised by the signer consolidation
// (2026-10-13): every hot key sends through its KeyLane (prepare → sign →
// broadcast under the one lock and nonce floor its key shares with every other
// role). A .SignAndBroadcast( call on a value whose static type implements
// ReleaseSigner — any signer, ChainSender included — is allowed only inside
// release_signer*.go, where KeyLane and the signers themselves live. A role
// that signs directly has a private lock again and races the shared lane.
func TestReleaseKey_NoSignAndBroadcastOnAChainSender(t *testing.T) {
	fset, imp, pkgs := lintChecker(t)
	svc, err := imp.Import(lintServicesPath)
	if err != nil {
		t.Fatal(err)
	}
	signer := svc.Scope().Lookup("ReleaseSigner").Type().Underlying().(*types.Interface)
	var findings []string
	for _, p := range pkgs {
		if p.DepOnly {
			continue
		}
		var files []lintFile
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, lintFile{file: f, path: filepath.Join(p.Dir, name)})
		}
		l, err := lintCheck(fset, imp, p.ImportPath, files)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasPrefix(filepath.Base(f.path), "release_signer") {
				continue
			}
			ast.Inspect(f.file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "SignAndBroadcast" {
					return true
				}
				if s, ok := l.info.Selections[sel]; ok && implementsIface(s.Recv(), signer) {
					findings = append(findings, fset.Position(sel.Pos()).String())
				}
				return true
			})
		}
	}
	if len(findings) > 0 {
		t.Fatalf("SignAndBroadcast on a signer outside release_signer*.go (send through the key's KeyLane):\n%s", strings.Join(findings, "\n"))
	}
}

func implementsIface(t types.Type, cs *types.Interface) bool {
	return types.Implements(t, cs) || types.Implements(types.NewPointer(t), cs)
}

// The guard is not vacuous: a direct sign on a ChainSender or on any other
// ReleaseSigner (a role reintroducing a private lock) is flagged; a
// SignAndBroadcast method on a type that is not a signer is not.
func TestReleaseKeyGuard_FlagsASyntheticDirectCall(t *testing.T) {
	fset, imp, _ := lintChecker(t)
	src := `package x
import (
	"context"
	"github.com/ojimcy/tsa-api-go/internal/services"
)
type notASigner struct{}
func (notASigner) SignAndBroadcast(context.Context, []byte) (string, error) { return "", nil }
func bad(s services.ChainSender) { _, _ = s.SignAndBroadcast(context.Background(), nil) }
func alsoBad(s services.ReleaseSigner) { _, _ = s.SignAndBroadcast(context.Background(), nil) }
func privateLock(s *services.LocalKeyReleaseSigner) { _, _ = s.SignAndBroadcast(context.Background(), nil) }
func fine(n notASigner) { _, _ = n.SignAndBroadcast(context.Background(), nil) }
`
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	l, err := lintCheck(fset, imp, "example.com/x", []lintFile{{file: f, path: "synthetic.go"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := imp.Import(lintServicesPath)
	rs := svc.Scope().Lookup("ReleaseSigner").Type().Underlying().(*types.Interface)
	hits := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SignAndBroadcast" {
			if s, ok := l.info.Selections[sel]; ok && implementsIface(s.Recv(), rs) {
				hits++
			}
		}
		return true
	})
	if hits != 3 {
		t.Fatalf("hits = %d, want the three signer calls and not the plain method", hits)
	}
}

// Review item 8 / re-review 7: a KeyLane is built only in
// release_signer_lanes*.go (the registry, and soloLane for tests) — never a
// KeyLane literal, new(KeyLane), soloLane or NewKeyLane anywhere else in
// production code, package-level declarations included. No production code
// calls SetOperatorSigner (a bare signer on a private lane: tests only), and
// boot (cmd/server) never builds a bare signer.
func TestKeyLanes_OnlyFromTheRegistry(t *testing.T) {
	fset, _, pkgs := lintChecker(t)
	var findings []string
	for _, p := range pkgs {
		if p.DepOnly {
			continue
		}
		boot := strings.HasSuffix(p.ImportPath, "/cmd/server")
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			findings = append(findings, keyLaneFindings(fset, f, name, boot)...)
		}
	}
	if len(findings) > 0 {
		t.Fatalf("a key lane built outside the registry:\n%s", strings.Join(findings, "\n"))
	}
}

// keyLaneFindings is the lint over one whole file (every declaration).
func keyLaneFindings(fset *token.FileSet, f *ast.File, name string, boot bool) []string {
	lanesFile := strings.HasPrefix(name, "release_signer_lanes") && !strings.HasSuffix(name, "_test.go")
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			if !lanesFile && lintTypeName(x.Type) == "KeyLane" {
				out = append(out, fset.Position(x.Pos()).String()+": KeyLane literal")
			}
		case *ast.CallExpr:
			fn := lintTypeName(x.Fun)
			switch {
			case fn == "new" && len(x.Args) == 1 && lintTypeName(x.Args[0]) == "KeyLane" && !lanesFile:
				out = append(out, fset.Position(x.Pos()).String()+": new(KeyLane)")
			case (fn == "soloLane" || fn == "NewKeyLane") && !lanesFile:
				out = append(out, fset.Position(x.Pos()).String()+": "+fn)
			case fn == "SetOperatorSigner":
				out = append(out, fset.Position(x.Pos()).String()+": SetOperatorSigner (tests only; boot uses SetOperatorLane)")
			case boot && fn == "NewLocalKeyReleaseSigner":
				out = append(out, fset.Position(x.Pos()).String()+": boot builds a bare signer; use Signers().Lane")
			}
		}
		return true
	})
	return out
}

func lintTypeName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.StarExpr:
		return lintTypeName(x.X)
	}
	return ""
}

// The lane lint is not vacuous: package-level and in-function constructions,
// new(KeyLane), soloLane, SetOperatorSigner and a boot bare signer all fail;
// the lanes file itself may build lanes.
func TestKeyLaneLint_FlagsPrivateLanes(t *testing.T) {
	fset := token.NewFileSet()
	src := `package x
var global = &KeyLane{}
var other = new(KeyLane)
func a() { _ = soloLane(nil, nil); s.SetOperatorSigner(nil) }
func boot() { _ = services.NewLocalKeyReleaseSigner(k, c) }
`
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := keyLaneFindings(fset, f, "x.go", false); len(got) != 4 {
		t.Fatalf("library findings = %v, want the literal, new, soloLane and SetOperatorSigner", got)
	}
	if got := keyLaneFindings(fset, f, "x.go", true); len(got) != 5 {
		t.Fatalf("boot findings = %v, want those plus NewLocalKeyReleaseSigner", got)
	}
	lanes, _ := parser.ParseFile(fset, "release_signer_lanes.go", "package x\nfunc soloLane() *KeyLane { return &KeyLane{} }\n", 0)
	if got := keyLaneFindings(fset, lanes, "release_signer_lanes.go", false); len(got) != 0 {
		t.Fatalf("the lanes file itself: %v, want none", got)
	}
}
