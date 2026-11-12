package desktop

import (
	"encoding/json"
	"jevcancode/internal/application"
)

func MergeSnapshot(frames []Frame, s application.Snapshot) []Frame {
	if s.RunID == "" || s.Seq == 0 {
		return frames
	}
	if s.JobKind == "hypothesis" {
		frames = nil
	}
	if len(frames) > 0 {
		last := frames[len(frames)-1]
		if last.RunID != s.RunID || s.Seq < last.Seq {
			return frames
		}
	}
	// 비교 실행의 실시간 Snapshot에는 개별 탐색 trace가 투영되지 않으므로 직전 판단 정보를 보존한다.
	if s.JobKind == "compare" && len(frames) > 0 {
		var prior application.Snapshot
		if json.Unmarshal([]byte(frames[len(frames)-1].Text), &prior) == nil {
			s.Model = prior.Model
			s.Current = prior.Current
			s.Step = prior.Step
			s.Frontier = prior.Frontier
			s.Candidates = prior.Candidates
			s.DecisionContext = prior.DecisionContext
			s.ChosenID = prior.ChosenID
			s.DecisionStatus = prior.DecisionStatus
			s.ForcedID = prior.ForcedID
			s.ForcedLexeme = prior.ForcedLexeme
		}
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return frames
	}
	result := append([]Frame(nil), frames...)
	f := Frame{RunID: s.RunID, Seq: s.Seq, Text: string(b), Step: s.Step, DecisionStatus: s.DecisionStatus}
	if s.Current != nil {
		f.ProblemID = s.Current.ID
	}
	if s.DecisionStatus == "forced" {
		f.Choice = s.ForcedLexeme
	} else if s.ChosenID != "" {
		for _, c := range s.Candidates {
			if c.ID == s.ChosenID {
				f.Choice = c.Lexeme
				break
			}
		}
	}
	if len(result) > 0 && result[len(result)-1].Seq == s.Seq {
		f.Markers = result[len(result)-1].Markers
		f.Group = result[len(result)-1].Group
		result[len(result)-1] = f
	} else {
		if len(result) > 0 {
			f.Group = result[len(result)-1].Group
		}
		result = append(result, f)
	}
	return result
}

func mergeReplayFinal(frame Frame, final *application.Snapshot, seq uint64) Frame {
	if final == nil || final.Seq != seq {
		return frame
	}
	var frames []Frame
	if frame.Seq != 0 {
		frames = []Frame{frame}
	}
	merged := MergeSnapshot(frames, *final)
	if len(merged) == 0 {
		return frame
	}
	return merged[len(merged)-1]
}
