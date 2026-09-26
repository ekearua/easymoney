package store

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Trust Fabric network layer: entity-link analysis over the edges the
// platform actually stores. Nodes are identities (users and merchants);
// edges are shared facts:
//
//   - shared trusted device (two subjects signing in or checking out from
//     the same browser fingerprint)
//   - shared merchant settlement bank account (via merchant ownership)
//   - the same channel identifier value appearing under two different
//     identifier columns (whatsapp, telegram, instagram, tiktok) — the
//     columns are UNIQUE individually, so only cross-column reuse can
//     produce an edge, and that reuse is exactly what an investigator
//     wants to see (identifier recycling or account takeover).
//
// A ring is a connected component over these edges with two or more
// distinct identities — the fraud-shaped pattern: mule clusters reusing
// one device or one bank account across several profiles.

// IdentityLinkRow is one shared-fact edge between two identities.
type IdentityLinkRow struct {
	// LinkKind is "device", "settlement_account", or "channel".
	LinkKind string
	// LinkValue is the fingerprint, the bank account number, or the channel
	// identifier the two sides share. Device fingerprints are opaque hashes;
	// account numbers are masked for display with MaskAccount.
	LinkValue string
	// SubjectA / SubjectB are the two linked identities (user ids as text; a
	// settlement edge carries the owning users behind the two merchants).
	SubjectA, SubjectB string
	// NameA / NameB are display names resolved for the subjects.
	NameA, NameB string
	// LastSharedAt is the most recent sighting of the shared fact.
	LastSharedAt time.Time
	// Sightings counts observations of the shared fact (2 = one shared
	// device row on each side; grows with repeated device sightings).
	Sightings int64
}

// MaskedValue is the display form of the shared fact: bank account numbers
// mask to their last four digits; fingerprints and channel identifiers are
// already opaque. A method (not a template func) so admin views need no
// extra funcmap entries.
func (r IdentityLinkRow) MaskedValue() string {
	if r.LinkKind == "settlement_account" {
		return MaskAccount(r.LinkValue)
	}
	return r.LinkValue
}

// IdentityLinks returns the shared-fact edges, newest evidence first. Rings
// are derived by grouping these edges into components (DetectRings) — the
// edge set is small, one row per shared device/account/identifier pair.
func (s *Store) IdentityLinks(ctx context.Context, limit int) ([]IdentityLinkRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
WITH device_edges AS (
    SELECT 'device'::text AS kind,
           a.device_fingerprint AS value,
           a.user_id::text AS a_id,
           b.user_id::text AS b_id,
           GREATEST(a.last_seen_at, b.last_seen_at) AS last_seen,
           (a.last_seen_at IS NOT NULL)::int + (b.last_seen_at IS NOT NULL)::int AS sightings
    FROM trusted_devices a
    JOIN trusted_devices b
      ON a.device_fingerprint = b.device_fingerprint
     AND a.user_id < b.user_id
),
settlement_edges AS (
    SELECT 'settlement_account'::text AS kind,
           sa.account_number AS value,
           oa.user_id::text AS a_id,
           ob.user_id::text AS b_id,
           GREATEST(sa.created_at, sb.created_at) AS last_seen,
           2::bigint AS sightings
    FROM merchant_settlement_accounts sa
    JOIN merchant_owners oa ON oa.merchant_id = sa.merchant_id
    JOIN merchant_settlement_accounts sb
      ON sb.bank_code = sa.bank_code
     AND sb.account_number = sa.account_number
     AND sb.merchant_id <> sa.merchant_id
    JOIN merchant_owners ob ON ob.merchant_id = sb.merchant_id
    WHERE oa.user_id < ob.user_id
),
channel_cols AS (
    SELECT id, display_name, updated_at, 'whatsapp' AS col, whatsapp_number AS val FROM users WHERE whatsapp_number <> ''
    UNION ALL
    SELECT id, display_name, updated_at, 'telegram', telegram_chat_id FROM users WHERE telegram_chat_id IS NOT NULL AND telegram_chat_id <> ''
    UNION ALL
    SELECT id, display_name, updated_at, 'instagram', instagram_igsid FROM users WHERE instagram_igsid IS NOT NULL AND instagram_igsid <> ''
    UNION ALL
    SELECT id, display_name, updated_at, 'tiktok', tiktok_open_id FROM users WHERE tiktok_open_id IS NOT NULL AND tiktok_open_id <> ''
),
channel_edges AS (
    SELECT 'channel'::text AS kind,
           a.val AS value,
           a.id::text AS a_id,
           b.id::text AS b_id,
           GREATEST(a.updated_at, b.updated_at) AS last_seen,
           2::bigint AS sightings
    FROM channel_cols a
    JOIN channel_cols b
      ON b.val = a.val
     AND (b.id <> a.id OR b.col <> a.col)
    WHERE a.id <> b.id
)
SELECT k.kind, k.value, k.a_id, k.b_id,
       COALESCE(na.display_name,''), COALESCE(nb.display_name,''),
       k.last_seen, k.sightings
FROM (
    SELECT * FROM device_edges
    UNION ALL SELECT * FROM settlement_edges
    UNION ALL SELECT * FROM channel_edges
) k
LEFT JOIN users na ON na.id::text = k.a_id
LEFT JOIN users nb ON nb.id::text = k.b_id
ORDER BY k.last_seen DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityLinkRow
	for rows.Next() {
		var r IdentityLinkRow
		if err := rows.Scan(&r.LinkKind, &r.LinkValue, &r.SubjectA, &r.SubjectB,
			&r.NameA, &r.NameB, &r.LastSharedAt, &r.Sightings); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Ring is one connected component over the identity-link edges: a set of
// identities that share devices, accounts, or channel identifiers, listed
// with the edges that bind them.
type Ring struct {
	// Members are the distinct subject ids in the component, sorted.
	Members []string
	// Names maps subject id to display name (best effort from edges).
	Names map[string]string
	// Edges are the link rows inside this component.
	Edges []IdentityLinkRow
	// Kinds summarises which link kinds bind this ring, e.g. device+settlement.
	Kinds []string
}

// DetectRings groups link edges into connected components (union-find) and
// keeps those with at least minMembers distinct identities, largest first.
func DetectRings(edges []IdentityLinkRow, minMembers int) []Ring {
	if minMembers < 2 {
		minMembers = 2
	}
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, e := range edges {
		union(e.SubjectA, e.SubjectB)
	}
	members := map[string]map[string]bool{}
	kinds := map[string]map[string]bool{}
	edgeRows := map[string][]IdentityLinkRow{}
	for _, e := range edges {
		root := find(e.SubjectA)
		if members[root] == nil {
			members[root] = map[string]bool{}
			kinds[root] = map[string]bool{}
		}
		members[root][e.SubjectA] = true
		members[root][e.SubjectB] = true
		kinds[root][e.LinkKind] = true
		edgeRows[root] = append(edgeRows[root], e)
	}
	var rings []Ring
	for root, mem := range members {
		if len(mem) < minMembers {
			continue
		}
		ids := make([]string, 0, len(mem))
		for id := range mem {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		names := map[string]string{}
		for _, e := range edgeRows[root] {
			if e.NameA != "" {
				names[e.SubjectA] = e.NameA
			}
			if e.NameB != "" {
				names[e.SubjectB] = e.NameB
			}
		}
		kindList := make([]string, 0, len(kinds[root]))
		for k := range kinds[root] {
			kindList = append(kindList, k)
		}
		sort.Strings(kindList)
		rings = append(rings, Ring{
			Members: ids, Names: names,
			Edges: edgeRows[root], Kinds: kindList,
		})
	}
	sort.Slice(rings, func(i, j int) bool {
		return len(rings[i].Members) > len(rings[j].Members)
	})
	return rings
}

// MaskAccount shows only the last four digits of a bank account number in
// admin surfaces; investigators confirm the full number out-of-band.
func MaskAccount(number string) string {
	if len(number) <= 4 {
		return "••••"
	}
	return "••••" + number[len(number)-4:]
}

// TimelineEntry is one row of a subject's trust timeline (M-layer admin
// surface): an alert, a device sighting, or a user-scoped audit entry.
type TimelineEntry struct {
	Kind   string // "alert" | "device" | "audit"
	When   time.Time
	Label  string
	Detail string
}

// TrustTimeline returns the subject's alerts, device sightings and
// user-scoped audit entries in one chronological feed, newest first.
func (s *Store) TrustTimeline(ctx context.Context, userID uuid.UUID, limit int) ([]TimelineEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
	SELECT * FROM (
	    SELECT 'alert'::text AS kind, created_at AS at_ts,
	           rule || ' · ' || severity AS label,
	           status AS detail
	    FROM transaction_alerts WHERE user_id = $1
	    UNION ALL
	    SELECT 'device', first_seen_at,
	           'device seen: ' || label,
	           device_fingerprint
	    FROM trusted_devices WHERE user_id = $1
	    UNION ALL
	    SELECT 'audit', occurred_at,
	           action,
	           COALESCE(actor_email, actor_type)
	    FROM audit_logs
	    WHERE resource_type = 'user' AND resource_id = $1::text
	) t
	ORDER BY t.at_ts DESC
	LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelineEntry
	for rows.Next() {
		var e TimelineEntry
		if err := rows.Scan(&e.Kind, &e.When, &e.Label, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
