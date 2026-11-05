package spec

import (
	"strings"
	"testing"
)

func TestRejectWrongArgumentType(t *testing.T) {
	s := Problem{Name: "F", Intent: "test", Params: []Param{{Name: "x", Type: "int"}}, ReturnType: "int", PublicTests: []Test{{Args: []any{true}, Want: float64(1)}}}
	if s.Validate() == nil {
		t.Fatal("expected wrong argument type to fail")
	}
}
func TestReservedFunctionName(t *testing.T) {
	p := Problem{Name: "main", Intent: "test", ReturnType: "bool", PublicTests: []Test{{Want: true}}}
	if p.Validate() == nil {
		t.Fatal("main function name must be rejected")
	}
}
func TestRejectEmbeddedHeldout(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"intent":"x","name":"F","return_type":"bool","public_tests":[{"args":[],"want":true}],"heldout_tests":[]}`))
	if err == nil {
		t.Fatal("embedded heldout accepted")
	}
}
func TestRejectNullAndLimits(t *testing.T) {
	cases := []string{`{"intent":"x","name":"F","return_type":"int","public_tests":[{"args":[],"want":null}]}`, `{"intent":"x","name":"F","return_type":"int","params":[{"name":"x","type":"int"}],"public_tests":[{"args":[null],"want":0}]}`, `{"intent":"x","name":"F","return_type":"int","constants":[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16],"public_tests":[{"args":[],"want":0}]}`}
	for _, s := range cases {
		if _, err := Decode(strings.NewReader(s)); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestLocalAndStatementBudgets(t *testing.T) {
	p, err := Decode(strings.NewReader(`{"intent":"x","name":"F","return_type":"int","public_tests":[{"args":[],"want":0}],"budgets":{"max_locals":0,"max_statements":8}}`))
	if err != nil || p.Budgets.MaxLocals == nil || *p.Budgets.MaxLocals != 0 {
		t.Fatalf("%+v %v", p, err)
	}
}
