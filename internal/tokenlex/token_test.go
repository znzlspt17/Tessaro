package tokenlex

import "testing"

func TestRejectCompleteCodeAndCompositeType(t *testing.T) {
	for _, value := range []string{"return a - b", "[]int", "a+b", "", "a /* comment */"} {
		if _, err := Kind(value); err == nil {
			t.Errorf("not a single token: %q", value)
		}
	}
}
func TestGoLexicalTokenIsAtomic(t *testing.T) {
	for _, value := range []string{"func", "a", "(", ":=", "!=", "\"two words\"", ";", "123"} {
		if _, err := Kind(value); err != nil {
			t.Errorf("valid token %q: %v", value, err)
		}
	}
}
