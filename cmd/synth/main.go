package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"jevcancoding/internal/app"
	"jevcancoding/internal/runner"
	"jevcancoding/internal/spec"
	"jevcancoding/internal/synth"
	"jevcancoding/internal/tokenspec"
	"jevcancoding/internal/tokensynth"
	"math"
	"os"
	"time"
)

type reportedError struct{ error }

func run() error {
	path := flag.String("spec", "", "problem JSON path")
	heldout := flag.String("heldout", "", "heldout tests JSON path")
	engine := flag.String("engine", "token", "token or ast")
	strategy := flag.String("strategy", "", "greedy, strict-greedy or beam")
	maxTokens := flag.Int("max-tokens", 0, "token budget")
	maxCandidates := flag.Int("max-candidates", 0, "candidate budget")
	maxCalls := flag.Int("max-calls", 0, "ranking call budget")
	maxDepth := flag.Int("max-depth", 0, "AST expression depth")
	maxLocals := flag.Int("max-locals", 0, "maximum local declarations")
	maxStatements := flag.Int("max-statements", 0, "maximum statements")
	maxNesting := flag.Int("max-nesting", 0, "token nesting limit")
	timeout := flag.Duration("timeout", 0, "total timeout")
	trace := flag.Bool("trace", false, "save trace")
	traceCode := flag.Bool("trace-code", false, "include generated code in trace")
	inPrice := flag.Float64("input-price-per-million", -1, "input token price")
	outPrice := flag.Float64("output-price-per-million", -1, "output token price")
	rf := app.Flags()
	flag.Parse()
	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if *path == "" || (*engine != "token" && *engine != "ast") || *rf.RequestTimeout <= 0 {
		return fmt.Errorf("invalid arguments")
	}
	if (*engine == "token" && visited["max-depth"]) || (*engine == "ast" && (visited["max-tokens"] || visited["max-nesting"])) {
		return fmt.Errorf("budget flag does not apply to engine")
	}
	if (visited["input-price-per-million"] && (*inPrice < 0 || math.IsNaN(*inPrice) || math.IsInf(*inPrice, 0))) || (visited["output-price-per-million"] && (*outPrice < 0 || math.IsNaN(*outPrice) || math.IsInf(*outPrice, 0))) {
		return fmt.Errorf("invalid price")
	}
	if *strategy == "" {
		if *engine == "token" {
			*strategy = "beam"
		} else {
			*strategy = "greedy"
		}
	}
	if *strategy != "greedy" && *strategy != "beam" && *strategy != "strict-greedy" {
		return fmt.Errorf("unknown strategy")
	}
	r, err := rf.New()
	if err != nil {
		return err
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	var p spec.Problem
	var tp tokenspec.Problem
	if *engine == "token" {
		tp, err = tokenspec.Decode(f)
	} else {
		p, err = spec.Decode(f)
	}
	f.Close()
	if err != nil {
		return err
	}
	flags := limits{MaxTokens: *maxTokens, MaxCandidates: *maxCandidates, MaxCalls: *maxCalls, MaxDepth: *maxDepth, MaxLocals: *maxLocals, MaxStatements: *maxStatements, MaxNesting: *maxNesting, Timeout: *timeout}
	l := defaultLimits(*engine)
	if *engine == "token" {
		l = tokenLimits(l, tp.Budgets, flags, visited)
	} else {
		if p.Budgets != nil {
			b := p.Budgets
			if b.MaxCandidates != nil {
				l.MaxCandidates = *b.MaxCandidates
			}
			if b.MaxCalls != nil {
				l.MaxCalls = *b.MaxCalls
			}
			if b.MaxDepth != nil {
				l.MaxDepth = *b.MaxDepth
			}
			if b.MaxLocals != nil {
				l.MaxLocals = *b.MaxLocals
			}
			if b.MaxStatements != nil {
				l.MaxStatements = *b.MaxStatements
			}
			if b.TimeoutSeconds != nil {
				l.Timeout = time.Duration(*b.TimeoutSeconds) * time.Second
			}
		}
		if visited["max-candidates"] {
			l.MaxCandidates = flags.MaxCandidates
		}
		if visited["max-calls"] {
			l.MaxCalls = flags.MaxCalls
		}
		if visited["max-depth"] {
			l.MaxDepth = flags.MaxDepth
		}
		if visited["max-locals"] {
			l.MaxLocals = flags.MaxLocals
		}
		if visited["max-statements"] {
			l.MaxStatements = flags.MaxStatements
		}
		if visited["timeout"] {
			l.Timeout = flags.Timeout
		}
	}
	if l.MaxCandidates < 1 || l.MaxCalls < 1 || l.MaxLocals < 0 || l.MaxLocals > 63 || l.MaxStatements < 1 || l.Timeout <= 0 {
		return fmt.Errorf("invalid budgets")
	}
	if *engine == "token" && (l.MaxTokens < 1 || l.MaxStatements > 128 || l.MaxNesting < 1 || l.MaxNesting > 8) {
		return fmt.Errorf("invalid token budgets")
	}
	if *engine == "ast" && (l.MaxDepth < 1 || l.MaxStatements > 63) {
		return fmt.Errorf("invalid AST budgets")
	}
	q := runner.Request{Strategy: *strategy, MaxTokens: l.MaxTokens, MaxCandidates: l.MaxCandidates, MaxCalls: l.MaxCalls, MaxDepth: l.MaxDepth, MaxLocals: l.MaxLocals, MaxStatements: l.MaxStatements, MaxNesting: l.MaxNesting, LocalLimitSet: true, Timeout: l.Timeout, TraceCode: *traceCode}
	if *inPrice >= 0 {
		q.InputPrice = inPrice
	}
	if *outPrice >= 0 {
		q.OutputPrice = outPrice
	}
	if *heldout != "" {
		if *engine == "token" {
			q.TokenHeldout = func() ([]tokenspec.Test, error) {
				f, e := os.Open(*heldout)
				if e != nil {
					return nil, e
				}
				defer f.Close()
				return tokenspec.DecodeTests(f, tp)
			}
		} else {
			q.Heldout = func() ([]spec.Test, error) {
				f, e := os.Open(*heldout)
				if e != nil {
					return nil, e
				}
				defer f.Close()
				return spec.DecodeTests(f, p)
			}
		}
	}
	var events []any
	if *trace || *traceCode {
		q.Trace = func(e synth.TraceEvent) { events = append(events, e) }
		q.TokenTrace = func(e tokensynth.Event) { events = append(events, e) }
	}
	var result runner.Result
	if *engine == "token" {
		result = runner.RunToken(context.Background(), tp, r, q)
	} else {
		result = runner.Run(context.Background(), p, r, q)
	}
	outPath, err := app.WriteResultWithPath(os.Stdout, "output", time.Now(), result)
	if err != nil {
		return reportedError{fmt.Errorf("save result: %w", err)}
	}
	if *trace || *traceCode {
		if err = app.WriteTrace(outPath, events); err != nil {
			return reportedError{err}
		}
	}
	if result.Status != "success" && result.Status != "heldout_failed" {
		return reportedError{fmt.Errorf("%s", result.Status)}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		var reported reportedError
		if !errors.As(err, &reported) {
			if writeErr := app.WriteResult(os.Stdout, "output", time.Now(), map[string]any{"status": "input_or_execution_error"}); writeErr != nil {
				err = errors.Join(err, fmt.Errorf("save result: %w", writeErr))
			}
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
