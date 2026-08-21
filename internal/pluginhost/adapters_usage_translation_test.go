package pluginhost

import (
	"context"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestUsageAdapterPreservesIdentityAndIndependentTokenBuckets(t *testing.T) {
	var got pluginapi.UsageRecord
	plugin := usagePluginFunc(func(_ context.Context, record pluginapi.UsageRecord) {
		got = record
	})
	host := newHostWithRecords(capabilityRecord{
		id: "usage-translation",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: plugin,
		}},
	})
	adapter := &usageAdapter{host: host, pluginID: "usage-translation"}

	adapter.HandleUsage(context.Background(), coreusage.Record{
		Provider:     "claude",
		ExecutorType: "ClaudeExecutor",
		AuthID:       "auth-uuid",
		AuthIndex:    "auth-index",
		AuthType:     "oauth",
		Source:       "account-uuid",
		Detail: coreusage.Detail{
			InputTokens:         2,
			OutputTokens:        244,
			ReasoningTokens:     40,
			CachedTokens:        44225,
			CacheReadTokens:     44225,
			CacheCreationTokens: 831,
			TotalTokens:         45302,
		},
	})

	if got.Provider != "claude" || got.ExecutorType != "ClaudeExecutor" || got.AuthID != "auth-uuid" || got.AuthIndex != "auth-index" || got.AuthType != "oauth" || got.Source != "account-uuid" {
		t.Fatalf("identity was not preserved: %+v", got)
	}
	if got.Detail.InputTokens != 2 || got.Detail.OutputTokens != 244 || got.Detail.ReasoningTokens != 40 || got.Detail.CachedTokens != 44225 || got.Detail.CacheReadTokens != 44225 || got.Detail.CacheCreationTokens != 831 || got.Detail.TotalTokens != 45302 {
		t.Fatalf("token buckets were not preserved: %+v", got.Detail)
	}
}
