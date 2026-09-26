package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Recipient-account link edges and ring triage. user_payout_destinations
// rows are the queryable form of the individual-pay and payout rails'
// recipient bank details (web-flow, in-chat FSM and the payments service
// all write the same table at completion time), so an account number that
// appears behind two different users is a linkable shared fact — the
// mule-account pattern that used to hide inside encrypted flow payloads.

// RecipientLinkRow is one shared recipient bank account between two users.
type RecipientLinkRow struct {
	AccountNumber string
	BankCode      string
	SubjectA      string
	SubjectB      string
	NameA, NameB  string
	LastUsedAt    time.Time
	Sightings     int64
}

// MaskedValue masks the account number for admin display.
func (r RecipientLinkRow) MaskedValue() string { return MaskAccount(r.AccountNumber) }

// IdentityLink is the graph-facing union of every link kind.
type IdentityLink struct {
	Kind      string // "device" | "settlement_account" | "channel" | "recipient_account"
	Value     string // raw shared value; mask for display with MaskAccount
	SubjectA  string
	SubjectB  string
	NameA     string
	NameB     string
	LastSeen  time.Time
	Sightings int64
}

// IdentityLinksWithRecipients returns every edge kind in the normalized
// graph form: the three established kinds from IdentityLinks plus
// recipient-account edges from user_payout_destinations.
func (s *Store) IdentityLinksWithRecipients(ctx context.Context, limit int) ([]IdentityLink, error) {
	if limit <= 0 || limit > 500 {
		limit = 300
	}
	rows, err := s.pool.Query(ctx, `
SELECT kind, value, a_id, b_id,
       COALESCE(na.display_name,''), COALESCE(nb.display_name,''),
       last_seen, sightings
FROM (
    SELECT 'recipient_account'::text AS kind,
           a.account_number AS value,
           a.user_id::text AS a_id,
           b.user_id::text AS b_id,
           GREATEST(a.updated_at, b.updated_at) AS last_seen,
           2::bigint AS sightings
    FROM user_payout_destinations a
    JOIN user_payout_destinations b
      ON a.bank_code = b.bank_code
     AND a.account_number = b.account_number
     AND a.user_id < b.user_id
) k
LEFT JOIN users na ON na.id::text = k.a_id
LEFT JOIN users nb ON nb.id::text = k.b_id
ORDER BY last_seen DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]IdentityLink, 0, limit)
	for rows.Next() {
		var l IdentityLink
		if err := rows.Scan(&l.Kind, &l.Value, &l.SubjectA, &l.SubjectB,
			&l.NameA, &l.NameB, &l.LastSeen, &l.Sightings); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The established edge kinds come from IdentityLinks; reuse its rows
	// rather than duplicating that SQL.
	base, err := s.IdentityLinks(ctx, limit)
	if err != nil {
		return nil, err
	}
	for _, r := range base {
		out = append(out, IdentityLink{
			Kind: r.LinkKind, Value: r.LinkValue,
			SubjectA: r.SubjectA, SubjectB: r.SubjectB,
			NameA: r.NameA, NameB: r.NameB,
			LastSeen: r.LastSharedAt, Sightings: r.Sightings,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

// MaskedValue is the display form of the shared fact: account numbers mask
// to their last four digits; fingerprints and channel identifiers are
// already opaque.
func (l IdentityLink) MaskedValue() string {
	if l.Kind == "recipient_account" || l.Kind == "settlement_account" {
		return MaskAccount(l.Value)
	}
	return l.Value
}

// RingMember is one identity in a ring with its current trust posture.
type RingMember struct {
	Subject  string
	Name     string
	Tier     string
	RiskBand string
}

// ScoredRing is a ring with triage signals: a weighted score, the members'
// trust posture, and the alert counts that drive linked-case creation.
type ScoredRing struct {
	Members       []RingMember
	Edges         []IdentityLink
	Kinds         []string
	Score         float64
	ConfirmedHits int64
	OpenAlerts    int64
	// HasCase reports whether a linked network case already covers this
	// member set (so the UI offers resolution instead of duplication).
	HasCase bool
}

// ringWeight turns sightings and recency into a per-edge weight: each
// additional sighting adds weight with diminishing returns, and evidence
// decays with a 30-day half-life (a shared browser from a year ago means
// less than yesterday's). Pure function, unit-tested.
func ringWeight(sightings int64, lastSeen, now time.Time) float64 {
	if sightings < 1 {
		sightings = 1
	}
	sightTerm := 1 + math.Log2(float64(sightings))
	ageDays := now.Sub(lastSeen).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	recency := math.Exp2(-ageDays / 30)
	return sightTerm * recency
}

// ringKindWeight: account-sharing edges are stronger identity facts than a
// shared browser; a channel-identifier collision sits between.
var ringKindWeight = map[string]float64{
	"recipient_account":  1.5,
	"settlement_account": 1.5,
	"channel":            1.25,
	"device":             1.0,
}

// ScoreRing sums the edges' weighted strength by kind and multiplies by the
// alert signal: confirmed alerts multiply hardest, open alerts bump
// modestly. The point is ordering for triage, not an absolute meaning.
func ScoreRing(edges []IdentityLink, confirmedHits, openAlerts int64, now time.Time) float64 {
	byKind := map[string]float64{}
	for _, e := range edges {
		byKind[e.Kind] += ringWeight(e.Sightings, e.LastSeen, now)
	}
	score := 0.0
	for kind, weight := range byKind {
		score += weight * ringKindWeight[kind]
	}
	if confirmedHits > 0 {
		score *= 1 + float64(confirmedHits)
	} else if openAlerts > 0 {
		score *= 1.25
	}
	return score
}

// EnrichRings attaches member trust posture, alert signals and triage
// scores to rings detected over the normalized edge list. Members' tiers
// and bands come from one batched query; alert signals are per-member
// counts summed over the ring.
func (s *Store) EnrichRings(ctx context.Context, rings []Ring, edges []IdentityLink, now time.Time) ([]ScoredRing, error) {
	if len(rings) == 0 {
		return nil, nil
	}
	// Batch-fetch profiles for every member across all rings.
	seen := map[string]bool{}
	var ids []uuid.UUID
	for _, ring := range rings {
		for _, m := range ring.Members {
			if seen[m] {
				continue
			}
			seen[m] = true
			if id, err := uuid.Parse(m); err == nil {
				ids = append(ids, id)
			}
		}
	}
	profiles := map[string]KYCProfile{}
	if len(ids) > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT user_id, tier, COALESCE(risk_band,'low')
			FROM kyc_profiles WHERE user_id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var tier, band string
			if err := rows.Scan(&id, &tier, &band); err != nil {
				return nil, err
			}
			profiles[id.String()] = KYCProfile{UserID: id, Tier: tier, RiskBand: band}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	confirmedByUser, openByUser, err := s.memberAlertSignals(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ScoredRing, 0, len(rings))
	for _, ring := range rings {
		members := make([]RingMember, 0, len(ring.Members))
		var confirmedHits, openAlerts int64
		for _, id := range ring.Members {
			p, ok := profiles[id]
			if !ok {
				p = KYCProfile{Tier: "L0", RiskBand: "unknown"}
			}
			members = append(members, RingMember{Subject: id, Name: ring.Names[id], Tier: p.Tier, RiskBand: p.RiskBand})
			confirmedHits += confirmedByUser[id]
			openAlerts += openByUser[id]
		}
		memberSet := map[string]bool{}
		for _, id := range ring.Members {
			memberSet[id] = true
		}
		var ringEdges []IdentityLink
		for _, e := range edges {
			if memberSet[e.SubjectA] && memberSet[e.SubjectB] {
				ringEdges = append(ringEdges, e)
			}
		}
		kinds := map[string]bool{}
		for _, e := range ringEdges {
			kinds[e.Kind] = true
		}
		kindList := make([]string, 0, len(kinds))
		for k := range kinds {
			kindList = append(kindList, k)
		}
		sort.Strings(kindList)
		out = append(out, ScoredRing{
			Members: members, Edges: ringEdges, Kinds: kindList,
			Score:         ScoreRing(ringEdges, confirmedHits, openAlerts, now),
			ConfirmedHits: confirmedHits, OpenAlerts: openAlerts,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// RefreshNetworkCases is the one ring-case pipeline, shared by the alert
// resolution path and the network page: re-reads the live link graph,
// re-detects rings, enriches them, and upserts network cases for rings with
// at least minConfirmed confirmed member alerts. Running it after every
// confirmation means a case is raised the moment evidence exists, not the
// next time someone opens /admin/network. Returns the upsert count.
func (s *Store) RefreshNetworkCases(ctx context.Context, minConfirmed int) (int, error) {
	links, err := s.IdentityLinksWithRecipients(ctx, 300)
	if err != nil {
		return 0, err
	}
	rings := DetectRingsFromIdentityLinks(links, 2)
	scored, err := s.EnrichRings(ctx, rings, links, time.Now())
	if err != nil {
		return 0, err
	}
	return s.EnsureNetworkCases(ctx, scored, minConfirmed)
}

// memberAlertSignals returns, per user id, the count of confirmed alert
// outcomes and the count of currently-open alerts.
func (s *Store) memberAlertSignals(ctx context.Context) (map[string]int64, map[string]int64, error) {
	confirmed := map[string]int64{}
	open := map[string]int64{}
	rows, err := s.pool.Query(ctx, `
		SELECT t.user_id::text,
		       count(*) FILTER (WHERE t.status='open'),
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM transaction_alert_outcomes o
		           WHERE o.alert_id = t.id AND o.outcome = 'confirmed'))
		FROM transaction_alerts t
		GROUP BY t.user_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var o, c int64
		if err := rows.Scan(&id, &o, &c); err != nil {
			return nil, nil, err
		}
		confirmed[id] = c
		open[id] = o
	}
	return confirmed, open, rows.Err()
}

// MembersKey is a stable key for a ring's member set (sorted ids joined),
// used to key network cases to their member sets.
func (r Ring) MembersKey() string {
	return strings.Join(r.Members, ",")
}

// MembersKeyOf is the scored-ring form of MembersKey (sorted member ids).
func (r ScoredRing) MembersKeyOf() string {
	ids := make([]string, 0, len(r.Members))
	for _, m := range r.Members {
		ids = append(ids, m.Subject)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// DetectRingsFromIdentityLinks runs ring detection over the normalized
// edge form (IdentityLink), reusing DetectRings' union-find.
func DetectRingsFromIdentityLinks(links []IdentityLink, minMembers int) []Ring {
	rows := make([]IdentityLinkRow, 0, len(links))
	for _, l := range links {
		rows = append(rows, IdentityLinkRow{
			LinkKind: l.Kind, LinkValue: l.Value,
			SubjectA: l.SubjectA, SubjectB: l.SubjectB,
			NameA: l.NameA, NameB: l.NameB,
			LastSharedAt: l.LastSeen, Sightings: l.Sightings,
		})
	}
	return DetectRings(rows, minMembers)
}

// NetworkCase is one linked-case covering a whole ring: raised when a ring
// has a confirmed alert outcome among its members, resolved once for the
// group. members_key (the sorted member set) is unique, so re-detection
// updates the existing case instead of duplicating it.
type NetworkCase struct {
	ID              int64
	MembersKey      string
	MemberCount     int
	Members         []string
	LinkKinds       []string
	Score           float64
	ConfirmedAlerts int
	Status          string // "open" | "resolved" | "false_positive"
	ReviewerEmail   string
	OutcomeNote     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EnsureNetworkCases upserts one network case per ring that has at least
// minConfirmed confirmed alert outcomes among its members (the linked-case
// rule: one disposition flow per ring, not per alert). Existing cases for
// the same member set are refreshed with the latest score and alert count;
// a case previously resolved is reopened when new confirmed evidence
// arrives. Returns the number of cases created or updated.
func (s *Store) EnsureNetworkCases(ctx context.Context, rings []ScoredRing, minConfirmed int) (int, error) {
	if minConfirmed < 1 {
		minConfirmed = 1
	}
	n := 0
	for _, ring := range rings {
		if ring.ConfirmedHits < int64(minConfirmed) {
			continue
		}
		key := strings.Join(memberSubjects(ring), ",")
		membersJSON, err := json.Marshal(memberSubjects(ring))
		if err != nil {
			return n, err
		}
		kindsJSON, err := json.Marshal(ring.Kinds)
		if err != nil {
			return n, err
		}
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO network_cases
			    (members_key, member_count, members, link_kinds, score, confirmed_alerts, status)
			VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6, 'open')
			ON CONFLICT (members_key) DO UPDATE SET
			    score = EXCLUDED.score,
			    confirmed_alerts = EXCLUDED.confirmed_alerts,
			    link_kinds = EXCLUDED.link_kinds,
			    -- fresh confirmed evidence reopens a closed case
			    status = CASE WHEN network_cases.status <> 'open' AND EXCLUDED.confirmed_alerts > network_cases.confirmed_alerts
			                  THEN 'open' ELSE network_cases.status END,
			    updated_at = now()
			WHERE EXCLUDED.confirmed_alerts > network_cases.confirmed_alerts
			   OR EXCLUDED.score > network_cases.score
			   OR network_cases.status = 'open'`,
			key, len(ring.Members), string(membersJSON), string(kindsJSON),
			ring.Score, ring.ConfirmedHits)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() > 0 {
			n++
		}
	}
	return n, nil
}

// memberSubjects extracts a ring's member ids as strings.
func memberSubjects(r ScoredRing) []string {
	out := make([]string, 0, len(r.Members))
	for _, m := range r.Members {
		out = append(out, m.Subject)
	}
	return out
}

// ListNetworkCases lists linked cases, open first then newest.
func (s *Store) ListNetworkCases(ctx context.Context, limit int) ([]NetworkCase, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, members_key, member_count, members::text, link_kinds::text,
		       score, confirmed_alerts, status, reviewer_email, outcome_note,
		       created_at, updated_at
		FROM network_cases
		ORDER BY (status = 'open') DESC, updated_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkCase
	for rows.Next() {
		var c NetworkCase
		var membersRaw, kindsRaw string
		if err := rows.Scan(&c.ID, &c.MembersKey, &c.MemberCount, &membersRaw, &kindsRaw,
			&c.Score, &c.ConfirmedAlerts, &c.Status, &c.ReviewerEmail, &c.OutcomeNote,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(membersRaw), &c.Members)
		_ = json.Unmarshal([]byte(kindsRaw), &c.LinkKinds)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveNetworkCase moves a linked case to a terminal status with the
// reviewer's note; the whole ring's disposition is recorded once.
func (s *Store) ResolveNetworkCase(ctx context.Context, caseID int64, status, reviewerEmail, note string) (NetworkCase, error) {
	valid := map[string]bool{"resolved": true, "false_positive": true}
	if !valid[status] {
		return NetworkCase{}, fmt.Errorf("invalid network case status %q", status)
	}
	var c NetworkCase
	var membersRaw, kindsRaw string
	err := s.pool.QueryRow(ctx, `
		UPDATE network_cases
		SET status=$2, reviewer_email=$3, outcome_note=$4, updated_at=now()
		WHERE id=$1
		RETURNING id, members_key, member_count, members::text, link_kinds::text,
		          score, confirmed_alerts, status, reviewer_email, outcome_note,
		          created_at, updated_at`,
		caseID, status, reviewerEmail, note).Scan(
		&c.ID, &c.MembersKey, &c.MemberCount, &membersRaw, &kindsRaw,
		&c.Score, &c.ConfirmedAlerts, &c.Status, &c.ReviewerEmail, &c.OutcomeNote,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return NetworkCase{}, fmt.Errorf("resolve network case: %w", err)
	}
	_ = json.Unmarshal([]byte(membersRaw), &c.Members)
	_ = json.Unmarshal([]byte(kindsRaw), &c.LinkKinds)
	return c, nil
}
