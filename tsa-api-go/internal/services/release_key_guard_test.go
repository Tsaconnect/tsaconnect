package services

import (
	"go/ast"
	"go/parser"
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
