package logqa

import (
	"sort"
	"time"
)

// AggregateSessions groups request records by session_id and picks the max-input snapshot.
func AggregateSessions(requests []RequestRecord, rules RulesConfig) []SessionRecord {
	type group struct {
		requests []RequestRecord
	}
	groups := make(map[string]*group)
	order := make([]string, 0)
	for _, req := range requests {
		sid := req.SessionID
		if sid == "" {
			sid = "unknown:" + req.SourceFile
		}
		g, ok := groups[sid]
		if !ok {
			g = &group{}
			groups[sid] = g
			order = append(order, sid)
		}
		g.requests = append(g.requests, req)
	}

	sessions := make([]SessionRecord, 0, len(order))
	for _, sid := range order {
		g := groups[sid]
		snapshot := pickSnapshot(g.requests)
		ok, reasons := EvaluateSession(snapshot.PromptRounds, snapshot.ToolCalls, snapshot.DupAssistant, rules)

		threadSet := map[string]struct{}{}
		keySet := map[string]struct{}{}
		files := make([]string, 0, len(g.requests))
		var firstTS, lastTS time.Time
		for _, r := range g.requests {
			if r.ThreadID != "" {
				threadSet[r.ThreadID] = struct{}{}
			}
			if r.KeyName != "" {
				keySet[r.KeyName] = struct{}{}
			}
			files = append(files, r.SourceFile)
			if firstTS.IsZero() || r.Timestamp.Before(firstTS) {
				firstTS = r.Timestamp
			}
			if lastTS.IsZero() || r.Timestamp.After(lastTS) {
				lastTS = r.Timestamp
			}
			if !r.ModTime.IsZero() {
				if lastTS.IsZero() || r.ModTime.After(lastTS) {
					// keep timestamp preference but track modtime already in request
				}
			}
		}
		threads := sortedKeys(threadSet)
		keys := sortedKeys(keySet)
		sort.Strings(files)

		sessions = append(sessions, SessionRecord{
			SessionID:          sid,
			ThreadIDs:          threads,
			KeyNames:           keys,
			PromptRounds:       snapshot.PromptRounds,
			ToolCalls:          snapshot.ToolCalls,
			DupAssistantGroups: snapshot.DupAssistant,
			OK:                 ok,
			FailReasons:        reasons,
			FirstTS:            firstTS,
			LastTS:             lastTS,
			SourceFiles:        files,
			SamplePrompts:      snapshot.SamplePrompts,
			InputLen:           snapshot.InputLen,
			RequestCount:       len(g.requests),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].OK != sessions[j].OK {
			return !sessions[i].OK && sessions[j].OK // fails first
		}
		return sessions[i].SessionID < sessions[j].SessionID
	})
	return sessions
}

func pickSnapshot(requests []RequestRecord) RequestRecord {
	if len(requests) == 0 {
		return RequestRecord{}
	}
	best := requests[0]
	for _, r := range requests[1:] {
		if r.InputLen > best.InputLen {
			best = r
			continue
		}
		if r.InputLen == best.InputLen && r.Timestamp.After(best.Timestamp) {
			best = r
		}
	}
	return best
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func summarizeSessions(sessions []SessionRecord) (total, pass, fail int, hist map[string]int, rate float64) {
	hist = map[string]int{
		"prompt_rounds":       0,
		"no_tool_call":        0,
		"duplicate_assistant": 0,
	}
	total = len(sessions)
	for _, s := range sessions {
		if s.OK {
			pass++
			continue
		}
		fail++
		for _, reason := range s.FailReasons {
			switch {
			case hasPrefix(reason, "prompt_rounds"):
				hist["prompt_rounds"]++
			case reason == "no_tool_call":
				hist["no_tool_call"]++
			case hasPrefix(reason, "duplicate_assistant"):
				hist["duplicate_assistant"]++
			}
		}
	}
	if total > 0 {
		rate = float64(pass) / float64(total)
	}
	return total, pass, fail, hist, rate
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
