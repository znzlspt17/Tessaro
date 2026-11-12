package desktop

type HistoryGuard struct{ generation uint64 }
type HistoryRequest struct {
	generation uint64
	id         string
	manager    any
}

func (g *HistoryGuard) Begin(id string, manager any) HistoryRequest {
	g.generation++
	return HistoryRequest{g.generation, id, manager}
}
func (g *HistoryGuard) Accept(r HistoryRequest, id string, manager any) bool {
	return r.generation == g.generation && r.id == id && r.manager == manager
}
