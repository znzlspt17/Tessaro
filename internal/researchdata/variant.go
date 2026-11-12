package researchdata

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
)

// 2026-11-12: 참조 평가에만 적용하며 기존 이름과 충돌하지 않는 지역변수로 계산 순서와 분기 범위를 보존한다.
func Variant(source string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reference.go", source, 0)
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			used[id.Name] = true
		}
		return true
	})
	next := 0
	var failure error
	ast.Inspect(file, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		var stmts []ast.Stmt
		for _, stmt := range block.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok {
				stmts = append(stmts, stmt)
				continue
			}
			if len(ret.Results) != 1 {
				failure = fmt.Errorf("unsupported reference return arity")
				return false
			}
			if atomic(ret.Results[0]) {
				stmts = append(stmts, stmt)
				continue
			}
			name := ""
			for {
				name = fmt.Sprintf("v%d", next)
				next++
				if !used[name] {
					used[name] = true
					break
				}
			}
			stmts = append(stmts, &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.DEFINE, Rhs: []ast.Expr{ret.Results[0]}}, &ast.ReturnStmt{Results: []ast.Expr{ast.NewIdent(name)}})
		}
		block.List = stmts
		return true
	})
	if failure != nil {
		return "", failure
	}
	var out bytes.Buffer
	if err := format.Node(&out, fset, file); err != nil {
		return "", err
	}
	return out.String(), nil
}
func atomic(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return atomic(x.X)
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
