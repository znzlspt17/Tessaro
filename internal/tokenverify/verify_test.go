package tokenverify

import (
	"context"
	"errors"
	"jevcancoding/internal/tokenspec"
	"jevcancoding/internal/verify"
	"strings"
	"testing"
)

func problem(t *testing.T, s string) tokenspec.Problem {
	t.Helper()
	p, e := tokenspec.Decode(strings.NewReader(s))
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestValidateReorderedGroupedParameters(t *testing.T) {
	p := problem(t, `{"intent":"sum","inputs":["a","b"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":2,"b":3},"want":5}]}`)
	b, e := Validate(p, `package main
func Sum(b, a int) int { return a+b }`)
	if e != nil || b.Name != "Sum" || strings.Join(b.Params, ",") != "b,a" {
		t.Fatalf("%+v %v", b, e)
	}
}
func TestRejectDisallowedCall(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`)
	if _, e := Validate(p, `package main
func F() int { return F() }`); e == nil {
		t.Fatal("recursive call accepted")
	}
}
func TestCheckSliceLoop(t *testing.T) {
	p := problem(t, `{"intent":"sum","inputs":["xs"],"allowed_types":["int","[]int"],"public_tests":[{"inputs":{"xs":[1,2,3]},"want":6},{"inputs":{"xs":[]},"want":0}]}`)
	ok, timing, e := Check(context.Background(), p, `package main
func Sum(xs []int) int { sum := 0; for _,x := range xs { sum += x }; return sum }`, p.PublicTests)
	if e != nil || !ok || !timing.Compiled {
		t.Fatalf("%v %+v %v", ok, timing, e)
	}
}
func TestCheckMapUpdatesAndFreshInputs(t *testing.T) {
	p := problem(t, `{"intent":"count","inputs":["xs"],"allowed_types":["[]string","map[string]int"],"public_tests":[{"inputs":{"xs":["a","b","a"]},"want":{"a":2,"b":1}},{"inputs":{"xs":["a"]},"want":{"a":1}}]}`)
	source := `package main
func Count(xs []string) map[string]int { m := make(map[string]int); for _, v := range xs { m[v]++ }; return m }`
	ok, _, err := Check(context.Background(), p, source, p.PublicTests)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
func TestReorderedCallsAndStringByte(t *testing.T) {
	p := problem(t, `{"intent":"calculate","inputs":["s","n"],"allowed_types":["string","int"],"public_tests":[{"inputs":{"s":"abc","n":2},"want":99}]}`)
	source := `package main
func Last(n int,s string) int { b := s[n]; return int(b) }`
	ok, _, err := Check(context.Background(), p, source, p.PublicTests)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
func TestRejectUnsafeSyntaxAndOversizedConstantMake(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`)
	cases := []string{`package main
import "os"
func F() int { return 1 }`, `package main
func F() int { go F(); return 1 }`, `package main
func F() int { x := make([]int, 1 << 13); return len(x) }`, `package main
func F() int { f := func()int{return 1}; return f() }`}
	for _, src := range cases {
		if _, err := Validate(p, src); err == nil {
			t.Fatalf("accepted %s", src)
		}
	}
}
func TestExecutionTimeoutAndParentCancellation(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`)
	src := `package main
func F() int { for {} }`
	_, timing, err := Check(context.Background(), p, src, p.PublicTests)
	if !errors.Is(err, ErrExecutionTimeout) || !timing.Compiled {
		t.Fatalf("%+v %v", timing, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = Check(ctx, p, src, p.PublicTests)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation: %v", err)
	}
}
func TestMutatingInputUsesFreshLiterals(t *testing.T) {
	p := problem(t, `{"intent":"increment","inputs":["xs"],"allowed_types":["int","[]int"],"public_tests":[{"inputs":{"xs":[1]},"want":2},{"inputs":{"xs":[1]},"want":2}]}`)
	src := `package main
func Inc(xs []int) int { xs[0]++; return xs[0] }`
	ok, _, err := Check(context.Background(), p, src, p.PublicTests)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
func TestRejectInvalidTestValues(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`)
	src := `package main
func F(a int) int { return a }`
	for _, test := range []tokenspec.Test{{Inputs: map[string]any{"a": nil}, Want: 1}, {Inputs: map[string]any{"a": 1}, Want: nil}, {Inputs: map[string]any{}, Want: 1}} {
		if _, _, err := Check(context.Background(), p, src, []tokenspec.Test{test}); err == nil {
			t.Fatal("invalid test accepted")
		}
	}
}
func TestCommentsNotRetainedAndEnvironmentAllowlisted(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":[],"allowed_types":["int"],"public_tests":[{"inputs":{},"want":1}]}`)
	b, err := Validate(p, "package main\n// hidden marker\nfunc F() int { return 1 }")
	if err != nil || strings.Contains(b.Source, "hidden marker") {
		t.Fatalf("comment retained: %+v %v", b, err)
	}
	t.Setenv("TOKENVERIFY_FAKE_SECRET_MARKER", "fake")
	for _, env := range [][]string{verify.ExportCompileEnv(t.TempDir()), verify.ExportExecuteEnv(t.TempDir())} {
		for _, v := range env {
			if strings.HasPrefix(v, "TOKENVERIFY_FAKE_SECRET_MARKER=") {
				t.Fatal("environment marker inherited")
			}
		}
	}
}
func TestRejectMissingReturnAndExcessNesting(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`)
	bad := []string{`package main
func F(a int) int { if a>0 { return 1 } }`, `package main
func F(a int) int { if a>0 {if a>0 {if a>0 {if a>0 {if a>0 {return 1}}}}}; return 1 }`}
	for _, src := range bad {
		if _, err := Validate(p, src); err == nil {
			t.Fatal("invalid flow accepted")
		}
	}
}
func TestDeleteAndContinue(t *testing.T) {
	p := problem(t, `{"intent":"filter","inputs":["m"],"allowed_types":["map[string]int"],"public_tests":[{"inputs":{"m":{"a":0,"b":2}},"want":{"b":2}}]}`)
	src := `package main
func Filter(m map[string]int) map[string]int { for k,v := range m { if v != 0 {continue}; delete(m,k) }; return m }`
	ok, _, err := Check(context.Background(), p, src, p.PublicTests)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
func TestNamedResultAndBareReturn(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":3},"want":3}]}`)
	ok, _, err := Check(context.Background(), p, `package main
func F(a int) (out int) { out=a; return }`, p.PublicTests)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
func TestExplicitZeroLocals(t *testing.T) {
	p := problem(t, `{"intent":"x","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`)
	p.Budgets.MaxLocals = 0
	p.Budgets.MaxLocalsSet = true
	if _, err := Validate(p, `package main
func F(a int) int { x:=a; return x }`); err == nil {
		t.Fatal("explicit zero locals ignored")
	}
	if _, err := Validate(p, `package main
func F(a int) int { return a }`); err != nil {
		t.Fatal(err)
	}
}
