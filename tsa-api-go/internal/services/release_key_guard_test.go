package services

import (
	"go/ast"
	"go/parser"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// Review m8 on Plan A / S8, S39: every use of the release key on a chain goes
// through ChainSender.Send (prepare → sign → broadcast under one lock). A
// .SignAndBroadcast( call on a value whose static type implements ChainSender
// is allowed only inside release_signer*.go.
func TestReleaseKey_NoSignAndBroadcastOnAChainSender(t *testing.T) {
	fset, imp, pkgs := lintChecker(t)
	svc, err := imp.Import(lintServicesPath)
	if err != nil {
		t.Fatal(err)
	}
	chainSender := svc.Scope().Lookup("ChainSender").Type().Underlying().(*types.Interface)
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
				if s, ok := l.info.Selections[sel]; ok && implementsChainSender(s.Recv(), chainSender) {
					findings = append(findings, fset.Position(sel.Pos()).String())
				}
				return true
			})
		}
	}
	if len(findings) > 0 {
		t.Fatalf("SignAndBroadcast on a ChainSender outside release_signer*.go (use Send):\n%s", strings.Join(findings, "\n"))
	}
}

func implementsChainSender(t types.Type, cs *types.Interface) bool {
	return types.Implements(t, cs) || types.Implements(types.NewPointer(t), cs)
}

// The guard is not vacuous.
func TestReleaseKeyGuard_FlagsASyntheticDirectCall(t *testing.T) {
	fset, imp, _ := lintChecker(t)
	src := `package x
import (
	"context"
	"github.com/ojimcy/tsa-api-go/internal/services"
)
func bad(s services.ChainSender) { _, _ = s.SignAndBroadcast(context.Background(), nil) }
func fine(s services.ReleaseSigner) { _, _ = s.SignAndBroadcast(context.Background(), nil) }
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
	cs := svc.Scope().Lookup("ChainSender").Type().Underlying().(*types.Interface)
	hits := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SignAndBroadcast" {
			if s, ok := l.info.Selections[sel]; ok && implementsChainSender(s.Recv(), cs) {
				hits++
			}
		}
		return true
	})
	if hits != 1 {
		t.Fatalf("hits = %d, want only the ChainSender call", hits)
	}
}
