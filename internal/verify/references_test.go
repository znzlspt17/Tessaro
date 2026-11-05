package verify

import (
	"context"
	"encoding/json"
	"jevcancoding/internal/spec"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSuiteReferences(t *testing.T) {
	base := filepath.Join("..", "..", "examples", "suite")
	b, err := os.ReadFile(filepath.Join(base, "references.json"))
	if err != nil {
		t.Fatal(err)
	}
	var refs []struct{ ID, Spec, Heldout, Body string }
	if err = json.Unmarshal(b, &refs); err != nil || len(refs) != 24 {
		t.Fatalf("references %d: %v", len(refs), err)
	}
	for _, ref := range refs {
		t.Run(ref.ID, func(t *testing.T) {
			f, err := os.Open(filepath.Join(base, ref.Spec))
			if err != nil {
				t.Fatal(err)
			}
			p, err := spec.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			f, err = os.Open(filepath.Join(base, ref.Heldout))
			if err != nil {
				t.Fatal(err)
			}
			held, err := spec.DecodeTests(f, p)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			tests := append(append([]spec.Test(nil), p.PublicTests...), held...)
			ok, err := Check(ctx, p, ref.Body, tests)
			if err != nil || !ok {
				t.Fatalf("reference failed: %v %v", ok, err)
			}
		})
	}
}
