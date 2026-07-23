package logqa

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	titleSourceCodex = "codex_title"
	titleSourceUser  = "user_prompt"
	maxTitleRunes    = 120
)

var outputTextDoneLine = regexp.MustCompile(`(?i)"type"\s*:\s*"response\.output_text\.done"`)

// resolveRequestTitle extracts a best-effort session title for one log file.
// Prefer the short assistant output of Codex title-generation turns; otherwise
// leave empty (session aggregation may fall back to the first user prompt).
func resolveRequestTitle(requestKind string, input []any, logText string) (title, source string) {
	if !isTitleGenerationTurn(requestKind, input) {
		return "", ""
	}
	if t := extractTitleFromSSE(logText); t != "" {
		return t, titleSourceCodex
	}
	return "", ""
}

func isTitleGenerationTurn(requestKind string, input []any) bool {
	rk := strings.ToLower(strings.TrimSpace(requestKind))
	if rk != "" && strings.Contains(rk, "title") && !strings.Contains(rk, "subtitle") {
		return true
	}
	// Fallback: user message matches title-generation prompts.
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if stringField(item, "role") != "user" {
			continue
		}
		text := contentText(item)
		if text == "" {
			continue
		}
		for _, pat := range titleSummaryPatterns {
			if pat.MatchString(text) && (strings.Contains(strings.ToLower(text), "title") ||
				strings.Contains(strings.ToLower(text), "标题")) {
				return true
			}
		}
	}
	return false
}

// extractTitleFromSSE pulls a short title string from Responses SSE events.
func extractTitleFromSSE(logText string) string {
	sections := []string{
		extractSection(logText, "API RESPONSE 1"),
		extractSection(logText, "API RESPONSE"),
		extractSection(logText, "RESPONSE"),
	}
	var candidates []string
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		for _, line := range strings.Split(section, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			payload := line
			if strings.HasPrefix(strings.ToLower(line), "data:") {
				payload = strings.TrimSpace(line[5:])
			}
			if payload == "" || payload == "[DONE]" {
				continue
			}
			if !outputTextDoneLine.MatchString(payload) && !strings.Contains(payload, `"response.completed"`) {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(payload), &obj); err != nil {
				continue
			}
			switch stringField(obj, "type") {
			case "response.output_text.done":
				if t := strings.TrimSpace(stringField(obj, "text")); t != "" {
					candidates = append(candidates, t)
				}
			case "response.completed":
				if t := titleFromCompletedResponse(obj); t != "" {
					candidates = append(candidates, t)
				}
			}
		}
		if len(candidates) > 0 {
			break
		}
	}
	return pickBestTitleCandidate(candidates)
}

func titleFromCompletedResponse(obj map[string]any) string {
	resp, _ := obj["response"].(map[string]any)
	if resp == nil {
		return ""
	}
	output, _ := resp["output"].([]any)
	var parts []string
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if stringField(item, "type") != "message" {
			continue
		}
		content, _ := item["content"].([]any)
		for _, c := range content {
			part, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if t := strings.TrimSpace(stringField(part, "text")); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func pickBestTitleCandidate(candidates []string) string {
	best := ""
	bestLen := 0
	for _, c := range candidates {
		c = normalizeTitle(c)
		if c == "" || !looksLikeTitle(c) {
			continue
		}
		n := utf8.RuneCountInString(c)
		// Prefer the shortest plausible title (Codex titles are short phrases).
		if best == "" || n < bestLen {
			best = c
			bestLen = n
		}
	}
	return best
}

func normalizeTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	// Strip leading markdown heading markers: "# Pulse Relay" -> "Pulse Relay"
	s = strings.TrimSpace(strings.TrimLeft(s, "#"))
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxTitleRunes {
		runes := []rune(s)
		s = string(runes[:maxTitleRunes]) + "…"
	}
	return s
}

func looksLikeTitle(s string) bool {
	n := utf8.RuneCountInString(s)
	if n < 1 || n > maxTitleRunes {
		return false
	}
	// Reject long handoff / compaction prose.
	lower := strings.ToLower(s)
	if strings.Contains(lower, "context checkpoint") ||
		strings.Contains(lower, "handoff summary") ||
		strings.Contains(lower, "you are performing") {
		return false
	}
	// Titles are usually one short phrase, not multi-sentence essays.
	if strings.Count(s, "。") >= 2 || strings.Count(s, ". ") >= 3 {
		return false
	}
	return true
}

// pickSessionTitle chooses a display title for a session.
// Prefer official Codex title-generation results; else first real user prompt.
func pickSessionTitle(requests []RequestRecord) (title, source string) {
	if len(requests) == 0 {
		return "", ""
	}
	ordered := append([]RequestRecord(nil), requests...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Timestamp.Equal(ordered[j].Timestamp) {
			return ordered[i].SourceFile < ordered[j].SourceFile
		}
		return ordered[i].Timestamp.Before(ordered[j].Timestamp)
	})
	for _, r := range ordered {
		if strings.TrimSpace(r.Title) != "" && r.TitleSource == titleSourceCodex {
			return r.Title, titleSourceCodex
		}
	}
	for _, r := range ordered {
		if strings.TrimSpace(r.Title) != "" {
			return r.Title, firstNonEmpty(r.TitleSource, titleSourceCodex)
		}
	}
	for _, r := range ordered {
		for _, p := range r.SamplePrompts {
			p = normalizeTitle(p)
			if p == "" {
				continue
			}
			return p, titleSourceUser
		}
	}
	return "", ""
}
