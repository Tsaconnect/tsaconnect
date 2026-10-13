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

// soloLaneCallers are the only functions allowed to build a lane outside the
// registry: constructors that take a bare signer (tests), never reached by
// boot — the cmd/server half of this lint forbids boot from calling them.
var soloLaneCallers = map[string]bool{
	"SetOperatorSigner": true, // MCGPSwapService: tests' directly-wired operator
	"NewRelayerService": true, // replaced at boot by UseKeyLane(registry lane)
	"nonceLane":         true, // SponsorLane with no registry lane (tests)
}

// Review item 8: a KeyLane is built only in release_signer_lanes.go (the
// registry), or by soloLane from the bare-signer constructors above; boot
// (cmd/server) never wires an operator or relayer from a bare signer.
func TestKeyLanes_OnlyFromTheRegistry(t *testing.T) {
	fset, _, pkgs := lintChecker(t)
	var findings []string
	for _, p := range pkgs {
		if p.DepOnly {
			continue
		}
		boot := strings.HasSuffix(p.ImportPath, "/cmd/server")
		for _, name := range p.GoFiles {
			if strings.HasPrefix(name, "release_signer_lanes") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			findings = append(findings, keyLaneFindings(fset, f, boot)...)
		}
	}
	if len(findings) > 0 {
		t.Fatalf("a key lane built outside the registry:\n%s", strings.Join(findings, "\n"))
	}
}

// keyLaneFindings is the lint over one file.
func keyLaneFindings(fset *token.FileSet, f *ast.File, boot bool) []string {
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if lintTypeName(x.Type) == "KeyLane" {
					out = append(out, fset.Position(x.Pos()).String()+": KeyLane literal")
				}
			case *ast.CallExpr:
				name := lintTypeName(x.Fun)
				switch {
				case (name == "soloLane" || name == "NewKeyLane") && !soloLaneCallers[fn.Name.Name]:
					out = append(out, fset.Position(x.Pos()).String()+": "+name+" in "+fn.Name.Name)
				case boot && (name == "SetOperatorSigner" || name == "NewLocalKeyReleaseSigner"):
					out = append(out, fset.Position(x.Pos()).String()+": boot wires a bare signer ("+name+"); use Signers().Lane")
				}
			}
			return true
		})
	}
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

// The lane lint is not vacuous.
func TestKeyLaneLint_FlagsPrivateLanes(t *testing.T) {
	fset := token.NewFileSet()
	src := `package x
func a() { _ = &KeyLane{}; _ = soloLane(nil, nil) }
func SetOperatorSigner() { _ = soloLane(nil, nil) }
func boot() { s.SetOperatorSigner(services.NewLocalKeyReleaseSigner(k, c)) }
`
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := keyLaneFindings(fset, f, false); len(got) != 2 {
		t.Fatalf("library findings = %v, want the literal and the soloLane in a()", got)
	}
	if got := keyLaneFindings(fset, f, true); len(got) != 4 {
		t.Fatalf("boot findings = %v, want those plus SetOperatorSigner and NewLocalKeyReleaseSigner", got)
	}
}
