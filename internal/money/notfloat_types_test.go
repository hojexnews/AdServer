package money_test

// notfloat_types_test.go — TX-2 backstop via go/types (real type-check, not
// text matching), added in the 27a onda / FIX #5 alongside the DECIMAL_PAT
// extension to scripts/ci/no-float-go.sh.
//
// scripts/ci/no-float-go.sh (grep backstop) and .golangci.yml (forbidigo)
// both scan SOURCE TEXT: forbidigo for the token "float32"/"float64", the
// shell script for that token PLUS decimal literals like "12.50". Both are
// still, fundamentally, pattern matches over text. This test instead runs
// Go's own type-checker (go/types) over every non-test .go file in this
// package and inspects the RESOLVED type of every declared identifier and
// every expression. That makes it immune to the shape of the source: an
// implicit float produced through constant folding, a value returned from
// some call, or a literal buried behind several layers of expressions all
// still resolve, after type-checking, to the Basic Float32/Float64 (or
// UntypedFloat) kind — and this test catches all of them without needing
// to anticipate the syntax.
//
// Scope: internal/money only. This package is pure-money (see doc.go/
// ecpm.go header) — float has zero legitimate use here, so the walk needs
// no allowlist (contrast with internal/ranker/score.go, a mixed pCTR/eCPM
// file that legitimately uses float32/float64 for the ranking signal and is
// explicitly OUT of scope for this AST/types walk).
import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoFloatTypes_ASTWalk(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package dir: %v", err)
	}

	var srcFiles []string
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue // only production code must be float-free; test fixtures are out of scope
		}
		srcFiles = append(srcFiles, p)
	}
	if len(srcFiles) == 0 {
		t.Fatal("no non-test .go files found in internal/money — check test working directory")
	}

	var astFiles []*ast.File
	for _, p := range srcFiles {
		af, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", p, perr)
		}
		astFiles = append(astFiles, af)
	}

	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	if _, err := conf.Check("money", fset, astFiles, info); err != nil {
		t.Fatalf("go/types check failed (package must compile cleanly): %v", err)
	}

	isFloat := func(tt types.Type) bool {
		if tt == nil {
			return false
		}
		b, ok := tt.Underlying().(*types.Basic)
		if !ok {
			return false
		}
		switch b.Kind() {
		case types.Float32, types.Float64, types.UntypedFloat:
			return true
		default:
			return false
		}
	}

	reported := map[token.Pos]bool{}
	report := func(pos token.Pos, what string) {
		if reported[pos] {
			return
		}
		reported[pos] = true
		t.Errorf("%s: %s has float type (TX-2: money is int64 minor-units, never float)",
			fset.Position(pos), what)
	}

	// Every declared identifier (vars, consts, params, results) with a
	// resolved float type — catches `p := 12.50` even though the token
	// "float64" never appears in the source.
	for ident, obj := range info.Defs {
		if obj == nil {
			continue
		}
		if isFloat(obj.Type()) {
			report(ident.Pos(), "declared identifier "+ident.Name)
		}
	}

	// Every expression with a resolved float type — catches float used only
	// as an intermediate (e.g. `_ = p * 0.9`) without ever being bound to a
	// named identifier.
	for expr, tv := range info.Types {
		if isFloat(tv.Type) {
			report(expr.Pos(), "expression")
		}
	}
}
