package management

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logqa"
	"gopkg.in/yaml.v3"
)

const (
	logQAMaxSessionScan = 200_000
	logQADefaultLimit   = 50
	logQAMaxLimit       = 200
)

type logQAFileConfig struct {
	WorkDir  string `yaml:"work-dir"`
	LogsRoot string `yaml:"logs-root"`
	Timezone string `yaml:"timezone"`
}

type logQASettings struct {
	workDir    string
	logsRoot   string
	timezone   string
	found      bool
	configPath string
}

func (h *Handler) resolveLogQASettings() logQASettings {
	settings := logQASettings{
		workDir:  filepath.Join(h.cfg.AuthDir, "log-qa"),
		logsRoot: filepath.Join(h.cfg.AuthDir, "logs", "keys"),
		timezone: "Asia/Shanghai",
	}
	configDir := filepath.Dir(h.configFilePath)
	candidates := []string{
		filepath.Join(configDir, "log-qa.yaml"),
		filepath.Join(h.cfg.AuthDir, "log-qa.yaml"),
	}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg logQAFileConfig
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			continue
		}
		settings.found = true
		settings.configPath = path
		base := filepath.Dir(path)
		if strings.TrimSpace(cfg.WorkDir) != "" {
			settings.workDir = resolveMaybeRelative(base, cfg.WorkDir)
		}
		if strings.TrimSpace(cfg.LogsRoot) != "" {
			settings.logsRoot = resolveMaybeRelative(base, cfg.LogsRoot)
		}
		if strings.TrimSpace(cfg.Timezone) != "" {
			settings.timezone = cfg.Timezone
		}
		break
	}
	return settings
}

func resolveMaybeRelative(base, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(base, value)
}

// GetLogQAStatus returns whether QA reports are available.
func (h *Handler) GetLogQAStatus(c *gin.Context) {
	settings := h.resolveLogQASettings()
	latest, errLatest := logqa.ReadLatestPointer(settings.workDir)
	status := gin.H{
		"config_found":  settings.found,
		"config_path":   settings.configPath,
		"work_dir":      settings.workDir,
		"logs_root":     settings.logsRoot,
		"timezone":      settings.timezone,
		"latest_run_id": "",
		"has_report":    false,
		"message":       "尚无质检报告。请先运行 log-qa 服务。",
	}
	if errLatest == nil && (latest.RunID != "" || latest.Dir != "") {
		status["has_report"] = true
		status["latest_run_id"] = firstNonEmptyStr(latest.RunID, latest.Dir)
		status["message"] = "正常"
	}
	c.JSON(http.StatusOK, status)
}

// GetLogQASummary returns the latest or selected run summary.
func (h *Handler) GetLogQASummary(c *gin.Context) {
	settings := h.resolveLogQASettings()
	runID := strings.TrimSpace(c.Query("run_id"))
	summary, err := logqa.ReadSummary(settings.workDir, runID)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{
				"has_report": false,
				"message":    "尚无质检报告",
			})
			return
		}
		if strings.Contains(err.Error(), "invalid run_id") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"has_report": false,
			"message":    err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"has_report": true,
		"summary":    summary,
	})
}

// GetLogQARuns lists recent report run directories.
func (h *Handler) GetLogQARuns(c *gin.Context) {
	settings := h.resolveLogQASettings()
	dir := filepath.Join(settings.workDir, "reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"runs": []any{}, "message": "报告目录不存在"})
		return
	}
	type runInfo struct {
		RunID string `json:"run_id"`
	}
	runs := make([]runInfo, 0)
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		if strings.Contains(e.Name(), "..") {
			continue
		}
		runs = append(runs, runInfo{RunID: e.Name()})
	}
	// reverse chronological by name (UTC timestamp format sorts)
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	if len(runs) > 48 {
		runs = runs[:48]
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

// GetLogQASessions streams session rows from a report with filters.
func (h *Handler) GetLogQASessions(c *gin.Context) {
	settings := h.resolveLogQASettings()
	runID := strings.TrimSpace(c.Query("run_id"))
	if runID == "" {
		latest, err := logqa.ReadLatestPointer(settings.workDir)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"sessions": []any{}, "total": 0, "has_report": false})
			return
		}
		runID = firstNonEmptyStr(latest.Dir, latest.RunID)
	}
	if err := validateLogQARunID(runID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	statusFilter := strings.ToLower(strings.TrimSpace(c.DefaultQuery("status", "all")))
	reasonFilter := strings.TrimSpace(c.Query("reason"))
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	limit := logQADefaultLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > logQAMaxLimit {
		limit = logQAMaxLimit
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}

	path := filepath.Join(settings.workDir, "reports", runID, "session_qa.jsonl")
	file, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"sessions": []any{}, "total": 0, "has_report": false, "run_id": runID})
		return
	}
	defer func() { _ = file.Close() }()

	matched := make([]logqa.SessionRecord, 0, limit)
	totalMatched := 0
	scanned := 0
	scanner := bufio.NewScanner(file)
	// large lines possible
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		scanned++
		if scanned > logQAMaxSessionScan {
			break
		}
		line := scanner.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		var rec logqa.SessionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if statusFilter == "pass" && !rec.OK {
			continue
		}
		if statusFilter == "fail" && rec.OK {
			continue
		}
		if reasonFilter != "" && !sessionHasReason(rec, reasonFilter) {
			continue
		}
		if q != "" && !sessionMatchesQuery(rec, q) {
			continue
		}
		if totalMatched >= offset && len(matched) < limit {
			matched = append(matched, rec)
		}
		totalMatched++
	}

	c.JSON(http.StatusOK, gin.H{
		"has_report": true,
		"run_id":     runID,
		"sessions":   matched,
		"total":      totalMatched,
		"limit":      limit,
		"offset":     offset,
	})
}

func sessionHasReason(rec logqa.SessionRecord, reason string) bool {
	reason = strings.ToLower(reason)
	for _, r := range rec.FailReasons {
		if strings.Contains(strings.ToLower(r), reason) {
			return true
		}
	}
	// also accept bucket names
	switch reason {
	case "prompt_rounds":
		for _, r := range rec.FailReasons {
			if strings.HasPrefix(r, "prompt_rounds") {
				return true
			}
		}
	case "no_tool_call":
		for _, r := range rec.FailReasons {
			if r == "no_tool_call" {
				return true
			}
		}
	case "duplicate_assistant":
		for _, r := range rec.FailReasons {
			if strings.HasPrefix(r, "duplicate_assistant") {
				return true
			}
		}
	}
	return false
}

func sessionMatchesQuery(rec logqa.SessionRecord, q string) bool {
	if strings.Contains(strings.ToLower(rec.SessionID), q) {
		return true
	}
	for _, t := range rec.ThreadIDs {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	for _, k := range rec.KeyNames {
		if strings.Contains(strings.ToLower(k), q) {
			return true
		}
	}
	return false
}

func validateLogQARunID(runID string) error {
	if runID == "" || strings.Contains(runID, "..") || strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("invalid run_id")
	}
	return nil
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
