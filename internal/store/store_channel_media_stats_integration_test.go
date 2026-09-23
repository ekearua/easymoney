package store

import (
	"context"
	"os"
	"testing"
)

// TestChannelMediaStats verifies the admin channel-media report aggregates:
// successful OCR/STT extractions, failed ones (provider error or empty text),
// and the reported AI token usage, bucketed per channel per media kind — with
// messages that never went through extraction (no AI enabled) counted as
// neither success nor failure.
func TestChannelMediaStats(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `TRUNCATE inbound_messages, media_usage_log CASCADE`); err != nil {
		t.Fatal(err)
	}

	// Un-sealed rows are fine for this report: the aggregate reads the
	// media_ok/media_ai_tokens outcome columns, not the encrypted payload.
	seed := func(id, channel, mediaType, status string, ok *bool, tokens int) {
		t.Helper()
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO inbound_messages
				(provider_message_id, channel, sender, recipient, payload, media_type, media_id, status, media_ok, media_ai_tokens)
			VALUES ($1,$2,$3,$3,'{}'::jsonb,$4,$5,$6,$7,$8)`,
			id, channel, "+"+id, mediaType, "media-"+id, status, ok, tokens); err != nil {
			t.Fatal(err)
		}
	}
	ok := true
	bad := false
	seed("wa-img-ok", "whatsapp", "image", "processed", &ok, 210)
	seed("wa-img-bad", "whatsapp", "photo", "processed", &bad, 180) // provider returned empty text
	seed("wa-img-skip", "whatsapp", "image", "pending", nil, 0)     // AI disabled: never extracted
	seed("tg-voice-ok", "telegram", "voice", "processed", &ok, 120)
	seed("ig-audio-bad", "instagram", "audio", "processed", &bad, 0) // download/STT failed (message still processed)

	report, err := repository.ChannelMediaStats(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalAttempts != 5 {
		t.Fatalf("total attempts = %d, want 5", report.TotalAttempts)
	}
	if len(report.Totals) != 3 { // whatsapp|image, telegram|voice, instagram|voice
		t.Fatalf("totals cells = %d (%v), want 3", len(report.Totals), report.Totals)
	}
	byKey := map[string]ChannelMediaStat{}
	for _, stat := range report.Totals {
		byKey[stat.Channel+"|"+stat.MediaKind] = stat
	}
	wa := byKey["whatsapp|image"]
	if wa.Attempts != 3 || wa.Successes != 1 || wa.Failures != 1 || wa.AITokens != 390 {
		t.Fatalf("whatsapp/image = %+v, want attempts 3, successes 1, failures 1, tokens 390", wa)
	}
	if rate := wa.SuccessRate(); rate < 0.32 || rate > 0.34 {
		t.Fatalf("whatsapp/image success rate = %f, want 1/3", rate)
	}
	tg := byKey["telegram|voice"]
	if tg.Attempts != 1 || tg.Successes != 1 || tg.AITokens != 120 {
		t.Fatalf("telegram/voice = %+v, want attempts 1, successes 1, tokens 120", tg)
	}
	ig := byKey["instagram|voice"]
	if ig.Failures != 1 {
		t.Fatalf("instagram/voice = %+v, want 1 failure", ig)
	}
	if report.TotalTokens != 510 {
		t.Fatalf("total tokens = %d, want 510", report.TotalTokens)
	}
	if len(report.PerChannelDay) == 0 || len(report.PerChannelDay) < len(report.Totals) {
		t.Fatalf("per-day buckets = %d, want at least the totals cells", len(report.PerChannelDay))
	}
	if got := ChannelMediaTotals(report.PerChannelDay); len(got) != len(report.Totals) {
		t.Fatalf("ChannelMediaTotals folded %d buckets into %d cells, want %d",
			len(report.PerChannelDay), len(got), len(report.Totals))
	}

	// The days window must exclude older rows.
	if _, err := repository.pool.Exec(ctx, `
		UPDATE inbound_messages SET received_at = now() - interval '30 days'
		WHERE provider_message_id = 'wa-img-ok'`); err != nil {
		t.Fatal(err)
	}
	filtered, err := repository.ChannelMediaStats(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.TotalAttempts != 4 {
		t.Fatalf("7-day attempts = %d, want 4 (30-day-old row excluded)", filtered.TotalAttempts)
	}

	// The web-flow rail must land in the same aggregate (channel "web").
	if err := repository.RecordMediaUsage(ctx, MediaUsageRecord{
		Channel: "", MediaKind: "voice", Success: true, AITokens: 88,
	}); err != nil {
		t.Fatal(err)
	}
	withWeb, err := repository.ChannelMediaStats(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if withWeb.TotalAttempts != 5 {
		t.Fatalf("attempts with web row = %d, want 5", withWeb.TotalAttempts)
	}
	if withWeb.TotalTokens != 388 { // 300 chat + 88 web
		t.Fatalf("tokens with web row = %d, want 388", withWeb.TotalTokens)
	}
	var webCell ChannelMediaStat
	for _, stat := range withWeb.Totals {
		if stat.Channel == "web" {
			webCell = stat
		}
	}
	if webCell.MediaKind != "voice" || webCell.Successes != 1 || webCell.AITokens != 88 {
		t.Fatalf("web cell = %+v, want voice, 1 success, 88 tokens", webCell)
	}

	// A report over 30 days must include it again.
	old, err := repository.ChannelMediaStats(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	if old.TotalAttempts != 6 { // 5 chat + the web rail row
		t.Fatalf("60-day attempts = %d, want 6", old.TotalAttempts)
	}
}
