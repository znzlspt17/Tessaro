package tokensynth_test

import (
	"bytes"
	"encoding/json"
	"go/scanner"
	"go/token"
	"tessaro/internal/researchdata"
	"tessaro/internal/tokensynth"
	"os"
	"path/filepath"
	"testing"
)

func TestUnaryReferenceGrammarContexts(t *testing.T) {
	// 2026-11-12: 무시된 생성물 없이도 고정 참조 전체의 단항 연산 경로를 검사한다.
	base := t.TempDir()
	if err := researchdata.Generate(filepath.Join("..", "..", "examples", "token"), base); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(base, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile(filepath.Join("..", "..", "examples", "research", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, pinned) {
		t.Fatal("generated catalog differs from pinned catalog")
	}
	var catalog struct {
		Records []struct{ ID, Variant string }
	}
	if err = json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Records) != 60 {
		t.Fatalf("want 60 references, got %d", len(catalog.Records))
	}
	g, err := tokensynth.Grammar()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range catalog.Records {
		t.Run(rec.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rec.Variant)))
			if err != nil {
				t.Fatal(err)
			}
			var scan scanner.Scanner
			scan.Init(token.NewFileSet().AddFile("ref.go", -1, len(data)), data, nil, 0)
			state := g.NewState("source_file")
			pos := 0
			for {
				_, kind, lex := scan.Scan()
				if kind == token.EOF {
					break
				}
				if lex == "" {
					lex = kind.String()
				}
				if kind == token.SEMICOLON {
					lex = ";"
				}
				if lex == ";" && state.Accepts() {
					break
				}
				state, err = state.Advance(lex)
				pos++
				if err != nil {
					t.Fatalf("%s: %v", lex, err)
				}
				if lex == "!" && state.NotOnlyStatementLeft(pos) {
					t.Fatalf("valid not rejected")
				}
			}
		})
	}
}
