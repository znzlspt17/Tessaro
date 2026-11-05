package tokenverify

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"jevcancoding/internal/tokenspec"
	"strconv"
)

type checker struct{ statements, maxStatements, maxNesting int }

func limit(n, def, cap int) int {
	if n <= 0 {
		return def
	}
	if n > cap {
		return cap
	}
	return n
}
func allowedType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return allowedType(x.X)
	case *ast.Ident:
		switch x.Name {
		case "int", "bool", "string", "byte", "rune":
			return true
		}
	case *ast.ArrayType:
		if x.Len != nil {
			return false
		}
		id, ok := x.Elt.(*ast.Ident)
		return ok && (id.Name == "int" || id.Name == "string" || id.Name == "byte" || id.Name == "rune")
	case *ast.MapType:
		k, ok := x.Key.(*ast.Ident)
		if !ok || k.Name != "string" {
			return false
		}
		v, ok := x.Value.(*ast.Ident)
		return ok && (v.Name == "int" || v.Name == "string")
	}
	return false
}
func (c *checker) expr(e ast.Expr) bool {
	if e == nil {
		return true
	}
	switch x := e.(type) {
	case *ast.Ident:
		return true
	case *ast.BasicLit:
		return x.Kind == token.INT || x.Kind == token.STRING || x.Kind == token.CHAR
	case *ast.ParenExpr:
		return c.expr(x.X)
	case *ast.BinaryExpr:
		switch x.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO, token.REM, token.AND, token.OR, token.XOR, token.SHL, token.SHR, token.AND_NOT, token.LAND, token.LOR, token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return c.expr(x.X) && c.expr(x.Y)
		}
		return false
	case *ast.UnaryExpr:
		return (x.Op == token.ADD || x.Op == token.SUB || x.Op == token.NOT || x.Op == token.XOR) && c.expr(x.X)
	case *ast.IndexExpr:
		return c.expr(x.X) && c.expr(x.Index)
	case *ast.SliceExpr:
		return c.expr(x.X) && c.expr(x.Low) && c.expr(x.High) && c.expr(x.Max)
	case *ast.CompositeLit:
		if !allowedType(x.Type) {
			return false
		}
		for _, v := range x.Elts {
			switch kv := v.(type) {
			case *ast.KeyValueExpr:
				if !c.expr(kv.Key) || !c.expr(kv.Value) {
					return false
				}
			default:
				if !c.expr(v) {
					return false
				}
			}
		}
		return true
	case *ast.CallExpr:
		if x.Ellipsis.IsValid() && len(x.Args) == 0 {
			return false
		}
		switch fn := x.Fun.(type) {
		case *ast.Ident:
			switch fn.Name {
			case "len", "cap", "append", "copy", "delete", "make", "int", "bool", "string", "byte", "rune":
			default:
				return false
			}
			if fn.Name == "make" {
				if len(x.Args) < 1 || !allowedType(x.Args[0]) {
					return false
				}
				for _, v := range x.Args[1:] {
					if !c.expr(v) {
						return false
					}
					if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.INT {
						n, e := strconv.ParseInt(lit.Value, 0, 64)
						if e != nil || n > 4096 {
							return false
						}
					}
				}
				return true
			}
		default:
			if !allowedType(x.Fun) {
				return false
			}
		}
		for _, a := range x.Args {
			if !c.expr(a) {
				return false
			}
		}
		return true
	}
	return false
}
func (c *checker) lhs(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return true
	case *ast.IndexExpr:
		return c.expr(x.X) && c.expr(x.Index)
	}
	return false
}
func (c *checker) block(b *ast.BlockStmt, nest, loops int) bool {
	if nest > c.maxNesting {
		return false
	}
	for _, s := range b.List {
		if !c.stmt(s, nest, loops) {
			return false
		}
	}
	return true
}
func (c *checker) stmt(s ast.Stmt, nest, loops int) bool {
	if s == nil {
		return true
	}
	c.statements++
	if c.statements > c.maxStatements {
		return false
	}
	switch x := s.(type) {
	case *ast.ReturnStmt:
		if len(x.Results) == 0 {
			return true
		}
		if len(x.Results) != 1 {
			return false
		}
		return c.expr(x.Results[0])
	case *ast.AssignStmt:
		switch x.Tok {
		case token.ASSIGN, token.DEFINE, token.ADD_ASSIGN, token.SUB_ASSIGN, token.MUL_ASSIGN, token.QUO_ASSIGN, token.REM_ASSIGN, token.AND_ASSIGN, token.OR_ASSIGN, token.XOR_ASSIGN, token.SHL_ASSIGN, token.SHR_ASSIGN, token.AND_NOT_ASSIGN:
		default:
			return false
		}
		for _, v := range x.Lhs {
			if !c.lhs(v) {
				return false
			}
		}
		for _, v := range x.Rhs {
			if !c.expr(v) {
				return false
			}
		}
		return true
	case *ast.IncDecStmt:
		return c.lhs(x.X)
	case *ast.ExprStmt:
		_, ok := x.X.(*ast.CallExpr)
		return ok && c.expr(x.X)
	case *ast.DeclStmt:
		d, ok := x.Decl.(*ast.GenDecl)
		if !ok || d.Tok != token.VAR {
			return false
		}
		for _, s := range d.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok || len(v.Names) == 0 || len(v.Values) != len(v.Names) || v.Type != nil && !allowedType(v.Type) {
				return false
			}
			for _, e := range v.Values {
				if !c.expr(e) {
					return false
				}
			}
		}
		return true
	case *ast.IfStmt:
		if nest >= c.maxNesting || !c.stmt(x.Init, nest+1, loops) || !c.expr(x.Cond) || !c.block(x.Body, nest+1, loops) {
			return false
		}
		if x.Else != nil {
			switch e := x.Else.(type) {
			case *ast.BlockStmt:
				return c.block(e, nest+1, loops)
			case *ast.IfStmt:
				return c.stmt(e, nest+1, loops)
			default:
				return false
			}
		}
		return true
	case *ast.ForStmt:
		if nest >= c.maxNesting || !c.stmt(x.Init, nest+1, loops) || !c.expr(x.Cond) || !c.stmt(x.Post, nest+1, loops+1) {
			return false
		}
		return c.block(x.Body, nest+1, loops+1)
	case *ast.RangeStmt:
		if nest >= c.maxNesting || !c.expr(x.X) || !c.lhsOrNil(x.Key) || !c.lhsOrNil(x.Value) {
			return false
		}
		if x.Tok != token.ILLEGAL && x.Tok != token.ASSIGN && x.Tok != token.DEFINE {
			return false
		}
		return c.block(x.Body, nest+1, loops+1)
	case *ast.BranchStmt:
		return loops > 0 && x.Label == nil && (x.Tok == token.BREAK || x.Tok == token.CONTINUE)
	case *ast.BlockStmt:
		return c.block(x, nest+1, loops)
	case *ast.EmptyStmt:
		return true
	}
	return false
}
func (c *checker) lhsOrNil(e ast.Expr) bool { return e == nil || c.lhs(e) }
func supportedInternal(t types.Type) bool {
	switch x := t.Underlying().(type) {
	case *types.Basic:
		switch x.Kind() {
		case types.Int, types.Bool, types.String, types.Uint8, types.Int32:
			return true
		}
	case *types.Slice:
		switch e := x.Elem().Underlying().(type) {
		case *types.Basic:
			return e.Kind() == types.Int || e.Kind() == types.String || e.Kind() == types.Uint8 || e.Kind() == types.Int32
		}
	case *types.Map:
		k, ok := x.Key().Underlying().(*types.Basic)
		if !ok || k.Kind() != types.String {
			return false
		}
		v, ok := x.Elem().Underlying().(*types.Basic)
		return ok && (v.Kind() == types.Int || v.Kind() == types.String)
	}
	return false
}

// 2026-11-05: 컴파일 전 AST와 타입을 검증하고 주석을 제거한 정규화 소스만 실행한다.
func Validate(p tokenspec.Problem, source string) (*Binding, error) {
	if len(source) > 128*1024 {
		return nil, fmt.Errorf("source too large")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	contract, result, err := p.Contract()
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "candidate.go", source, 0)
	if err != nil {
		return nil, fmt.Errorf("invalid source")
	}
	if f.Name.Name != "main" || len(f.Imports) != 0 || len(f.Decls) != 1 {
		return nil, fmt.Errorf("single main package function required")
	}
	fn, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || fn.Name.Name == "main" || fn.Name.Name == "init" || fn.Body == nil {
		return nil, fmt.Errorf("invalid function")
	}
	c := checker{maxStatements: limit(p.Budgets.MaxStatements, 32, 128), maxNesting: limit(p.Budgets.MaxNesting, 4, 8)}
	if !c.block(fn.Body, 0, 0) {
		return nil, fmt.Errorf("unsupported statement or expression")
	}
	info := types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{}
	if _, err = conf.Check("candidate", fset, []*ast.File{f}, &info); err != nil {
		return nil, fmt.Errorf("type check failed")
	}
	obj, ok := info.Defs[fn.Name].(*types.Func)
	if !ok {
		return nil, fmt.Errorf("invalid function signature")
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return nil, fmt.Errorf("signature mismatch")
	}
	names, params, reason := signatureMatch(sig, contract, result, true)
	if reason != "" {
		return nil, fmt.Errorf("signature mismatch")
	}
	locals := 0
	for id, o := range info.Defs {
		if o == nil || id == fn.Name {
			continue
		}
		if v, ok := o.(*types.Var); ok {
			if id.Name == "_" {
				continue
			}
			if !supportedInternal(v.Type()) {
				return nil, fmt.Errorf("unsupported local type")
			}
			if !params[v] {
				locals++
			}
		}
	}
	maxLocals := limit(p.Budgets.MaxLocals, 8, 63)
	if p.Budgets.MaxLocalsSet && p.Budgets.MaxLocals == 0 {
		maxLocals = 0
	}
	if locals > maxLocals {
		return nil, fmt.Errorf("local budget exceeded")
	}
	validCalls := true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		o := info.Uses[id]
		if o == nil || types.Universe.Lookup(id.Name) != o {
			validCalls = false
		}
		if id.Name == "make" {
			for _, arg := range call.Args[1:] {
				if tv, ok := info.Types[arg]; ok && tv.Value != nil {
					n, ok := constant.Int64Val(tv.Value)
					if !ok || n < 0 || n > 4096 {
						validCalls = false
					}
				}
			}
		}
		return true
	})
	if !validCalls {
		return nil, fmt.Errorf("unsupported call target")
	}
	var out bytes.Buffer
	if err = format.Node(&out, fset, f); err != nil {
		return nil, fmt.Errorf("format failed")
	}
	return &Binding{Name: fn.Name.Name, Params: names, ReturnType: result, Locals: locals, Source: out.String()}, nil
}
