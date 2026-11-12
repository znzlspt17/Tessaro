package tokensynth_test

import (
	"strings"
	"testing"

	"tessaro/internal/tokenspec"
	"tessaro/internal/tokensynth"
	"tessaro/internal/tokenverify"
)

func acceptanceSpec(names, types []string, output string) tokenspec.Problem {
	values := map[string]any{"int": 1, "bool": true, "string": "x", "[]int": []any{1}, "[]string": []any{"x"}, "map[string]int": map[string]any{"k": 1}, "map[string]string": map[string]any{"k": "v"}}
	inputs := map[string]any{}
	for i, name := range names {
		inputs[name] = values[types[i]]
	}
	return tokenspec.Problem{Intent: "contract acceptance fixture", Inputs: names, AllowedTypes: append([]string(nil), tokenspec.Types...), PublicTests: []tokenspec.Test{{Inputs: inputs, Want: values[output]}}}
}

func TestExistingGoSignatureSpellingsRemainReachable(t *testing.T) {
	tests := []struct{ name, typ, header string }{
		{"named_predeclared_int", "int", "package main ; func f0 ( a int ) ( int int ) {"},
		{"nested_scalar_parentheses", "int", "package main ; func f0 ( a ( ( int ) ) ) ( ( int ) ) {"},
		{"parenthesized_slice_element", "[]int", "package main ; func f0 ( a [ ] ( int ) ) [ ] ( int ) {"},
		{"parenthesized_map_parts", "map[string]string", "package main ; func f0 ( a map [ ( string ) ] ( string ) ) map [ ( string ) ] ( string ) {"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := acceptanceSpec([]string{"a"}, []string{tc.typ}, tc.typ)
			if _, err := tokenverify.Validate(p, tc.header+" return a }"); err != nil {
				t.Fatalf("existing Go verifier rejected fixture: %v", err)
			}
			acceptancePrefix(t, p, tc.header)
		})
	}
}

func observedChoices(t *testing.T, p tokenspec.Problem, s *tokensynth.State) []tokensynth.Choice {
	t.Helper()
	out, err := tokensynth.Next(p, s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func acceptancePrefix(t *testing.T, p tokenspec.Problem, source string) *tokensynth.State {
	t.Helper()
	s, err := tokensynth.NewState()
	if err != nil {
		t.Fatal(err)
	}
	for _, lex := range strings.Fields(source) {
		found := false
		for _, c := range observedChoices(t, p, s) {
			if c.Lexeme == lex {
				s, err = s.Advance(c, "acceptance")
				if err != nil {
					t.Fatal(err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("valid prefix token %q absent after %s", lex, s.Source())
		}
	}
	return s
}

func TestContractCandidateAcceptanceAtEveryToken(t *testing.T) {
	tests := []struct {
		name                string
		names, types        []string
		output, prefix, bad string
	}{
		{"zero_inputs", nil, nil, "int", "package main ; func f0 (", "int"},
		{"extra_parameter", []string{"a", "b"}, []string{"int", "int"}, "int", "package main ; func f0 ( a int , b int ,", "int"},
		{"three_inputs_still_allow_c", []string{"a", "b", "c"}, []string{"int", "int", "int"}, "int", "package main ; func f0 ( a int , b int ,", "int"},
		{"first_wrong_scalar_type", []string{"a"}, []string{"int"}, "int", "package main ; func f0 ( a", "bool"},
		{"impossible_group_comma", []string{"a", "b"}, []string{"int", "string"}, "int", "package main ; func f0 ( a", ","},
		{"mixed_group_name", []string{"a", "b", "c"}, []string{"int", "string", "int"}, "int", "package main ; func f0 ( a ,", "b"},
		{"wrong_slice_element", []string{"a"}, []string{"[]int"}, "int", "package main ; func f0 ( a [ ]", "string"},
		{"wrong_map_key", []string{"a"}, []string{"map[string]string"}, "int", "package main ; func f0 ( a map [", "int"},
		{"wrong_return_element", []string{"a"}, []string{"int"}, "[]string", "package main ; func f0 ( a int ) [ ]", "int"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := acceptanceSpec(tc.names, tc.types, tc.output)
			s := acceptancePrefix(t, p, tc.prefix)
			for _, c := range observedChoices(t, p, s) {
				if c.Lexeme == tc.bad {
					t.Fatalf("forbidden %q offered after %s", tc.bad, s.Source())
				}
			}
			if tc.name == "three_inputs_still_allow_c" {
				found := false
				for _, c := range observedChoices(t, p, s) {
					if c.Lexeme == "c" {
						found = true
					}
				}
				if !found {
					t.Fatal("third contracted input c was removed")
				}
			}
		})
	}
}
