package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ErrCodes feeds the OpenAPI enum of APIError.code, so it must list every ErrCode constant.
func TestErrCodesListsEveryConstant(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			if !strings.HasPrefix(vs.Names[0].Name, "ErrCode") {
				continue
			}
			v, err := strconv.Unquote(vs.Values[0].(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			declared = append(declared, v)
		}
	}
	got := ErrCodes()
	slices.Sort(declared)
	sorted := slices.Clone(got)
	slices.Sort(sorted)
	if !slices.Equal(declared, sorted) {
		t.Errorf("ErrCodes() = %v\nconstants  = %v", sorted, declared)
	}
	if len(slices.Compact(sorted)) != len(sorted) {
		t.Errorf("ErrCodes() has a duplicate: %v", got)
	}
}
