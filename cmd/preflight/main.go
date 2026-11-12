package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"tessaro/internal/researchdata"
	"os"
)

// 2026-11-12: 오프라인 사전 검증은 모델이나 참조 정답을 검색 경로에 전달하지 않는다.
func main() {
	generate := flag.Bool("generate", false, "generate separate research corpus")
	id := flag.String("id", "", "preflight one problem")
	frontierOnly := flag.Bool("frontier-only", false, "replay reference frontiers only")
	split := flag.String("split", "all", "dev, eval, or all")
	output := flag.String("out", "", "write JSON report to file; stdout if empty")
	flag.Parse()
	fail := func(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	if *generate {
		if err := researchdata.Generate("examples/token", "examples/research"); err != nil {
			fail(err)
		}
		return
	}
	if *split != "all" && *split != "dev" && *split != "eval" {
		fail(fmt.Errorf("invalid split"))
	}
	corpus, err := researchdata.LoadCatalog("examples/research")
	if err != nil {
		fail(err)
	}
	var results []researchdata.Check
	for _, r := range corpus.Records {
		if (*id != "" && r.ID != *id) || (*split != "all" && r.Split != *split) {
			continue
		}
		fmt.Fprintln(os.Stderr, "preflight", r.ID)
		var c researchdata.Check
		var e error
		if *frontierOnly {
			c, e = researchdata.FrontierOnly("examples/research", r)
		} else {
			c, e = researchdata.Preflight(context.Background(), "examples/research", r)
		}
		if e != nil {
			fail(fmt.Errorf("%s: %w", r.ID, e))
		}
		results = append(results, c)
	}
	if len(results) == 0 {
		fail(fmt.Errorf("no matching problems"))
	}
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fail(err)
	}
	b = append(b, '\n')
	if *output == "" {
		_, err = os.Stdout.Write(b)
	} else {
		err = os.WriteFile(*output, b, 0600)
	}
	if err != nil {
		fail(err)
	}
}
