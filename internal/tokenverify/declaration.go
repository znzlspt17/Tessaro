package tokenverify

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"jevcancode/internal/tokenspec"
	"strings"
)

type DeclarationStatus string
type DeclarationReason string

const (
	DeclarationPending  DeclarationStatus = "pending"
	DeclarationAccepted DeclarationStatus = "accepted"
	DeclarationRejected DeclarationStatus = "rejected"
)

type DeclarationCheck struct {
	Status            DeclarationStatus `json:"status"`
	Reason            DeclarationReason `json:"reason,omitempty"`
	ParametersChecked bool              `json:"parameters_checked"`
}

func signatureMatch(sig *types.Signature, contract map[string]string, result string, checkReturn bool) ([]string, map[types.Object]bool, DeclarationReason) {
	if sig == nil {
		return nil, nil, "invalid_signature"
	}
	if sig.Variadic() {
		return nil, nil, "variadic"
	}
	if sig.Params().Len() != len(contract) {
		return nil, nil, "parameter_count"
	}
	names := make([]string, 0, sig.Params().Len())
	seen := map[string]bool{}
	params := map[types.Object]bool{}
	for i := 0; i < sig.Params().Len(); i++ {
		v := sig.Params().At(i)
		name := v.Name()
		if seen[name] || contract[name] == "" {
			return nil, nil, "parameter_name"
		}
		if types.TypeString(v.Type(), nil) != contract[name] {
			return nil, nil, "parameter_type"
		}
		seen[name] = true
		params[v] = true
		names = append(names, name)
	}
	if checkReturn && (sig.Results().Len() != 1 || types.TypeString(sig.Results().At(0).Type(), nil) != result) {
		return nil, nil, "return_type"
	}
	return names, params, ""
}

// 2026-11-05: 선언부가 확정된 경계만 검사해 미완성 함수 본문의 진단을 탐색에서 배제한다.
func CheckDeclaration(p tokenspec.Problem, tokens []string) (DeclarationCheck, error) {
	contract, result, err := p.Contract()
	if err != nil {
		return DeclarationCheck{}, fmt.Errorf("invalid problem contract")
	}
	funcAt, open, closeAt, body := -1, -1, -1, -1
	depth := 0
	for i, t := range tokens {
		if funcAt < 0 {
			if t == "func" {
				funcAt = i
			}
			continue
		}
		if open < 0 {
			if t == "(" {
				open = i
				depth = 1
			}
			continue
		}
		if closeAt < 0 {
			switch t {
			case "(":
				depth++
			case ")":
				depth--
				if depth == 0 {
					closeAt = i
				}
			}
			continue
		}
		if t == "{" {
			body = i
			break
		}
	}
	if closeAt < 0 {
		return DeclarationCheck{Status: DeclarationPending}, nil
	}
	end := closeAt + 1
	checkReturn := body >= 0
	if checkReturn {
		end = body
	}
	source := strings.Join(tokens[:end], " ") + " { }"
	fset := token.NewFileSet()
	f, e := parser.ParseFile(fset, "signature.go", source, 0)
	if e != nil || f.Name.Name != "main" || len(f.Imports) != 0 || len(f.Decls) != 1 {
		return DeclarationCheck{Status: DeclarationRejected, Reason: "invalid_signature", ParametersChecked: true}, nil
	}
	fn, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || fn.Name.Name == "main" || fn.Name.Name == "init" {
		return DeclarationCheck{Status: DeclarationRejected, Reason: "invalid_signature", ParametersChecked: true}, nil
	}
	info := types.Info{Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{IgnoreFuncBodies: true}
	if _, e = conf.Check("signature", fset, []*ast.File{f}, &info); e != nil {
		return DeclarationCheck{Status: DeclarationRejected, Reason: "invalid_signature", ParametersChecked: true}, nil
	}
	obj, ok := info.Defs[fn.Name].(*types.Func)
	if !ok {
		return DeclarationCheck{Status: DeclarationRejected, Reason: "invalid_signature", ParametersChecked: true}, nil
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return DeclarationCheck{Status: DeclarationRejected, Reason: "invalid_signature", ParametersChecked: true}, nil
	}
	_, _, reason := signatureMatch(sig, contract, result, checkReturn)
	if reason != "" {
		return DeclarationCheck{Status: DeclarationRejected, Reason: reason, ParametersChecked: true}, nil
	}
	if checkReturn {
		return DeclarationCheck{Status: DeclarationAccepted, ParametersChecked: true}, nil
	}
	return DeclarationCheck{Status: DeclarationPending, ParametersChecked: true}, nil
}
