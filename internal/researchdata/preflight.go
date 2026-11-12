package researchdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokenverify"
	"os"
	"path/filepath"
)

type Limits struct {
	Tokens     int `json:"tokens"`
	Locals     int `json:"locals"`
	Statements int `json:"statements"`
}
type Check struct {
	ID                string                        `json:"id"`
	Split             string                        `json:"split"`
	Category          string                        `json:"category,omitempty"`
	ValidationMode    string                        `json:"validation_mode"`
	HashesOK          bool                          `json:"hashes_ok"`
	ValidateError     string                        `json:"validate_error,omitempty"`
	Frontier          Frontier                      `json:"frontier"`
	Used              Limits                        `json:"used"`
	Limit             Limits                        `json:"limit"`
	Excess            []string                      `json:"excess,omitempty"`
	DiagnosticRelaxed bool                          `json:"diagnostic_relaxed"`
	Public            *tokenverify.PublicEvaluation `json:"public,omitempty"`
	Heldout           *tokenverify.PublicEvaluation `json:"heldout,omitempty"`
	HeldoutState      string                        `json:"heldout_state"`
	OriginalFrontier  *Frontier                     `json:"original_frontier,omitempty"`
	OriginalPublic    *tokenverify.PublicEvaluation `json:"original_public,omitempty"`
	OriginalHeldout   *tokenverify.PublicEvaluation `json:"original_heldout,omitempty"`
}

func readHashed(base, path, want string) ([]byte, error) {
	b, e := os.ReadFile(filepath.Join(base, filepath.FromSlash(path)))
	if e != nil {
		return nil, e
	}
	if hash(b) != want {
		return nil, fmt.Errorf("hash mismatch: %s", path)
	}
	return b, nil
}
func LoadCatalog(base string) (Corpus, error) {
	var c Corpus
	b, e := os.ReadFile(filepath.Join(base, "catalog.json"))
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func metrics(source string) (int, int, error) {
	fs := token.NewFileSet()
	f, e := parser.ParseFile(fs, "reference.go", source, 0)
	if e != nil {
		return 0, 0, e
	}
	info := types.Info{Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{}
	if _, e = conf.Check("reference", fs, []*ast.File{f}, &info); e != nil {
		return 0, 0, e
	}
	params := map[types.Object]bool{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			for _, field := range fn.Type.Params.List {
				for _, id := range field.Names {
					params[info.Defs[id]] = true
				}
			}
			if fn.Type.Results != nil {
				for _, field := range fn.Type.Results.List {
					for _, id := range field.Names {
						params[info.Defs[id]] = true
					}
				}
			}
		}
	}
	locals := 0
	for id, obj := range info.Defs {
		if id.Name != "_" && !params[obj] {
			if _, ok := obj.(*types.Var); ok {
				locals++
			}
		}
	}
	stmts := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(ast.Stmt); ok {
			if _, block := n.(*ast.BlockStmt); !block {
				stmts++
			}
		}
		return true
	})
	return locals, stmts, nil
}
func defaultLimit(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// 2026-11-12: 원래 제한과 진단용 완화 검증을 분리해 참조 초과를 해결 가능성으로 오인하지 않게 한다.
func Preflight(ctx context.Context, base string, r Record) (Check, error) {
	c := Check{ID: r.ID, Split: r.Split, Category: r.Category, ValidationMode: r.ValidationMode, HeldoutState: "present"}
	spec, e := readHashed(base, r.Spec, r.SpecHash)
	if e != nil {
		return c, e
	}
	source, e := readHashed(base, r.Variant, r.VariantHash)
	if e != nil {
		return c, e
	}
	var held []byte
	if r.Heldout != "" {
		held, e = readHashed(base, r.Heldout, r.HeldoutHash)
		if e != nil {
			return c, e
		}
	} else if r.ValidationMode == "exhaustive_public" {
		c.HeldoutState = "exhaustive_public"
	} else {
		c.HeldoutState = "missing"
		return c, fmt.Errorf("%s: heldout missing", r.ID)
	}
	c.HashesOK = true
	p, e := tokenspec.Decode(bytes.NewReader(spec))
	if e != nil {
		return c, e
	}
	// 2026-11-12: 명시된 0 지역변수 예산도 생략된 기본값과 구분해 초과 여부를 보고한다.
	var rawSpec struct {
		Budgets map[string]json.RawMessage `json:"budgets"`
	}
	if err := json.Unmarshal(spec, &rawSpec); err != nil {
		return c, err
	}
	if _, present := rawSpec.Budgets["max_locals"]; present {
		p.Budgets.MaxLocalsSet = true
	}
	if p.ReturnPolicy != tokenspec.ReturnPolicyValuesOnly {
		return c, fmt.Errorf("invalid profile")
	}
	c.Frontier, e = CheckFrontier(p, string(source))
	if e != nil {
		return c, e
	}
	locals, stmts, e := metrics(string(source))
	if e != nil {
		return c, e
	}
	c.Used = Limits{Tokens: c.Frontier.Tokens, Locals: locals, Statements: stmts}
	c.Limit = Limits{Tokens: defaultLimit(p.Budgets.MaxTokens, 256), Locals: defaultLimit(p.Budgets.MaxLocals, 8), Statements: defaultLimit(p.Budgets.MaxStatements, 32)}
	if p.Budgets.MaxLocalsSet {
		c.Limit.Locals = p.Budgets.MaxLocals
	}
	if c.Used.Tokens > c.Limit.Tokens {
		c.Excess = append(c.Excess, "tokens")
	}
	if locals > c.Limit.Locals {
		c.Excess = append(c.Excess, "locals")
	}
	if stmts > c.Limit.Statements {
		c.Excess = append(c.Excess, "statements")
	}
	if _, e = tokenverify.Validate(p, string(source)); e != nil {
		c.ValidateError = e.Error()
	}
	diagnostic := p
	if len(c.Excess) > 0 {
		diagnostic.Budgets.MaxLocals = 63
		diagnostic.Budgets.MaxLocalsSet = true
		diagnostic.Budgets.MaxStatements = 128
		c.DiagnosticRelaxed = true
	}
	pub, e := tokenverify.EvaluatePublic(ctx, diagnostic, string(source), p.PublicTests)
	if e != nil {
		return c, e
	}
	c.Public = &pub
	if len(held) > 0 {
		tests, e := tokenspec.DecodeTests(bytes.NewReader(held), p)
		if e != nil {
			return c, e
		}
		ev, e := tokenverify.EvaluatePublic(ctx, diagnostic, string(source), tests)
		if e != nil {
			return c, e
		}
		c.Heldout = &ev
	}
	if r.Split == "dev" {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(base), "token", "references.json"))
		if err != nil {
			return c, err
		}
		var originals []original
		if err = json.Unmarshal(data, &originals); err != nil {
			return c, err
		}
		originalSource := ""
		for _, item := range originals {
			if item.ID == r.ID {
				originalSource = item.Source
				break
			}
		}
		if originalSource == "" || hash([]byte(originalSource)) != r.SourceHash {
			return c, fmt.Errorf("original source hash mismatch: %s", r.ID)
		}
		legacy := diagnostic
		legacy.ReturnPolicy = ""
		before, err := CheckFrontier(legacy, originalSource)
		if err != nil {
			return c, err
		}
		c.OriginalFrontier = &before
		originalPub, err := tokenverify.EvaluatePublic(ctx, legacy, originalSource, p.PublicTests)
		if err != nil {
			return c, err
		}
		c.OriginalPublic = &originalPub
		if len(held) > 0 {
			tests, err := tokenspec.DecodeTests(bytes.NewReader(held), p)
			if err != nil {
				return c, err
			}
			originalHeld, err := tokenverify.EvaluatePublic(ctx, legacy, originalSource, tests)
			if err != nil {
				return c, err
			}
			c.OriginalHeldout = &originalHeld
		}
	}
	return c, nil
}

// 2026-11-12: 타입 monitor 변경 후 이미 실행한 테스트를 반복하지 않고 실제 선택지 경로만 재검증한다.
func FrontierOnly(base string, r Record) (Check, error) {
	c := Check{ID: r.ID, Split: r.Split, Category: r.Category, ValidationMode: r.ValidationMode}
	spec, err := readHashed(base, r.Spec, r.SpecHash)
	if err != nil {
		return c, err
	}
	source, err := readHashed(base, r.Variant, r.VariantHash)
	if err != nil {
		return c, err
	}
	p, err := tokenspec.Decode(bytes.NewReader(spec))
	if err != nil {
		return c, err
	}
	c.Frontier, err = CheckFrontier(p, string(source))
	if err != nil {
		return c, err
	}
	c.HashesOK = true
	if r.Split == "dev" {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(base), "token", "references.json"))
		if err != nil {
			return c, err
		}
		var refs []original
		if err = json.Unmarshal(data, &refs); err != nil {
			return c, err
		}
		found := false
		for _, ref := range refs {
			if ref.ID == r.ID {
				if hash([]byte(ref.Source)) != r.SourceHash {
					return c, fmt.Errorf("original source hash mismatch: %s", r.ID)
				}
				p.ReturnPolicy = ""
				f, err := CheckFrontier(p, ref.Source)
				if err != nil {
					return c, err
				}
				c.OriginalFrontier = &f
				found = true
				break
			}
		}
		if !found {
			return c, fmt.Errorf("original reference missing: %s", r.ID)
		}
	}
	return c, nil
}
