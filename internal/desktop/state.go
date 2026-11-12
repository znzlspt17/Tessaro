package desktop

import "strings"

type Frame struct {
	RunID                             string
	Seq                               uint64
	Group                             string
	Text                              string
	Markers                           map[string]Marker
	Step                              int
	ProblemID, DecisionStatus, Choice string
}
type Candidate struct{ Text, Reason string }
type Replay struct {
	frames   []Frame
	paused   bool
	selected int
}

func (r *Replay) Add(f Frame) bool {
	if len(r.frames) > 0 {
		last := r.frames[len(r.frames)-1]
		if f.RunID != last.RunID || f.Seq <= last.Seq {
			return false
		}
	}
	r.frames = append(r.frames, f)
	if !r.paused {
		r.selected = len(r.frames) - 1
	}
	return true
}
func (r *Replay) Merge(f Frame) bool {
	if len(r.frames) > 0 {
		last := len(r.frames) - 1
		if r.frames[last].RunID == f.RunID && r.frames[last].Seq == f.Seq {
			r.frames[last] = f
			return true
		}
	}
	return r.Add(f)
}
func (r *Replay) Pause() { r.paused = true }
func (r *Replay) Resume() {
	r.paused = false
	if len(r.frames) > 0 {
		r.selected = len(r.frames) - 1
	}
}
func (r *Replay) View() Frame {
	if len(r.frames) == 0 {
		return Frame{}
	}
	return r.frames[r.selected]
}
func (r *Replay) Select(seq uint64) bool {
	for i, f := range r.frames {
		if f.Seq == seq {
			r.selected = i
			r.paused = true
			return true
		}
	}
	return false
}
func FilterCandidates(c []Candidate, query string) []Candidate {
	var out []Candidate
	q := strings.ToLower(query)
	for _, x := range c {
		if strings.Contains(strings.ToLower(x.Text+" "+x.Reason), q) {
			out = append(out, x)
		}
	}
	return out
}
