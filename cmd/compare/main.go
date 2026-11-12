package main

import (
	"context"
	"flag"
	"fmt"
	"jevcancode/internal/app"
	"jevcancode/internal/compare"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func run() error {
	path := flag.String("manifest", "", "comparison JSON path")
	engine := flag.String("engine", "", "override manifest engine (token or ast)")
	trace := flag.Bool("trace", false, "save trace")
	traceCode := flag.Bool("trace-code", false, "include generated code in trace")
	flag.Parse()
	if *path == "" {
		return fmt.Errorf("-manifest required")
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	m, err := compare.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	if *engine != "" {
		if *engine != "token" && *engine != "ast" {
			return fmt.Errorf("invalid engine")
		}
		m.Engine = *engine
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var events []any
	var emit func(any)
	if *trace || *traceCode {
		emit = func(e any) { events = append(events, e) }
	}
	report := compare.Run(ctx, filepath.Dir(*path), m, emit, *traceCode)
	outPath, err := app.WriteResultWithPath(os.Stdout, "output", time.Now(), report)
	if err != nil {
		return err
	}
	if emit != nil {
		if err = app.WriteTrace(outPath, events); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
