package main

import (
	"context"
	"flag"
	"fmt"
	"jevcancode/internal/compare"
	"jevcancode/internal/rank"
	"jevcancode/internal/webapp"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func run() error {
	address := flag.String("listen", "127.0.0.1:8080", "loopback address")
	manifest := flag.String("manifest", "examples/token/compare-live.json", "token comparison manifest")
	output := flag.String("output", "output", "result directory")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil {
		return fmt.Errorf("invalid listen address")
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("only loopback listening is allowed")
	}
	f, err := os.Open(*manifest)
	if err != nil {
		return err
	}
	m, err := compare.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	if m.Engine != "token" {
		return fmt.Errorf("token manifest required")
	}
	models := []webapp.Model{{ID: "fake", Label: "Fake (test)", NewRanker: func() rank.Ranker { return rank.Fake{} }}}
	for _, model := range m.Models {
		if model.ID == "fake" {
			continue
		}
		copy := model
		models = append(models, webapp.Model{ID: copy.ID, Label: copy.ID, NewRanker: copy.NewRanker})
	}
	problems := make([]webapp.Problem, 0, len(m.Problems))
	base := filepath.Dir(*manifest)
	for _, p := range m.Problems {
		heldout := ""
		if p.Heldout != "" {
			heldout = filepath.Join(base, p.Heldout)
		}
		problems = append(problems, webapp.Problem{ID: p.ID, Label: p.ID, SpecPath: filepath.Join(base, p.Spec), HeldoutPath: heldout})
	}
	handler, err := webapp.New(webapp.Config{OutputDir: *output, Problems: problems, Models: models})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = handler.Close(shutdown)
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("Web app: http://%s/\n", listener.Addr())
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		<-shutdownDone
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
