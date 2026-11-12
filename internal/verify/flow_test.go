package verify

import (
	"jevcancode/internal/spec"
	"testing"
)

func flowProblem() spec.Problem {
	return spec.Problem{Intent: "return integer", Name: "F", Params: []spec.Param{{Name: "x", Type: "int"}, {Name: "flag", Type: "bool"}}, ReturnType: "int", Constants: []int{-1, 0, 1}, PublicTests: []spec.Test{{Args: []any{1, true}, Want: 1}}}
}

func TestVerifierRespectsExpressionDepthBudget(t *testing.T) {
	for _, tc := range []struct {
		depth int
		body  string
		valid bool
	}{
		{1, "return x + (x + 1)", false},
		{5, "return x + (x + (x + (x + (x + 1))))", true},
		{4, "return x + (x + (x + (x + -1)))", true},
	} {
		p := flowProblem()
		p.Budgets = &spec.Budgets{MaxDepth: &tc.depth}
		_, err := Source(p, tc.body)
		if (err == nil) != tc.valid {
			t.Errorf("depth %d: valid=%v error=%v", tc.depth, tc.valid, err)
		}
	}
}

func TestRejectInvalidLocalFlow(t *testing.T) {
	for _, body := range []string{
		"v0 := v0 + 1; return v0",
		"v0 = x; return v0",
		"x = 1; return x",
		"if flag { v0 := x; return v0 } else { return v0 }",
		"v0 := x; if flag { v0 := 1; return v0 } else { return v0 }",
		"v0 := true; v0 = x; return x",
		"if flag { return x } else { v0 := x; v0 = 1 }",
		"return x; return 1",
		"v0 := x; return x",
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := Source(flowProblem(), body); err == nil {
				t.Fatal("invalid local flow accepted")
			}
		})
	}
}

func TestGlobalLocalAndStatementLimits(t *testing.T) {
	zero, two := 0, 2
	p := flowProblem()
	p.Budgets = &spec.Budgets{MaxLocals: &zero}
	if _, err := Source(p, "v0 := x; return v0"); err == nil {
		t.Fatal("local declaration exceeded zero budget")
	}
	p.Budgets = &spec.Budgets{MaxStatements: &two}
	if _, err := Source(p, "if flag { return x } else { return 1 }"); err == nil {
		t.Fatal("branches were not included in the statement budget")
	}
}
