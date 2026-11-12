package tokenverify

import (
	"jevcancode/internal/tokenspec"
	"strings"
	"testing"
)

func TestValuesOnlyRejectsComputedReturns(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"x","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}],"return_policy":"values-only-v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"return a + 1", "return int(a)", "return"} {
		if _, err := Validate(p, "package main\nfunc F(a int) int { "+body+" }"); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	if _, err := Validate(p, "package main\nfunc F(a int) int { return ((-1)) }"); err != nil {
		t.Fatal(err)
	}
}

func TestReturnPrefixConservativeAndRejectsComputation(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}],"return_policy":"values-only-v1"}`)
	for _, tc := range []struct {
		tokens   []string
		rejected bool
	}{
		{[]string{"return", "(", "(", "-"}, false},
		{[]string{"return", "(", "-", "1", ")", ";"}, false},
		{[]string{"return", "a", "+"}, true},
		{[]string{"return", "a", "("}, true},
		{[]string{"return", "a", "["}, true},
		{[]string{"return", "}"}, true},
	} {
		got := CheckReturnPrefix(p, tc.tokens)
		if (got != "") != tc.rejected {
			t.Errorf("%v: %q", tc.tokens, got)
		}
	}
}

func TestValuesOnlyAcceptsTypedNilAndRejectsIndex(t *testing.T) {
	p := problem(t, `{"intent":"map","inputs":["m"],"allowed_types":["map[string]int"],"public_tests":[{"inputs":{"m":{"x":1}},"want":{"x":1}}],"return_policy":"values-only-v1"}`)
	if _, err := Validate(p, "package main\nfunc F(m map[string]int) map[string]int { return nil }"); err != nil {
		t.Fatal(err)
	}
	p2 := problem(t, `{"intent":"index","inputs":["xs"],"allowed_types":["[]int","int"],"public_tests":[{"inputs":{"xs":[1]},"want":1}],"return_policy":"values-only-v1"}`)
	if _, err := Validate(p2, "package main\nfunc F(xs []int) int { return xs[0] }"); err == nil {
		t.Fatal("index return accepted")
	}
}
