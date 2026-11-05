package tokensynth

import (
	"context"
	"encoding/json"
	"testing"
)

func TestObserverHasAtomicFrontiersAndDoesNotChangeSearch(t *testing.T) {
	p := subtractionSpec(t)
	target := scripted("package main ; func f0 ( a int , b int ) int { return a - b ; }")
	check := func(source string) (bool, error) { return source == string(target), nil }
	plain, err := Search(context.Background(), p, target, Options{Strategy: "beam"}, check)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	observed, err := Search(context.Background(), p, target, Options{Strategy: "beam", Trace: func(e Event) {
		data, _ := json.Marshal(e)
		var v map[string]any
		json.Unmarshal(data, &v)
		events = append(events, v)
	}}, check)
	if err != nil || plain.Source != observed.Source || plain.Calls != observed.Calls || plain.Candidates != observed.Candidates {
		t.Fatalf("observer changed search: %+v %+v %v", plain, observed, err)
	}
	frontiers, started, finished := 0, false, false
	for _, e := range events {
		switch e["reason"] {
		case "frontier":
			frontiers++
			paths, ok := e["frontier"].([]any)
			if !ok || len(paths) > 3 {
				t.Fatalf("invalid frontier %v", e)
			}
			for i, raw := range paths {
				path := raw.(map[string]any)
				if path["rank"] != float64(i+1) {
					t.Fatal("non-contiguous rank")
				}
			}
		case "model_started":
			started = true
		case "model_finished":
			finished = true
		}
	}
	if frontiers < 2 || !started || !finished {
		t.Fatalf("missing observation contract: frontiers=%d start=%v end=%v", frontiers, started, finished)
	}
}
