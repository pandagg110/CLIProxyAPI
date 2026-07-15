package logging

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishRequestLogPublishesOnlyCompleteFile(t *testing.T) {
	logsDir := t.TempDir()
	finalPath := filepath.Join(logsDir, "request.log")
	partialWritten := make(chan struct{})
	finishWrite := make(chan struct{})
	errChan := make(chan error, 1)

	go func() {
		errChan <- publishRequestLog(finalPath, func(file *os.File) error {
			if _, errWrite := file.WriteString("partial-"); errWrite != nil {
				return errWrite
			}
			close(partialWritten)
			<-finishWrite
			_, errWrite := file.WriteString("complete")
			return errWrite
		})
	}()

	<-partialWritten
	if _, errStat := os.Stat(finalPath); !os.IsNotExist(errStat) {
		t.Fatalf("final log became visible before writing completed: %v", errStat)
	}
	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir while write is blocked: %v", errRead)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".tmp") {
		t.Fatalf("entries while write is blocked = %v, want one temp file", entryNames(entries))
	}

	close(finishWrite)
	if errPublish := <-errChan; errPublish != nil {
		t.Fatalf("publishRequestLog: %v", errPublish)
	}
	raw, errReadLog := os.ReadFile(finalPath)
	if errReadLog != nil {
		t.Fatalf("ReadFile final log: %v", errReadLog)
	}
	if string(raw) != "partial-complete" {
		t.Fatalf("final log = %q, want complete content", string(raw))
	}
	assertOnlyFinalLog(t, logsDir, filepath.Base(finalPath))
}

func TestPublishRequestLogCleansTempAfterWriteFailure(t *testing.T) {
	logsDir := t.TempDir()
	finalPath := filepath.Join(logsDir, "request.log")
	wantErr := errors.New("write failed")

	errPublish := publishRequestLog(finalPath, func(file *os.File) error {
		if _, errWrite := file.WriteString("partial"); errWrite != nil {
			return errWrite
		}
		return wantErr
	})
	if !errors.Is(errPublish, wantErr) {
		t.Fatalf("publishRequestLog error = %v, want wrapped %v", errPublish, wantErr)
	}
	if _, errStat := os.Stat(finalPath); !os.IsNotExist(errStat) {
		t.Fatalf("final log exists after failed write: %v", errStat)
	}
	assertDirectoryEmpty(t, logsDir)
}

func TestPublishRequestLogDoesNotOverwriteExistingLog(t *testing.T) {
	logsDir := t.TempDir()
	finalPath := filepath.Join(logsDir, "request.log")
	if errWrite := os.WriteFile(finalPath, []byte("first"), 0644); errWrite != nil {
		t.Fatalf("WriteFile existing log: %v", errWrite)
	}

	errPublish := publishRequestLog(finalPath, func(file *os.File) error {
		_, errWrite := file.WriteString("second")
		return errWrite
	})
	if errPublish != nil {
		t.Fatalf("publishRequestLog: %v", errPublish)
	}
	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir: %v", errRead)
	}
	if len(entries) != 2 {
		t.Fatalf("entries after collision = %v, want two logs", entryNames(entries))
	}
	first, errFirst := os.ReadFile(finalPath)
	if errFirst != nil {
		t.Fatalf("ReadFile existing log: %v", errFirst)
	}
	if string(first) != "first" {
		t.Fatalf("existing log = %q, want first", first)
	}
	foundSecond := false
	for _, entry := range entries {
		if entry.Name() == filepath.Base(finalPath) {
			continue
		}
		second, errSecond := os.ReadFile(filepath.Join(logsDir, entry.Name()))
		if errSecond != nil {
			t.Fatalf("ReadFile collision log: %v", errSecond)
		}
		foundSecond = string(second) == "second" && strings.HasSuffix(entry.Name(), ".log")
	}
	if !foundSecond {
		t.Fatalf("entries after collision = %v, missing second complete log", entryNames(entries))
	}
}

func TestFileRequestLoggerPublishesNonStreamingLogWithoutTempArtifacts(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	errLog := logger.LogRequest(
		"/v1/responses",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusOK,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"output":"complete"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		"atomic-non-streaming",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequest: %v", errLog)
	}

	logPath := onlyPublishedLogPath(t, logsDir)
	raw, errRead := os.ReadFile(logPath)
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	if !bytes.Contains(raw, []byte(`{"output":"complete"}`)) {
		t.Fatalf("published log is incomplete: %s", raw)
	}
}

func TestFileRequestLoggerNonStreamingFailureLeavesNoPublishedOrTempLog(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)
	badSourceDir := filepath.Join(logsDir, "bad-source")
	if errMkdir := os.Mkdir(badSourceDir, 0755); errMkdir != nil {
		t.Fatalf("Mkdir bad source: %v", errMkdir)
	}
	badSource := &FileBodySource{dir: badSourceDir, paths: []string{badSourceDir}}

	errLog := logger.LogRequestWithOptionsAndAllSources(
		"/v1/responses",
		http.MethodPost,
		nil,
		[]byte(`{}`),
		http.StatusOK,
		nil,
		[]byte(`{}`),
		nil,
		nil,
		nil,
		badSource,
		nil,
		nil,
		nil,
		nil,
		nil,
		false,
		"atomic-non-streaming-failure",
		time.Now(),
		time.Now(),
	)
	if errLog == nil {
		t.Fatal("LogRequestWithOptionsAndAllSources succeeded, want source read error")
	}
	assertDirectoryEmpty(t, logsDir)
}

func TestFileStreamingLogWriterPublishesFinalLogWithoutTempArtifacts(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)
	writer, errCreate := logger.LogStreamingRequest(
		"/v1/responses",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		"atomic-streaming",
	)
	if errCreate != nil {
		t.Fatalf("LogStreamingRequest: %v", errCreate)
	}
	if errStatus := writer.WriteStatus(http.StatusOK, map[string][]string{"Content-Type": {"text/event-stream"}}); errStatus != nil {
		t.Fatalf("WriteStatus: %v", errStatus)
	}
	writer.WriteChunkAsync([]byte("data: complete\n\n"))
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close: %v", errClose)
	}

	logPath := onlyPublishedLogPath(t, logsDir)
	raw, errRead := os.ReadFile(logPath)
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	if !bytes.Contains(raw, []byte("data: complete")) {
		t.Fatalf("published streaming log is incomplete: %s", raw)
	}
}

func TestFileStreamingLogWriterFinalizationFailureCleansTempFiles(t *testing.T) {
	logsDir := t.TempDir()
	requestBodyPath := filepath.Join(logsDir, "request-body.tmp")
	if errWrite := os.WriteFile(requestBodyPath, []byte(`{"input":"hello"}`), 0644); errWrite != nil {
		t.Fatalf("WriteFile request body: %v", errWrite)
	}
	responseBodyPath := filepath.Join(logsDir, "missing-response-body.tmp")
	finalPath := filepath.Join(logsDir, "stream.log")
	writer := &FileStreamingLogWriter{
		logFilePath:      finalPath,
		url:              "/v1/responses",
		method:           http.MethodPost,
		timestamp:        time.Now(),
		requestBodyPath:  requestBodyPath,
		responseBodyPath: responseBodyPath,
		errorChan:        make(chan error, 1),
	}

	errClose := writer.Close()
	if errClose == nil || !errors.Is(errClose, os.ErrNotExist) {
		t.Fatalf("Close error = %v, want missing response body error", errClose)
	}
	if _, errStat := os.Stat(finalPath); !os.IsNotExist(errStat) {
		t.Fatalf("final streaming log exists after finalization failure: %v", errStat)
	}
	assertDirectoryEmpty(t, logsDir)
}

func onlyPublishedLogPath(t *testing.T, dir string) string {
	t.Helper()
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		t.Fatalf("ReadDir: %v", errRead)
	}
	if len(entries) != 1 || entries[0].IsDir() || filepath.Ext(entries[0].Name()) != ".log" {
		t.Fatalf("published entries = %v, want one .log file", entryNames(entries))
	}
	return filepath.Join(dir, entries[0].Name())
}

func assertOnlyFinalLog(t *testing.T, dir, finalName string) {
	t.Helper()
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		t.Fatalf("ReadDir: %v", errRead)
	}
	if len(entries) != 1 || entries[0].Name() != finalName || entries[0].IsDir() {
		t.Fatalf("published entries = %v, want only %s", entryNames(entries), finalName)
	}
}

func assertDirectoryEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		t.Fatalf("ReadDir: %v", errRead)
	}
	if len(entries) != 0 {
		t.Fatalf("directory entries = %v, want empty directory", entryNames(entries))
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
