package main

import (
	"context"
	"flag"
	"fmt"
	"jevcancode/internal/app"
	"jevcancode/internal/benchmark"
	"os"
	"time"
)

func run() error {
	path := flag.String("manifest", "", "benchmark JSON path")
	timeout := flag.Duration("timeout", 300*time.Second, "total timeout")
	rf := app.Flags()
	flag.Parse()
	if *path == "" || *timeout <= 0 || *rf.RequestTimeout <= 0 {
		return fmt.Errorf("manifest and positive timeout required")
	}
	r, err := rf.New()
	if err != nil {
		return err
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	m, err := benchmark.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	return app.WriteResult(os.Stdout, "output", time.Now(), benchmark.Run(ctx, m, r))
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
