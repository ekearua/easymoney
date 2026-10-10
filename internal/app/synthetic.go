package app

// Post-deploy synthetic checks. Run after a rollout (deploy/release.sh invokes
// this) and on demand via `demo synthetic`, they answer "is the deployed
// revision actually doing its job" rather than "is the process up".
//
// Scope is deliberate: every stage is *read-only* over the payments domain. A
// check that fabricated a payment in a live ledger would write fake money into
// a regulated, audited store — an AML problem, not a test — so the payment path
// is exercised the way a customer-visible artefact actually resolves: today's
// real payments must still answer by reference and by receipt token. The
// write-side equivalent (a walled-off sandbox payment) is a separate,
// opt-in capability, not something a smoke test should do silently.
//
// buildSyntheticPanel is pure — given a store summary, a clock and the outbox
// age ceiling it decides every row — so the pass/fail semantics are unit
// testable without a database.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// SyntheticStage is one named step of a run.
type SyntheticStage struct {
	Name     string
	OK       bool
	Skipped  bool
	Detail   string
	Duration time.Duration
}

// SyntheticReport is one completed run: every stage always produces a result
// (a failed stage carries its own error), so a run is never partial.
type SyntheticReport struct {
	RunID  uuid.UUID
	RanAt  time.Time
	OK     bool
	Source string
	Stages []SyntheticStage
}

// SyntheticStageRow is one line of the dashboard panel.
type SyntheticStageRow struct {
	Stage   string
	State   string // "ok" | "failed" | "skipped"
	OK      bool
	Detail  string
	Elapsed string
}

// SyntheticPanel is the dashboard view model: the last run's stages plus the
// trailing-window pass rate.
type SyntheticPanel struct {
	HasRun        bool
	Title         string
	RanAt         string
	LastOK        bool
	Source        string
	Rows          []SyntheticStageRow
	Runs          int
	OKRuns        int
	FailedRuns    int
	FailingStages []string
	Outbox        string
	OutboxStale   bool
}

// syntheticOutboxLabel renders the backlog as an operator reads it.
func syntheticOutboxLabel(backlog store.OutboxBacklog, now time.Time) string {
	if backlog.Pending == 0 {
		return "empty"
	}
	label := fmt.Sprintf("%d queued", backlog.Pending)
	if backlog.OldestPendingAt != nil {
		label += ", oldest " + FormatAge(now.Sub(*backlog.OldestPendingAt))
	}
	return label
}

// buildSyntheticPanel assembles the panel from a store snapshot. maxOutboxAge
// of zero disables the backlog alert (the row still renders).
func buildSyntheticPanel(summary store.SyntheticCheckSummary, backlog store.OutboxBacklog, now time.Time, maxOutboxAge time.Duration) SyntheticPanel {
	panel := SyntheticPanel{
		HasRun:        summary.HasRun,
		Runs:          summary.Runs,
		OKRuns:        summary.OKRuns,
		FailedRuns:    summary.FailedRuns,
		FailingStages: summary.FailingStages,
		LastOK:        summary.LastOK,
		Source:        summary.LastSource,
		Title:         "Synthetic checks — no run recorded yet",
		Outbox:        syntheticOutboxLabel(backlog, now),
	}
	if summary.LastRanAt != nil {
		panel.RanAt = FormatAge(now.Sub(*summary.LastRanAt)) + " ago"
	}
	switch {
	case !summary.HasRun:
		panel.Title = "Synthetic checks — no run recorded yet"
	case summary.LastOK:
		panel.Title = "Synthetic checks — last run passed"
	default:
		panel.Title = "Synthetic checks — last run FAILED (" + joinStages(summary.FailingStages) + ")"
	}
	for _, stage := range summary.LastStages {
		row := SyntheticStageRow{
			Stage:   stage.Stage,
			Detail:  stage.Detail,
			Elapsed: fmt.Sprintf("%dms", stage.DurationMS),
			OK:      stage.OK,
		}
		switch {
		case stage.Skipped:
			row.State = "skipped"
		case stage.OK:
			row.State = "ok"
		default:
			row.State = "failed"
		}
		panel.Rows = append(panel.Rows, row)
	}
	if maxOutboxAge > 0 && backlog.Pending > 0 && backlog.OldestPendingAt != nil {
		panel.OutboxStale = now.Sub(*backlog.OldestPendingAt) > maxOutboxAge
	}
	return panel
}

func joinStages(stages []string) string {
	if len(stages) == 0 {
		return "unknown stage"
	}
	out := stages[0]
	for _, s := range stages[1:] {
		out += ", " + s
	}
	return out
}

// RunSyntheticChecks executes every stage, records each one, and returns the
// report. Recording happens after the stages so that a stage which needs its
// own read does not interleave with the evidence write; if the database is
// unreachable the `db` stage fails and the record write fails with it, which
// the caller reports as an error rather than a silent no-op.
func (a *App) RunSyntheticChecks(ctx context.Context, source string) (SyntheticReport, error) {
	if source == "" {
		source = "cli"
	}
	report := SyntheticReport{RunID: uuid.New(), RanAt: time.Now().UTC(), Source: source}

	report.Stages = append(report.Stages, a.runSyntheticStage(ctx, "db", func(ctx context.Context) (bool, bool, string, error) {
		if err := a.store.Ping(ctx); err != nil {
			return false, false, "", err
		}
		return true, false, "database reachable", nil
	}))

	// `schema` reads the columns the current binary expects across the payment,
	// messaging and data tables, so a revision deployed against a stale schema
	// fails here instead of on the first customer request.
	report.Stages = append(report.Stages, a.runSyntheticStage(ctx, "schema", func(ctx context.Context) (bool, bool, string, error) {
		m, err := a.store.Metrics(ctx)
		if err != nil {
			return false, false, "", err
		}
		return true, false, fmt.Sprintf("%d payments, %d succeeded, %d pending", m.Payments, m.Succeeded, m.Pending), nil
	}))

	report.Stages = append(report.Stages, a.runSyntheticStage(ctx, "payment_path", a.checkPaymentPath))

	report.Stages = append(report.Stages, a.runSyntheticStage(ctx, "outbox", a.checkOutbox))

	report.OK = true
	for _, stage := range report.Stages {
		if !stage.OK && !stage.Skipped {
			report.OK = false
			break
		}
	}
	for _, stage := range report.Stages {
		check := store.SyntheticCheck{
			ID:          uuid.New(),
			RunID:       report.RunID,
			RanAt:       report.RanAt,
			Stage:       stage.Name,
			OK:          stage.OK,
			Skipped:     stage.Skipped,
			Detail:      stage.Detail,
			DurationMS:  int(stage.Duration.Milliseconds()),
			Source:      report.Source,
			Environment: a.cfg.Environment,
		}
		if err := a.store.RecordSyntheticCheck(ctx, check); err != nil {
			return report, fmt.Errorf("record synthetic stage %q: %w", stage.Name, err)
		}
	}
	return report, nil
}

// RunSynthetic executes the checks for the CLI: it logs every stage and returns
// an error when any stage failed, so `demo synthetic` exits non-zero and a
// rollout (or a post-deploy job) can gate on the result.
func (a *App) RunSynthetic(ctx context.Context, source string) error {
	report, err := a.RunSyntheticChecks(ctx, source)
	if err != nil {
		return err
	}
	for _, stage := range report.Stages {
		state := "ok"
		switch {
		case stage.Skipped:
			state = "skipped"
		case !stage.OK:
			state = "FAILED"
		}
		a.logger.InfoContext(ctx, "synthetic stage",
			"stage", stage.Name, "state", state,
			"detail", stage.Detail, "duration_ms", stage.Duration.Milliseconds())
	}
	if !report.OK {
		return fmt.Errorf("synthetic checks failed (run %s)", report.RunID)
	}
	return nil
}

// runSyntheticStage times and normalises one stage. An error is folded into the
// stage's detail so a run always produces a complete, recordable result, and a
// stage can declare itself skipped (neither pass nor fail) when the data it
// needs does not exist yet.
func (a *App) runSyntheticStage(ctx context.Context, name string, fn func(context.Context) (ok, skipped bool, detail string, err error)) SyntheticStage {
	start := time.Now()
	ok, skipped, detail, err := fn(ctx)
	stage := SyntheticStage{Name: name, OK: ok, Skipped: skipped, Detail: detail, Duration: time.Since(start)}
	if err != nil {
		stage.OK = false
		stage.Skipped = false
		stage.Detail = err.Error()
	}
	if stage.Skipped {
		// A skipped stage is neither a pass nor a failure.
		stage.OK = false
	}
	if stage.Duration < 0 {
		stage.Duration = 0
	}
	return stage
}

// checkPaymentPath resolves the newest real payment by reference and by receipt
// token. Both are customer-visible artefacts — the reference is what the rail
// echoes back and the receipt token is what the customer opens — so this covers
// the payment path end to end from the read side without inventing money.
func (a *App) checkPaymentPath(ctx context.Context) (bool, bool, string, error) {
	payments, err := a.store.ListPayments(ctx, 1)
	if err != nil {
		return false, false, "", err
	}
	if len(payments) == 0 {
		return false, true, "no payments yet — reference and receipt lookup not exercised", nil
	}
	payment := payments[0]
	if payment.ProviderReference == "" {
		return false, false, fmt.Sprintf("newest payment %s carries no provider reference", payment.ID), nil
	}
	byRef, err := a.store.PaymentByReference(ctx, payment.ProviderReference)
	if err != nil {
		return false, false, "", fmt.Errorf("reference %s did not resolve: %w", payment.ProviderReference, err)
	}
	if byRef.ID != payment.ID {
		return false, false, fmt.Sprintf("reference %s resolved to %s, expected %s", payment.ProviderReference, byRef.ID, payment.ID), nil
	}
	if payment.ReceiptToken == "" {
		return true, false, fmt.Sprintf("payment %s resolves by reference (no receipt token issued yet)", payment.ID), nil
	}
	byToken, err := a.store.PaymentByReceiptToken(ctx, payment.ReceiptToken)
	if err != nil {
		return false, false, "", fmt.Errorf("receipt token did not resolve: %w", err)
	}
	if byToken.ID != payment.ID {
		return false, false, fmt.Sprintf("receipt token resolved to %s, expected %s", byToken.ID, payment.ID), nil
	}
	return true, false, fmt.Sprintf("payment %s resolves by reference and receipt token", payment.ID), nil
}

// checkOutbox asserts the outbound pipeline is draining: a backlog whose oldest
// row is older than the configured ceiling means messages are queued but no
// worker is moving them, which is invisible to every HTTP probe.
func (a *App) checkOutbox(ctx context.Context) (bool, bool, string, error) {
	backlog, err := a.store.OutboxBacklog(ctx)
	if err != nil {
		return false, false, "", err
	}
	detail := syntheticOutboxLabel(backlog, time.Now())
	if backlog.Pending == 0 || a.cfg.SyntheticOutboxMaxAge <= 0 || backlog.OldestPendingAt == nil {
		return true, false, detail, nil
	}
	if age := time.Since(*backlog.OldestPendingAt); age > a.cfg.SyntheticOutboxMaxAge {
		return false, false, fmt.Sprintf("%s — over the %s ceiling", detail, a.cfg.SyntheticOutboxMaxAge), nil
	}
	return true, false, detail, nil
}
