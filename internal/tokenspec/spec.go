package tokenspec

import (
	"encoding/json"
	"fmt"
	"go/token"
	"io"
	"jevcancoding/internal/jsoninput"
	"strconv"
	"strings"
)

type Test struct {
	Inputs map[string]any `json:"inputs"`
	Want   any            `json:"want"`
}
type Literals struct {
	Integers []int    `json:"integers"`
	Strings  []string `json:"strings"`
}
type Budgets struct {
	MaxLocalsSet   bool `json:"-"`
	MaxTokens      int  `json:"max_tokens,omitempty"`
	MaxCalls       int  `json:"max_calls,omitempty"`
	MaxCandidates  int  `json:"max_candidates,omitempty"`
	MaxLocals      int  `json:"max_locals,omitempty"`
	MaxStatements  int  `json:"max_statements,omitempty"`
	MaxNesting     int  `json:"max_nesting,omitempty"`
	TimeoutSeconds int  `json:"timeout_seconds,omitempty"`
}
type Problem struct {
	Intent         string            `json:"intent"`
	Inputs         []string          `json:"inputs"`
	AllowedTypes   []string          `json:"allowed_types"`
	Literals       Literals          `json:"literals"`
	PublicTests    []Test            `json:"public_tests"`
	TypeHints      map[string]string `json:"type_hints,omitempty"`
	OutputTypeHint string            `json:"output_type_hint,omitempty"`
	ValidationMode string            `json:"validation_mode,omitempty"`
	Budgets        Budgets           `json:"budgets,omitempty"`
}

var Types = []string{"int", "bool", "string", "[]int", "[]string", "map[string]int", "map[string]string"}

func SupportedType(name string) bool {
	for _, t := range Types {
		if name == t {
			return true
		}
	}
	return false
}
func Decode(r io.Reader) (Problem, error) {
	var p Problem
	d := json.NewDecoder(jsoninput.Reader(r))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("token specification requires named inputs, not name/params/return_type; use -engine ast for legacy files: %w", err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, fmt.Errorf("trailing JSON")
	}
	if len(p.AllowedTypes) == 0 {
		p.AllowedTypes = append([]string(nil), Types...)
	}
	if len(p.Literals.Integers) == 0 {
		p.Literals.Integers = []int{0, 1}
	}
	if len(p.Literals.Strings) == 0 {
		p.Literals.Strings = []string{""}
	}
	return p, p.Validate()
}
func DecodeTests(r io.Reader, p Problem) ([]Test, error) {
	var tests []Test
	d := json.NewDecoder(jsoninput.Reader(r))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&tests); err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	if len(tests) == 0 {
		return nil, fmt.Errorf("tests required")
	}
	inputs, output, err := p.Contract()
	if err != nil {
		return nil, err
	}
	for _, test := range tests {
		if err := validateTest(test, p.Inputs, inputs, output); err != nil {
			return nil, err
		}
	}
	return tests, nil
}
func (p Problem) Validate() error {
	if strings.TrimSpace(p.Intent) == "" || len(p.Inputs) > 8 || len(p.PublicTests) == 0 {
		return fmt.Errorf("intent, at most 8 input names and public tests are required")
	}
	names := map[string]bool{}
	for _, name := range p.Inputs {
		if !token.IsIdentifier(name) || name == "_" || names[name] || isReserved(name) {
			return fmt.Errorf("invalid input name")
		}
		names[name] = true
	}
	types := map[string]bool{}
	for _, name := range p.AllowedTypes {
		if !SupportedType(name) || types[name] {
			return fmt.Errorf("invalid allowed type")
		}
		types[name] = true
	}
	if len(p.AllowedTypes) == 0 {
		return fmt.Errorf("allowed types required")
	}
	if len(p.Literals.Integers) > 16 || len(p.Literals.Strings) > 16 {
		return fmt.Errorf("at most 16 literals per kind")
	}
	for _, s := range p.Literals.Strings {
		if len(s) > 1024 {
			return fmt.Errorf("string literal too large")
		}
	}
	for name, hint := range p.TypeHints {
		if !names[name] || !types[hint] {
			return fmt.Errorf("invalid input type hint")
		}
	}
	if p.OutputTypeHint != "" && !types[p.OutputTypeHint] {
		return fmt.Errorf("invalid output type hint")
	}
	if p.ValidationMode != "" && p.ValidationMode != "sampled_holdout" && p.ValidationMode != "exhaustive_public" {
		return fmt.Errorf("invalid validation mode")
	}
	b := p.Budgets
	for _, v := range []int{b.MaxTokens, b.MaxCalls, b.MaxCandidates, b.MaxLocals, b.MaxStatements, b.MaxNesting, b.TimeoutSeconds} {
		if v < 0 || v > 1000000 {
			return fmt.Errorf("invalid budget")
		}
	}
	_, _, err := p.Contract()
	return err
}
func isReserved(name string) bool {
	switch name {
	case "int", "bool", "string", "true", "false", "nil", "len", "cap", "append", "copy", "delete", "make":
		return true
	}
	return false
}

// 2026-11-05: 사례에서 호출 계약만 추론한다. Go 함수 이름·인자 순서·완성된 서명은 생성하지 않는다.
func (p Problem) Contract() (map[string]string, string, error) {
	inputs := map[string]string{}
	for _, name := range p.Inputs {
		inputs[name] = p.TypeHints[name]
	}
	output := p.OutputTypeHint
	for _, test := range p.PublicTests {
		if len(test.Inputs) != len(p.Inputs) {
			return nil, "", fmt.Errorf("input names mismatch")
		}
		for _, name := range p.Inputs {
			value, ok := test.Inputs[name]
			if !ok {
				return nil, "", fmt.Errorf("missing input")
			}
			typ, err := valueType(value)
			if err != nil {
				return nil, "", err
			}
			if typ != "" {
				if inputs[name] != "" && inputs[name] != typ {
					return nil, "", fmt.Errorf("inconsistent input type")
				}
				inputs[name] = typ
			}
		}
		typ, err := valueType(test.Want)
		if err != nil {
			return nil, "", err
		}
		if typ != "" {
			if output != "" && output != typ {
				return nil, "", fmt.Errorf("inconsistent output type")
			}
			output = typ
		}
	}
	allowed := map[string]bool{}
	for _, t := range p.AllowedTypes {
		allowed[t] = true
	}
	for _, name := range p.Inputs {
		if !allowed[inputs[name]] {
			return nil, "", fmt.Errorf("input %s requires an allowed type or type hint", name)
		}
	}
	if !allowed[output] {
		return nil, "", fmt.Errorf("output requires an allowed type or type hint")
	}
	for _, test := range p.PublicTests {
		if err := validateTest(test, p.Inputs, inputs, output); err != nil {
			return nil, "", err
		}
	}
	return inputs, output, nil
}
func validateTest(test Test, names []string, types map[string]string, output string) error {
	if len(test.Inputs) != len(names) {
		return fmt.Errorf("input names mismatch")
	}
	for _, name := range names {
		v, ok := test.Inputs[name]
		if !ok || !Matches(v, types[name]) {
			return fmt.Errorf("input value does not match contract")
		}
	}
	if !Matches(test.Want, output) {
		return fmt.Errorf("output value does not match contract")
	}
	return nil
}
func valueType(value any) (string, error) {
	switch v := value.(type) {
	case bool:
		return "bool", nil
	case string:
		if len(v) > 4096 {
			return "", fmt.Errorf("input string too large")
		}
		return "string", nil
	case json.Number:
		if _, err := strconv.ParseInt(string(v), 10, strconv.IntSize); err != nil {
			return "", fmt.Errorf("only machine int values are supported")
		}
		return "int", nil
	case int:
		return "int", nil
	case []any:
		if len(v) > 128 {
			return "", fmt.Errorf("collection too large")
		}
		element := ""
		for _, item := range v {
			t, err := valueType(item)
			if err != nil || (t != "int" && t != "string") {
				return "", fmt.Errorf("unsupported slice element")
			}
			if element != "" && element != t {
				return "", fmt.Errorf("mixed slice types")
			}
			element = t
		}
		if element == "" {
			return "", nil
		}
		return "[]" + element, nil
	case map[string]any:
		if len(v) > 128 {
			return "", fmt.Errorf("collection too large")
		}
		element := ""
		for key, item := range v {
			if len(key) > 4096 {
				return "", fmt.Errorf("map key too large")
			}
			t, err := valueType(item)
			if err != nil || (t != "int" && t != "string") {
				return "", fmt.Errorf("unsupported map element")
			}
			if element != "" && element != t {
				return "", fmt.Errorf("mixed map types")
			}
			element = t
		}
		if element == "" {
			return "", nil
		}
		return "map[string]" + element, nil
	}
	return "", fmt.Errorf("unsupported value; null and nested containers are not supported")
}
func Matches(value any, typ string) bool {
	actual, err := valueType(value)
	if err != nil {
		return false
	}
	if actual != "" {
		return actual == typ
	}
	switch value.(type) {
	case []any:
		return typ == "[]int" || typ == "[]string"
	case map[string]any:
		return typ == "map[string]int" || typ == "map[string]string"
	}
	return false
}
