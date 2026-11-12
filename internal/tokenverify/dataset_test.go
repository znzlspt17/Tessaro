package tokenverify

import (
	"context"
	"encoding/json"
	"jevcancode/internal/tokenspec"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type datasetReference struct {
	ID      string `json:"id"`
	Spec    string `json:"spec"`
	Heldout string `json:"heldout,omitempty"`
	Source  string `json:"source"`
}

func TestTokenDatasetReferences(t *testing.T) {
	base := filepath.Join("..", "..", "examples", "token")
	data, err := os.ReadFile(filepath.Join(base, "references.json"))
	if err != nil {
		t.Fatal(err)
	}
	var refs []datasetReference
	if err = json.Unmarshal(data, &refs); err != nil || len(refs) != 40 {
		t.Fatalf("references %d: %v", len(refs), err)
	}
	ids := map[string]bool{}
	semaphore := make(chan struct{}, 4)
	for _, ref := range refs {
		if ref.ID == "" || ids[ref.ID] {
			t.Fatalf("duplicate or empty id %q", ref.ID)
		}
		ids[ref.ID] = true
		ref := ref
		t.Run(ref.ID, func(t *testing.T) {
			t.Parallel()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			f, err := os.Open(filepath.Join(base, ref.Spec))
			if err != nil {
				t.Fatal(err)
			}
			p, err := tokenspec.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if p.ValidationMode == "exhaustive_public" {
				if ref.Heldout != "" {
					t.Fatal("exhaustive boolean case has heldout")
				}
				if len(p.PublicTests) != (1 << len(p.Inputs)) {
					t.Fatal("incomplete boolean truth table")
				}
				seen := map[string]bool{}
				for _, test := range p.PublicTests {
					for _, name := range p.Inputs {
						if _, ok := test.Inputs[name].(bool); !ok {
							t.Fatal("nonboolean exhaustive input")
						}
					}
					key, _ := json.Marshal(test.Inputs)
					if seen[string(key)] {
						t.Fatal("duplicate truth-table input")
					}
					seen[string(key)] = true
				}
			} else if ref.Heldout == "" {
				t.Fatal("sampled case lacks heldout")
			}
			tests := append([]tokenspec.Test(nil), p.PublicTests...)
			if ref.Heldout != "" {
				f, err = os.Open(filepath.Join(base, ref.Heldout))
				if err != nil {
					t.Fatal(err)
				}
				held, err := tokenspec.DecodeTests(f, p)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
				seen := map[string]bool{}
				for _, test := range p.PublicTests {
					key, _ := json.Marshal(test.Inputs)
					seen[string(key)] = true
				}
				for _, test := range held {
					key, _ := json.Marshal(test.Inputs)
					if seen[string(key)] {
						t.Fatalf("heldout input overlaps public: %s", key)
					}
				}
				tests = append(tests, held...)
			}
			b, err := Validate(p, ref.Source)
			if err != nil {
				t.Fatal(err)
			}
			if b.Name != "f0" || !strings.HasPrefix(b.Source, "package main") {
				t.Fatal("invalid reference binding")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			ok, _, err := Check(ctx, p, ref.Source, tests)
			if err != nil || !ok {
				t.Fatalf("reference failed: %v %v", ok, err)
			}
		})
	}
}
