package logqa

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	titleSummaryPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bgenerate (a )?title\b`),
		regexp.MustCompile(`(?i)\bcreate (a )?title\b`),
		regexp.MustCompile(`(?i)\bthread title\b`),
		regexp.MustCompile(`(?i)\bsession title\b`),
		regexp.MustCompile(`(?i)\bconversation title\b`),
		regexp.MustCompile(`(?i)\bsummarize (this|the) (conversation|chat|thread)\b`),
		regexp.MustCompile(`(?i)\bconversation summary\b`),
		regexp.MustCompile(`(?i)produced a summary of its thinking process`),
		regexp.MustCompile(`(?i)\bcompact(ion)?\b.*\bsummary\b`),
	}

	toolTypes = map[string]struct{}{
		"function_call":           {},
		"function_call_output":    {},
		"custom_tool_call":        {},
		"custom_tool_call_output": {},
		"computer_call":           {},
		"computer_call_output":    {},
		"web_search_call":         {},
		"file_search_call":        {},
		"code_interpreter_call":   {},
		"mcp_call":                {},
		"image_generation_call":   {},
	}

	toolNames = map[string]struct{}{
		"exec":        {},
		"shell":       {},
		"apply_patch": {},
		"update_plan": {},
		"read_file":   {},
		"write_file":  {},
		"grep_files":  {},
		"list_dir":    {},
	}
)

type inputMetrics struct {
	PromptRounds   int
	ToolCalls      int
	DupAssistant   int
	SamplePrompts  []string
	AssistantTexts []string
}

func scoreInput(input []any, requestKind string, rules RulesConfig) inputMetrics {
	var m inputMetrics
	assistantCounts := make(map[string]int)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		typeName := stringField(item, "type")
		role := stringField(item, "role")
		name := stringField(item, "name")
		text := contentText(item)

		if _, isTool := toolTypes[typeName]; isTool {
			m.ToolCalls++
		} else if _, isToolName := toolNames[name]; isToolName {
			m.ToolCalls++
		}

		if isRealUserPrompt(typeName, role, text, requestKind, rules) {
			m.PromptRounds++
			if len(m.SamplePrompts) < 5 {
				m.SamplePrompts = append(m.SamplePrompts, truncate(text, 160))
			}
		}

		if role == "assistant" || typeName == "agent_message" {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				assistantCounts[trimmed]++
				m.AssistantTexts = append(m.AssistantTexts, trimmed)
			}
		}
	}
	for _, count := range assistantCounts {
		if count >= 2 {
			m.DupAssistant++
		}
	}
	return m
}

func isRealUserPrompt(typeName, role, text, requestKind string, rules RulesConfig) bool {
	if role != "user" {
		return false
	}
	if typeName != "" && typeName != "message" {
		return false
	}
	if strings.TrimSpace(text) == "" {
		return false
	}
	if rules.ExcludeIDEContext && isIDEContext(text) {
		return false
	}
	if rules.ExcludeEnvContext && isEnvContext(text) {
		return false
	}
	if rules.ExcludeTitleSummary && isTitleOrSummary(text, requestKind) {
		return false
	}
	return true
}

func isIDEContext(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "# Context from my IDE setup")
}

func isEnvContext(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	return strings.HasPrefix(trimmed, "<environment_context>") || strings.Contains(trimmed[:min(200, len(trimmed))], "<environment_context>")
}

func isTitleOrSummary(text, requestKind string) bool {
	rk := strings.ToLower(requestKind)
	if strings.Contains(rk, "title") || strings.Contains(rk, "summary") || strings.Contains(rk, "compact") {
		return true
	}
	for _, pat := range titleSummaryPatterns {
		if pat.MatchString(text) {
			return true
		}
	}
	return false
}

func contentText(item map[string]any) string {
	if v, ok := item["content"]; ok {
		switch content := v.(type) {
		case string:
			return content
		case []any:
			var parts []string
			for _, part := range content {
				switch p := part.(type) {
				case string:
					parts = append(parts, p)
				case map[string]any:
					if t := stringField(p, "text"); t != "" {
						parts = append(parts, t)
					} else if t := stringField(p, "input"); t != "" {
						parts = append(parts, t)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	if t := stringField(item, "text"); t != "" {
		return t
	}
	return ""
}

func stringField(obj map[string]any, key string) string {
	v, ok := obj[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// EvaluateSession applies the three delivery rules to a session snapshot.
func EvaluateSession(promptRounds, toolCalls, dupAssistant int, rules RulesConfig) (ok bool, reasons []string) {
	if promptRounds < rules.MinPromptRounds {
		reasons = append(reasons, fmt.Sprintf("prompt_rounds=%d<%d", promptRounds, rules.MinPromptRounds))
	}
	if rules.RequireToolCall && toolCalls < 1 {
		reasons = append(reasons, "no_tool_call")
	}
	if rules.RejectDuplicateAssistant && dupAssistant > 0 {
		reasons = append(reasons, fmt.Sprintf("duplicate_assistant_groups=%d", dupAssistant))
	}
	return len(reasons) == 0, reasons
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
