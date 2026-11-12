package tokensynth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

func orderingOperator(s string) bool {
	switch s {
	case "<", "<=", ">", ">=":
		return true
	}
	return false
}

// 2026-11-08: 완성된 실제 식만 파싱하고 Go의 우선순위로 다음 비교의 왼쪽을 찾는다.
// 오류 복구 AST 또는 미완성 식을 이용해 후보를 제거하지 않는다.
func (s *State) comparisonResultBefore(position int) bool {
	if s == nil || s.Parser == nil || position < 1 || position > len(s.Tokens) {
		return false
	}
	// 같은 끝 위치에서 시작점이 가장 이른 완결 식을 사용한다. 더 늦은 원점들은
	// `x < 2`의 `2`처럼 오른쪽 피연산자 자체의 문법 항목일 수 있다.
	for _, origin := range s.Parser.CompletedOrigins(position, "_expression") {
		if origin < 0 || origin >= position {
			continue
		}
		expr, err := parser.ParseExpr(strings.Join(s.Tokens[origin:position], " "))
		if err != nil {
			continue
		}
		return comparisonBindingResult(expr)
	}
	return false
}

func comparisonBindingResult(expr ast.Expr) bool {
	// &&, ||의 오른쪽에는 비교가 먼저 결합된다.
	for {
		binary, ok := expr.(*ast.BinaryExpr)
		if !ok || (binary.Op != token.LAND && binary.Op != token.LOR) {
			break
		}
		expr = binary.Y
	}
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	binary, ok := expr.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	switch binary.Op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

func (s *State) priorBoolOrdering() *int {
	if s == nil {
		return nil
	}
	for i, lexeme := range s.Tokens {
		if orderingOperator(lexeme) && s.comparisonResultBefore(i) {
			return &i
		}
	}
	return nil
}
