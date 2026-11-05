package compare

import (
	"encoding/json"
	"fmt"
	"io"
	"jevcancoding/internal/jsoninput"
	"jevcancoding/internal/rank"
	"math"
	"math/rand"
	"net/url"
	"time"
)

type Model struct {
	ID          string   `json:"id"`
	Ranker      string   `json:"ranker"`
	URL         string   `json:"url,omitempty"`
	Model       string   `json:"model,omitempty"`
	KeyEnv      string   `json:"key_env,omitempty"`
	Revision    string   `json:"revision,omitempty"`
	Runtime     string   `json:"runtime,omitempty"`
	InputPrice  *float64 `json:"input_price_per_million,omitempty"`
	OutputPrice *float64 `json:"output_price_per_million,omitempty"`
}
type Problem struct {
	ID      string `json:"id"`
	Spec    string `json:"spec"`
	Heldout string `json:"heldout,omitempty"`
}
type Manifest struct {
	Engine     string    `json:"engine"`
	Models     []Model   `json:"models"`
	Problems   []Problem `json:"problems"`
	Strategies []string  `json:"strategies"`
	Repeats    int       `json:"repeats"`
	Seed       int64     `json:"seed"`
}
type Trial struct {
	Engine   string `json:"engine"`
	Model    string `json:"model"`
	Problem  string `json:"problem"`
	Strategy string `json:"strategy"`
	Repeat   int    `json:"repeat"`
}

func Decode(r io.Reader) (Manifest, error) {
	m := Manifest{Engine: "token", Repeats: 1, Seed: 42}
	d := json.NewDecoder(jsoninput.Reader(r))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return m, fmt.Errorf("trailing JSON")
	}
	if (m.Engine != "token" && m.Engine != "ast") || len(m.Models) == 0 || len(m.Problems) == 0 || len(m.Strategies) == 0 || m.Repeats < 1 || m.Repeats > 10000 {
		return m, fmt.Errorf("invalid manifest")
	}
	ids := map[string]bool{}
	for _, x := range m.Models {
		if x.ID == "" || ids[x.ID] || (x.Ranker != "fake" && x.Ranker != "jev" && x.Ranker != "local") {
			return m, fmt.Errorf("invalid model")
		}
		ids[x.ID] = true
		for _, v := range []*float64{x.InputPrice, x.OutputPrice} {
			if v != nil && (*v < 0 || math.IsInf(*v, 0) || math.IsNaN(*v)) {
				return m, fmt.Errorf("invalid price")
			}
		}
		if x.Ranker == "local" {
			if x.Model == "" {
				return m, fmt.Errorf("local model required")
			}
			u, e := url.Parse(x.URL)
			if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return m, fmt.Errorf("invalid local endpoint")
			}
		}
	}
	ids = map[string]bool{}
	for _, x := range m.Problems {
		if x.ID == "" || x.Spec == "" || ids[x.ID] {
			return m, fmt.Errorf("invalid problem")
		}
		ids[x.ID] = true
	}
	ids = map[string]bool{}
	for _, x := range m.Strategies {
		if ids[x] || (x != "greedy" && x != "beam" && x != "strict-greedy") {
			return m, fmt.Errorf("invalid strategy")
		}
		ids[x] = true
	}
	return m, nil
}

// 2026-11-05: 같은 seed로 문제 순서를 공유해 모델 간 비교 조건을 고정한다.
func Plan(m Manifest) []Trial {
	if m.Engine == "" {
		m.Engine = "token"
	}
	problems := append([]Problem(nil), m.Problems...)
	rand.New(rand.NewSource(m.Seed)).Shuffle(len(problems), func(i, j int) { problems[i], problems[j] = problems[j], problems[i] })
	out := make([]Trial, 0, len(m.Models)*len(problems)*len(m.Strategies)*m.Repeats)
	for _, model := range m.Models {
		for _, p := range problems {
			for _, strategy := range m.Strategies {
				for i := 1; i <= m.Repeats; i++ {
					out = append(out, Trial{Engine: m.Engine, Model: model.ID, Problem: p.ID, Strategy: strategy, Repeat: i})
				}
			}
		}
	}
	return out
}
func (m Model) NewRanker() rank.Ranker {
	if m.Ranker == "fake" {
		return rank.Fake{}
	}
	endpoint := m.URL
	if endpoint == "" {
		endpoint = "https://api.typesafe.ai/v1/systemone"
	}
	model := m.Model
	if model == "" {
		model = "jev-latest"
		if m.Ranker == "local" {
			model = "Mapika/decider-4b"
		}
	}
	return &rank.HTTP{URL: endpoint, Model: model, APIKeyEnv: m.KeyEnv, Revision: m.Revision, Runtime: m.Runtime, Timeout: 60 * time.Second}
}
