package app

import (
	"flag"
	"fmt"
	"jevcancoding/internal/rank"
	"time"
)

type RankFlags struct {
	Kind, URL, Model, KeyEnv, Revision, Runtime *string
	RequestTimeout                              *time.Duration
}

func Flags() RankFlags {
	return RankFlags{Kind: flag.String("ranker", "fake", "fake, jev, or local"), URL: flag.String("url", "", "System One endpoint URL"), Model: flag.String("model", "jev-latest", "model name"), KeyEnv: flag.String("key-env", "", "environment variable containing API key"), Revision: flag.String("revision", "", "declared model revision"), Runtime: flag.String("runtime", "", "declared model runtime"), RequestTimeout: flag.Duration("request-timeout", 60*time.Second, "per request timeout")}
}
func (f RankFlags) New() (rank.Ranker, error) {
	switch *f.Kind {
	case "fake":
		return rank.Fake{}, nil
	case "jev", "local":
		url := *f.URL
		if url == "" {
			if *f.Kind == "jev" {
				url = "https://api.typesafe.ai/v1/systemone"
			} else {
				return nil, fmt.Errorf("local ranker requires -url")
			}
		}
		if *f.Model == "" {
			return nil, fmt.Errorf("model required")
		}
		return &rank.HTTP{URL: url, Model: *f.Model, APIKeyEnv: *f.KeyEnv, Timeout: *f.RequestTimeout, Revision: *f.Revision, Runtime: *f.Runtime}, nil
	default:
		return nil, fmt.Errorf("unknown ranker")
	}
}
