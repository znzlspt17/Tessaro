//go:build windows

package runner

import (
	"context"
	"encoding/json"
	"jevcancode/internal/rank"
	"jevcancode/internal/spec"
	"jevcancode/internal/tokenspec"
	"testing"
)

func TestRunnerResourceTelemetryIncludesFailuresWithoutInventingServerValues(t *testing.T) {
	results := []Result{Run(context.Background(), spec.Problem{}, rank.Fake{}, Request{}), RunToken(context.Background(), tokenspec.Problem{}, rank.Fake{}, Request{})}
	for _, result := range results {
		data, _ := json.Marshal(result)
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["resource_scope"] != "client_process_lifetime" {
			t.Fatalf("wrong scope: %s", data)
		}
		if peak, ok := fields["process_peak_working_set_bytes"].(float64); !ok || peak <= 0 {
			t.Fatalf("peak missing: %s", data)
		}
		if cpu, ok := fields["client_process_cpu_ms"].(float64); !ok || cpu < 0 {
			t.Fatalf("cpu missing: %s", data)
		}
		if v, ok := fields["server_cpu_ms"]; !ok || v != nil {
			t.Fatalf("server cpu fabricated: %s", data)
		}
		if v, ok := fields["server_compute_ms"]; !ok || v != nil {
			t.Fatalf("server compute fabricated: %s", data)
		}
	}
}
