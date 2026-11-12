package researchdata

import (
	"bytes"
	"encoding/json"
	"tessaro/internal/compare"
	"tessaro/internal/tokenspec"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResearchCorpusIntegrityAndSeparation(t *testing.T) {
	pinned := filepath.Join("..", "..", "examples", "research")
	orig := filepath.Join("..", "..", "examples", "token")
	// 2026-11-12: 생성 산출물 없이 checkout해도 고정 카탈로그와 실제 생성 결과를 동일하게 검증한다.
	base := t.TempDir()
	if err := Generate(orig, base); err != nil {
		t.Fatal(err)
	}
	catalogBytes, err := os.ReadFile(filepath.Join(pinned, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if hash(catalogBytes) != "9175b989ab5c78b19fa53c825cdeb0f7a9f9bd82f83c23912d02356f930e668b" {
		t.Fatal("catalog hash changed")
	}
	generatedCatalog, err := os.ReadFile(filepath.Join(base, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generatedCatalog, catalogBytes) {
		t.Fatal("generated catalog differs from pinned catalog")
	}
	corpus, err := LoadCatalog(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.Records) != 60 {
		t.Fatalf("got %d", len(corpus.Records))
	}
	raw, err := os.ReadFile(filepath.Join(orig, "references.json"))
	if err != nil {
		t.Fatal(err)
	}
	var refs []original
	if err = json.Unmarshal(raw, &refs); err != nil {
		t.Fatal(err)
	}
	byID := map[string]original{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	counts := map[string]int{}
	cats := map[string]int{}
	ids := map[string]bool{}
	exhaustive := 0
	for _, r := range corpus.Records {
		if ids[r.ID] {
			t.Fatalf("duplicate %s", r.ID)
		}
		ids[r.ID] = true
		counts[r.Split]++
		if r.Split == "eval" {
			cats[r.Category]++
		}
		spec, err := readHashed(base, r.Spec, r.SpecHash)
		if err != nil {
			t.Fatal(err)
		}
		src, err := readHashed(base, r.Variant, r.VariantHash)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tokenspec.Decode(bytes.NewReader(spec))
		if err != nil {
			t.Fatal(err)
		}
		if p.ReturnPolicy != tokenspec.ReturnPolicyValuesOnly || r.Policy != p.ReturnPolicy {
			t.Fatalf("policy %s", r.ID)
		}
		if r.Heldout != "" {
			heldBytes, err := readHashed(base, r.Heldout, r.HeldoutHash)
			if err != nil {
				t.Fatal(err)
			}
			if r.Split == "eval" {
				held, err := tokenspec.DecodeTests(bytes.NewReader(heldBytes), p)
				if err != nil {
					t.Fatal(err)
				}
				public := map[string]bool{}
				for _, tc := range p.PublicTests {
					k, _ := json.Marshal(tc.Inputs)
					public[string(k)] = true
				}
				for _, tc := range held {
					k, _ := json.Marshal(tc.Inputs)
					if public[string(k)] {
						t.Fatalf("public/heldout overlap %s", r.ID)
					}
				}
			}
		} else if p.ValidationMode == "exhaustive_public" {
			exhaustive++
		} else {
			t.Fatalf("missing heldout %s", r.ID)
		}
		if r.Split == "dev" {
			o, ok := byID[r.ID]
			if !ok || hash([]byte(o.Source)) != r.SourceHash {
				t.Fatalf("source hash %s", r.ID)
			}
			variant, err := Variant(o.Source)
			if err != nil || variant != string(src) {
				t.Fatalf("variant %s", r.ID)
			}
			old, err := os.ReadFile(filepath.Join(orig, filepath.FromSlash(o.Spec)))
			if err != nil {
				t.Fatal(err)
			}
			var oldFields, newFields map[string]json.RawMessage
			if err := json.Unmarshal(old, &oldFields); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(spec, &newFields); err != nil {
				t.Fatal(err)
			}
			delete(newFields, "return_policy")
			oldJSON, err := json.Marshal(oldFields)
			if err != nil {
				t.Fatal(err)
			}
			newJSON, err := json.Marshal(newFields)
			if err != nil {
				t.Fatal(err)
			}
			var oldValue, newValue any
			if err := json.Unmarshal(oldJSON, &oldValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(newJSON, &newValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(oldValue, newValue) {
				t.Fatalf("original spec fields changed %s", r.ID)
			}
			oldp, err := tokenspec.Decode(bytes.NewReader(old))
			if err != nil {
				t.Fatal(err)
			}
			oldp.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
			if !reflect.DeepEqual(oldp, p) {
				t.Fatalf("changed dev contract %s", r.ID)
			}
			if o.Heldout != "" {
				b, err := os.ReadFile(filepath.Join(orig, filepath.FromSlash(o.Heldout)))
				if err != nil || hash(b) != r.HeldoutHash {
					t.Fatalf("heldout changed %s", r.ID)
				}
			}
		}
	}
	if counts["dev"] != 40 || counts["eval"] != 20 || exhaustive != 5 || len(cats) != 5 {
		t.Fatalf("counts=%v categories=%v exhaustive=%d", counts, cats, exhaustive)
	}
	for k, n := range cats {
		if n != 4 {
			t.Fatalf("category %s count %d", k, n)
		}
	}
	for _, split := range []string{"dev", "eval", "all"} {
		data, err := os.ReadFile(filepath.Join(base, "compare-"+split+"-jev.json"))
		if err != nil {
			t.Fatal(err)
		}
		pinnedManifest, err := os.ReadFile(filepath.Join(pinned, "compare-"+split+"-jev.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, pinnedManifest) {
			t.Fatalf("generated manifest %s differs from pinned manifest", split)
		}
		m, err := compare.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		want := counts[split]
		if split == "all" {
			want = 60
		}
		if len(m.Problems) != want || m.Repeats != 3 || m.Seed != 42 || m.Ordering != "paired-cross-over-v1" || len(m.Strategies) != 6 || m.Models[0].Model != "jev-1.14.0" || m.Models[0].KeyEnv != "TYPESAFE_API_KEY" || strings.Contains(string(data), "reference") {
			t.Fatalf("manifest %s invalid", split)
		}
	}
}

func TestCorpusGenerationIsDeterministic(t *testing.T) {
	out := t.TempDir()
	original := filepath.Join("..", "..", "examples", "token")
	if err := Generate(original, out); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile(filepath.Join("..", "..", "examples", "research", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, pinned) {
		t.Fatal("regeneration changes pinned catalog")
	}
}
