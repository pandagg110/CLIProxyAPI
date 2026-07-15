package loguploader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessLockAllowsOnlyOneServicePerWorkDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	logsRoot := createLockTestLogsRoot(t, root, "logs", "keys")
	workDir := filepath.Join(root, "work")
	first := lockTestService(logsRoot, workDir, "target-one")
	second := lockTestService(logsRoot, workDir, "target-one")

	firstLock, errFirst := first.acquireProcessLock()
	if errFirst != nil {
		t.Fatalf("acquire first process lock: %v", errFirst)
	}
	if _, errSecond := second.acquireProcessLock(); errSecond == nil || !strings.Contains(errSecond.Error(), "another log uploader") {
		t.Fatalf("second process lock error = %v", errSecond)
	}
	if errClose := firstLock.Close(); errClose != nil {
		t.Fatalf("release first process lock: %v", errClose)
	}
	secondLock, errSecond := second.acquireProcessLock()
	if errSecond != nil {
		t.Fatalf("acquire process lock after release: %v", errSecond)
	}
	if errClose := secondLock.Close(); errClose != nil {
		t.Fatalf("release second process lock: %v", errClose)
	}
}

func TestProcessLockAllowsOnlyOneServicePerLogsRootAndTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	logsRoot := createLockTestLogsRoot(t, root, "logs", "keys")
	first := lockTestService(logsRoot, filepath.Join(root, "work-one"), "shared-target")
	second := lockTestService(logsRoot, filepath.Join(root, "work-two"), "shared-target")

	firstLock, errFirst := first.acquireProcessLock()
	if errFirst != nil {
		t.Fatalf("acquire first process lock: %v", errFirst)
	}
	if _, errSecond := second.acquireProcessLock(); errSecond == nil || !strings.Contains(errSecond.Error(), "logs root and upload target") {
		t.Fatalf("second shared resource lock error = %v", errSecond)
	}
	if errClose := firstLock.Close(); errClose != nil {
		t.Fatalf("release first process lock: %v", errClose)
	}
	secondLock, errSecond := second.acquireProcessLock()
	if errSecond != nil {
		t.Fatalf("acquire shared resource lock after release: %v", errSecond)
	}
	if errClose := secondLock.Close(); errClose != nil {
		t.Fatalf("release second process lock: %v", errClose)
	}
}

func TestProcessLockCanonicalizesLogsRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	logsRoot := createLockTestLogsRoot(t, root, "logs", "keys")
	alias := filepath.Join(logsRoot, "..", filepath.Base(logsRoot))
	first := lockTestService(logsRoot, filepath.Join(root, "work-one"), "shared-target")
	second := lockTestService(alias, filepath.Join(root, "work-two"), "shared-target")

	firstPath, errFirstPath := first.sharedResourceLockPath()
	if errFirstPath != nil {
		t.Fatalf("resolve first shared lock path: %v", errFirstPath)
	}
	secondPath, errSecondPath := second.sharedResourceLockPath()
	if errSecondPath != nil {
		t.Fatalf("resolve second shared lock path: %v", errSecondPath)
	}
	if firstPath != secondPath {
		t.Fatalf("canonical shared lock paths differ: %q != %q", firstPath, secondPath)
	}
	if strings.Contains(filepath.Base(firstPath), first.target.ID) {
		t.Fatalf("shared lock filename exposes target identity: %q", firstPath)
	}
	canonicalRoot, errCanonical := canonicalLogsRoot(logsRoot)
	if errCanonical != nil {
		t.Fatalf("canonicalize test logs root: %v", errCanonical)
	}
	if filepath.Dir(filepath.Dir(firstPath)) != filepath.Dir(canonicalRoot) {
		t.Fatalf("shared lock path %q is not beside the logs root", firstPath)
	}
}

func TestProcessLockAllowsIndependentResources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	logsRoot := createLockTestLogsRoot(t, root, "logs", "keys")
	first := lockTestService(logsRoot, filepath.Join(root, "work-one"), "target-one")
	second := lockTestService(logsRoot, filepath.Join(root, "work-two"), "target-two")

	firstLock, errFirst := first.acquireProcessLock()
	if errFirst != nil {
		t.Fatalf("acquire first process lock: %v", errFirst)
	}
	defer func() {
		if errClose := firstLock.Close(); errClose != nil {
			t.Errorf("release first process lock: %v", errClose)
		}
	}()
	secondLock, errSecond := second.acquireProcessLock()
	if errSecond != nil {
		t.Fatalf("different target unexpectedly shared a resource lock: %v", errSecond)
	}
	if errClose := secondLock.Close(); errClose != nil {
		t.Fatalf("release second process lock: %v", errClose)
	}
}

func TestProcessLockReleasesWorkLockWhenSharedLockFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	logsRoot := createLockTestLogsRoot(t, root, "logs", "keys")
	holder := lockTestService(logsRoot, filepath.Join(root, "holder-work"), "shared-target")
	blockedWorkDir := filepath.Join(root, "blocked-work")
	blocked := lockTestService(logsRoot, blockedWorkDir, "shared-target")
	probe := lockTestService(logsRoot, blockedWorkDir, "independent-target")

	holderLock, errHolder := holder.acquireProcessLock()
	if errHolder != nil {
		t.Fatalf("acquire shared resource holder lock: %v", errHolder)
	}
	defer func() {
		if errClose := holderLock.Close(); errClose != nil {
			t.Errorf("release shared resource holder lock: %v", errClose)
		}
	}()
	if _, errBlocked := blocked.acquireProcessLock(); errBlocked == nil || !strings.Contains(errBlocked.Error(), "logs root and upload target") {
		t.Fatalf("blocked shared resource lock error = %v", errBlocked)
	}
	probeLock, errProbe := probe.acquireProcessLock()
	if errProbe != nil {
		t.Fatalf("work lock was not released after shared lock failure: %v", errProbe)
	}
	if errClose := probeLock.Close(); errClose != nil {
		t.Fatalf("release probe process lock: %v", errClose)
	}
}

func createLockTestLogsRoot(t *testing.T, elements ...string) string {
	t.Helper()
	logsRoot := filepath.Join(elements...)
	if errMkdir := os.MkdirAll(logsRoot, 0o750); errMkdir != nil {
		t.Fatalf("create logs root: %v", errMkdir)
	}
	return logsRoot
}

func lockTestService(logsRoot, workDir, targetID string) *Service {
	return &Service{
		cfg:    Config{LogsRoot: logsRoot, WorkDir: workDir},
		target: uploadTarget{ID: targetID},
	}
}
