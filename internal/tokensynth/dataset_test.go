package tokensynth

import (
	"encoding/json"
	"fmt"
	"go/scanner"
	"go/token"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokenverify"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 참조 정답은 테스트 전용이다. 런타임 모델에 전달하지 않고 토큰 공급기의 도달성을 검사한다.
func TestDatasetReferenceFrontiers(t *testing.T) {
	base := filepath.Join("..", "..", "examples", "token")
	data, e := os.ReadFile(filepath.Join(base, "references.json"))
	if e != nil {
		t.Fatal(e)
	}
	var refs []struct{ ID, Spec, Source string }
	if e = json.Unmarshal(data, &refs); e != nil {
		t.Fatal(e)
	}
	for _, ref := range refs {
		t.Run(ref.ID, func(t *testing.T) {
			data, e := os.ReadFile(filepath.Join(base, ref.Spec))
			if e != nil {
				t.Fatal(e)
			}
			p, e := tokenspec.Decode(strings.NewReader(string(data)))
			if e != nil {
				t.Fatal(e)
			}
			var scan scanner.Scanner
			scan.Init(token.NewFileSet().AddFile("ref.go", -1, len(ref.Source)), []byte(ref.Source), nil, 0)
			state, e := NewState()
			if e != nil {
				t.Fatal(e)
			}
			names := map[string]string{}
			for _, name := range p.Inputs {
				names[name] = name
			}
			for _, name := range []string{"main", "f0", "int", "bool", "string", "true", "false", "nil", "len", "cap", "append", "copy", "delete", "make", "_"} {
				names[name] = name
			}
			nextName := 0
			for {
				_, kind, lit := scan.Scan()
				if kind == token.EOF {
					break
				}
				if Complete(state) {
					if kind == token.SEMICOLON {
						continue
					}
					t.Fatal("tokens after completed source")
				}
				lexeme := lit
				if lexeme == "" {
					lexeme = kind.String()
				}
				if kind == token.SEMICOLON {
					lexeme = ";"
				}
				if kind == token.IDENT {
					if renamed, ok := names[lexeme]; ok {
						lexeme = renamed
					} else {
						for {
							fresh := fmt.Sprintf("v%d", nextName)
							nextName++
							if _, used := names[fresh]; !used {
								names[lexeme] = fresh
								names[fresh] = fresh
								lexeme = fresh
								break
							}
						}
					}
				}
				choices := mustNext(t, p, state)
				var chosen *Choice
				for i := range choices {
					if choices[i].Lexeme == lexeme {
						chosen = &choices[i]
						break
					}
				}
				if chosen == nil {
					t.Fatalf("unreachable %q after %s; choices=%v", lexeme, state.Source(), choices)
				}
				state, e = state.Advance(*chosen, "replay")
				if e != nil {
					t.Fatal(e)
				}
				if reason := tokenverify.CheckBodyPrefix(p, state.Tokens); reason != "" {
					t.Fatalf("reference body rejected: %s after %s", reason, state.Source())
				}
			}
			if !Complete(state) {
				t.Fatalf("not complete: %s", state.Source())
			}
		})
	}
}
