//go:build windows

package desktop

import (
	"encoding/json"
	"flag"
	"path/filepath"
	"runtime"
	"strings"
	"tessaro/internal/application"
	"tessaro/internal/compare"
	"tessaro/internal/researchresults"
	"tessaro/internal/runner"
	"tessaro/internal/tokenspec"
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
	if got := w.mw.Title(); got != "Tessaro" {
		t.Errorf("window title = %q", got)
	}
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
	checkNativeResearchControls(t, w)
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

func checkNativeResearchControls(t *testing.T, w *window) {
	t.Helper()
	v := w.research
	if v == nil || v.path == nil || v.trials == nil {
		t.Fatal("research controls missing")
	}
	v.study = researchresults.Study{Strategies: []researchresults.StrategySummary{{Strategy: "beam", All: researchresults.Metrics{Planned: 2, Success: 1}, Dev: researchresults.Metrics{Planned: 1, Success: 1}}}, Trials: []researchresults.Trial{{Split: "dev", Outcome: compare.Outcome{Trial: compare.Trial{Strategy: "beam", Problem: "p", Seed: 42}, Result: runner.Result{Status: "success", Source: "return x"}}}, {Split: "eval", Outcome: compare.Outcome{Trial: compare.Trial{Strategy: "beam", Problem: "q"}, Result: runner.Result{Status: "failed"}}}}}
	_ = v.split.SetCurrentIndex(1)
	if len(v.rows) != 1 || v.summaryModel.RowCount() != 1 {
		t.Fatalf("dev rows %d summary %d", len(v.rows), v.summaryModel.RowCount())
	}
	_ = v.trials.SetCurrentIndex(0)
	if !strings.Contains(v.detail.Text(), "return x") {
		t.Fatalf("trial details missing source: %q", v.detail.Text())
	}
}

func TestSettingsPathUsesTessaroDirectory(t *testing.T) {
	if got := settingsPath(); got != "" && filepath.Base(filepath.Dir(got)) != "Tessaro" {
		t.Fatalf("settings directory = %s", filepath.Base(filepath.Dir(got)))
	}
}
