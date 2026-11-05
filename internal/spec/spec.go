package spec

import (
	"encoding/json"
	"fmt"
	"go/token"
	"io"
	"jevcancoding/internal/jsoninput"
	"math"
	"strconv"
)

type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type Test struct {
	Args []any `json:"args"`
	Want any   `json:"want"`
}
type Problem struct {
	Intent      string   `json:"intent"`
	Name        string   `json:"name"`
	Params      []Param  `json:"params"`
	ReturnType  string   `json:"return_type"`
	Constants   []int    `json:"constants"`
	PublicTests []Test   `json:"public_tests"`
	Budgets     *Budgets `json:"budgets,omitempty"`
}
type Budgets struct {
	MaxLocals      *int `json:"max_locals,omitempty"`
	MaxStatements  *int `json:"max_statements,omitempty"`
	MaxCandidates  *int `json:"max_candidates,omitempty"`
	MaxCalls       *int `json:"max_calls,omitempty"`
	MaxDepth       *int `json:"max_depth,omitempty"`
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
}

func Decode(r io.Reader) (Problem, error) {
	var p Problem
	d := json.NewDecoder(jsoninput.Reader(r))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("trailing JSON")
	}
	return p, p.Validate()
}
func DecodeTests(r io.Reader, p Problem) ([]Test, error) {
	var ts []Test
	d := json.NewDecoder(jsoninput.Reader(r))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&ts); err != nil {
		return nil, err
	}
	var x any
	if d.Decode(&x) != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	if len(ts) == 0 {
		return nil, fmt.Errorf("empty tests")
	}
	return ts, validateTests(p, ts)
}
func validName(s string) bool {
	return token.IsIdentifier(s) && s != "_" && s != "true" && s != "false" && s != "int" && s != "bool"
}
func (p Problem) Validate() error {
	if p.Budgets != nil {
		if v := p.Budgets.MaxLocals; v != nil && (*v < 0 || *v > 63) {
			return fmt.Errorf("invalid local budget")
		}
		if v := p.Budgets.MaxStatements; v != nil && (*v < 1 || *v > 63) {
			return fmt.Errorf("invalid statement budget")
		}
		for _, v := range []*int{p.Budgets.MaxCandidates, p.Budgets.MaxCalls, p.Budgets.MaxDepth, p.Budgets.TimeoutSeconds} {
			if v != nil && (*v <= 0 || *v > 1000000) {
				return fmt.Errorf("invalid budget")
			}
		}
	}
	if len(p.Params) > 8 || len(p.Constants) > 16 {
		return fmt.Errorf("at most 8 parameters and 16 constants")
	}
	if p.Intent == "" || !validName(p.Name) || p.Name == "main" || p.Name == "init" {
		return fmt.Errorf("invalid intent or function name")
	}
	if p.ReturnType != "int" && p.ReturnType != "bool" {
		return fmt.Errorf("invalid return type")
	}
	names := map[string]bool{p.Name: true}
	for _, v := range p.Params {
		if !validName(v.Name) || names[v.Name] || (v.Type != "int" && v.Type != "bool") {
			return fmt.Errorf("invalid parameter %q", v.Name)
		}
		names[v.Name] = true
	}
	if len(p.PublicTests) == 0 {
		return fmt.Errorf("public tests required")
	}
	return validateTests(p, p.PublicTests)
}
func validateTests(p Problem, ts []Test) error {
	for i, t := range ts {
		if len(t.Args) != len(p.Params) {
			return fmt.Errorf("test %d: argument count", i)
		}
		for j, v := range t.Args {
			if !validValue(v, p.Params[j].Type) {
				return fmt.Errorf("test %d: argument %d type", i, j)
			}
		}
		if !validValue(t.Want, p.ReturnType) {
			return fmt.Errorf("test %d: return type", i)
		}
	}
	return nil
}
func validValue(v any, typ string) bool {
	if typ == "bool" {
		_, ok := v.(bool)
		return ok
	}
	switch x := v.(type) {
	case json.Number:
		_, err := strconv.ParseInt(string(x), 10, strconv.IntSize)
		return err == nil
	case float64:
		return math.Trunc(x) == x && x >= float64(-1<<53) && x <= float64(1<<53) && strconv.IntSize == 64 || math.Trunc(x) == x && x >= math.MinInt32 && x <= math.MaxInt32
	case int:
		return true
	}
	return false
}
