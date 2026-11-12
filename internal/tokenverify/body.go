package tokenverify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"jevcancode/internal/tokenspec"
	"strings"
)

// 2026-11-07: 닫힌 경계에서 파싱 가능한 경우에만 확정된 오류를 찾는다. 합성한 중괄호로 미완성 표현식이나 제어문을 완성하지 않는다.
func CheckBodyPrefix(p tokenspec.Problem, tokens []string) DeclarationReason {
	if len(tokens) == 0 {
		return ""
	}
	last := tokens[len(tokens)-1]
	if last == "(" {
		return checkNonCallablePrefix(tokens)
	}
	if last != ";" && last != "}" && last != "{" {
		return ""
	}
	depth := 0
	for _, s := range tokens {
		if s == "{" {
			depth++
		}
		if s == "}" {
			depth--
		}
		if depth < 0 {
			return ""
		}
	}
	source := strings.Join(tokens, " ")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "prefix.go", source+strings.Repeat(" }", depth), 0)
	if err != nil || len(file.Decls) != 1 {
		return ""
	}
	// 합성 중괄호가 컬렉션 리터럴을 닫았다면 해당 식은 아직 완결되지 않았다.
	unfinishedLiteral := false
	ast.Inspect(file, func(n ast.Node) bool {
		if literal, ok := n.(*ast.CompositeLit); ok && fset.Position(literal.Rbrace).Offset >= len(source) {
			unfinishedLiteral = true
		}
		return !unfinishedLiteral
	})
	if unfinishedLiteral {
		return ""
	}
	fn, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Body == nil {
		return ""
	}
	maxStatements := limit(p.Budgets.MaxStatements, 32, 128)
	maxNesting := limit(p.Budgets.MaxNesting, 4, 8)
	statements := 0
	var block func(*ast.BlockStmt, int, int) DeclarationReason
	var stmt func(ast.Stmt, int, int) DeclarationReason
	block = func(b *ast.BlockStmt, nest, loops int) DeclarationReason {
		if nest > maxNesting {
			return "nesting_limit"
		}
		for _, s := range b.List {
			if why := stmt(s, nest, loops); why != "" {
				return why
			}
		}
		return ""
	}
	stmt = func(s ast.Stmt, nest, loops int) DeclarationReason {
		if s == nil {
			return ""
		}
		statements++
		if statements > maxStatements {
			return "statement_limit"
		}
		switch x := s.(type) {
		case *ast.ExprStmt:
			if _, ok := x.X.(*ast.CallExpr); !ok {
				return "unused_expression"
			}
		case *ast.BranchStmt:
			if loops == 0 && (x.Tok == token.BREAK || x.Tok == token.CONTINUE) {
				return "branch_outside_loop"
			}
		case *ast.BlockStmt:
			return block(x, nest+1, loops)
		case *ast.IfStmt:
			if nest >= maxNesting {
				return "nesting_limit"
			}
			if why := stmt(x.Init, nest+1, loops); why != "" {
				return why
			}
			if why := block(x.Body, nest+1, loops); why != "" {
				return why
			}
			if x.Else != nil {
				switch e := x.Else.(type) {
				case *ast.BlockStmt:
					return block(e, nest+1, loops)
				case *ast.IfStmt:
					return stmt(e, nest+1, loops)
				}
			}
		case *ast.ForStmt:
			if nest >= maxNesting {
				return "nesting_limit"
			}
			if why := stmt(x.Init, nest+1, loops); why != "" {
				return why
			}
			if why := stmt(x.Post, nest+1, loops+1); why != "" {
				return why
			}
			return block(x.Body, nest+1, loops+1)
		case *ast.RangeStmt:
			if nest >= maxNesting {
				return "nesting_limit"
			}
			return block(x.Body, nest+1, loops+1)
		}
		return ""
	}
	return block(fn.Body, 0, 0)
}

// 2026-11-07: 완성 가능한 호출만 임시로 닫아 타입이 확정된 변수 호출을 조기에 배제한다.
// 파싱이나 타입 판정이 불확실하면 미완성 경로를 보존한다.
func checkNonCallablePrefix(tokens []string) DeclarationReason {
	depth := 0
	parens := 0
	for _, tok := range tokens {
		switch tok {
		case "{":
			depth++
		case "}":
			depth--
		case "(":
			parens++
		case ")":
			parens--
		}
		if depth < 0 || parens < 0 {
			return ""
		}
	}
	if depth == 0 {
		return ""
	}
	source := strings.Join(tokens, " ")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "prefix.go", source+" 0"+strings.Repeat(" )", parens)+strings.Repeat(" }", depth), 0)
	if err != nil {
		return ""
	}
	info := types.Info{Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{Error: func(error) {}}
	_, _ = config.Check("prefix", fset, []*ast.File{file}, &info)
	var rejected bool
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || fset.Position(call.Lparen).Offset != len(source)-1 {
			return true
		}
		target := call.Fun
		for {
			paren, ok := target.(*ast.ParenExpr)
			if !ok {
				break
			}
			target = paren.X
		}
		id, ok := target.(*ast.Ident)
		if !ok {
			return true
		}
		obj, ok := info.Uses[id].(*types.Var)
		if !ok || obj.Type() == nil || obj.Type() == types.Typ[types.Invalid] {
			return true
		}
		_, callable := obj.Type().Underlying().(*types.Signature)
		rejected = !callable
		return true
	})
	if rejected {
		return "non_callable"
	}
	return ""
}
