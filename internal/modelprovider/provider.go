package modelprovider

import (
	"context"
	"errors"
	"tessaro/internal/compare"
	"tessaro/internal/rank"
	"net/url"
	"strings"
	"sync"
)

type Config struct{ Python, Script, ModelDir string }
type Provider struct {
	config Config
	mu     sync.Mutex
	worker *rank.Worker
	closed bool
}

func New(config Config) *Provider { return &Provider{config: config} }
func (p *Provider) Resolve(ctx context.Context, m compare.Model) (rank.Ranker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("model_provider_closed")
	}
	switch m.Ranker {
	case "jev":
		p.mu.Unlock()
		return m.NewRanker(), nil
	case "local":
		if !supportedLocal(m) {
			p.mu.Unlock()
			return nil, errors.New("unsupported_local_model")
		}
		if p.config.Python == "" || p.config.Script == "" || p.config.ModelDir == "" {
			p.mu.Unlock()
			return nil, errors.New("local_runtime_not_configured")
		}
		// 2026-11-07: 모델 공급을 한 곳으로 모아 비교 trial마다 GPU 모델을 다시 로드하지 않는다.
		if p.worker == nil {
			p.worker = rank.NewWorker(rank.WorkerConfig{Python: p.config.Python, Script: p.config.Script, ModelDir: p.config.ModelDir, Model: "Mapika/decider-4b"})
		}
		worker := p.worker
		p.mu.Unlock()
		if err := worker.Prepare(ctx); err != nil {
			return nil, err
		}
		return &declaredWorker{Worker: worker, revision: m.Revision, runtime: m.Runtime}, nil
	default:
		p.mu.Unlock()
		return nil, errors.New("select_real_model")
	}
}

func supportedLocal(m compare.Model) bool {
	if m.Model != "" && m.Model != "Mapika/decider-4b" {
		return false
	}
	if m.URL == "" {
		return true
	}
	u, err := url.Parse(m.URL)
	return err == nil && u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || u.Hostname() == "127.0.0.1") && u.Port() == "8000" && u.Path == "/v1/systemone" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

type declaredWorker struct {
	*rank.Worker
	revision, runtime string
}

func (w *declaredWorker) Metadata() rank.Metadata {
	m := w.Worker.Metadata()
	m.Revision, m.Runtime = w.revision, w.runtime
	return m
}

func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	w := p.worker
	p.mu.Unlock()
	if w != nil {
		return w.Close()
	}
	return nil
}
