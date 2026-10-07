package app

// Backup & PITR health panel (pre-deploy resilience prerequisites): ages
// the heartbeats deploy/backup.sh records in backup_runs against the
// configured staleness thresholds so an operator sees "WAL stopped shipping"
// on the monitoring dashboard instead of discovering it at restore time.
//
// buildBackupPanel is deliberately pure: given the store snapshot, a clock
// and the three thresholds it decides every row, so the alert semantics are
// unit-testable without a database. A threshold of zero disables that row's
// alert (it still renders, marked as such), and a missing heartbeat is the
// oldest possible age — always stale whenever its threshold is on.

import (
	"fmt"
	"time"

	"whatsapp-payment-demo/internal/store"
)

// BackupStatRow is one line of the panel: label, humanised age, whether the
// age crossed its threshold, and the threshold for context.
type BackupStatRow struct {
	Label     string
	Age       string
	Threshold string
	Stale     bool
	Disabled  bool
	Detail    string
}

// BackupPanel is the full view model: the three heartbeat rows plus the
// archiver cross-check. ArchiverFailing reflects the *latest* outcome
// (failure newer than success), not merely that a failure once happened.
type BackupPanel struct {
	Rows            []BackupStatRow
	ArchiverAge     string
	ArchiverError   string
	ArchiverUnknown bool
	ArchiverFailing bool
	AnyStale        bool
}

// FormatAge renders a duration the way an operator reads it: minutes under
// an hour, hours under two days, days beyond. Negative ages (clock skew,
// future-stamped rows) clamp to "0m" rather than rendering nonsense.
func FormatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours()/24), int(d.Hours())%24)
	}
}

// FormatThreshold renders a staleness threshold in its natural unit so the
// row reads "age 27h05m · over 26h".
func FormatThreshold(d time.Duration) string {
	switch {
	case d <= 0:
		return "off"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// backupRow ages one heartbeat against its threshold. A nil timestamp means
// no successful run ever happened, which is stale under any enabled
// threshold and "never" as an age.
func backupRow(label string, at *time.Time, threshold time.Duration, now time.Time) BackupStatRow {
	row := BackupStatRow{
		Label:     label,
		Threshold: FormatThreshold(threshold),
		Disabled:  threshold <= 0,
	}
	switch {
	case at == nil:
		row.Age = "never"
		row.Stale = !row.Disabled
	case threshold <= 0:
		row.Age = FormatAge(now.Sub(*at))
	default:
		age := now.Sub(*at)
		row.Age = FormatAge(age)
		row.Stale = age > threshold
	}
	if row.Stale {
		row.Detail = "over " + row.Threshold
	}
	return row
}

// buildBackupPanel assembles the panel from a store snapshot. The three
// thresholds come straight from config (BACKUP_BASE_STALE_AFTER,
// WAL_SHIP_STALE_AFTER, RESTORE_DRILL_STALE_AFTER).
func buildBackupPanel(h store.BackupHealth, now time.Time, baseStale, walStale, drillStale time.Duration) BackupPanel {
	p := BackupPanel{
		Rows: []BackupStatRow{
			backupRow("Base backup", h.LastBaseBackupAt, baseStale, now),
			backupRow("WAL ship (offsite)", h.LastWALShipAt, walStale, now),
			backupRow("PITR restore drill", h.LastRestoreDrillAt, drillStale, now),
		},
	}
	for _, r := range p.Rows {
		if r.Stale {
			p.AnyStale = true
			break
		}
	}

	// Archiver cross-check: unknown when the view is unreadable, failing
	// only when the newest attempt is a failure (failure time after the
	// last success), otherwise informational.
	switch {
	case h.ArchiverUnavailable != "":
		p.ArchiverUnknown = true
		p.ArchiverError = h.ArchiverUnavailable
	case h.LastWALFailedAt != nil &&
		(h.LastArchivedWALAt == nil || h.LastWALFailedAt.After(*h.LastArchivedWALAt)):
		p.ArchiverFailing = true
		p.ArchiverError = "last attempt failed: " + h.LastFailedWAL
	}
	if h.LastArchivedWALAt != nil {
		p.ArchiverAge = FormatAge(now.Sub(*h.LastArchivedWALAt))
	} else {
		p.ArchiverAge = "never"
	}
	return p
}
