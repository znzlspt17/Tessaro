package grammardata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestPinnedAssetIntegrity(t *testing.T) {
	var source struct {
		Revision string
		Files    map[string]struct {
			SHA256 string `json:"sha256"`
		}
	}
	if err := json.Unmarshal(Source, &source); err != nil {
		t.Fatal(err)
	}
	if source.Revision != Revision {
		t.Fatal("revision mismatch")
	}
	for name, entry := range source.Files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			t.Errorf("hash mismatch: %s", name)
		}
	}
	var grammar struct{ Rules map[string]json.RawMessage }
	if err := json.Unmarshal(Grammar, &grammar); err != nil || len(grammar.Rules) != 116 {
		t.Fatalf("rules: %d, %v", len(grammar.Rules), err)
	}
	var nodes []json.RawMessage
	if err := json.Unmarshal(NodeTypes, &nodes); err != nil || len(nodes) == 0 {
		t.Fatalf("node metadata: %v", err)
	}
}
