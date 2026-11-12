package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"jevcancode/internal/spec"
	"jevcancode/internal/synth"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func allowedExpr(e ast.Expr, names map[string]bool) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return names[x.Name] || x.Name == "true" || x.Name == "false"
	case *ast.BasicLit:
		return x.Kind == token.INT
	case *ast.ParenExpr:
		return allowedExpr(x.X, names)
	case *ast.UnaryExpr:
		return (x.Op == token.SUB || x.Op == token.ADD || x.Op == token.NOT) && allowedExpr(x.X, names)
	case *ast.BinaryExpr:
		switch x.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO, token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ, token.LAND, token.LOR:
			return allowedExpr(x.X, names) && allowedExpr(x.Y, names)
		}
	}
	return false
}

type flowCheck struct {
	statements, locals, nodes          int
	maxLocals, maxStatements, maxDepth int
	used                               map[string]bool
}

func copyScope(scope map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range scope {
		out[k] = v
	}
	return out
}
func (v *flowCheck) expr(e ast.Expr, scope map[string]bool, depth int) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return v.expr(x.X, scope, depth)
	case *ast.Ident:
		v.nodes++
		return depth <= v.maxDepth && (scope[x.Name] || x.Name == "true" || x.Name == "false")
	case *ast.BasicLit:
		v.nodes++
		return depth <= v.maxDepth && x.Kind == token.INT
	case *ast.UnaryExpr:
		v.nodes++
		if lit, ok := x.X.(*ast.BasicLit); ok && lit.Kind == token.INT && (x.Op == token.SUB || x.Op == token.ADD) {
			return depth <= v.maxDepth
		}
		return depth <= v.maxDepth && (x.Op == token.SUB || x.Op == token.ADD || x.Op == token.NOT) && v.expr(x.X, scope, depth+1)
	case *ast.BinaryExpr:
		v.nodes++
		switch x.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO, token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ, token.LAND, token.LOR:
			return depth <= v.maxDepth && v.expr(x.X, scope, depth+1) && v.expr(x.Y, scope, depth+1)
		}
	}
	return false
}

// 2026-11-05: 분기별 스코프와 반환 여부를 추적해 지역변수 유출과 도달 불가능한 문장을 차단한다.
func (v *flowCheck) block(b *ast.BlockStmt, scope map[string]bool, locals map[string]bool, ifDepth int) (bool, bool) {
	fall := true
	for _, s := range b.List {
		if !fall {
			return false, false
		}
		v.statements++
		v.nodes++
		if v.statements > v.maxStatements || v.nodes > 63 {
			return false, false
		}
		switch x := s.(type) {
		case *ast.ReturnStmt:
			if len(x.Results) != 1 || !v.expr(x.Results[0], scope, 0) {
				return false, false
			}
			fall = false
		case *ast.AssignStmt:
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return false, false
			}
			id, ok := x.Lhs[0].(*ast.Ident)
			if !ok || !v.expr(x.Rhs[0], scope, 0) {
				return false, false
			}
			switch x.Tok {
			case token.DEFINE:
				index, err := strconv.Atoi(strings.TrimPrefix(id.Name, "v"))
				if !strings.HasPrefix(id.Name, "v") || err != nil || index < 0 || index >= 63 || id.Name != fmt.Sprintf("v%d", index) || scope[id.Name] || v.used[id.Name] || v.locals >= v.maxLocals {
					return false, false
				}
				scope[id.Name] = true
				locals[id.Name] = true
				v.used[id.Name] = true
				v.locals++
			case token.ASSIGN:
				if !locals[id.Name] {
					return false, false
				}
			default:
				return false, false
			}
		case *ast.IfStmt:
			other, ok := x.Else.(*ast.BlockStmt)
			if !ok || x.Init != nil || ifDepth >= 2 || !v.expr(x.Cond, scope, 0) {
				return false, false
			}
			a, valid := v.block(x.Body, copyScope(scope), copyScope(locals), ifDepth+1)
			if !valid {
				return false, false
			}
			b, valid := v.block(other, copyScope(scope), copyScope(locals), ifDepth+1)
			if !valid {
				return false, false
			}
			fall = a || b
		default:
			return false, false
		}
		if v.nodes > 63 {
			return false, false
		}
	}
	return fall, true
}
func Source(p spec.Problem, body string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	src := "package main\n" + synth.Function(p, body)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "generated.go", src, 0)
	if err != nil {
		return "", err
	}
	if len(f.Decls) != 1 {
		return "", fmt.Errorf("invalid declaration")
	}
	fn, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Name.Name != p.Name || fn.Recv != nil {
		return "", fmt.Errorf("invalid function")
	}
	names := map[string]bool{}
	for _, a := range p.Params {
		names[a.Name] = true
	}
	flow := flowCheck{used: map[string]bool{}, maxLocals: 2, maxStatements: 8, maxDepth: 4}
	if p.Budgets != nil {
		if p.Budgets.MaxDepth != nil {
			flow.maxDepth = *p.Budgets.MaxDepth
		}
		if p.Budgets.MaxLocals != nil {
			flow.maxLocals = *p.Budgets.MaxLocals
		}
		if p.Budgets.MaxStatements != nil {
			flow.maxStatements = *p.Budgets.MaxStatements
		}
	}
	fall, ok := flow.block(fn.Body, names, map[string]bool{}, 0)
	if !ok || fall {
		return "", fmt.Errorf("unsupported generated syntax")
	}
	conf := types.Config{}
	if _, err = conf.Check("generated", fset, []*ast.File{f}, nil); err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err = format.Node(&b, fset, f); err != nil {
		return "", err
	}
	return b.String(), nil
}

type limited struct{ b bytes.Buffer }

func (w *limited) Write(p []byte) (int, error) {
	n := len(p)
	if w.b.Len() < 8192 {
		remaining := 8192 - w.b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.b.Write(p)
	}
	return n, nil
}
func literal(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case json.Number:
		return string(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// 2026-11-05: 생성된 제한 문법만 실행하고 타임아웃을 적용하지만 OS sandbox를 제공하지 않는다.
func Check(ctx context.Context, p spec.Problem, body string, tests []spec.Test) (bool, error) {
	return check(ctx, p, body, tests, nil, nil, nil)
}
func CheckDetailed(ctx context.Context, p spec.Problem, body string, tests []spec.Test) (passed, attempted, compiled bool, err error) {
	passed, err = check(ctx, p, body, tests, &attempted, &compiled, nil)
	return
}

type Timing struct{ Compile, Execute time.Duration }

func CheckMeasured(ctx context.Context, p spec.Problem, body string, tests []spec.Test) (passed, attempted, compiled bool, timing Timing, err error) {
	passed, err = check(ctx, p, body, tests, &attempted, &compiled, &timing)
	return
}
func check(ctx context.Context, p spec.Problem, body string, tests []spec.Test, attempted, compiled *bool, timing *Timing) (bool, error) {
	src, err := Source(p, body)
	if err != nil {
		return false, err
	}
	if len(tests) == 0 {
		return false, fmt.Errorf("tests required")
	}
	dir, err := os.MkdirTemp("", "jevcancoding-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	var h strings.Builder
	alias := "_jev_runtime"
	for alias == p.Name {
		alias += "_"
	}
	fmt.Fprintf(&h, "package main\nimport %s \"os\"\nfunc main(){\n", alias)
	for _, t := range tests {
		if len(t.Args) != len(p.Params) {
			return false, fmt.Errorf("argument count")
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = literal(a)
			if args[i] == "" {
				return false, fmt.Errorf("invalid value")
			}
		}
		want := literal(t.Want)
		if want == "" {
			return false, fmt.Errorf("invalid value")
		}
		fmt.Fprintf(&h, "if %s(%s) != %s { %s.Exit(1) }\n", p.Name, strings.Join(args, ","), want, alias)
	}
	h.WriteString("}\n")
	if err = os.WriteFile(filepath.Join(dir, "generated.go"), []byte(src), 0600); err != nil {
		return false, err
	}
	if err = os.WriteFile(filepath.Join(dir, "main.go"), []byte(h.String()), 0600); err != nil {
		return false, err
	}
	exe := filepath.Join(dir, "candidate.exe")
	var out limited
	goPath, err := exec.LookPath("go")
	if err != nil {
		return false, fmt.Errorf("go executable not found")
	}
	goPath, err = filepath.Abs(goPath)
	if err != nil {
		return false, fmt.Errorf("invalid go executable path")
	}
	cmd := exec.CommandContext(ctx, goPath, "build", "-o", exe, filepath.Join(dir, "generated.go"), filepath.Join(dir, "main.go"))
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Env = compileEnv(dir)
	if attempted != nil {
		*attempted = true
	}
	started := time.Now()
	err = cmd.Run()
	if timing != nil {
		timing.Compile = time.Since(started)
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("compile failed: %s", out.b.String())
	}
	if compiled != nil {
		*compiled = true
	}
	cmd = exec.CommandContext(ctx, exe)
	cmd.Env = executeEnv(dir)
	cmd.Stdout = &out
	cmd.Stderr = &out
	started = time.Now()
	err = cmd.Run()
	if timing != nil {
		timing.Execute = time.Since(started)
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
