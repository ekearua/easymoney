package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// WebFlow is one browser flow session minted from a chat conversation. The
// token is an opaque capability (like checkout tokens): possession of the
// link is the user's phone number, so no separate web login is needed.
type WebFlow struct {
	ID          uuid.UUID
	Token       string
	UserID      uuid.UUID
	Channel     string
	FlowType    string
	Payload     map[string]string
	Step        string
	Status      string // open | complete | expired
	CreatedAt   time.Time
	ExpiresAt   time.Time
	CompletedAt *time.Time
}

// WebFlow statuses.
const (
	WebFlowOpen      = "open"
	WebFlowComplete  = "complete"
	WebFlowExpired   = "expired"
	WebFlowCancelled = "cancelled"
)

// MintWebFlow creates a new open web flow with the given initial payload and
// step. Token collisions are retried by the caller via MintWebFlow's
// uniqueness constraint being surfaced as an error; production callers use
// token reuse (see OpenWebFlowForUser).
func (s *Store) MintWebFlow(ctx context.Context, userID uuid.UUID, channel, flowType string, payload map[string]string, step string, ttl time.Duration) (WebFlow, error) {
	token, err := domain.NewCheckoutToken()
	if err != nil {
		return WebFlow{}, err
	}
	sealed, paymentID, err := s.sealWebFlowPayload(payload)
	if err != nil {
		return WebFlow{}, err
	}
	flow := WebFlow{
		ID: uuid.New(), Token: token, UserID: userID, Channel: channel,
		FlowType: flowType, Step: step, Status: WebFlowOpen,
		ExpiresAt: time.Now().Add(ttl),
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO web_flows (id, token, user_id, channel, flow_type, payload, payment_id, step, status, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		flow.ID, flow.Token, userID, channel, flowType, sealed, paymentID, step, WebFlowOpen, flow.ExpiresAt)
	if err != nil {
		return WebFlow{}, fmt.Errorf("mint web flow: %w", err)
	}
	return flow, nil
}

// sealWebFlowPayload marshals the flow payload, seals it at rest, and returns
// the stored text plus any payment_id carried in the payload (promoted to its
// own indexable column so gateway callbacks can resolve the flow without
// decrypting every open flow).
func (s *Store) sealWebFlowPayload(payload map[string]string) (string, *uuid.UUID, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	sealed, err := s.sealValue(raw)
	if err != nil {
		return "", nil, err
	}
	var paymentID *uuid.UUID
	if rawID := strings.TrimSpace(payload["payment_id"]); rawID != "" {
		if id, err := uuid.Parse(rawID); err == nil {
			paymentID = &id
		}
	}
	return sealed, paymentID, nil
}

// OpenWebFlowForUser returns an unexpired open flow of the given type for the
// user, if one exists, so repeated chat intents reuse the same link instead
// of spamming new messages.
func (s *Store) OpenWebFlowForUser(ctx context.Context, userID uuid.UUID, flowType string) (WebFlow, bool, error) {
	flow, err := s.webFlowByQuery(ctx, `
		SELECT id, token, user_id, channel, flow_type, payload, step, status, created_at, expires_at, completed_at
		FROM web_flows
		WHERE user_id=$1 AND flow_type=$2 AND status='open' AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1`, userID, flowType)
	if err != nil {
		if err == pgx.ErrNoRows {
			return WebFlow{}, false, nil
		}
		return WebFlow{}, false, err
	}
	return flow, true, nil
}

// WebFlowByToken resolves a flow by its capability token. Open flows past
// their expiry are lazily marked expired so the page can show a useful state.
func (s *Store) WebFlowByToken(ctx context.Context, token string) (WebFlow, error) {
	flow, err := s.webFlowByQuery(ctx, `
		SELECT id, token, user_id, channel, flow_type, payload, step, status, created_at, expires_at, completed_at
		FROM web_flows WHERE token=$1`, token)
	if err != nil {
		return WebFlow{}, err
	}
	if flow.Status == WebFlowOpen && time.Now().After(flow.ExpiresAt) {
		if _, err := s.pool.Exec(ctx, `UPDATE web_flows SET status=$1 WHERE id=$2 AND status='open'`, WebFlowExpired, flow.ID); err == nil {
			flow.Status = WebFlowExpired
		}
	}
	return flow, nil
}

func (s *Store) webFlowByQuery(ctx context.Context, query string, args ...any) (WebFlow, error) {
	var flow WebFlow
	var raw string
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&flow.ID, &flow.Token, &flow.UserID, &flow.Channel, &flow.FlowType,
		&raw, &flow.Step, &flow.Status, &flow.CreatedAt, &flow.ExpiresAt, &flow.CompletedAt)
	if err != nil {
		return WebFlow{}, err
	}
	flow.Payload = map[string]string{}
	plain, err := s.openValue(raw)
	if err != nil {
		return WebFlow{}, err
	}
	if len(plain) > 0 {
		_ = json.Unmarshal(plain, &flow.Payload)
	}
	return flow, nil
}

// OpenWebFlowByPayment finds the open web flow whose payload references the
// payment (payment_id column). Gateway callbacks use it to complete the flow
// and send the confirmation message once a payment verifies. The payment_id
// is promoted to a plaintext column so this lookup stays indexable even
// though the payload blob itself is sealed at rest.
func (s *Store) OpenWebFlowByPayment(ctx context.Context, paymentID uuid.UUID) (WebFlow, error) {
	flow, err := s.webFlowByQuery(ctx, `
		SELECT id, token, user_id, channel, flow_type, payload, step, status, created_at, expires_at, completed_at
		FROM web_flows
		WHERE status='open' AND expires_at > now() AND payment_id = $1
		LIMIT 1`, paymentID)
	if err != nil {
		return WebFlow{}, err
	}
	return flow, nil
}

// SaveWebFlowProgress persists a step and payload change for an open flow.
// The payload is sealed at rest, and any payment_id it carries is promoted to
// the indexable payment_id column for gateway lookups.
func (s *Store) SaveWebFlowProgress(ctx context.Context, token, step string, payload map[string]string) error {
	sealed, paymentID, err := s.sealWebFlowPayload(payload)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE web_flows SET step=$2, payload=$3, payment_id=COALESCE($4, payment_id) WHERE token=$1 AND status='open'`, token, step, sealed, paymentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web flow %q is not open", token)
	}
	return nil
}

// AbandonWebFlow cancels an open browser flow from the chat side (the CANCEL
// interrupt): the link stops working, a fresh flow mints cleanly, and the
// flow is recorded as cancelled so admin flow counts can audit chat cancels.
// It is best-effort and idempotent — a flow already completed or expired is a
// no-op, and the chat session is reset regardless of the outcome.
func (s *Store) AbandonWebFlow(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `UPDATE web_flows SET status='cancelled' WHERE token=$1 AND status='open'`, token)
	return err
}

// ReopenWebFlowForRetry rewinds an open money flow that was parked on its
// gateway "checkout"/"done" step back to the given step (normally "review",
// where the payment-method options render) after a gateway attempt did not
// succeed — the customer cancelled on the hosted Interswitch page, or the
// card was declined. The payment association is cleared so the abandoned
// attempt can neither claim this flow later nor be looked up by payment id,
// and the abandoned attempt is marked superseded (superseded_at): if it later
// verifies as succeeded the app auto-refunds it, so a slow gateway callback
// can never double-charge a customer who retried and paid again. The reopen
// only acts when the flow is open and still references exactly that payment:
// a stale webhook for an old attempt can never rewind a flow a newer attempt
// has moved forward. Returns the reopened flow with its payload decrypted.
func (s *Store) ReopenWebFlowForRetry(ctx context.Context, token string, paymentID uuid.UUID, step string, payload map[string]string) (WebFlow, error) {
	sealed, _, err := s.sealWebFlowPayload(payload)
	if err != nil {
		return WebFlow{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WebFlow{}, err
	}
	defer tx.Rollback(ctx)
	var flow WebFlow
	var raw string
	err = tx.QueryRow(ctx, `
		UPDATE web_flows SET step=$2, payload=$3, payment_id=NULL
		WHERE token=$1 AND status='open' AND payment_id=$4
		RETURNING id, token, user_id, channel, flow_type, payload, step, status, created_at, expires_at, completed_at`, token, step, sealed, paymentID).
		Scan(&flow.ID, &flow.Token, &flow.UserID, &flow.Channel, &flow.FlowType,
			&raw, &flow.Step, &flow.Status, &flow.CreatedAt, &flow.ExpiresAt, &flow.CompletedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return WebFlow{}, fmt.Errorf("web flow %q is not open on payment %s", token, paymentID)
		}
		return WebFlow{}, err
	}
	// Mark the abandoned attempt superseded so its late success is auto-refunded
	// by the app instead of silently double-charging the customer. Setting it is
	// idempotent per payment and atomic with the guarded reopen above.
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET superseded_at=now()
		WHERE id=$1 AND superseded_at IS NULL`, paymentID); err != nil {
		return WebFlow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WebFlow{}, err
	}
	flow.Payload = map[string]string{}
	plain, err := s.openValue(raw)
	if err != nil {
		return WebFlow{}, err
	}
	if len(plain) > 0 {
		_ = json.Unmarshal(plain, &flow.Payload)
	}
	return flow, nil
}

// WebFlowAttemptSuperseded reports whether the payment belonged to a web-flow
// attempt that was abandoned at the gateway and later reopened for retry. Such
// an attempt must be auto-refunded the moment it unexpectedly succeeds.
func (s *Store) WebFlowAttemptSuperseded(ctx context.Context, paymentID uuid.UUID) (bool, error) {
	var superseded bool
	err := s.pool.QueryRow(ctx, `
		SELECT superseded_at IS NOT NULL FROM payments WHERE id=$1`, paymentID).Scan(&superseded)
	if err != nil {
		return false, err
	}
	return superseded, nil
}

// CompleteWebFlow atomically claims an open flow (open -> complete) and
// reports whether this call performed the transition. Only the claimer sends
// the confirmation message, so double submissions can never double-notify.
func (s *Store) CompleteWebFlow(ctx context.Context, token string) (WebFlow, bool, error) {
	var flow WebFlow
	var raw string
	err := s.pool.QueryRow(ctx, `
		UPDATE web_flows SET status='complete', completed_at=now()
		WHERE token=$1 AND status='open'
		RETURNING id, token, user_id, channel, flow_type, payload, step, status, created_at, expires_at, completed_at`, token).
		Scan(&flow.ID, &flow.Token, &flow.UserID, &flow.Channel, &flow.FlowType,
			&raw, &flow.Step, &flow.Status, &flow.CreatedAt, &flow.ExpiresAt, &flow.CompletedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Already complete or missing: resolve the current state so the
			// caller can render the terminal page without double-notifying.
			existing, lookupErr := s.WebFlowByToken(ctx, token)
			if lookupErr != nil {
				return WebFlow{}, false, lookupErr
			}
			return existing, false, nil
		}
		return WebFlow{}, false, err
	}
	flow.Payload = map[string]string{}
	plain, err := s.openValue(raw)
	if err != nil {
		return WebFlow{}, false, err
	}
	if len(plain) > 0 {
		_ = json.Unmarshal(plain, &flow.Payload)
	}
	return flow, true, nil
}

// UserContactForChannel returns the outbound recipient address for a user on
// the given messenger channel (normalized WhatsApp number or Telegram chat id).
func (s *Store) UserContactForChannel(ctx context.Context, userID uuid.UUID, channel string) (string, error) {
	switch channel {
	case "telegram":
		var chatID string
		err := s.pool.QueryRow(ctx, `SELECT COALESCE(telegram_chat_id,'') FROM users WHERE id=$1`, userID).Scan(&chatID)
		return chatID, err
	case "instagram":
		var igsid string
		err := s.pool.QueryRow(ctx, `SELECT COALESCE(instagram_igsid,'') FROM users WHERE id=$1`, userID).Scan(&igsid)
		return igsid, err
	case "tiktok":
		var openID string
		err := s.pool.QueryRow(ctx, `SELECT COALESCE(tiktok_open_id,'') FROM users WHERE id=$1`, userID).Scan(&openID)
		return openID, err
	}
	var number string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(whatsapp_number,'') FROM users WHERE id=$1`, userID).Scan(&number)
	if err != nil {
		return "", err
	}
	return normalizePhoneForStore(number), nil
}

func normalizePhoneForStore(value string) string {
	if value == "" {
		return value
	}
	if value[0] != '+' {
		return "+" + value
	}
	return value
}

// MessageLogEntry is one recorded outbound customer-facing message.
type MessageLogEntry struct {
	Channel     string
	Recipient   string
	Flow        string
	MessageType string
}

// RecordMessage writes one outbound message to the message cost meter.
func (s *Store) RecordMessage(ctx context.Context, entry MessageLogEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO message_log (channel, recipient, flow, message_type)
		VALUES ($1,$2,$3,$4)`,
		entry.Channel, entry.Recipient, entry.Flow, entry.MessageType)
	return err
}

// MediaUsageRecord is one OCR/STT extraction outcome to tally in the
// channel media report.
type MediaUsageRecord struct {
	Channel   string
	MediaKind string // image | voice
	Success   bool
	AITokens  int64
}

// RecordMediaUsage tallies one web-flow media extraction into the
// media_usage_log table the channel media report unions with the chat-side
// inbound_messages outcomes. Rows carry the kind and the AI provider's
// reported token usage when known.
func (s *Store) RecordMediaUsage(ctx context.Context, rec MediaUsageRecord) error {
	if rec.Channel == "" {
		rec.Channel = "web"
	}
	if rec.MediaKind != "voice" {
		rec.MediaKind = "image"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO media_usage_log (channel, media_kind, success, ai_tokens)
		VALUES ($1,$2,$3,$4)`, rec.Channel, rec.MediaKind, rec.Success, rec.AITokens)
	return err
}

// RecordChatMediaOutcome stamps the OCR/STT outcome of one chat media message
// onto its inbound_messages row so the channel media report can separate
// successes from failures without reading the (encrypted) payload. A nil
// success means the message was never extracted (AI disabled, unsupported
// type). best-effort errors are returned for the worker to log.
func (s *Store) RecordChatMediaOutcome(ctx context.Context, mediaID string, success bool, aiTokens int64) error {
	if mediaID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE inbound_messages SET media_ok = $2, media_ai_tokens = $3
		WHERE provider_message_id = $1`, mediaID, success, aiTokens)
	return err
}

// ChannelMediaStat is one channel × media-kind cell of the extraction report.
type ChannelMediaStat struct {
	Channel   string
	MediaKind string // image | voice
	Attempts  int64  // every inbound media message of that kind
	Successes int64  // OCR/STT returned non-empty text
	Failures  int64  // provider errors and empty extractions
	AITokens  int64  // reported token usage from the AI provider (0 when unknown)
}

// SuccessRate returns the extraction success ratio (0 when no attempts).
func (m ChannelMediaStat) SuccessRate() float64 {
	if m.Attempts == 0 {
		return 0
	}
	return float64(m.Successes) / float64(m.Attempts)
}

// SuccessRatePct renders the success rate as a whole-percent string.
func (m ChannelMediaStat) SuccessRatePct() string {
	return fmt.Sprintf("%.0f%%", m.SuccessRate()*100)
}

// ChannelMediaReport aggregates channel media extraction over the last days.
type ChannelMediaReport struct {
	Since         time.Time
	PerChannelDay []ChannelMediaDayStat
	Totals        []ChannelMediaStat
	TotalAttempts int64
	TotalTokens   int64
}

// ChannelMediaDayStat is one channel × media-kind × calendar-day bucket.
type ChannelMediaDayStat struct {
	Day       time.Time
	Channel   string
	MediaKind string
	Attempts  int64
	Successes int64
	Failures  int64
	AITokens  int64
}

// ChannelMediaStats reports OCR/STT extraction outcomes per chat channel over
// the last days window. Attempts count every inbound image/photo/document or
// audio/voice message; successes are extractions that yielded non-empty text;
// failures are provider errors and empty results. AI token usage is reported
// by the AI provider via RecordMediaAIUsage; channels without a reporting
// provider show 0.
func (s *Store) ChannelMediaStats(ctx context.Context, days int) (ChannelMediaReport, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	report := ChannelMediaReport{Since: since}
	rows, err := s.pool.Query(ctx, `
		SELECT day, channel, media_kind, attempts, successes, failures, ai_tokens
		FROM (
		    SELECT date_trunc('day', received_at) AS day, channel,
		           CASE WHEN media_type IN ('image','photo','document') THEN 'image'
		                WHEN media_type IN ('audio','voice') THEN 'voice'
		                ELSE 'other' END AS media_kind,
		           count(*) AS attempts,
		           count(*) FILTER (WHERE status = 'processed' AND media_ok) AS successes,
		           count(*) FILTER (WHERE status = 'processed' AND media_ok = false) AS failures,
		           sum(media_ai_tokens)::bigint AS ai_tokens
		    FROM inbound_messages
		    WHERE received_at >= $1 AND media_type <> ''
		    GROUP BY day, channel, media_kind
		    UNION ALL
		    SELECT date_trunc('day', created_at) AS day, channel, media_kind,
		           count(*) AS attempts,
		           count(*) FILTER (WHERE success) AS successes,
		           count(*) FILTER (WHERE NOT success) AS failures,
		           sum(ai_tokens)::bigint AS ai_tokens
		    FROM media_usage_log
		    WHERE created_at >= $1
		    GROUP BY day, channel, media_kind
		) merged
		ORDER BY day DESC, channel, media_kind`, since)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var stat ChannelMediaDayStat
		if err := rows.Scan(&stat.Day, &stat.Channel, &stat.MediaKind, &stat.Attempts, &stat.Successes, &stat.Failures, &stat.AITokens); err != nil {
			return report, err
		}
		report.PerChannelDay = append(report.PerChannelDay, stat)
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	report.Totals = ChannelMediaTotals(report.PerChannelDay)
	for _, stat := range report.Totals {
		report.TotalAttempts += stat.Attempts
		report.TotalTokens += stat.AITokens
	}
	return report, nil
}

// TokenDayStat is one calendar-day slice of the AI token spend, so the admin
// media report can chart extraction cost over the window.
type TokenDayStat struct {
	Day    time.Time
	Tokens int64
}

// DailyTokens buckets total reported AI token usage per calendar day over the
// window, merging both extraction rails (chat inbound_messages and the
// web-flow media_usage_log) and filling gaps with zero-token days so the
// trend chart shows a continuous timeline.
func (s *Store) DailyTokens(ctx context.Context, days int) ([]TokenDayStat, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT d::date AS day, COALESCE(sum(t.tokens),0)::bigint AS tokens
		FROM generate_series(date_trunc('day', $1::timestamptz), date_trunc('day', now()), interval '1 day') d
		LEFT JOIN (
		    SELECT received_at AS at, sum(media_ai_tokens) AS tokens
		    FROM inbound_messages
		    WHERE received_at >= $1 AND media_type <> ''
		    GROUP BY received_at
		    UNION ALL
		    SELECT created_at AS at, sum(ai_tokens)
		    FROM media_usage_log
		    WHERE created_at >= $1
		    GROUP BY created_at
		) t ON date_trunc('day', t.at) = d
		GROUP BY d ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenDayStat
	for rows.Next() {
		var stat TokenDayStat
		if err := rows.Scan(&stat.Day, &stat.Tokens); err != nil {
			return nil, err
		}
		out = append(out, stat)
	}
	return out, rows.Err()
}

// ChannelMediaTotals folds per-day buckets into per channel × media-kind totals.
func ChannelMediaTotals(days []ChannelMediaDayStat) []ChannelMediaStat {
	order := []string{}
	byKey := map[string]*ChannelMediaStat{}
	for _, day := range days {
		k := day.Channel + "|" + day.MediaKind
		stat, ok := byKey[k]
		if !ok {
			stat = &ChannelMediaStat{Channel: day.Channel, MediaKind: day.MediaKind}
			byKey[k] = stat
			order = append(order, k)
		}
		stat.Attempts += day.Attempts
		stat.Successes += day.Successes
		stat.Failures += day.Failures
		stat.AITokens += day.AITokens
	}
	totals := make([]ChannelMediaStat, 0, len(order))
	for _, k := range order {
		totals = append(totals, *byKey[k])
	}
	return totals
}

// MessageFlowStat aggregates message_log rows per flow over a window, with
// the number of completed web flows in that flow as the transaction count.
type MessageFlowStat struct {
	Flow             string
	Channel          string
	Messages         int64
	MessageTypes     map[string]int64
	Transactions     int64
	AveragePerTxn    float64
	EstimatedCostNGN int64
}

// MessageLogSummary is the shape returned by the admin messaging view.
type MessageLogSummary struct {
	Since              time.Time
	TotalMessages      int64
	ServiceCostNGN     int64
	MarketingCostNGN   int64
	EstimatedCostNGN   int64
	EstimatedCostUSDC  int64
	PerFlow            []MessageFlowStat
	PerChannel         map[string]int64
	MarketingMessages  int64
	ServiceMessages    int64
	TotalTransactions  int64
	AveragePerTxn      float64
	MessageCostService int64 // naira per service message (echo of config)
}

// MessageStats reports message volumes and per-flow averages over the last
// days window. Transaction counts come from completed web flows, which are
// the transactional flows this metering is designed around.
func (s *Store) MessageStats(ctx context.Context, days int, serviceCostNGN, marketingCostNGN int64) (MessageLogSummary, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	summary := MessageLogSummary{
		Since:              since,
		MessageCostService: serviceCostNGN,
	}
	rows, err := s.pool.Query(ctx, `
		SELECT channel, flow, message_type, count(*)
		FROM message_log WHERE created_at >= $1
		GROUP BY channel, flow, message_type`, since)
	if err != nil {
		return summary, err
	}
	type key struct{ channel, flow string }
	byFlow := map[key]*MessageFlowStat{}
	perChannel := map[string]int64{}
	for rows.Next() {
		var channel, flow, msgType string
		var count int64
		if err := rows.Scan(&channel, &flow, &msgType, &count); err != nil {
			rows.Close()
			return summary, err
		}
		k := key{channel, flow}
		st, ok := byFlow[k]
		if !ok {
			st = &MessageFlowStat{Flow: flow, Channel: channel, MessageTypes: map[string]int64{}}
			byFlow[k] = st
		}
		st.Messages += count
		st.MessageTypes[msgType] += count
		perChannel[channel] += count
		summary.TotalMessages += count
		if msgType == "marketing" {
			summary.MarketingMessages += count
		} else {
			summary.ServiceMessages += count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return summary, err
	}
	// Transaction counts per flow from completed web flows in the window.
	txRows, err := s.pool.Query(ctx, `
		SELECT flow_type, channel, count(*) FROM web_flows
		WHERE status='complete' AND completed_at >= $1
		GROUP BY flow_type, channel`, since)
	if err != nil {
		return summary, err
	}
	for txRows.Next() {
		var flow, channel string
		var count int64
		if err := txRows.Scan(&flow, &channel, &count); err != nil {
			txRows.Close()
			return summary, err
		}
		if st, ok := byFlow[key{channel, flow}]; ok {
			st.Transactions = count
			summary.TotalTransactions += count
		}
	}
	txRows.Close()
	if err := txRows.Err(); err != nil {
		return summary, err
	}
	summary.PerFlow = make([]MessageFlowStat, 0, len(byFlow))
	for k, st := range byFlow {
		if st.Transactions > 0 {
			st.AveragePerTxn = float64(st.Messages) / float64(st.Transactions)
		}
		st.EstimatedCostNGN = serviceMessagesNGN(st, serviceCostNGN, marketingCostNGN)
		summary.EstimatedCostNGN += st.EstimatedCostNGN
		summary.PerFlow = append(summary.PerFlow, MessageFlowStat{
			Flow: k.flow, Channel: k.channel, Messages: st.Messages,
			MessageTypes: st.MessageTypes, Transactions: st.Transactions,
			AveragePerTxn: st.AveragePerTxn, EstimatedCostNGN: st.EstimatedCostNGN,
		})
	}
	summary.EstimatedCostUSDC = summary.EstimatedCostNGN * 100 / 1340 // ₦1340/USD → US cents
	summary.ServiceCostNGN = summary.ServiceMessages * serviceCostNGN
	summary.MarketingCostNGN = summary.MarketingMessages * marketingCostNGN
	if summary.TotalTransactions > 0 {
		summary.AveragePerTxn = float64(summary.TotalMessages) / float64(summary.TotalTransactions)
	}
	return summary, nil
}

func serviceMessagesNGN(st *MessageFlowStat, serviceCostNGN, marketingCostNGN int64) int64 {
	var cost int64
	for t, n := range st.MessageTypes {
		if t == "marketing" {
			cost += n * marketingCostNGN
		} else {
			cost += n * serviceCostNGN
		}
	}
	return cost
}
