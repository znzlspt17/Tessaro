package researchdata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"jevcancode/internal/compare"
	"jevcancode/internal/tokenspec"
	"os"
	"path/filepath"
	"strings"
)

type Record struct {
	ID             string `json:"id"`
	Split          string `json:"split"`
	Category       string `json:"category,omitempty"`
	Spec           string `json:"spec"`
	Heldout        string `json:"heldout,omitempty"`
	Variant        string `json:"variant"`
	SourceHash     string `json:"source_sha256"`
	VariantHash    string `json:"variant_sha256"`
	SpecHash       string `json:"spec_sha256"`
	HeldoutHash    string `json:"heldout_sha256,omitempty"`
	Policy         string `json:"return_policy"`
	ValidationMode string `json:"validation_mode"`
}
type Corpus struct {
	Records []Record `json:"records"`
}
type original struct{ ID, Spec, Heldout, Source string }
type task struct{ id, category, intent, names, types, result, source, public, heldout string }

func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func jsonBytes(v any) ([]byte, error) {
	b, e := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), e
}
func write(base, name string, b []byte) error {
	path := filepath.Join(base, filepath.FromSlash(name))
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	return os.WriteFile(path, b, 0644)
}
func parseTests(s string) ([]tokenspec.Test, error) {
	var tests []tokenspec.Test
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	e := d.Decode(&tests)
	return tests, e
}

// 2026-11-12: 원본 데이터는 읽기만 하고 연구용 문제와 참조를 별도 파일로 고정한다.
func Generate(originalDir, out string) error {
	data, e := os.ReadFile(filepath.Join(originalDir, "references.json"))
	if e != nil {
		return e
	}
	var refs []original
	if e = json.Unmarshal(data, &refs); e != nil {
		return e
	}
	if len(refs) != 40 {
		return fmt.Errorf("expected 40 original references, got %d", len(refs))
	}
	var corpus Corpus
	manifest := compare.Manifest{Engine: "token", Models: []compare.Model{{ID: "jev", Ranker: "jev", Model: "jev-1.14.0", KeyEnv: "TYPESAFE_API_KEY"}}, Strategies: []string{"strict-greedy", "beam", "lds", "levin", "phs", "mcts"}, Repeats: 3, Seed: 42, Ordering: "paired-cross-over-v1"}
	add := func(id, split, category string, p tokenspec.Problem, source string, held []byte, originalSpec []byte) error {
		p.ReturnPolicy = tokenspec.ReturnPolicyValuesOnly
		var specBytes []byte
		var e error
		if originalSpec != nil {
			var fields map[string]json.RawMessage
			if e = json.Unmarshal(originalSpec, &fields); e != nil {
				return e
			}
			fields["return_policy"] = json.RawMessage(`"values-only-v1"`)
			specBytes, e = jsonBytes(fields)
		} else {
			specBytes, e = jsonBytes(p)
		}
		if e != nil {
			return e
		}
		variant, e := Variant(source)
		if e != nil {
			return fmt.Errorf("%s variant: %w", id, e)
		}
		specPath := split + "/specs/" + id + ".json"
		varPath := split + "/references/" + id + ".go.txt"
		holdPath := ""
		if e = write(out, specPath, specBytes); e != nil {
			return e
		}
		if e = write(out, varPath, []byte(variant)); e != nil {
			return e
		}
		if len(held) > 0 {
			holdPath = split + "/heldout/" + id + ".json"
			if e = write(out, holdPath, held); e != nil {
				return e
			}
		}
		record := Record{ID: id, Split: split, Category: category, Spec: specPath, Heldout: holdPath, Variant: varPath, SourceHash: hash([]byte(source)), VariantHash: hash([]byte(variant)), SpecHash: hash(specBytes), Policy: p.ReturnPolicy, ValidationMode: p.ValidationMode}
		if len(held) > 0 {
			record.HeldoutHash = hash(held)
		}
		corpus.Records = append(corpus.Records, record)
		manifest.Problems = append(manifest.Problems, compare.Problem{ID: id, Spec: specPath, Heldout: holdPath})
		return nil
	}
	for _, ref := range refs {
		raw, e := os.ReadFile(filepath.Join(originalDir, filepath.FromSlash(ref.Spec)))
		if e != nil {
			return e
		}
		p, e := tokenspec.Decode(bytes.NewReader(raw))
		if e != nil {
			return e
		}
		var held []byte
		if ref.Heldout != "" {
			held, e = os.ReadFile(filepath.Join(originalDir, filepath.FromSlash(ref.Heldout)))
			if e != nil {
				return e
			}
		}
		if e = add(ref.ID, "dev", "", p, ref.Source, held, raw); e != nil {
			return e
		}
	}
	for _, t := range evaluationTasks() {
		names := strings.Split(t.names, ",")
		typs := strings.Split(t.types, ",")
		hints := map[string]string{}
		for i, n := range names {
			hints[n] = typs[i]
		}
		pub, e := parseTests(t.public)
		if e != nil {
			return e
		}
		held, e := parseTests(t.heldout)
		if e != nil {
			return e
		}
		p := tokenspec.Problem{Intent: t.intent, Inputs: names, AllowedTypes: []string{"int", "bool", "string", "[]int", "[]string", "map[string]int", "map[string]string"}, TypeHints: hints, OutputTypeHint: t.result, PublicTests: pub, ValidationMode: "sampled_holdout", Literals: tokenspec.Literals{Integers: []int{-2, -1, 0, 1, 2}, Strings: []string{""}}}
		if e = p.Validate(); e != nil {
			return fmt.Errorf("%s: %w", t.id, e)
		}
		hb, e := jsonBytes(held)
		if e != nil {
			return e
		}
		if e = add(t.id, "eval", t.category, p, t.source, hb, nil); e != nil {
			return e
		}
	}
	for _, split := range []string{"dev", "eval", "all"} {
		m := manifest
		m.Problems = nil
		for i, r := range corpus.Records {
			if split == "all" || split == r.Split {
				m.Problems = append(m.Problems, manifest.Problems[i])
			}
		}
		b, e := jsonBytes(m)
		if e != nil {
			return e
		}
		if e = write(out, "compare-"+split+"-jev.json", b); e != nil {
			return e
		}
	}
	b, e := jsonBytes(corpus)
	if e != nil {
		return e
	}
	return write(out, "catalog.json", b)
}
