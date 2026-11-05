package synth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"jevcancoding/internal/rank"
	"jevcancoding/internal/spec"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Beam, MaxCandidates, MaxCalls, MaxDepth int
	MaxLocals, MaxStatements                int
	LocalLimitSet                           bool
	Strict                                  bool
	Trace                                   func(TraceEvent)
	TraceCode                               bool
}
type TraceEvent struct {
	ID        string   `json:"id"`
	ParentID  string   `json:"parent_id,omitempty"`
	Score     *float64 `json:"score,omitempty"`
	Reason    string   `json:"reason"`
	Code      string   `json:"code,omitempty"`
	Step      int      `json:"step"`
	Rank      int      `json:"rank"`
	ElapsedMS float64  `json:"elapsed_ms"`
}
type Result struct {
	Code            string `json:"code"`
	Source          string `json:"source"`
	Candidates      int    `json:"candidates"`
	Calls           int    `json:"calls"`
	CompileAttempts int    `json:"compile_attempts"`
	UsedLocals      int    `json:"used_locals"`
}

var ErrBudget = errors.New("search budget exhausted")
var ErrNoSolution = errors.New("no solution found")

type node struct {
	kind, typ, text string
	depth           int
	ifDepth         int
	symbolID        int
	id              string
	scope           []localSymbol
	allowStop       bool
	children        []*node
}
type localSymbol struct {
	id        int
	name, typ string
}

func clone(n *node) *node {
	x := *n
	x.scope = append([]localSymbol(nil), n.scope...)
	x.children = make([]*node, len(n.children))
	for i, c := range n.children {
		x.children[i] = clone(c)
	}
	return &x
}
func hole(t string, d int) *node { return &node{kind: "hole", typ: t, depth: d} }
func scopedHole(t string, d int, scope []localSymbol) *node {
	n := hole(t, d)
	n.scope = append([]localSymbol(nil), scope...)
	return n
}
func stmtHole(scope []localSymbol, ifDepth int, stop bool) *node {
	n := scopedHole("stmt", 0, scope)
	n.ifDepth = ifDepth
	n.allowStop = stop
	return n
}
func count(n *node) int {
	total := 1
	for _, c := range n.children {
		total += count(c)
	}
	return total
}
func find(n *node) *node {
	if n.kind == "hole" {
		return n
	}
	for _, c := range n.children {
		if x := find(c); x != nil {
			return x
		}
	}
	return nil
}
func render(n *node) string {
	switch n.kind {
	case "hole":
		return fmt.Sprintf("<%s depth=%d if_depth=%d>", n.typ, n.depth, n.ifDepth)
	case "atom":
		return n.text
	case "symbol":
		return n.text
	case "local":
		return n.text
	case "empty":
		return ""
	case "decl", "assign":
		op := ":="
		if n.kind == "assign" {
			op = "="
		}
		return n.text + " " + op + " " + render(n.children[0]) + "; " + render(n.children[1])
	case "ifseq":
		return "if " + render(n.children[0]) + " { " + render(n.children[1]) + " } else { " + render(n.children[2]) + " }; " + render(n.children[3])
	case "unary":
		return "(!" + render(n.children[0]) + ")"
	case "bin":
		return "(" + render(n.children[0]) + " " + n.text + " " + render(n.children[1]) + ")"
	case "return":
		return "return " + render(n.children[0])
	case "if":
		return "if " + render(n.children[0]) + " { " + render(n.children[1]) + " } else { " + render(n.children[2]) + " }"
	}
	panic("bad node")
}
func choices(h *node, p spec.Problem, maxDepth int) []*node {
	d := h.depth
	if h.typ == "stmt" {
		out := []*node{{kind: "return", children: []*node{scopedHole(p.ReturnType, d, h.scope)}}}
		if h.allowStop {
			out = append(out, &node{kind: "empty"})
		}
		if h.ifDepth < 2 {
			left, right := scopedHole("stmt", 0, h.scope), scopedHole("stmt", 0, h.scope)
			left.ifDepth = h.ifDepth + 1
			right.ifDepth = h.ifDepth + 1
			out = append(out, &node{kind: "if", children: []*node{scopedHole("bool", 0, h.scope), left, right}})
		}
		return out
	}
	var out []*node
	for i, v := range p.Params {
		if v.Type == h.typ {
			out = append(out, &node{kind: "symbol", symbolID: i, text: v.Name})
		}
	}
	for _, v := range h.scope {
		if v.typ == h.typ {
			out = append(out, &node{kind: "local", text: v.name, symbolID: v.id})
		}
	}
	if h.typ == "int" {
		for _, c := range p.Constants {
			out = append(out, &node{kind: "atom", text: strconv.Itoa(c)})
		}
	} else {
		out = append(out, &node{kind: "atom", text: "true"}, &node{kind: "atom", text: "false"})
	}
	if d >= maxDepth {
		return out
	}
	if h.typ == "int" {
		for _, op := range []string{"+", "-", "*", "/"} {
			out = append(out, &node{kind: "bin", text: op, children: []*node{scopedHole("int", d+1, h.scope), scopedHole("int", d+1, h.scope)}})
		}
	} else {
		out = append(out, &node{kind: "unary", children: []*node{scopedHole("bool", d+1, h.scope)}})
		for _, op := range []string{"<", "<=", ">", ">=", "==", "!="} {
			out = append(out, &node{kind: "bin", text: op, children: []*node{scopedHole("int", d+1, h.scope), scopedHole("int", d+1, h.scope)}})
		}
		for _, op := range []string{"&&", "||"} {
			out = append(out, &node{kind: "bin", text: op, children: []*node{scopedHole("bool", d+1, h.scope), scopedHole("bool", d+1, h.scope)}})
		}
	}
	return out
}
func statistics(n *node) (int, int, map[string]bool) {
	statements, locals := 0, 0
	used := map[string]bool{}
	var walk func(*node)
	walk = func(x *node) {
		switch x.kind {
		case "return", "if", "ifseq", "assign":
			statements++
		case "decl":
			statements++
			locals++
			used[x.text] = true
		}
		for _, c := range x.children {
			walk(c)
		}
	}
	walk(n)
	return statements, locals, used
}
func fallsThrough(n *node) bool {
	switch n.kind {
	case "return":
		return false
	case "if":
		return fallsThrough(n.children[1]) || fallsThrough(n.children[2])
	case "decl", "assign":
		return fallsThrough(n.children[1])
	case "ifseq":
		return (fallsThrough(n.children[1]) || fallsThrough(n.children[2])) && fallsThrough(n.children[3])
	default:
		return true
	}
}
func reachable(n *node) bool {
	if n.kind == "ifseq" && !fallsThrough(n.children[1]) && !fallsThrough(n.children[2]) {
		return false
	}
	for _, c := range n.children {
		if !reachable(c) {
			return false
		}
	}
	return true
}
func moreChoices(h *node, p spec.Problem, root *node, maxLocals int) []*node {
	if h.typ != "stmt" {
		return nil
	}
	_, usedCount, used := statistics(root)
	for _, v := range p.Params {
		used[v.Name] = true
	}
	used[p.Name] = true
	var out []*node
	if usedCount < maxLocals {
		for i := 0; i < 63; i++ {
			name := fmt.Sprintf("v%d", i)
			if !used[name] {
				for _, typ := range []string{"int", "bool"} {
					scope := append(append([]localSymbol(nil), h.scope...), localSymbol{id: i, name: name, typ: typ})
					out = append(out, &node{kind: "decl", text: name, symbolID: i, children: []*node{scopedHole(typ, 0, h.scope), stmtHole(scope, h.ifDepth, h.allowStop)}})
				}
				break
			}
		}
	}
	for _, v := range h.scope {
		out = append(out, &node{kind: "assign", text: v.name, symbolID: v.id, children: []*node{scopedHole(v.typ, 0, h.scope), stmtHole(h.scope, h.ifDepth, h.allowStop)}})
	}
	if h.ifDepth < 2 {
		out = append(out, &node{kind: "ifseq", children: []*node{scopedHole("bool", 0, h.scope), stmtHole(h.scope, h.ifDepth+1, true), stmtHole(h.scope, h.ifDepth+1, true), stmtHole(h.scope, h.ifDepth, h.allowStop)}})
	}
	return out
}
func astExpr(p spec.Problem, n *node) ast.Expr {
	switch n.kind {
	case "symbol":
		return ast.NewIdent(p.Params[n.symbolID].Name)
	case "local":
		return ast.NewIdent(fmt.Sprintf("v%d", n.symbolID))
	case "atom":
		if n.text == "true" || n.text == "false" {
			return ast.NewIdent(n.text)
		}
		if strings.HasPrefix(n.text, "-") {
			return &ast.UnaryExpr{Op: token.SUB, X: &ast.BasicLit{Kind: token.INT, Value: n.text[1:]}}
		}
		return &ast.BasicLit{Kind: token.INT, Value: n.text}
	case "unary":
		return &ast.UnaryExpr{Op: token.NOT, X: astExpr(p, n.children[0])}
	case "bin":
		ops := map[string]token.Token{"+": token.ADD, "-": token.SUB, "*": token.MUL, "/": token.QUO, "<": token.LSS, "<=": token.LEQ, ">": token.GTR, ">=": token.GEQ, "==": token.EQL, "!=": token.NEQ, "&&": token.LAND, "||": token.LOR}
		return &ast.ParenExpr{X: &ast.BinaryExpr{X: astExpr(p, n.children[0]), Op: ops[n.text], Y: astExpr(p, n.children[1])}}
	}
	panic("incomplete expression")
}
func astStmt(p spec.Problem, n *node) ast.Stmt {
	if n.kind == "return" {
		return &ast.ReturnStmt{Results: []ast.Expr{astExpr(p, n.children[0])}}
	}
	if n.kind == "if" {
		return &ast.IfStmt{Cond: astExpr(p, n.children[0]), Body: &ast.BlockStmt{List: []ast.Stmt{astStmt(p, n.children[1])}}, Else: &ast.BlockStmt{List: []ast.Stmt{astStmt(p, n.children[2])}}}
	}
	panic("incomplete statement")
}
func astStatements(p spec.Problem, n *node) []ast.Stmt {
	switch n.kind {
	case "empty":
		return nil
	case "decl", "assign":
		tok := token.DEFINE
		if n.kind == "assign" {
			tok = token.ASSIGN
		}
		head := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(n.text)}, Tok: tok, Rhs: []ast.Expr{astExpr(p, n.children[0])}}
		return append([]ast.Stmt{head}, astStatements(p, n.children[1])...)
	case "ifseq":
		head := &ast.IfStmt{Cond: astExpr(p, n.children[0]), Body: &ast.BlockStmt{List: astStatements(p, n.children[1])}, Else: &ast.BlockStmt{List: astStatements(p, n.children[2])}}
		return append([]ast.Stmt{head}, astStatements(p, n.children[3])...)
	case "if":
		return []ast.Stmt{&ast.IfStmt{Cond: astExpr(p, n.children[0]), Body: &ast.BlockStmt{List: astStatements(p, n.children[1])}, Else: &ast.BlockStmt{List: astStatements(p, n.children[2])}}}
	default:
		return []ast.Stmt{astStmt(p, n)}
	}
}
func astSource(p spec.Problem, n *node) (string, error) {
	fields := make([]*ast.Field, 0, len(p.Params))
	for _, v := range p.Params {
		fields = append(fields, &ast.Field{Names: []*ast.Ident{ast.NewIdent(v.Name)}, Type: ast.NewIdent(v.Type)})
	}
	fn := &ast.FuncDecl{Name: ast.NewIdent(p.Name), Type: &ast.FuncType{Params: &ast.FieldList{List: fields}, Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent(p.ReturnType)}}}}, Body: &ast.BlockStmt{List: astStatements(p, n)}}
	f := &ast.File{Name: ast.NewIdent("main"), Decls: []ast.Decl{fn}}
	var b bytes.Buffer
	err := format.Node(&b, token.NewFileSet(), f)
	return b.String(), err
}

// 2026-11-05: 미완성 트리를 공동 순위화하여 서로 다른 호출 점수를 합산하지 않는다.
func Search(ctx context.Context, p spec.Problem, r rank.Ranker, o Options, check func(string) (bool, error)) (Result, error) {
	var result Result
	if err := p.Validate(); err != nil {
		return result, err
	}
	if o.Beam != 1 && o.Beam != 3 {
		return result, fmt.Errorf("beam must be 1 or 3")
	}
	if o.MaxCandidates <= 0 {
		o.MaxCandidates = 2000
	}
	if o.MaxCalls <= 0 {
		o.MaxCalls = 200
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = 4
	}
	if !o.LocalLimitSet {
		o.MaxLocals = 2
	}
	if o.MaxLocals < 0 || o.MaxLocals > 63 {
		return result, fmt.Errorf("invalid locals limit")
	}
	if o.MaxStatements <= 0 {
		o.MaxStatements = 8
	}
	if o.MaxStatements > 63 {
		return result, fmt.Errorf("invalid statements limit")
	}
	promptBytes, _ := json.Marshal(p)
	prompt := string(promptBytes)
	started := time.Now()
	step := 0
	root := hole("stmt", 0)
	root.id = "root"
	states := []*node{root}
	seen := map[string]bool{}
	for len(states) > 0 {
		step++
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var next []*node
		var cs []rank.Candidate
		for _, state := range states {
			h := find(state)
			if h == nil {
				continue
			}
			options := append(choices(h, p, o.MaxDepth), moreChoices(h, p, state, o.MaxLocals)...)
			for _, choice := range options {
				if result.Candidates >= o.MaxCandidates {
					return result, ErrBudget
				}
				n := clone(state)
				target := find(n)
				*target = *choice
				statements, locals, _ := statistics(n)
				if count(n) > 63 || statements > o.MaxStatements || locals > o.MaxLocals || !reachable(n) {
					continue
				}
				key := render(n)
				if seen[key] {
					continue
				}
				seen[key] = true
				result.Candidates++
				n.id = fmt.Sprintf("%08d", result.Candidates)
				next = append(next, n)
				cs = append(cs, rank.Candidate{ID: n.id, ParentID: state.id, Code: key, Size: count(n)})
			}
		}
		if len(cs) == 0 {
			break
		}
		ordered := cs
		scoreMap := map[string]float64{}
		if len(cs) > 1 {
			if result.Calls >= o.MaxCalls {
				return result, ErrBudget
			}
			result.Calls++
			scores, err := r.Rank(ctx, prompt, cs)
			if err != nil {
				return result, err
			}
			for _, s := range scores {
				scoreMap[s.ID] = s.Score
			}
			ordered, err = rank.Order(cs, scores)
			if err != nil {
				return result, err
			}
		}
		byID := map[string]*node{}
		for i, c := range cs {
			byID[c.ID] = next[i]
		}
		if o.Trace != nil {
			for i, c := range ordered {
				reason := "ranked"
				if o.Strict && i > 0 {
					reason = "pruned_strict"
				}
				event := TraceEvent{ID: c.ID, ParentID: c.ParentID, Reason: reason, Step: step, Rank: i + 1, ElapsedMS: float64(time.Since(started)) / float64(time.Millisecond)}
				if score, ok := scoreMap[c.ID]; ok {
					event.Score = &score
				}
				if o.TraceCode {
					event.Code = c.Code
				}
				o.Trace(event)
			}
		}
		states = nil
		for idx, c := range ordered {
			if o.Strict && idx > 0 {
				break
			}
			emit := func(reason string) {
				if o.Trace == nil {
					return
				}
				event := TraceEvent{ID: c.ID, ParentID: c.ParentID, Reason: reason, Step: step, Rank: idx + 1, ElapsedMS: float64(time.Since(started)) / float64(time.Millisecond)}
				if score, ok := scoreMap[c.ID]; ok {
					event.Score = &score
				}
				if o.TraceCode {
					event.Code = c.Code
				}
				o.Trace(event)
			}
			n := byID[c.ID]
			if find(n) == nil {
				result.CompileAttempts++
				ok, err := check(render(n))
				if err != nil {
					return result, err
				}
				if ok {
					emit("public_passed")
					result.Code = render(n)
					_, result.UsedLocals, _ = statistics(n)
					result.Source, err = astSource(p, n)
					if err != nil {
						return result, err
					}
					return result, nil
				}
				emit("public_failed")
				if o.Strict {
					return result, ErrNoSolution
				}
				continue
			}
			if len(states) < o.Beam {
				states = append(states, n)
				emit("active")
			} else {
				emit("pruned")
			}
		}
	}
	return result, ErrNoSolution
}
func Function(p spec.Problem, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "func %s(", p.Name)
	for i, a := range p.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %s", a.Name, a.Type)
	}
	fmt.Fprintf(&b, ") %s { %s }", p.ReturnType, body)
	return b.String()
}
