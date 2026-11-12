package grammar

import (
	"reflect"
	"sync"
	"testing"
)

const remainingFixture = `{"name":"x","rules":{"start":{"type":"SEQ","members":[{"type":"SYMBOL","name":"expr"},{"type":"STRING","value":";"}]},"expr":{"type":"CHOICE","members":[{"type":"SEQ","members":[{"type":"SYMBOL","name":"expr"},{"type":"STRING","value":"+"},{"type":"STRING","value":"x"}]},{"type":"BLANK"}]}}}`

func TestMinRemainingContinuations(t *testing.T) {
	g, err := Load([]byte(remainingFixture))
	if err != nil {
		t.Fatal(err)
	}
	s := g.NewState("start")
	check := func(want int, ok bool) {
		t.Helper()
		n, got := s.MinRemaining()
		if n != want || got != ok {
			t.Fatalf("got (%d,%v), want (%d,%v)", n, got, want, ok)
		}
	}
	check(1, true)
	s, err = s.Advance("+")
	if err != nil {
		t.Fatal(err)
	}
	check(2, true)
	s, err = s.Advance("x")
	if err != nil {
		t.Fatal(err)
	}
	check(1, true)
	s, err = s.Advance(";")
	if err != nil {
		t.Fatal(err)
	}
	check(0, true)
	dead, err := Load([]byte(`{"name":"dead","rules":{"start":{"type":"SYMBOL","name":"start"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s = dead.NewState("start")
	check(0, false)
	s = g.NewState("missing")
	check(0, false)
}

func TestPersistentChartBranches(t *testing.T) {
	g, err := Load([]byte(remainingFixture))
	if err != nil {
		t.Fatal(err)
	}
	parent := g.NewState("start")
	before := parent.Next()
	left, err := parent.Advance("+")
	if err != nil {
		t.Fatal(err)
	}
	right, err := parent.Clone().Advance(";")
	if err != nil {
		t.Fatal(err)
	}
	if !right.Accepts() || parent.Accepts() || !reflect.DeepEqual(before, parent.Next()) {
		t.Fatal("branch changed parent")
	}
	child, err := left.Advance("x")
	if err != nil {
		t.Fatal(err)
	}
	if child.Accepts() || left.Accepts() || !right.Accepts() {
		t.Fatal("branch state changed")
	}
	end, err := child.Advance(";")
	if err != nil || !end.Accepts() {
		t.Fatalf("end: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				b, e := parent.Advance(";")
				if e != nil || !b.Accepts() {
					t.Errorf("concurrent branch: %v", e)
				}
				parent.MinRemaining()
			}
		}()
	}
	wg.Wait()
}

func TestMinRemainingChoosesViableAncestor(t *testing.T) {
	g, err := Load([]byte(`{"name":"x","rules":{"start":{"type":"CHOICE","members":[{"type":"SEQ","members":[{"type":"SYMBOL","name":"a"},{"type":"STRING","value":"c"}]},{"type":"SEQ","members":[{"type":"SYMBOL","name":"a"},{"type":"STRING","value":"d"},{"type":"STRING","value":"e"}]}]},"a":{"type":"CHOICE","members":[{"type":"STRING","value":"a"},{"type":"SEQ","members":[{"type":"STRING","value":"a"},{"type":"STRING","value":"b"}]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := g.NewState("start").Advance("a")
	if err != nil {
		t.Fatal(err)
	}
	n, ok := s.MinRemaining()
	if !ok || n != 1 {
		t.Fatalf("got %d, %v", n, ok)
	}
	s, err = s.Advance("b")
	if err != nil {
		t.Fatal(err)
	}
	n, ok = s.MinRemaining()
	if !ok || n != 1 {
		t.Fatalf("got %d, %v", n, ok)
	}
}

func TestCloneCurrentChartIsIndependent(t *testing.T) {
	g, err := Load([]byte(remainingFixture))
	if err != nil {
		t.Fatal(err)
	}
	s, err := g.NewState("start").Advance("+")
	if err != nil {
		t.Fatal(err)
	}
	clone := s.Clone()
	fake := item{lhs: "fake"}
	clone.charts[len(clone.charts)-1].add(fake)
	if s.charts[len(s.charts)-1].seen[fake] {
		t.Fatal("clone mutated original chart")
	}
}

func BenchmarkAdvanceCharts(b *testing.B) {
	g, err := Load([]byte(fixture))
	if err != nil {
		b.Fatal(err)
	}
	s, err := g.NewState("start").Advance("x")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		s, err = s.Advance("+")
		if err != nil {
			b.Fatal(err)
		}
		s, err = s.Advance("x")
		if err != nil {
			b.Fatal(err)
		}
	}
	b.Run("shared", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, err := s.Advance("+")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("deep-copy-baseline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			next := &State{grammar: s.grammar, start: s.start, charts: make([]chart, len(s.charts))}
			for j, c := range s.charts {
				next.charts[j].items = append([]item(nil), c.items...)
				next.charts[j].seen = make(map[item]bool, len(c.seen))
				for k, v := range c.seen {
					next.charts[j].seen[k] = v
				}
			}
			c := chart{}
			for _, it := range s.charts[len(s.charts)-1].items {
				p := s.production(it)
				if it.dot < len(p.parts) && p.parts[it.dot].terminal != nil && matches(*p.parts[it.dot].terminal, "+") {
					it.dot++
					c.add(it)
				}
			}
			next.charts = append(next.charts, c)
			next.close()
		}
	})
}
