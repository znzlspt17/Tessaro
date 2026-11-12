package tokensynth

import (
	"fmt"
	"tessaro/internal/tokenspec"
	"strings"
	"testing"
)

func subtractionSpec(t *testing.T) tokenspec.Problem {
	t.Helper()
	p, e := tokenspec.Decode(strings.NewReader(`{"intent":"a minus b","inputs":["a","b"],"allowed_types":["int","bool"],"public_tests":[{"inputs":{"a":7,"b":2},"want":5}]}`))
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPrefixCandidatesComeFromGrammarOneTokenAtATime(t *testing.T) {
	p := subtractionSpec(t)
	s, e := NewState()
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Tokens) != 0 {
		t.Fatal("prefilled source")
	}
	for _, value := range strings.Fields("package main ; func f0 ( a int , b int ) int { return a - b ; }") {
		choices := mustNext(t, p, s)
		var chosen *Choice
		for i := range choices {
			if choices[i].Lexeme == value {
				chosen = &choices[i]
				break
			}
		}
		if chosen == nil {
			t.Fatalf("token %q absent after %q: %+v", value, s.Source(), choices)
		}
		s, e = s.Advance(*chosen, "next")
		if e != nil {
			t.Fatal(e)
		}
	}
	if !Complete(s) {
		t.Fatal("full function not accepted")
	}
}

func TestScopeAndCollectionFrontiers(t *testing.T) {
	p, e := tokenspec.Decode(strings.NewReader(`{"intent":"scope and collections","inputs":["a","flag","xs"],"allowed_types":["int","bool","[]int"],"public_tests":[{"inputs":{"a":1,"flag":true,"xs":[1]},"want":1}]}`))
	if e != nil {
		t.Fatal(e)
	}
	for _, body := range []string{
		"{ v0 := a ; return v0 ; }",
		"if flag { return a ; } else if flag { v0 := a ; return v0 ; } else { return a ; }",
		"var v0 int = a ; return v0 ;",
		"v0 := append ( xs , xs ... ) ; return len ( v0 ) ;",
		"v0 := copy ( xs , xs ) ; return v0 ;",
	} {
		t.Run(body, func(t *testing.T) {
			s, e := NewState()
			if e != nil {
				t.Fatal(e)
			}
			for _, lexeme := range strings.Fields("package main ; func f0 ( a int , flag bool , xs [ ] int ) int { " + body + " }") {
				choices := mustNext(t, p, s)
				found := false
				for _, c := range choices {
					if c.Lexeme == lexeme {
						s, e = s.Advance(c, "scope")
						if e != nil {
							t.Fatal(e)
						}
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing %s after %s", lexeme, s.Source())
				}
			}
			if !Complete(s) {
				t.Fatal("incomplete")
			}
		})
	}
}

func TestDisallowedTypeNamesNotOffered(t *testing.T) {
	p := subtractionSpec(t)
	p.AllowedTypes = []string{"int"}
	s, _ := NewState()
	for _, value := range strings.Fields("package main ; func f0 ( a") {
		found := false
		for _, choice := range mustNext(t, p, s) {
			if choice.Lexeme == value {
				s, _ = s.Advance(choice, "prefix")
				found = true
				break
			}
		}
		if !found {
			t.Fatal(value)
		}
	}
	for _, choice := range mustNext(t, p, s) {
		if choice.Lexeme == "bool" || choice.Lexeme == "string" {
			t.Fatalf("disallowed type offered: %+v", choice)
		}
	}
}

func mustNext(t *testing.T, p tokenspec.Problem, s *State) []Choice {
	t.Helper()
	choices, err := Next(p, s)
	if err != nil {
		t.Fatal(err)
	}
	return choices
}
func contractFixture(t *testing.T, names, typs []string, out string) tokenspec.Problem {
	t.Helper()
	values := map[string]any{"int": 1, "bool": true, "string": "x", "[]int": []any{1}, "[]string": []any{"x"}, "map[string]int": map[string]any{"k": 1}, "map[string]string": map[string]any{"k": "v"}}
	inputs := map[string]any{}
	for i, n := range names {
		inputs[n] = values[typs[i]]
	}
	p := tokenspec.Problem{Intent: "contract", Inputs: names, AllowedTypes: append([]string(nil), tokenspec.Types...), PublicTests: []tokenspec.Test{{Inputs: inputs, Want: values[out]}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}
func advanceContract(t *testing.T, p tokenspec.Problem, tokens []string) *State {
	t.Helper()
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range tokens {
		choices := mustNext(t, p, s)
		found := false
		for _, c := range choices {
			if c.Lexeme == v {
				s, err = s.Advance(c, "test")
				if err != nil {
					t.Fatal(err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %q after %q, choices=%+v", v, s.Source(), choices)
		}
	}
	return s
}
func TestHeaderZeroThroughEightInputs(t *testing.T) {
	for n := 0; n <= 8; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			names := make([]string, n)
			types := make([]string, n)
			for i := range names {
				names[i] = fmt.Sprintf("p%d", i)
				types[i] = "int"
			}
			p := contractFixture(t, names, types, "int")
			tokens := strings.Fields("package main ; func f0 (")
			for i, name := range names {
				if i > 0 {
					tokens = append(tokens, ",")
				}
				tokens = append(tokens, name)
			}
			if n > 0 {
				tokens = append(tokens, "int")
			}
			tokens = append(tokens, ")", "int", "{")
			advanceContract(t, p, tokens)
		})
	}
}
func TestHeaderAllTypePairs(t *testing.T) {
	for _, in := range tokenspec.Types {
		for _, out := range tokenspec.Types {
			t.Run(in+"_"+out, func(t *testing.T) {
				p := contractFixture(t, []string{"a"}, []string{in}, out)
				inTokens, _, _ := typeParts(in)
				outTokens, _, _ := typeParts(out)
				tokens := append(strings.Fields("package main ; func f0 ( a"), inTokens...)
				tokens = append(tokens, ")")
				tokens = append(tokens, outTokens...)
				tokens = append(tokens, "{")
				advanceContract(t, p, tokens)
			})
		}
	}
}
func TestHeaderNamedResultAndParenthesizedTypes(t *testing.T) {
	p := contractFixture(t, []string{"a"}, []string{"map[string]int"}, "int")
	advanceContract(t, p, strings.Fields("package main ; func f0 ( a ( map [ ( string ) ] ( int ) ) ) ( r ( int ) ) {"))
}
func TestHeaderBranchesDoNotMutateParent(t *testing.T) {
	p := contractFixture(t, []string{"a", "b"}, []string{"int", "int"}, "int")
	parent := advanceContract(t, p, strings.Fields("package main ; func f0 ( a"))
	before := mustNext(t, p, parent)
	var direct, group Choice
	for _, c := range before {
		if c.Lexeme == "int" {
			direct = c
		}
		if c.Lexeme == "," {
			group = c
		}
	}
	if direct.Lexeme == "" || group.Lexeme == "" {
		t.Fatal("expected both type and group options")
	}
	left, err := parent.Advance(direct, "left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := parent.Advance(group, "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = right.Advance(direct, "wrong-parent"); err == nil {
		t.Fatal("foreign choice accepted")
	}
	after := mustNext(t, p, parent)
	if len(after) != len(before) || parent.Source() != "package main ; func f0 ( a" {
		t.Fatal("parent was mutated")
	}
	leftChoices, rightChoices := mustNext(t, p, left), mustNext(t, p, right)
	has := func(cs []Choice, name string) bool {
		for _, c := range cs {
			if c.Lexeme == name {
				return true
			}
		}
		return false
	}
	if !has(leftChoices, ",") || !has(rightChoices, "b") || has(rightChoices, "int") {
		t.Fatalf("left=%+v right=%+v", leftChoices, rightChoices)
	}
}
func TestCandidateErrorsAreNotTreatedAsEmptyChoices(t *testing.T) {
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Next(tokenspec.Problem{}, s); err == nil {
		t.Fatal("invalid contract error was hidden")
	}
	p := contractFixture(t, []string{"a"}, []string{"int"}, "int")
	s = advanceContract(t, p, strings.Fields("package main ; func f0 ( a"))
	other := contractFixture(t, []string{"a"}, []string{"bool"}, "int")
	if _, err = Next(other, s); err == nil {
		t.Fatal("changed contract error was hidden")
	}
}
func TestNamedResultMayUsePredeclaredNameOutsideAllowedTypes(t *testing.T) {
	p := contractFixture(t, []string{"a"}, []string{"int"}, "int")
	p.AllowedTypes = []string{"int"}
	advanceContract(t, p, strings.Fields("package main ; func f0 ( a int ) ( bool int ) {"))
}
