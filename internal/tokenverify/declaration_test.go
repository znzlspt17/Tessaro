package tokenverify

import (
	"encoding/json"
	"go/scanner"
	"go/token"
	"tessaro/internal/tokenspec"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeclarationRejectsMissingReturnAtBodyBoundary(t *testing.T) {
	p := problem(t, `{"intent":"subtract","inputs":["a","b"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":3,"b":1},"want":2}]}`)
	prefix := strings.Fields("package main ; func f0 ( a , b int )")
	v, err := CheckDeclaration(p, prefix)
	if err != nil || v.Status != DeclarationPending || !v.ParametersChecked {
		t.Fatalf("params %+v %v", v, err)
	}
	v, err = CheckDeclaration(p, append(prefix, "{"))
	if err != nil || v.Status != DeclarationRejected || v.Reason != "return_type" {
		t.Fatalf("body %+v %v", v, err)
	}
}
func TestDeclarationBoundariesAndCompatibility(t *testing.T) {
	p := problem(t, `{"intent":"subtract","inputs":["a","b"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":3,"b":1},"want":2}]}`)
	tests := []struct {
		tokens string
		status DeclarationStatus
		reason DeclarationReason
	}{{"package main ; func f0 ( a , b int ) int {", DeclarationAccepted, ""}, {"package main ; func f0 ( b int , a int , ) ( int ) {", DeclarationAccepted, ""}, {"package main ; func f0 ( a ( int ) , b int ) ( r int ) {", DeclarationAccepted, ""}, {"package main ; func f0 ( a int )", DeclarationRejected, "parameter_count"}, {"package main ; func f0 ( a bool , b int )", DeclarationRejected, "parameter_type"}, {"package main ; func f0 ( a int , c int )", DeclarationRejected, "parameter_name"}, {"package main ; func f0 ( a int , b ... int )", DeclarationRejected, "variadic"}, {"package main ; func f0 ( a , b int ) bool {", DeclarationRejected, "return_type"}, {"package main ; func f0 ( a , b int ) int { return undefined +", DeclarationAccepted, ""}, {"package main ; func f0 ( a , b int ) (", DeclarationPending, ""}}
	for _, tc := range tests {
		t.Run(tc.tokens, func(t *testing.T) {
			v, e := CheckDeclaration(p, strings.Fields(tc.tokens))
			if e != nil || v.Status != tc.status || tc.reason != "" && v.Reason != tc.reason {
				t.Fatalf("%+v %v", v, e)
			}
		})
	}
	zero := problem(t, `{"intent":"one","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`)
	v, e := CheckDeclaration(zero, strings.Fields("package main ; func f0 ( ) int {"))
	if e != nil || v.Status != DeclarationAccepted {
		t.Fatalf("zero %+v %v", v, e)
	}
}
func TestAllReferencePrefixesKeepValidDeclarations(t *testing.T) {
	base := filepath.Join("..", "..", "examples", "token")
	data, err := os.ReadFile(filepath.Join(base, "references.json"))
	if err != nil {
		t.Fatal(err)
	}
	var refs []struct{ ID, Spec, Source string }
	if err = json.Unmarshal(data, &refs); err != nil || len(refs) != 40 {
		t.Fatalf("references=%d err=%v", len(refs), err)
	}
	for _, ref := range refs {
		t.Run(ref.ID, func(t *testing.T) {
			f, err := os.Open(filepath.Join(base, ref.Spec))
			if err != nil {
				t.Fatal(err)
			}
			p, err := tokenspec.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			fset := token.NewFileSet()
			var scan scanner.Scanner
			scan.Init(fset.AddFile("ref", -1, len(ref.Source)), []byte(ref.Source), nil, 0)
			var values []string
			accepted := false
			for {
				_, kind, literal := scan.Scan()
				if kind == token.EOF {
					break
				}
				if kind == token.SEMICOLON {
					literal = ";"
				} else if literal == "" {
					literal = kind.String()
				}
				values = append(values, literal)
				result, e := CheckDeclaration(p, values)
				if e != nil || result.Status == DeclarationRejected {
					t.Fatalf("prefix %q => %+v %v", strings.Join(values, " "), result, e)
				}
				accepted = accepted || result.Status == DeclarationAccepted
			}
			if !accepted {
				t.Fatal("declaration never accepted")
			}
		})
	}
}
