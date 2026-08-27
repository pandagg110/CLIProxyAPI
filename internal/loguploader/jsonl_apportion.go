package loguploader

import (
	"math"
	"math/bits"
	"path"
	"sort"
	"strings"
)

func cloneAuditKeyNames(keys map[string]auditKeyNameSummary) map[string]auditKeyNameSummary {
	cloned := make(map[string]auditKeyNameSummary, len(keys))
	for name, key := range keys {
		clonedKey := key
		if key.Models != nil {
			clonedKey.Models = make(map[string]auditModelSummary, len(key.Models))
			for modelName, model := range key.Models {
				clonedKey.Models[modelName] = model
			}
		}
		cloned[name] = clonedKey
	}
	return cloned
}

func fillAuditJSONLBytes(record *auditRecord) {
	if record == nil || record.JSONLBytes <= 0 || len(record.KeyNames) == 0 {
		return
	}
	var stored int64
	for _, key := range record.KeyNames {
		stored += key.JSONLBytes
	}
	if stored == 0 {
		apportionAuditKeyJSONL(record.KeyNames, record.JSONLBytes)
	}
	for name, key := range record.KeyNames {
		apportionAuditModelJSONL(key.Models, key.JSONLBytes)
		record.KeyNames[name] = key
	}
}

func usageMatchesHourProvider(usage []supabaseEventUsage, provider string) bool {
	if len(usage) == 0 || strings.TrimSpace(provider) == "" {
		return false
	}
	for _, row := range usage {
		if row.Provider != provider {
			return false
		}
	}
	return true
}

func auditUsageHasExactJSONL(record auditRecord, usage []supabaseEventUsage) bool {
	if record.JSONLBytes <= 0 || len(usage) == 0 {
		return false
	}
	var total int64
	for _, row := range usage {
		if row.JSONLBytes == nil {
			return false
		}
		next, errAdd := addSafeJSONInteger(total, *row.JSONLBytes)
		if errAdd != nil {
			return false
		}
		total = next
	}
	return total == record.JSONLBytes
}

func apportionAuditKeyJSONL(keys map[string]auditKeyNameSummary, total int64) {
	if total <= 0 || len(keys) == 0 {
		return
	}
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	var whole int64
	for _, name := range names {
		whole += keys[name].SourceBytes
	}
	var assigned int64
	for index, name := range names {
		key := keys[name]
		share := int64(0)
		if index == len(names)-1 {
			share = total - assigned
			if share < 0 {
				share = 0
			}
		} else {
			share = proportionalJSONLShare(total, key.SourceBytes, whole)
		}
		key.JSONLBytes = share
		assigned += share
		keys[name] = key
	}
}

func apportionAuditModelJSONL(models map[string]auditModelSummary, total int64) {
	if total <= 0 || len(models) == 0 {
		return
	}
	var stored int64
	for _, model := range models {
		stored += model.JSONLBytes
	}
	if stored > 0 {
		return
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	var whole int64
	for _, name := range names {
		whole += models[name].SourceBytes
	}
	var assigned int64
	for index, name := range names {
		model := models[name]
		share := int64(0)
		if index == len(names)-1 {
			share = total - assigned
			if share < 0 {
				share = 0
			}
		} else {
			share = proportionalJSONLShare(total, model.SourceBytes, whole)
		}
		model.JSONLBytes = share
		assigned += share
		models[name] = model
	}
}

func proportionalJSONLShare(total, part, whole int64) int64 {
	if total <= 0 || part <= 0 || whole <= 0 {
		return 0
	}
	if part >= whole {
		return total
	}
	hi, lo := bits.Mul64(uint64(total), uint64(part))
	if hi >= uint64(whole) {
		return total
	}
	quo, _ := bits.Div64(hi, lo, uint64(whole))
	if quo > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(quo)
}

func archiveFilenameJSONLLabel(objectKey string) string {
	base := path.Base(strings.TrimSpace(objectKey))
	if !strings.HasSuffix(base, ".jsonl.zst") {
		return ""
	}
	name := strings.TrimSuffix(base, ".jsonl.zst")
	parts := strings.Split(name, "-")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
