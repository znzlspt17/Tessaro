package tokenspec

import (
	"strings"
	"testing"
)

func TestNamedInputsDoNotRequireGoSignature(t *testing.T) {
	p, err := Decode(strings.NewReader(`{"intent":"a minus b","inputs":["a","b"],"allowed_types":["int","bool"],"public_tests":[{"inputs":{"a":7,"b":2},"want":5}]}`))
	if err != nil || len(p.Inputs) != 2 {
		t.Fatalf("named specification rejected: %v", err)
	}
}
func TestRejectLegacyCompleteSignature(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"intent":"x","name":"F","params":[],"return_type":"int","public_tests":[]}`))
	if err == nil {
		t.Fatal("legacy specification accepted in token engine")
	}
}
