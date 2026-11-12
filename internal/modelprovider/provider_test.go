package modelprovider

import (
	"context"
	"tessaro/internal/compare"
	"tessaro/internal/rank"
	"testing"
)

func TestRejectsTestAndUnsupportedLocalModelsBeforeLaunching(t *testing.T) {
	p := New(Config{})
	defer p.Close()
	for _, model := range []compare.Model{
		{ID: "fake", Ranker: "fake"},
		{ID: "unknown", Ranker: "unknown"},
		{ID: "other", Ranker: "local", Model: "other/model"},
		{ID: "decider", Ranker: "local", Model: "Mapika/decider-4b", URL: "http://example.invalid/v1/systemone"},
		{ID: "decider", Ranker: "local", Model: "Mapika/decider-4b", URL: "http://127.0.0.1:9000/v1/systemone"},
	} {
		t.Run(model.ID+model.URL, func(t *testing.T) {
			if r, err := p.Resolve(context.Background(), model); err == nil || r != nil {
				t.Fatalf("unsupported model was executable: ranker=%T error=%v", r, err)
			}
		})
	}
}

func TestJevKeepsRemoteConfigurationWithoutReadingAuthentication(t *testing.T) {
	p := New(Config{})
	defer p.Close()
	r, err := p.Resolve(context.Background(), compare.Model{ID: "jev", Ranker: "jev", Model: "jev-1.14.0", KeyEnv: "UNREAD_TEST_AUTH_NAME"})
	if err != nil {
		t.Fatal(err)
	}
	h, ok := r.(*rank.HTTP)
	if !ok || h.URL != "https://api.typesafe.ai/v1/systemone" || h.Model != "jev-1.14.0" || h.APIKeyEnv != "UNREAD_TEST_AUTH_NAME" {
		t.Fatalf("remote configuration lost: %T", r)
	}
}

func TestCancelledResolveDoesNotPrepareModel(t *testing.T) {
	p := New(Config{})
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := p.Resolve(ctx, compare.Model{ID: "jev", Ranker: "jev"}); err == nil || r != nil {
		t.Fatalf("cancelled resolution returned %T, %v", r, err)
	}
}

func TestClosedProviderRejectsWork(t *testing.T) {
	p := New(Config{})
	_ = p.Close()
	if r, err := p.Resolve(context.Background(), compare.Model{ID: "jev", Ranker: "jev"}); err == nil || r != nil {
		t.Fatalf("closed provider returned %T, %v", r, err)
	}
}
