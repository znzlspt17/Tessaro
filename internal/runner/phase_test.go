package runner

import (
	"context"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokensynth"
	"strings"
	"testing"
)

func TestTokenValidationPhasesAreObservedWithoutHeldoutValues(t *testing.T) {
	p, err := tokenspec.Decode(strings.NewReader(`{"intent":"identity","inputs":["a"],"allowed_types":["int"],"public_tests":[{"inputs":{"a":1},"want":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	phases := map[string]bool{}
	out := RunToken(context.Background(), p, &scriptedTokenRanker{target: "package main ; func f0 ( a int ) int { return a ; }"}, Request{Strategy: "beam", TokenTrace: func(e tokensynth.Event) {
		if e.Reason == "phase_started" {
			phases[e.Phase] = true
		}
	}, TokenHeldout: func() ([]tokenspec.Test, error) {
		return []tokenspec.Test{{Inputs: map[string]any{"a": 17}, Want: 17}}, nil
	}})
	if out.Status != "success" {
		t.Fatalf("%+v", out)
	}
	for _, name := range []string{"typecheck", "compile", "public", "heldout"} {
		if !phases[name] {
			t.Errorf("missing %s phase", name)
		}
	}
}
