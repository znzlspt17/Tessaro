package tokenverify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"jevcancode/internal/tokenspec"
)

func returnAtom(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return returnAtom(x.X)
	case *ast.Ident:
		return true
	case *ast.BasicLit:
		return x.Kind == token.INT || x.Kind == token.STRING
	case *ast.UnaryExpr:
		lit, ok := x.X.(*ast.BasicLit)
		return x.Op == token.SUB && ok && lit.Kind == token.INT
	}
	return false
}

// 2026-11-12: 미완성 접두사는 보존하고 반환 원자를 벗어남이 확정된 경우에만 제거한다.
func CheckReturnPrefix(p tokenspec.Problem, tokens []string) DeclarationReason {
	if p.ReturnPolicy != tokenspec.ReturnPolicyValuesOnly {
		return ""
	}
	active, atom, negative, depth := false, false, false, 0
	for _, t := range tokens {
		if !active {
			if t == "return" {
				active = true
				atom = false
				negative = false
				depth = 0
			}
			continue
		}
		if t == ";" || t == "}" {
			if !atom || depth != 0 {
				return "return_computation"
			}
			active = false
			continue
		}
		if !atom {
			if t == "(" && !negative {
				depth++
				continue
			}
			if t == "-" && !negative {
				negative = true
				continue
			}
			e, err := parser.ParseExpr(t)
			if err != nil {
				return ""
			}
			if negative {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.INT {
					return "return_computation"
				}
			} else if !returnAtom(e) {
				return "return_computation"
			}
			atom = true
			continue
		}
		if t == ")" && depth > 0 {
			depth--
			continue
		}
		return "return_computation"
	}
	return ""
}
