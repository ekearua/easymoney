// Physical card requests (migration 106). A customer asks Xego for a physical
// card to be handed over at a local agent in their area; compliance reviews the
// request, records that every issuer/PCI/funding/API/fulfilment gate is
// evidenced, and only then may an operator mark externally issued stock ready
// with a one-time pickup code. The agent hands the card over against that code
// and a visual government-ID check.
//
// This layer stores request metadata and opaque issuer references only. It
// never stores a PAN, CVV, PIN, track data or an identity-document number: the
// collection record keeps the ID *type* the agent inspected, not the number.
// The cardholder name is sealed at rest because it is personal data.
//
// Issuance itself is deliberately absent: Xego does not call an issuer card
// manufacturing API from this workflow. An operator orders the card externally
// and records the issuer reference, which is the evidence that the plastic
// exists before readiness can be granted. See docs/payments/CARD_REQUESTS.md.
package store

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Card request statuses.
const (
	CardRequestRequested = "requested"
	CardRequestApproved  = "approved"
	CardRequestRejected  = "rejected"
	CardRequestReady     = "ready"
	CardRequestCollected = "collected"
)

// Card pickup claim statuses.
const (
	CardPickupPending  = "pending"
	CardPickupRedeemed = "redeemed"
	CardPickupExpired  = "expired"
	CardPickupLocked   = "locked"
)

// Readiness gates an operator must evidence before any card is handed to a
// customer. They are deliberately default-closed: a gate with no row, or a row
// with ready=false, blocks MarkCardRequestReady.
const (
	CardGateIssuerContract    = "issuer_contract"
	CardGatePCIScope          = "pci_scope"
	CardGateFundingSettlement = "funding_settlement"
	CardGateIssuerAPI         = "issuer_api"
	CardGateFulfillment       = "fulfillment"
)

// cardReadinessGates is the canonical gate list in display order. Readiness is
// complete only when every one of these is attested ready.
var cardReadinessGates = []string{
	CardGateIssuerContract,
	CardGatePCIScope,
	CardGateFundingSettlement,
	CardGateIssuerAPI,
	CardGateFulfillment,
}

// CardRequest and card pickup sentinel errors.
var (
	ErrCardRequestNotFound       = errors.New("card request not found")
	ErrCardRequestOpen           = errors.New("customer already has an open card request")
	ErrCardRequestInvalidState   = errors.New("card request is not in a state that allows this action")
	ErrCardRequestRegionInvalid  = errors.New("card request needs an active collection area")
	ErrCardIssuerNotReady        = errors.New("issuer readiness gates are incomplete")
	ErrCardIssuerReferenceAbsent = errors.New("a card cannot be marked ready without an issuer reference")
	ErrCardAgentUnavailable      = errors.New("no active agent is available for this card")
	ErrCardRequestRegionMismatch = errors.New("this card is not assigned to the agent's area")
	ErrCardPickupInvalid         = errors.New("card pickup code is invalid")
	ErrCardPickupExpired         = errors.New("card pickup code has expired")
	ErrCardPickupLocked          = errors.New("card pickup code attempt limit reached")
	ErrCardPickupAlreadyUsed     = errors.New("this card has already been collected")
	ErrCardIDTypeRequired        = errors.New("the agent must record the government ID type inspected")
)

// cardIDTypes are the identity documents an agent may inspect at handover. Only
// the type is stored; the number is never captured.
var cardIDTypes = map[string]bool{
	"nin":             true,
	"bvn":             true,
	"drivers_licence": true,
	"voters_card":     true,
	"passport":        true,
}

// ValidCardIDType reports whether an ID type may be recorded at collection.
func ValidCardIDType(idType string) bool {
	return cardIDTypes[strings.ToLower(strings.TrimSpace(idType))]
}

// ValidCardReadinessGate reports whether a gate key is one of the five gates.
func ValidCardReadinessGate(gate string) bool {
	for _, g := range cardReadinessGates {
		if g == gate {
			return true
		}
	}
	return false
}

// CardRequest is one customer's physical-card request. CardholderName is
// decrypted for display; no card credential is ever part of this type.
type CardRequest struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	Channel          string
	RegionID         uuid.UUID
	RegionName       string
	CardholderName   string
	Status           string
	ReviewNote       string
	ReviewerEmail    string
	ReviewedAt       *time.Time
	IssuerReference  string
	AgentMerchantID  *uuid.UUID
	AgentName        string
	ReadyAt          *time.Time
	CollectionIDType string
	CollectedBy      *uuid.UUID
	CollectedAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CardIssuerGate is one readiness gate and the evidence behind it.
type CardIssuerGate struct {
	GateKey     string
	Ready       bool
	EvidenceRef string
	Note        string
	AttestedBy  string
	AttestedAt  *time.Time
}

// CreateCardRequestParams carries a customer's request.
type CreateCardRequestParams struct {
	UserID         uuid.UUID
	Channel        string
	RegionID       uuid.UUID
	CardholderName string
}

// MarkCardReadyParams carries the evidence that externally issued stock is in
// the agent's hands, plus the one-time pickup code (hashed by the caller).
type MarkCardReadyParams struct {
	CardRequestID   uuid.UUID
	IssuerReference string
	AgentMerchantID uuid.UUID
	CodeHash        string
	CodeSalt        string
	ExpiresAt       time.Time
}

// CollectCardParams is an agent's handover submission. Only the ID type is
// recorded; the number the agent looked at is never captured.
type CollectCardParams struct {
	AgentMerchantID uuid.UUID
	OperatorUserID  uuid.UUID
	Code            string
	IDType          string
}

// cardRequestColumns is the shared SELECT list so scans stay in step. The
// nullable agent join is left-joined because a request has no agent until it is
// ready.
const cardRequestColumns = `c.id, c.user_id, c.channel, c.region_id, COALESCE(r.name,''),
	c.cardholder_name_enc, c.status, c.review_note, c.reviewer_email, c.reviewed_at,
	c.issuer_reference, c.agent_merchant_id, COALESCE(m.name,''), c.ready_at,
	c.collection_id_type, c.collected_by, c.collected_at, c.created_at, c.updated_at`

const cardRequestFrom = `
	FROM card_requests c
	LEFT JOIN agent_regions r ON r.id = c.region_id
	LEFT JOIN merchants m ON m.id = c.agent_merchant_id`

func (s *Store) scanCardRequest(row pgx.Row) (CardRequest, error) {
	var c CardRequest
	var sealedName string
	err := row.Scan(&c.ID, &c.UserID, &c.Channel, &c.RegionID, &c.RegionName, &sealedName,
		&c.Status, &c.ReviewNote, &c.ReviewerEmail, &c.ReviewedAt, &c.IssuerReference,
		&c.AgentMerchantID, &c.AgentName, &c.ReadyAt, &c.CollectionIDType, &c.CollectedBy,
		&c.CollectedAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CardRequest{}, ErrCardRequestNotFound
		}
		return CardRequest{}, err
	}
	name, err := s.openValue(sealedName)
	if err != nil {
		return CardRequest{}, fmt.Errorf("open cardholder name: %w", err)
	}
	c.CardholderName = string(name)
	return c, nil
}

// CreateCardRequest records a customer's request for a physical card. It
// requires an active collection area, refuses a second open request for the
// same customer, and seals the cardholder name at rest.
func (s *Store) CreateCardRequest(ctx context.Context, p CreateCardRequestParams) (CardRequest, error) {
	if p.UserID == uuid.Nil {
		return CardRequest{}, errors.New("card request requires a customer")
	}
	name := strings.TrimSpace(p.CardholderName)
	if len([]rune(name)) < 2 {
		return CardRequest{}, errors.New("card request requires the cardholder name")
	}
	if p.RegionID == uuid.Nil {
		return CardRequest{}, ErrCardRequestRegionInvalid
	}
	channel := strings.TrimSpace(p.Channel)
	if channel == "" {
		channel = "whatsapp"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CardRequest{}, err
	}
	defer tx.Rollback(ctx)

	var regionStatus string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM agent_regions WHERE id=$1`, p.RegionID).Scan(&regionStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CardRequest{}, ErrCardRequestRegionInvalid
		}
		return CardRequest{}, err
	}
	if regionStatus != AgentRegionActive {
		return CardRequest{}, ErrCardRequestRegionInvalid
	}

	var open int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM card_requests
		WHERE user_id=$1 AND status IN ('requested','approved','ready')`, p.UserID).Scan(&open); err != nil {
		return CardRequest{}, err
	}
	if open > 0 {
		return CardRequest{}, ErrCardRequestOpen
	}

	sealedName, err := s.sealValue([]byte(name))
	if err != nil {
		return CardRequest{}, fmt.Errorf("seal cardholder name: %w", err)
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO card_requests (user_id, channel, region_id, cardholder_name_enc, status)
		VALUES($1,$2,$3,$4,'requested')
		RETURNING id`, p.UserID, channel, p.RegionID, sealedName).Scan(&id); err != nil {
		// The partial unique index on open requests is the authority: two
		// concurrent confirms cannot both open one.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return CardRequest{}, ErrCardRequestOpen
		}
		return CardRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CardRequest{}, err
	}
	return s.CardRequestByID(ctx, id)
}

// CardRequestByID loads one request (ErrCardRequestNotFound when absent).
func (s *Store) CardRequestByID(ctx context.Context, id uuid.UUID) (CardRequest, error) {
	return s.scanCardRequest(s.pool.QueryRow(ctx,
		`SELECT `+cardRequestColumns+cardRequestFrom+` WHERE c.id=$1`, id))
}

// CardRequestsByUser lists a customer's requests newest first, so the chat can
// answer "where is my card?" without exposing anyone else's request.
func (s *Store) CardRequestsByUser(ctx context.Context, userID uuid.UUID, limit int) ([]CardRequest, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+cardRequestColumns+cardRequestFrom+`
		 WHERE c.user_id=$1 ORDER BY c.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardRequest
	for rows.Next() {
		c, err := s.scanCardRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CardRequestsByStatus lists the compliance queue. An empty status returns the
// open queue (requested and approved) oldest first, which is the order a
// reviewer works through it; a specific status returns that status newest
// first.
func (s *Store) CardRequestsByStatus(ctx context.Context, status string, limit int) ([]CardRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	switch strings.TrimSpace(status) {
	case "", "open":
		rows, err = s.pool.Query(ctx,
			`SELECT `+cardRequestColumns+cardRequestFrom+`
			 WHERE c.status IN ('requested','approved') ORDER BY c.created_at LIMIT $1`, limit)
	default:
		rows, err = s.pool.Query(ctx,
			`SELECT `+cardRequestColumns+cardRequestFrom+`
			 WHERE c.status=$1 ORDER BY c.created_at DESC LIMIT $2`, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardRequest
	for rows.Next() {
		c, err := s.scanCardRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReviewCardRequest records a compliance decision on a request that is still
// awaiting review. Approving does not hand out a card: readiness is a separate,
// evidence-gated step.
func (s *Store) ReviewCardRequest(ctx context.Context, id uuid.UUID, approve bool, note, reviewerEmail string) (CardRequest, error) {
	status := CardRequestRejected
	if approve {
		status = CardRequestApproved
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE card_requests
		SET status=$2, review_note=$3, reviewer_email=$4, reviewed_at=now(), updated_at=now()
		WHERE id=$1 AND status='requested'`,
		id, status, strings.TrimSpace(note), strings.TrimSpace(reviewerEmail))
	if err != nil {
		return CardRequest{}, err
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "no such request" from "already reviewed" so the console
		// can say which happened.
		if _, err := s.CardRequestByID(ctx, id); err != nil {
			return CardRequest{}, err
		}
		return CardRequest{}, ErrCardRequestInvalidState
	}
	return s.CardRequestByID(ctx, id)
}

// CardIssuerReadiness returns all five gates in canonical order, merging stored
// attestations with the default-closed posture (a gate with no row is reported
// not ready).
func (s *Store) CardIssuerReadiness(ctx context.Context) ([]CardIssuerGate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT gate_key, ready, evidence_ref, note, attested_by, attested_at
		FROM card_issuer_readiness`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stored := map[string]CardIssuerGate{}
	for rows.Next() {
		var g CardIssuerGate
		if err := rows.Scan(&g.GateKey, &g.Ready, &g.EvidenceRef, &g.Note, &g.AttestedBy, &g.AttestedAt); err != nil {
			return nil, err
		}
		stored[g.GateKey] = g
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	gates := make([]CardIssuerGate, 0, len(cardReadinessGates))
	for _, key := range cardReadinessGates {
		g, ok := stored[key]
		if !ok {
			g = CardIssuerGate{GateKey: key}
		}
		gates = append(gates, g)
	}
	return gates, nil
}

// CardIssuerReady reports whether every readiness gate is attested ready. It is
// the precondition MarkCardRequestReady re-checks inside its transaction.
func (s *Store) CardIssuerReady(ctx context.Context) (bool, error) {
	return cardIssuerReadyQ(ctx, s.pool)
}

type cardReadinessQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func cardIssuerReadyQ(ctx context.Context, q cardReadinessQuerier) (bool, error) {
	var ready int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM card_issuer_readiness
		WHERE ready = true AND gate_key = ANY($1)`, cardReadinessGates).Scan(&ready)
	if err != nil {
		return false, err
	}
	return ready == len(cardReadinessGates), nil
}

// AttestCardIssuerGate records (or withdraws) one readiness gate. Marking a gate
// ready requires an evidence reference, so "we believe it is fine" cannot be
// recorded as readiness.
func (s *Store) AttestCardIssuerGate(ctx context.Context, gate string, ready bool, evidenceRef, note, attestedBy string) error {
	if !ValidCardReadinessGate(gate) {
		return fmt.Errorf("unknown card readiness gate %q", gate)
	}
	evidence := strings.TrimSpace(evidenceRef)
	if ready && evidence == "" {
		return errors.New("an evidenced gate needs an evidence reference")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO card_issuer_readiness (gate_key, ready, evidence_ref, note, attested_by, attested_at)
		VALUES($1,$2,$3,$4,$5,now())
		ON CONFLICT (gate_key) DO UPDATE SET
			ready=EXCLUDED.ready, evidence_ref=EXCLUDED.evidence_ref, note=EXCLUDED.note,
			attested_by=EXCLUDED.attested_by, attested_at=now(), updated_at=now()`,
		gate, ready, evidence, strings.TrimSpace(note), strings.TrimSpace(attestedBy))
	return err
}

// MarkCardRequestReady records that externally issued stock exists and assigns
// it to one agent for collection, minting the one-time pickup code. It is the
// single place a request becomes collectable, and it refuses unless:
//
//  1. the request is approved (not merely requested, rejected or already ready);
//  2. an issuer reference is recorded (the plastic exists outside Xego);
//  3. all five readiness gates are evidenced;
//  4. the agent exists, is active, and is in the request's collection area.
//
// The plaintext code is never passed here: the caller hashes it and delivers it
// to the customer through the encrypted outbox.
func (s *Store) MarkCardRequestReady(ctx context.Context, p MarkCardReadyParams) (CardRequest, error) {
	if strings.TrimSpace(p.IssuerReference) == "" {
		return CardRequest{}, ErrCardIssuerReferenceAbsent
	}
	if p.CodeHash == "" || p.CodeSalt == "" {
		return CardRequest{}, errors.New("card pickup requires a code hash and salt")
	}
	if p.ExpiresAt.IsZero() || !p.ExpiresAt.After(time.Now()) {
		return CardRequest{}, errors.New("card pickup requires a future expiry")
	}
	if p.AgentMerchantID == uuid.Nil {
		return CardRequest{}, ErrCardAgentUnavailable
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CardRequest{}, err
	}
	defer tx.Rollback(ctx)

	var status string
	var regionID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT status, region_id FROM card_requests WHERE id=$1 FOR UPDATE`, p.CardRequestID).
		Scan(&status, &regionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CardRequest{}, ErrCardRequestNotFound
	}
	if err != nil {
		return CardRequest{}, err
	}
	if status != CardRequestApproved {
		return CardRequest{}, ErrCardRequestInvalidState
	}

	ready, err := cardIssuerReadyQ(ctx, tx)
	if err != nil {
		return CardRequest{}, err
	}
	if !ready {
		return CardRequest{}, ErrCardIssuerNotReady
	}

	profile, err := agentProfileTx(ctx, tx, p.AgentMerchantID)
	if err != nil {
		return CardRequest{}, err
	}
	if profile.Status != AgentStatusActive {
		return CardRequest{}, fmt.Errorf("%w: agent is %s", ErrCardAgentUnavailable, profile.Status)
	}
	if profile.RegionID != regionID {
		return CardRequest{}, ErrCardRequestRegionMismatch
	}

	if _, err := tx.Exec(ctx, `
		UPDATE card_request_pickups SET status='expired'
		WHERE card_request_id=$1 AND status='pending'`, p.CardRequestID); err != nil {
		return CardRequest{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO card_request_pickups (card_request_id, code_hash, code_salt, expires_at)
		VALUES($1,$2,$3,$4)`, p.CardRequestID, p.CodeHash, p.CodeSalt, p.ExpiresAt); err != nil {
		return CardRequest{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE card_requests
		SET status='ready', issuer_reference=$2, agent_merchant_id=$3, ready_at=now(), updated_at=now()
		WHERE id=$1`, p.CardRequestID, strings.TrimSpace(p.IssuerReference), p.AgentMerchantID); err != nil {
		return CardRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CardRequest{}, err
	}
	return s.CardRequestByID(ctx, p.CardRequestID)
}

// ReissueCardPickupCode replaces a ready request's pending code with a fresh
// one. It exists for delivery failures (the outbox never reached the customer)
// and for a lost code; the card itself does not move, and the previous code is
// retired so two codes can never claim the same card.
func (s *Store) ReissueCardPickupCode(ctx context.Context, requestID uuid.UUID, codeHash, codeSalt string, expiresAt time.Time) (CardRequest, error) {
	if codeHash == "" || codeSalt == "" {
		return CardRequest{}, errors.New("card pickup requires a code hash and salt")
	}
	if expiresAt.IsZero() || !expiresAt.After(time.Now()) {
		return CardRequest{}, errors.New("card pickup requires a future expiry")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CardRequest{}, err
	}
	defer tx.Rollback(ctx)

	var status string
	err = tx.QueryRow(ctx,
		`SELECT status FROM card_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return CardRequest{}, ErrCardRequestNotFound
	}
	if err != nil {
		return CardRequest{}, err
	}
	if status != CardRequestReady {
		return CardRequest{}, ErrCardRequestInvalidState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE card_request_pickups SET status='expired'
		WHERE card_request_id=$1 AND status='pending'`, requestID); err != nil {
		return CardRequest{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO card_request_pickups (card_request_id, code_hash, code_salt, expires_at)
		VALUES($1,$2,$3,$4)`, requestID, codeHash, codeSalt, expiresAt); err != nil {
		return CardRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CardRequest{}, err
	}
	return s.CardRequestByID(ctx, requestID)
}

// AgentCardRequests lists the cards assigned to one agent and still awaiting
// handover. It is display-only: the code is re-resolved and locked by
// CollectCardRequest inside the transaction that records the handover.
func (s *Store) AgentCardRequests(ctx context.Context, agentMerchantID uuid.UUID, limit int) ([]CardRequest, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+cardRequestColumns+cardRequestFrom+`
		 WHERE c.agent_merchant_id=$1 AND c.status='ready'
		 ORDER BY c.ready_at, c.created_at LIMIT $2`, agentMerchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardRequest
	for rows.Next() {
		c, err := s.scanCardRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CollectCardRequest is the single authorisation choke point for a card
// handover. Inside one transaction it:
//
//  1. loads the agent profile and refuses unless the agent is active;
//  2. matches the presented code against that agent's pending card pickups in
//     constant time (the code is only ever stored as a salted digest);
//  3. refuses an expired or attempt-locked code. A wrong code cannot be
//     attributed to a particular customer's row (the agent may hold several
//     cards), so it never increments someone else's counter and could never
//     lock an unrelated card; brute-force resistance at this boundary is the
//     agent console's per-terminal failure throttle, on top of the code's short
//     life and the visual ID check;
//  4. re-checks that the request is still ready and assigned to the agent's
//     region;
//  5. records the ID *type* inspected and retires the code and request.
//
// A concurrent attempt on the same code blocks on the row lock and then finds
// nothing pending: the code is redeemed exactly once.
func (s *Store) CollectCardRequest(ctx context.Context, p CollectCardParams) (CardRequest, error) {
	if p.AgentMerchantID == uuid.Nil {
		return CardRequest{}, errors.New("card collection requires an agent")
	}
	if p.OperatorUserID == uuid.Nil {
		return CardRequest{}, errors.New("card collection requires an operator")
	}
	if strings.TrimSpace(p.Code) == "" {
		return CardRequest{}, ErrCardPickupInvalid
	}
	idType := strings.ToLower(strings.TrimSpace(p.IDType))
	if !ValidCardIDType(idType) {
		return CardRequest{}, ErrCardIDTypeRequired
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CardRequest{}, err
	}
	defer tx.Rollback(ctx)

	profile, err := agentProfileTx(ctx, tx, p.AgentMerchantID)
	if err != nil {
		return CardRequest{}, err
	}
	if profile.Status != AgentStatusActive {
		return CardRequest{}, fmt.Errorf("%w: agent is %s", ErrAgentNotActive, profile.Status)
	}

	type candidate struct {
		pickupID    uuid.UUID
		requestID   uuid.UUID
		codeHash    string
		codeSalt    string
		expiresAt   time.Time
		attempts    int
		maxAttempts int
		regionID    uuid.UUID
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.card_request_id, p.code_hash, p.code_salt, p.expires_at,
		       p.attempts, p.max_attempts, c.region_id
		FROM card_request_pickups p
		JOIN card_requests c ON c.id = p.card_request_id
		WHERE p.status='pending' AND c.status='ready' AND c.agent_merchant_id=$1
		ORDER BY p.created_at
		FOR UPDATE OF p, c`, p.AgentMerchantID)
	if err != nil {
		return CardRequest{}, err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.pickupID, &c.requestID, &c.codeHash, &c.codeSalt, &c.expiresAt,
			&c.attempts, &c.maxAttempts, &c.regionID); err != nil {
			rows.Close()
			return CardRequest{}, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return CardRequest{}, err
	}

	matched := -1
	for i, c := range candidates {
		if hmac.Equal([]byte(RemittanceCodeHash(c.codeSalt, strings.TrimSpace(p.Code))), []byte(c.codeHash)) {
			matched = i
			break
		}
	}
	if matched < 0 {
		// A wrong code cannot be attributed to a row, so there is nothing to
		// increment; the agent console applies its own per-session throttle.
		return CardRequest{}, ErrCardPickupInvalid
	}
	cand := candidates[matched]
	now := time.Now()
	if !cand.expiresAt.After(now) {
		if _, err := tx.Exec(ctx,
			`UPDATE card_request_pickups SET status='expired' WHERE id=$1`, cand.pickupID); err != nil {
			return CardRequest{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return CardRequest{}, err
		}
		return CardRequest{}, ErrCardPickupExpired
	}
	if cand.attempts >= cand.maxAttempts {
		if _, err := tx.Exec(ctx,
			`UPDATE card_request_pickups SET status='locked' WHERE id=$1`, cand.pickupID); err != nil {
			return CardRequest{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return CardRequest{}, err
		}
		return CardRequest{}, ErrCardPickupLocked
	}
	if cand.regionID != profile.RegionID {
		return CardRequest{}, ErrCardRequestRegionMismatch
	}

	if _, err := tx.Exec(ctx, `
		UPDATE card_request_pickups
		SET status='redeemed', redeemed_at=now(), redeemed_by=$2
		WHERE id=$1 AND status='pending'`, cand.pickupID, p.OperatorUserID); err != nil {
		return CardRequest{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE card_requests
		SET status='collected', collection_id_type=$2, collected_by=$3, collected_at=now(), updated_at=now()
		WHERE id=$1 AND status='ready'`, cand.requestID, idType, p.OperatorUserID)
	if err != nil {
		return CardRequest{}, err
	}
	if tag.RowsAffected() == 0 {
		return CardRequest{}, ErrCardPickupAlreadyUsed
	}
	if err := tx.Commit(ctx); err != nil {
		return CardRequest{}, err
	}
	return s.CardRequestByID(ctx, cand.requestID)
}

// ActiveAgentsInRegion lists the active agents an operator can assign issued
// card stock to, so the readiness step can only pick an agent that can actually
// hand the card over in the customer's area.
func (s *Store) ActiveAgentsInRegion(ctx context.Context, regionID uuid.UUID) ([]AgentOption, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.merchant_id, COALESCE(m.name,''), p.region_id
		FROM agent_profiles p
		LEFT JOIN merchants m ON m.id = p.merchant_id
		WHERE p.region_id=$1 AND p.status='active'
		ORDER BY m.name, p.created_at`, regionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentOption
	for rows.Next() {
		var a AgentOption
		if err := rows.Scan(&a.MerchantID, &a.Name, &a.RegionID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AgentOption is an active agent an operator can assign card stock to.
type AgentOption struct {
	MerchantID uuid.UUID
	Name       string
	RegionID   uuid.UUID
}
