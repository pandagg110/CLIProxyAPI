package loguploader

import "testing"

func TestFillAuditJSONLBytesApportionsLegacyKeys(t *testing.T) {
	record := auditRecord{
		JSONLBytes: 1000,
		KeyNames: map[string]auditKeyNameSummary{
			"alice": {SourceCount: 2, SourceBytes: 750, Models: map[string]auditModelSummary{"claude-fable-5": {SourceCount: 2, SourceBytes: 750}}},
			"bob":   {SourceCount: 1, SourceBytes: 250, Models: map[string]auditModelSummary{"claude-fable-5": {SourceCount: 1, SourceBytes: 250}}},
		},
	}
	fillAuditJSONLBytes(&record)
	if record.KeyNames["alice"].JSONLBytes != 750 || record.KeyNames["bob"].JSONLBytes != 250 {
		t.Fatalf("apportioned keys = %+v", record.KeyNames)
	}
	if record.KeyNames["alice"].Models["claude-fable-5"].JSONLBytes != 750 {
		t.Fatalf("apportioned alice model = %+v", record.KeyNames["alice"].Models)
	}
}

func TestFillAuditJSONLBytesKeepsExactKeyJSONL(t *testing.T) {
	record := auditRecord{
		JSONLBytes: 400,
		KeyNames: map[string]auditKeyNameSummary{
			"alice": {SourceCount: 2, SourceBytes: 800, JSONLBytes: 100, Models: map[string]auditModelSummary{"claude-fable-5": {SourceCount: 2, SourceBytes: 800, JSONLBytes: 100}}},
			"bob":   {SourceCount: 1, SourceBytes: 200, JSONLBytes: 300, Models: map[string]auditModelSummary{"claude-fable-5": {SourceCount: 1, SourceBytes: 200, JSONLBytes: 300}}},
		},
	}
	fillAuditJSONLBytes(&record)
	if record.KeyNames["alice"].JSONLBytes != 100 || record.KeyNames["bob"].JSONLBytes != 300 {
		t.Fatalf("exact keys overwritten: %+v", record.KeyNames)
	}
}

func TestArchiveFilenameJSONLLabel(t *testing.T) {
	if got := archiveFilenameJSONLLabel("cliproxy-logs/2026/08/27/2026-08-27-08-fable5-903.9M.jsonl.zst"); got != "903.9M" {
		t.Fatalf("label = %q", got)
	}
	if got := archiveFilenameJSONLLabel("cliproxy-logs/2026/07/18/archive"); got != "" {
		t.Fatalf("unlabeled object = %q", got)
	}
}
