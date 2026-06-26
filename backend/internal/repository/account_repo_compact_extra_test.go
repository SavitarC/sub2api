package repository

import "testing"

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_CompactCapabilityKeysAreRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_compact_supported":  true,
		"openai_compact_checked_at": "2026-04-10T10:00:00Z",
	}

	if !shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected compact capability updates to enqueue scheduler outbox")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_OpenAIResponsesCapabilityKeysAreRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_responses_mode":      "force_chat_completions",
		"openai_responses_supported": false,
	}

	if !shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected responses capability updates to enqueue scheduler outbox")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_CodexSparkObservationKeysAreNeutral(t *testing.T) {
	updates := map[string]any{
		"codex_spark_usage_updated_at":              "2026-03-11T10:00:00Z",
		"codex_spark_primary_used_percent":          12.5,
		"codex_spark_secondary_reset_after_seconds": 18000,
		"codex_spark_5h_used_percent":               12.5,
		"codex_spark_7d_reset_at":                   "2026-03-18T10:00:00Z",
	}

	if shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected codex_spark observation updates to skip scheduler outbox")
	}
}
