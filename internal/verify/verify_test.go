package verify

import (
	"context"
	"jevcancoding/internal/spec"
	"testing"
	"time"
)

func TestRejectCall(t *testing.T) {
	p := spec.Problem{Intent: "test", Name: "F", ReturnType: "int", PublicTests: []spec.Test{{Want: 1}}}
	_, err := Check(context.Background(), p, "return evil()", nil)
	if err == nil {
		t.Fatal("call accepted")
	}
}
func TestCompileAndExecuteGeneratedFunction(t *testing.T) {
	p := spec.Problem{Intent: "absolute value", Name: "Abs", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{1}, Want: 1}}}
	tests := []spec.Test{{Args: []any{-3}, Want: 3}, {Args: []any{2}, Want: 2}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ok, err := Check(ctx, p, "if x < 0 { return 0 - x } else { return x }", tests)
	if err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
	ok, err = Check(ctx, p, "return x", tests)
	if err != nil || ok {
		t.Fatalf("wrong candidate got %v, %v", ok, err)
	}
}
func TestFunctionNamedOSAndBooleanNot(t *testing.T) {
	p := spec.Problem{Intent: "negate", Name: "os", Params: []spec.Param{{Name: "x", Type: "bool"}}, ReturnType: "bool", PublicTests: []spec.Test{{Args: []any{true}, Want: false}}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ok, err := Check(ctx, p, "return !x", p.PublicTests)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
func TestDivisionByZeroFailsTypeCheck(t *testing.T) {
	p := spec.Problem{Intent: "division", Name: "F", ReturnType: "int", PublicTests: []spec.Test{{Want: 0}}}
	if _, err := Source(p, "return 1 / 0"); err == nil {
		t.Fatal("constant zero division accepted")
	}
}
func TestLocalAssignmentAndBranchContinuation(t *testing.T) {
	p := spec.Problem{Intent: "abs", Name: "Abs", Params: []spec.Param{{Name: "x", Type: "int"}}, ReturnType: "int", PublicTests: []spec.Test{{Args: []any{-2}, Want: 2}, {Args: []any{3}, Want: 3}}}
	body := "v0 := x; if x < 0 { v0 = 0 - x } else { v0 = x }; return v0"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ok, err := Check(ctx, p, body, p.PublicTests)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
