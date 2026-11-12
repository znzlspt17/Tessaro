//go:build windows

package desktop

import (
	"encoding/json"
	"flag"
	"jevcancode/internal/application"
	"jevcancode/internal/tokenspec"
	"runtime"
	"strings"
	"testing"
)

var nativeGUI = flag.Bool("native-gui", false, "run native Windows control constructor test with embedded manifest")

func TestNativeWindowDefaultValues(t *testing.T) {
	if !*nativeGUI {
		t.Skip("requires manifest; use scripts/test-desktop.ps1")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := newWindow(desktopSettings{})
	if err != nil {
		t.Fatal("window creation failed")
	}
	defer w.mw.Dispose()
	checks := []struct {
		name      string
		got, want float64
	}{
		{"tokens", w.tokens.Value(), 256}, {"calls", w.calls.Value(), 128}, {"candidates", w.candidates.Value(), 8192}, {"timeout", w.timeout.Value(), 300}, {"locals", w.locals.Value(), 8}, {"statements", w.statements.Value(), 32}, {"nesting", w.nesting.Value(), 4}, {"depth", w.depth.Value(), 4}, {"repeats", w.repeats.Value(), 1},
	}
	if got := w.runOptions(); got.ReturnPolicy != tokenspec.ReturnPolicyValuesOnly || got.Seed != 42 || got.DecisionBatchSize != 1 {
		t.Errorf("run options = %+v", got)
	}
	checkNativeStrategyChoicesFollowEngine(t)
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v want %v", c.name, c.got, c.want)
		}
	}
}

func TestHypothesisStatusShowsStageAndAPIUsage(t *testing.T) {
	var s application.Snapshot
	if err := json.Unmarshal([]byte(`{"job_kind":"hypothesis","status":"running","phase":"reachability","completed":7,"total":40,"requests_used":23,"request_limit":10000}`), &s); err != nil {
		t.Fatal(err)
	}
	got := statusText(s)
	for _, want := range []string{"도달 가능성", "7/40", "23/10000"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q missing %q", got, want)
		}
	}
}

func checkNativeStrategyChoicesFollowEngine(t *testing.T) {
	if !*nativeGUI {
		t.Skip("requires manifest; use scripts/test-desktop.ps1")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := newWindow(desktopSettings{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.mw.Dispose()
	if got := w.strategy.Model().([]string); strings.Join(got, ",") != "beam,greedy,strict-greedy,lds,levin,phs,mcts" {
		t.Errorf("token strategies = %v", got)
	}
	_ = w.strategy.SetCurrentIndex(6)
	_ = w.engine.SetCurrentIndex(1)
	if got := w.strategy.Model().([]string); strings.Join(got, ",") != "beam,greedy,strict-greedy" {
		t.Errorf("ast strategies = %v", got)
	}
	if got := w.strategy.CurrentIndex(); got != 0 {
		t.Errorf("ast fallback index = %d", got)
	}
	_ = w.engine.SetCurrentIndex(0)
	_ = w.strategy.SetCurrentIndex(1)
	_ = w.engine.SetCurrentIndex(1)
	if got := w.strategy.CurrentIndex(); got != 1 {
		t.Errorf("shared strategy index = %d", got)
	}
}
