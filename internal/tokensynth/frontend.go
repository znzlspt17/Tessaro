package tokensynth

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"jevcancoding/internal/grammar"
	"jevcancoding/internal/tokenlex"
	"jevcancoding/internal/tokenspec"
	"jevcancoding/internal/tokenverify"
	grammardata "jevcancoding/third_party/tree-sitter-go"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var loadOnce sync.Once
var loaded *grammar.Grammar
var loadError error

func Grammar() (*grammar.Grammar, error) {
	loadOnce.Do(func() { loaded, loadError = grammar.Load(grammardata.Grammar) })
	return loaded, loadError
}

type State struct {
	ID                  string
	ParentID            string
	Tokens              []string
	Parser              *grammar.State
	Forced, Selected    int
	parametersChecked   bool
	declarationAccepted bool
	declarationRejected tokenverify.DeclarationReason
	header              *headerState
}
type Choice struct {
	Lexeme, Kind, Rule string
	header             *headerState
	check              tokenverify.DeclarationCheck
	parent             *State
}
type Rejection struct {
	Choice Choice
	Reason tokenverify.DeclarationReason
}

func (s *State) Source() string { return strings.Join(s.Tokens, " ") }
func NewState() (*State, error) {
	g, e := Grammar()
	if e != nil {
		return nil, e
	}
	return &State{ID: "root", Parser: g.NewState("source_file")}, nil
}
func (s *State) Advance(c Choice, id string) (*State, error) {
	if s == nil || s.Parser == nil {
		return nil, fmt.Errorf("invalid token state")
	}
	if c.parent != nil && c.parent != s {
		return nil, fmt.Errorf("candidate belongs to another state")
	}
	if _, e := tokenlex.Kind(c.Lexeme); e != nil {
		return nil, e
	}
	next, e := s.Parser.Advance(c.Lexeme)
	if e != nil {
		return nil, e
	}
	header := c.header
	if header == nil && s.header != nil {
		var reason tokenverify.DeclarationReason
		var err error
		header, reason, err = s.header.advance(c.Lexeme)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			return nil, fmt.Errorf("declaration candidate rejected")
		}
	}
	child := &State{ID: id, ParentID: s.ID, Tokens: append(append([]string(nil), s.Tokens...), c.Lexeme), Parser: next, Forced: s.Forced, Selected: s.Selected, parametersChecked: s.parametersChecked, declarationAccepted: s.declarationAccepted, declarationRejected: s.declarationRejected, header: header}
	child.rememberDeclaration(c.check)
	return child, nil
}
func (s *State) rememberDeclaration(c tokenverify.DeclarationCheck) {
	if c.ParametersChecked {
		s.parametersChecked = true
	}
	if c.Status == tokenverify.DeclarationAccepted {
		s.declarationAccepted = true
	}
	if c.Status == tokenverify.DeclarationRejected {
		s.declarationRejected = c.Reason
	}
}

// 2026-11-05: 확정된 선언 판정은 후보 상태에 보관하여 본문 확장 시 재검사하지 않는다.
func (s *State) checkDeclaration(p tokenspec.Problem) (tokenverify.DeclarationCheck, error) {
	_, reason, e := semanticFor(p, s)
	if e != nil {
		return tokenverify.DeclarationCheck{}, e
	}
	if reason != "" {
		return tokenverify.DeclarationCheck{Status: tokenverify.DeclarationRejected, Reason: reason}, nil
	}
	if s.declarationRejected != "" {
		return tokenverify.DeclarationCheck{Status: tokenverify.DeclarationRejected, Reason: s.declarationRejected, ParametersChecked: true}, nil
	}
	if s.declarationAccepted {
		return tokenverify.DeclarationCheck{Status: tokenverify.DeclarationAccepted, ParametersChecked: true}, nil
	}
	if s.parametersChecked && (len(s.Tokens) == 0 || s.Tokens[len(s.Tokens)-1] != "{") {
		return tokenverify.DeclarationCheck{Status: tokenverify.DeclarationPending, ParametersChecked: true}, nil
	}
	c, e := tokenverify.CheckDeclaration(p, s.Tokens)
	if e == nil {
		s.rememberDeclaration(c)
	}
	return c, e
}
func (s *State) checkCandidateDeclaration(p tokenspec.Problem, choice Choice) (tokenverify.DeclarationCheck, error) {
	if choice.header != nil {
		return choice.check, nil
	}
	h, reason, e := semanticFor(p, s)
	if e != nil {
		return tokenverify.DeclarationCheck{}, e
	}
	if reason != "" {
		return tokenverify.DeclarationCheck{Status: tokenverify.DeclarationRejected, Reason: reason}, nil
	}
	_, reason, e = h.advance(choice.Lexeme)
	if e != nil {
		return tokenverify.DeclarationCheck{}, e
	}
	if reason != "" {
		return tokenverify.DeclarationCheck{Status: tokenverify.DeclarationRejected, Reason: reason}, nil
	}
	tokens := append(append([]string(nil), s.Tokens...), choice.Lexeme)
	return tokenverify.CheckDeclaration(p, tokens)
}

var allowedLiteral = map[string]bool{}

func init() {
	for _, v := range strings.Fields("package func var if else for range return map break continue ( ) { } [ ] , ; : ... := = + - * / % == != < <= > >= && || ! ++ -- += -= *= /= %=") {
		allowedLiteral[v] = true
	}
}
func functionName(p tokenspec.Problem) string {
	used := map[string]bool{}
	for _, n := range p.Inputs {
		used[n] = true
	}
	for i := 0; ; i++ {
		n := fmt.Sprintf("f%d", i)
		if !used[n] {
			return n
		}
	}
}

type prefix struct {
	funcAt, paramsEnd, bodyAt int
	visible                   map[string]bool
	declared                  map[string]bool
	fresh                     string
}

func describe(p tokenspec.Problem, s *State) prefix {
	info := prefix{funcAt: -1, paramsEnd: -1, bodyAt: -1, visible: map[string]bool{}, declared: map[string]bool{}}
	level := 0
	for i, v := range s.Tokens {
		if info.funcAt < 0 && v == "func" {
			info.funcAt = i
			continue
		}
		if info.funcAt >= 0 && i > info.funcAt+1 && info.paramsEnd < 0 {
			if v == "(" {
				level++
			}
			if v == ")" {
				level--
				if level == 0 {
					info.paramsEnd = i
				}
			}
			if level > 0 {
				for _, n := range p.Inputs {
					if v == n {
						info.visible[n] = true
					}
				}
			}
		}
		if info.paramsEnd >= 0 && i > info.paramsEnd && v == "{" && info.bodyAt < 0 {
			info.bodyAt = i
		}
		if i+1 < len(s.Tokens) && (s.Tokens[i+1] == ":=" || v == "var") {
			if v == "var" {
				info.declared[s.Tokens[i+1]] = true
			} else {
				info.declared[v] = true
			}
		}
	}
	if info.bodyAt >= 0 {
		for n := range info.visible {
			delete(info.visible, n)
		}
		for n := range visibleNames(s.Source()) {
			info.visible[n] = true
		}
	}
	used := map[string]bool{functionName(p): true}
	for _, n := range p.Inputs {
		used[n] = true
	}
	for n := range info.declared {
		used[n] = true
	}
	// 아직 :=를 선택하지 않은 다중 선언의 이름도 다음 새 이름과 충돌하지 않는다.
	for _, name := range s.Tokens {
		if strings.HasPrefix(name, "v") {
			if _, err := strconv.Atoi(strings.TrimPrefix(name, "v")); err == nil {
				used[name] = true
			}
		}
	}
	for i := 0; ; i++ {
		name := fmt.Sprintf("v%d", i)
		if !used[name] {
			info.fresh = name
			break
		}
	}
	return info
}

// 2026-11-05: 다음 구문 후보는 공개 문법에서 얻고, 이 계층은 실행 프로필과 유한한 값만 공급한다.
func rawNext(p tokenspec.Problem, s *State) []Choice {
	info := describe(p, s)
	out := map[string]Choice{}
	for _, terminal := range s.Parser.Next() {
		var values []string
		if terminal.Kind == "literal" {
			if allowedLiteral[terminal.Value] {
				values = []string{terminal.Value}
			}
		} else {
			switch terminal.Value {
			case "identifier":
				if len(s.Tokens) == 1 && s.Tokens[0] == "package" {
					values = []string{"main"}
				} else if info.funcAt >= 0 && len(s.Tokens) == info.funcAt+1 {
					values = []string{functionName(p)}
				} else if strings.Contains(terminal.Rule, "type_identifier") {
					values = []string{"int", "bool", "string"}
				} else if info.bodyAt < 0 {
					if info.paramsEnd < 0 {
						for _, name := range p.Inputs {
							if !info.visible[name] {
								values = append(values, name)
							}
						}
					} else {
						values = []string{"int", "bool", "string"}
						for _, name := range []string{"r", "result", "out"} {
							used := name == functionName(p)
							for _, input := range p.Inputs {
								if input == name {
									used = true
								}
							}
							if !used {
								values = append(values, name)
							}
						}
					}
				} else {
					for name := range info.visible {
						values = append(values, name)
					}
					values = append(values, "true", "false", "nil", "len", "cap", "append", "copy", "delete", "make", "int", "bool", "string")
					if declarationPosition(s.Tokens) {
						values = append(values, info.fresh, "_")
					}
				}
			case "int_literal":
				for _, v := range p.Literals.Integers {
					values = append(values, strings.TrimPrefix(strconv.Itoa(v), "-"))
				}
			case "interpreted_string_literal":
				for _, v := range p.Literals.Strings {
					values = append(values, strconv.Quote(v))
				}
			case "raw_string_literal": // 같은 값의 표기 변형은 기본 후보에 중복하지 않는다.
			}
		}
		for _, value := range values {
			kind, e := tokenlex.Kind(value)
			if e != nil || !profileAllows(p, s, info, value) {
				continue
			}
			if _, exists := out[value]; !exists {
				out[value] = Choice{Lexeme: value, Kind: kind.String(), Rule: terminal.Rule}
			}
		}
	}
	result := make([]Choice, 0, len(out))
	for _, c := range out {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Lexeme < result[j].Lexeme })
	return result
}

// 2026-11-05: 공개 문법 후보와 입력 계약 상태 전이의 교집합만 반환한다.
func Candidates(p tokenspec.Problem, s *State) ([]Choice, []Rejection, error) {
	allowed, rejected, _, err := candidatesWithin(p, s, -1)
	return allowed, rejected, err
}
func candidatesWithin(p tokenspec.Problem, s *State, limit int) ([]Choice, []Rejection, bool, error) {
	if s == nil || s.Parser == nil {
		return nil, nil, false, fmt.Errorf("invalid token state")
	}
	h, reason, err := semanticFor(p, s)
	if err != nil {
		return nil, nil, false, err
	}
	if reason != "" {
		return nil, []Rejection{{Reason: reason}}, false, nil
	}
	raw := rawNext(p, s)
	allowed := make([]Choice, 0, len(raw))
	var rejected []Rejection
	for i, c := range raw {
		if limit >= 0 && i >= limit {
			return allowed, rejected, true, nil
		}
		next, reason, e := h.advance(c.Lexeme)
		if e != nil {
			return nil, nil, false, e
		}
		if reason != "" {
			rejected = append(rejected, Rejection{Choice: c, Reason: reason})
			continue
		}
		check := tokenverify.DeclarationCheck{Status: tokenverify.DeclarationPending, ParametersChecked: s.parametersChecked}
		if s.declarationAccepted {
			check.Status = tokenverify.DeclarationAccepted
			check.ParametersChecked = true
		} else if c.Lexeme == ")" || c.Lexeme == "{" {
			tokens := append(append([]string(nil), s.Tokens...), c.Lexeme)
			check, e = tokenverify.CheckDeclaration(p, tokens)
			if e != nil {
				return nil, nil, false, e
			}
			if check.Status == tokenverify.DeclarationRejected {
				rejected = append(rejected, Rejection{Choice: c, Reason: check.Reason})
				continue
			}
		}
		c.header = next
		c.check = check
		c.parent = s
		allowed = append(allowed, c)
	}
	return allowed, rejected, false, nil
}
func Next(p tokenspec.Problem, s *State) ([]Choice, error) {
	allowed, _, err := Candidates(p, s)
	return allowed, err
}
func declarationPosition(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	switch tokens[len(tokens)-1] {
	case "{", ";", "for", "var", ",":
		return true
	}
	return false
}
func profileAllows(p tokenspec.Problem, s *State, info prefix, value string) bool {
	n := len(s.Tokens)
	if info.bodyAt >= 0 && (value == "int" || value == "bool" || value == "string") {
		allowed := false
		for _, typ := range p.AllowedTypes {
			if typ == value || typ == "[]"+value || strings.HasPrefix(typ, "map[") && (value == "string" || strings.HasSuffix(typ, "]"+value)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	// 실행 가능한 main 패키지와 함수 하나라는 프로필 제약이다. 문법 규칙 자체를 대체하지 않는다.
	if n == 0 {
		return value == "package"
	}
	if n == 1 {
		return value == "main"
	}
	if n == 2 {
		return value == ";"
	}
	if info.funcAt < 0 {
		return value == "func"
	}
	if n == info.funcAt+1 {
		return value == functionName(p)
	}
	if n == info.funcAt+2 {
		return value == "("
	}
	if value == "package" || value == "func" {
		return false
	}
	if info.bodyAt < 0 && value == ";" {
		return false
	}
	if n > 0 {
		last := s.Tokens[n-1]
		if info.bodyAt >= 0 && strings.HasPrefix(last, "v") && !info.visible[last] && info.declared[last] == false {
			if _, e := strconv.Atoi(strings.TrimPrefix(last, "v")); e == nil {
				if n >= 2 && s.Tokens[n-2] == "var" {
					return true
				}
				return value == ":=" || value == ","
			}
		}
	}
	return true
}

func Complete(s *State) bool {
	if !s.Parser.Accepts() || len(s.Tokens) == 0 || s.Tokens[len(s.Tokens)-1] != "}" {
		return false
	}
	f, e := parser.ParseFile(token.NewFileSet(), "prefix.go", s.Source(), 0)
	if e != nil || f.Name.Name != "main" || len(f.Decls) != 1 {
		return false
	}
	_, ok := f.Decls[0].(*ast.FuncDecl)
	return ok
}

func visibleNames(source string) map[string]bool {
	out := map[string]bool{}
	fs := token.NewFileSet()
	f, _ := parser.ParseFile(fs, "prefix.go", source, parser.AllErrors)
	if f == nil {
		return out
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if v, ok := d.(*ast.FuncDecl); ok {
			fn = v
			break
		}
	}
	if fn == nil {
		return out
	}
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name != "_" {
				out[n.Name] = true
			}
		}
	}
	inside := func(b *ast.BlockStmt) bool {
		return b != nil && (b.Rbrace == token.NoPos || fs.Position(b.Rbrace).Offset >= len(source))
	}
	complete := func(n ast.Node) bool {
		bad := false
		ast.Inspect(n, func(x ast.Node) bool {
			if _, ok := x.(*ast.BadExpr); ok {
				bad = true
			}
			return true
		})
		end := fs.Position(n.End()).Offset
		return !bad && end < len(source) && strings.ContainsAny(source[end:], ";}")
	}
	add := func(stmt ast.Stmt, env map[string]bool) {
		switch a := stmt.(type) {
		case *ast.AssignStmt:
			if a.Tok == token.DEFINE && complete(a) {
				for _, lhs := range a.Lhs {
					if n, ok := lhs.(*ast.Ident); ok && n.Name != "_" {
						env[n.Name] = true
					}
				}
			}
		case *ast.DeclStmt:
			if g, ok := a.Decl.(*ast.GenDecl); ok && g.Tok == token.VAR && complete(a) {
				for _, s := range g.Specs {
					if v, ok := s.(*ast.ValueSpec); ok && len(v.Values) > 0 {
						for _, n := range v.Names {
							env[n.Name] = true
						}
					}
				}
			}
		}
	}
	copyEnv := func(in map[string]bool) map[string]bool {
		e := map[string]bool{}
		for n := range in {
			e[n] = true
		}
		return e
	}
	var walk func(*ast.BlockStmt, map[string]bool) map[string]bool
	walk = func(b *ast.BlockStmt, env map[string]bool) map[string]bool {
		if b == nil {
			return env
		}
		for _, stmt := range b.List {
			switch x := stmt.(type) {
			case *ast.IfStmt:
				inner := copyEnv(env)
				if x.Init != nil {
					add(x.Init, inner)
				}
				if inside(x.Body) {
					return walk(x.Body, inner)
				}
				if other, ok := x.Else.(*ast.BlockStmt); ok && inside(other) {
					return walk(other, inner)
				}
				if other, ok := x.Else.(*ast.IfStmt); ok && (other.Body == nil || fs.Position(other.End()).Offset >= len(source)) {
					return walk(&ast.BlockStmt{List: []ast.Stmt{other}}, inner)
				}
			case *ast.BlockStmt:
				if inside(x) {
					return walk(x, copyEnv(env))
				}
			case *ast.ForStmt:
				inner := copyEnv(env)
				if x.Init != nil {
					add(x.Init, inner)
				}
				if x.Body == nil || inside(x.Body) {
					return walk(x.Body, inner)
				}
			case *ast.RangeStmt:
				if x.Body == nil || inside(x.Body) {
					inner := copyEnv(env)
					if x.Tok == token.DEFINE {
						for _, e := range []ast.Expr{x.Key, x.Value} {
							if n, ok := e.(*ast.Ident); ok && n.Name != "_" {
								inner[n.Name] = true
							}
						}
					}
					return walk(x.Body, inner)
				}
			default:
				add(stmt, env)
			}
		}
		return env
	}
	return walk(fn.Body, out)
}
