package tokensynth

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"jevcancode/internal/tokenlex"
	"jevcancode/internal/tokenspec"
	"strings"
)

type PrefixAssessment struct {
	Status string
	Reason string
}

const (
	prefixPossible   = "possible"
	prefixImpossible = "impossible"
	prefixUnknown    = "unknown"
)

type semanticPrefix struct {
	key        string
	assessment PrefixAssessment
	variables  map[string]types.Type
}

func semanticKey(p tokenspec.Problem) string {
	inputs, output, err := p.Contract()
	if err != nil {
		return ""
	}
	b, _ := json.Marshal(struct {
		Policy string
		Inputs map[string]string
		Output string
	}{p.ReturnPolicy, inputs, output})
	return string(b)
}

// 2026-11-12: values-only 반환 원자는 타입을 바꾸는 후속 연산이 금지되므로 확정된 불일치만 후보에서 제거한다.
func typedReturnMismatch(p tokenspec.Problem, s *State, lexeme string, inputs map[string]string, result string) bool {
	if p.ReturnPolicy != tokenspec.ReturnPolicyValuesOnly || result == "" {
		return false
	}
	active, negative := false, false
	for i := len(s.Tokens) - 1; i >= 0; i-- {
		t := s.Tokens[i]
		if t == "return" {
			active = true
			break
		}
		if t == "(" {
			continue
		}
		if t == "-" && !negative {
			negative = true
			continue
		}
		return false
	}
	if !active {
		return false
	}
	if lexeme == "(" {
		return false
	}
	if lexeme == "-" {
		return result != "int"
	}
	kind, err := tokenlex.Kind(lexeme)
	if err != nil {
		return false
	}
	if kind == token.INT {
		return result != "int"
	}
	if kind == token.STRING {
		return result != "string"
	}
	if kind != token.IDENT {
		return false
	}
	// 스냅샷이 정확한 경우 실제 변수 바인딩이 predeclared 이름보다 우선한다.
	var scope map[string]types.Type
	if s.semantic != nil && s.semantic.key == semanticKey(p) {
		scope = s.semantic.variables
	}
	if scope != nil {
		if typ, ok := scope[lexeme]; ok {
			if typ == nil || typ == types.Typ[types.Invalid] {
				return false
			}
			return types.TypeString(typ, nil) != result
		}
	} else {
		// 재분석 불가한 스코프에서는 입력 변수가 숨겨졌을 가능성까지 보존한다.
		shadowPossible := false
		for _, t := range s.Tokens {
			if t == "var" || t == ":=" || t == "for" {
				shadowPossible = true
				break
			}
		}
		if typ, ok := inputs[lexeme]; ok && !shadowPossible {
			return typ != "" && typ != result
		}
		return false
	}
	switch lexeme {
	case "true", "false":
		return result != "bool"
	case "nil":
		return !strings.HasPrefix(result, "[]") && !strings.HasPrefix(result, "map[")
	}
	if types.Universe.Lookup(lexeme) != nil {
		return true
	}
	return false
}

// 2026-11-12: 실제 문장이나 조건 경계에서만 검사하고 열린 블록만 닫는다. 구문이나 타입이 불확실하면 가지치기하지 않는다.
func assessSemantic(p tokenspec.Problem, tokens []string, previous *semanticPrefix) *semanticPrefix {
	key := semanticKey(p)
	boundary := len(tokens) > 0 && (tokens[len(tokens)-1] == ";" || tokens[len(tokens)-1] == "{" || tokens[len(tokens)-1] == "}")
	if previous != nil && previous.key == key && !boundary {
		return previous
	}
	out := &semanticPrefix{key: key, assessment: PrefixAssessment{Status: prefixUnknown}}
	if p.ReturnPolicy != tokenspec.ReturnPolicyValuesOnly || !boundary {
		return out
	}
	braces, parens, brackets := 0, 0, 0
	for _, v := range tokens {
		switch v {
		case "{":
			braces++
		case "}":
			braces--
		case "(":
			parens++
		case ")":
			parens--
		case "[":
			brackets++
		case "]":
			brackets--
		}
	}
	if braces < 0 || parens != 0 || brackets != 0 {
		return out
	}
	source := strings.Join(tokens, " ")
	src := source + strings.Repeat(" }", braces)
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "prefix.go", src, 0)
	if err != nil {
		return out
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{Error: func(error) {}}
	pkg, _ := conf.Check("prefix", fs, []*ast.File{f}, info)
	// 2026-11-12: 확정된 경계의 유효 범위와 타입을 자식 상태에 보존해 형제 상태가 공유 변경을 하지 않게 한다.
	if pkg != nil {
		pos := token.Pos(fs.File(f.Pos()).Base() + len(source))
		if scope := pkg.Scope().Innermost(pos); scope != nil {
			out.variables = make(map[string]types.Type)
			for ; scope != nil; scope = scope.Parent() {
				for _, name := range scope.Names() {
					if _, exists := out.variables[name]; exists {
						continue
					}
					if v, ok := scope.Lookup(name).(*types.Var); ok {
						out.variables[name] = v.Type()
					}
				}
			}
		}
	}
	lastEnd := token.Pos(fs.File(f.Pos()).Base() + len(source) - 2)
	lastBrace := token.Pos(fs.File(f.Pos()).Base() + len(source) - 1)
	checkExpr := func(expr ast.Expr) {
		if expr == nil || pkg == nil {
			return
		}
		known := true
		ast.Inspect(expr, func(node ast.Node) bool {
			if _, ok := node.(*ast.SelectorExpr); ok {
				known = false
				return false
			}
			if id, ok := node.(*ast.Ident); ok && info.Uses[id] != nil && info.Uses[id].Type() == types.Typ[types.Invalid] {
				known = false
			}
			if id, ok := node.(*ast.Ident); ok && info.Uses[id] == nil && types.Universe.Lookup(id.Name) == nil {
				known = false
			}
			return true
		})
		if known && types.CheckExpr(fs, pkg, expr.Pos(), expr, nil) != nil {
			out.assessment = PrefixAssessment{Status: prefixImpossible, Reason: "expression_type_invalid"}
		}
	}
	ast.Inspect(f, func(node ast.Node) bool {
		if tokens[len(tokens)-1] == "{" {
			// 2026-11-12: 같은 중괄호가 조건문의 본문일 수 있으므로 Go 파서가 이 위치를
			// composite literal로 해석한 경우에만 실제 바인딩과 타입을 확인한다.
			if lit, ok := node.(*ast.CompositeLit); ok && lit.Lbrace == lastBrace {
				if id, ok := lit.Type.(*ast.Ident); ok {
					obj := info.Uses[id]
					if obj == nil && previous != nil && previous.key == key && previous.variables != nil {
						if typ, bound := previous.variables[id.Name]; bound {
							obj = types.NewVar(id.Pos(), nil, id.Name, typ)
						} else {
							obj = types.Universe.Lookup(id.Name)
						}
					}
					switch value := obj.(type) {
					case *types.Var, *types.Const, *types.Builtin, *types.Nil:
						out.assessment = PrefixAssessment{Status: prefixImpossible, Reason: "composite_literal_type_invalid"}
					case *types.TypeName:
						if value.Type() != nil {
							if _, scalar := value.Type().Underlying().(*types.Basic); scalar {
								out.assessment = PrefixAssessment{Status: prefixImpossible, Reason: "composite_literal_type_invalid"}
							}
						}
					}
				}
			}
			var cond ast.Expr
			switch n := node.(type) {
			case *ast.IfStmt:
				if n.Body != nil && n.Body.Lbrace == lastBrace {
					cond = n.Cond
				}
			case *ast.ForStmt:
				if n.Body != nil && n.Body.Lbrace == lastBrace {
					cond = n.Cond
				}
			}
			if cond != nil {
				checkExpr(cond)
				typ := info.Types[cond].Type
				if typ != nil && typ != types.Typ[types.Invalid] && !types.AssignableTo(typ, types.Typ[types.Bool]) {
					out.assessment = PrefixAssessment{Status: prefixImpossible, Reason: "condition_type_mismatch"}
				}
			}
			return true
		}
		a, ok := node.(*ast.AssignStmt)
		if !ok || a.End() != lastEnd {
			return true
		}
		for _, expr := range a.Rhs {
			checkExpr(expr)
		}
		if out.assessment.Status == prefixImpossible || a.Tok != token.ASSIGN || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
			return true
		}
		lhs, ok := a.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		obj := info.Uses[lhs]
		rhs := info.Types[a.Rhs[0]].Type
		if obj == nil || rhs == nil || obj.Type() == nil || obj.Type() == types.Typ[types.Invalid] || rhs == types.Typ[types.Invalid] {
			return true
		}
		if !types.AssignableTo(rhs, obj.Type()) {
			out.assessment = PrefixAssessment{Status: prefixImpossible, Reason: "assignment_type_mismatch"}
		}
		return true
	})
	return out
}
func MonitorPrefix(p tokenspec.Problem, s *State) PrefixAssessment {
	if s == nil {
		return PrefixAssessment{Status: prefixUnknown}
	}
	return assessSemantic(p, s.Tokens, s.semantic).assessment
}

// 2026-11-12: 모델에는 현재 가시성과 확정된 타입의 교집합만 전달한다.
// 미완성 선언에서 발생할 수 있는 shadowing은 타입이 확정될 때까지 생략한다.
func VisibleTypes(p tokenspec.Problem, s *State) map[string]string {
	out := map[string]string{}
	if s == nil || s.Parser == nil {
		return out
	}
	inputs, _, err := p.Contract()
	if err != nil {
		return out
	}
	info := describe(p, s)
	if info.bodyAt < 0 {
		return out
	}
	for i := len(s.Tokens) - 1; i > info.bodyAt; i-- {
		switch s.Tokens[i] {
		case ";", "{", "}":
			i = info.bodyAt
		case "var", ":=", "for", "if":
			return out
		}
	}
	if s.semantic != nil && s.semantic.key == semanticKey(p) && s.semantic.variables != nil {
		for name, typ := range s.semantic.variables {
			if !info.visible[name] || typ == nil {
				continue
			}
			typeName := types.TypeString(typ, nil)
			if tokenspec.SupportedType(typeName) {
				out[name] = typeName
			}
		}
		return out
	}
	// 스냅샷이 없다면 모든 선언 구간에서 입력 이름이 가려질 수 있다.
	for _, tok := range s.Tokens[info.bodyAt+1:] {
		if tok == "var" || tok == ":=" || tok == "for" {
			return out
		}
	}
	for name, typ := range inputs {
		if info.visible[name] && tokenspec.SupportedType(typ) {
			out[name] = typ
		}
	}
	return out
}
