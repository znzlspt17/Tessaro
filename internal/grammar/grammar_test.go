package grammar

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const fixture = `{"name":"sample","rules":{"start":{"type":"SEQ","members":[{"type":"SYMBOL","name":"expr"},{"type":"REPEAT","content":{"type":"STRING","value":";"}}]},"expr":{"type":"CHOICE","members":[{"type":"SEQ","members":[{"type":"SYMBOL","name":"expr"},{"type":"STRING","value":"+"},{"type":"SYMBOL","name":"identifier"}]},{"type":"SYMBOL","name":"identifier"}]},"identifier":{"type":"PATTERN","value":"[a-z]+"}}}`

func TestIncrementalLeftRecursionAndRepeat(t *testing.T) {
	g, e := Load([]byte(fixture))
	if e != nil {
		t.Fatal(e)
	}
	s := g.NewState("start")
	if s.Accepts() || len(s.Next()) == 0 {
		t.Fatal("wrong initial state")
	}
	s, e = s.Advance("x")
	if e != nil || !s.Accepts() {
		t.Fatalf("identifier %v", e)
	}
	s, e = s.Advance("+")
	if e != nil || s.Accepts() {
		t.Fatalf("plus %v", e)
	}
	s, e = s.Advance("y")
	if e != nil || !s.Accepts() {
		t.Fatalf("second identifier %v", e)
	}
	s, e = s.Advance(";")
	if e != nil || !s.Accepts() {
		t.Fatalf("repeat %v", e)
	}
	if _, e = s.Advance("42"); e == nil {
		t.Fatal("unexpected token accepted")
	}
}
func TestCloneAndUnknownConstruct(t *testing.T) {
	g, e := Load([]byte(fixture))
	if e != nil {
		t.Fatal(e)
	}
	a := g.NewState("start")
	b, e := a.Clone().Advance("x")
	if e != nil || !b.Accepts() || a.Accepts() {
		t.Fatal("clone changed original")
	}
	if _, e = Load([]byte(strings.Replace(fixture, `"type":"REPEAT"`, `"type":"BOGUS"`, 1))); e == nil {
		t.Fatal("unknown construct accepted")
	}
}
func TestPinnedGoFunction(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "third_party", "tree-sitter-go", "grammar.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewState("function_declaration")
	for _, lex := range []string{"func", "f", "(", "x", "int", ")", "int", "{", "return", "x", ";", "}"} {
		next, e := s.Advance(lex)
		if e != nil {
			t.Fatalf("%q: %v, next=%+v", lex, e, s.Next())
		}
		s = next
	}
	if !s.Accepts() {
		t.Fatalf("incomplete next=%+v", s.Next())
	}
}
func TestAliasLexicalProvenance(t *testing.T) {
	g, e := Load([]byte(`{"name":"alias","rules":{"start":{"type":"ALIAS","value":"type_identifier","named":true,"content":{"type":"SYMBOL","name":"identifier"}},"identifier":{"type":"PATTERN","value":"[a-z]+"}}}`))
	if e != nil {
		t.Fatal(e)
	}
	s := g.NewState("start")
	next := s.Next()
	if len(next) != 1 || next[0].Rule != "type_identifier" || next[0].Value != "identifier" {
		t.Fatalf("%+v", next)
	}
	s, e = s.Advance("Thing")
	if e != nil || !s.Accepts() {
		t.Fatalf("%v", e)
	}
}
func TestRejectUnknownInsideAtomicRule(t *testing.T) {
	_, err := Load([]byte(`{"name":"x","rules":{"identifier":{"type":"BOGUS"}}}`))
	if err == nil {
		t.Fatal("unknown atomic construct accepted")
	}
}
func TestPinnedGoSourceFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "third_party", "tree-sitter-go", "grammar.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewState("source_file")
	for _, lex := range []string{"package", "main", ";", "func", "f", "(", "x", "int", ")", "int", "{", "return", "x", ";", "}", ";"} {
		n, e := s.Advance(lex)
		if e != nil {
			t.Fatalf("%q: %v, next=%+v", lex, e, s.Next())
		}
		s = n
	}
	if !s.Accepts() {
		t.Fatalf("source incomplete next=%+v", s.Next())
	}
}
func TestAtomicGoLexemes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "third_party", "tree-sitter-go", "grammar.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ rule, valid, invalid string }{{"identifier", "thing", "for"}, {"int_literal", "0x2a", "12+3"}, {"interpreted_string_literal", `"hello"`, `"bad`}, {"raw_string_literal", "`hello`", "`bad"}}
	for _, tc := range tests {
		t.Run(tc.rule, func(t *testing.T) {
			s := g.NewState(tc.rule)
			n, e := s.Advance(tc.valid)
			if e != nil || !n.Accepts() {
				t.Fatalf("valid: %v", e)
			}
			if _, e = s.Advance(tc.invalid); e == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
}
func TestTokenSequenceAndMetadata(t *testing.T) {
	g, err := Load([]byte(`{"name":"x","rules":{"start":{"type":"TOKEN","content":{"type":"SEQ","members":[{"type":"STRING","value":"ab"},{"type":"PATTERN","value":"[0-9]+"}]}},"unused":{"type":"PREC_LEFT","value":4,"content":{"type":"BLANK"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(g.Rules["unused"]), `"value":4`) {
		t.Fatal("metadata lost")
	}
	s := g.NewState("start")
	s, err = s.Advance("ab123")
	if err != nil || !s.Accepts() {
		t.Fatalf("token: %v", err)
	}
}
func TestPinnedGoLexicalPatterns(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "third_party", "tree-sitter-go", "grammar.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, rules := range g.rules {
		for _, p := range rules {
			for _, part := range p.parts {
				if part.terminal != nil && part.terminal.Kind == "lexical" && !atomic[part.terminal.Rule] {
					if _, err := regexp.Compile("^(?:" + part.terminal.Value + ")$"); err != nil {
						t.Errorf("unsupported lexical pattern for %s", part.terminal.Rule)
					}
				}
			}
		}
	}
}

func TestUnsupportedLexicalPatternFailsLoad(t *testing.T) {
	if _, err := Load([]byte(`{"name":"bad","rules":{"start":{"type":"PATTERN","value":"(?=a)a"}}}`)); err == nil {
		t.Fatal("unsupported regex silently accepted")
	}
}
