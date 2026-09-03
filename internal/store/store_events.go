package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MerchantEvent is a dedicated event created by a merchant with ticket tiers.
type MerchantEvent struct {
	ID           uuid.UUID
	MerchantID   uuid.UUID
	Name         string
	Description  string
	Venue        string
	EventStartAt *time.Time
	EventEndAt   *time.Time
	ImageURL     string
	IsActive     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EventTicketTier is a purchasable ticket tier within an event.
type EventTicketTier struct {
	ID        uuid.UUID
	EventID   uuid.UUID
	Name      string
	PriceKobo int64
	Capacity  int
	Sold      int
	SortOrder int
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EventTicketPurchaseView links a ticket purchase to a payment with customer info.
type EventTicketPurchaseView struct {
	ID             uuid.UUID
	TierID         uuid.UUID
	TierName       string
	EventName      string
	PaymentID      uuid.UUID
	UserID         uuid.UUID
	UserName       string
	WhatsAppNumber string
	Quantity       int
	UnitPriceKobo  int64
	TotalKobo      int64
	CreatedAt      time.Time
}

// EventCustomField is a custom data field defined by a merchant for a ticket tier.
type EventCustomField struct {
	ID           uuid.UUID
	TierID       uuid.UUID
	FieldName    string
	FieldType    string
	FieldOptions string
	IsRequired   bool
	SortOrder    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EventCustomFieldSpec is input for creating/updating an event custom field definition.
type EventCustomFieldSpec struct {
	FieldName    string
	FieldType    string
	FieldOptions string
	IsRequired   bool
	SortOrder    int
}

// EventPurchaseCustomDataValue holds one custom field value for a ticket purchase.
type EventPurchaseCustomDataValue struct {
	FieldName  string
	FieldValue string
}

// EventWithTiers is an event with its ticket tiers pre-loaded for display.
type EventWithTiers struct {
	Event MerchantEvent
	Tiers []EventTicketTier
}

// ---------- MerchantEvent CRUD ----------

func (s *Store) CreateEvent(ctx context.Context, merchantID uuid.UUID, name, description, venue string, eventStartAt, eventEndAt *time.Time, imageURL string) (MerchantEvent, error) {
	id := uuid.New()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO merchant_events(id,merchant_id,name,description,venue,event_start_at,event_end_at,image_url)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, merchantID, name, description, venue, eventStartAt, eventEndAt, imageURL)
	if err != nil {
		return MerchantEvent{}, err
	}
	return s.EventByID(ctx, id)
}

func (s *Store) UpdateEvent(ctx context.Context, eventID uuid.UUID, name, description, venue string, eventStartAt, eventEndAt *time.Time, imageURL string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_events SET
			name=$2, description=$3, venue=$4, event_start_at=$5, event_end_at=$6,
			image_url=$7, updated_at=now()
		WHERE id=$1`, eventID, name, description, venue, eventStartAt, eventEndAt, imageURL)
	return err
}

func (s *Store) EventByID(ctx context.Context, id uuid.UUID) (MerchantEvent, error) {
	var evt MerchantEvent
	err := s.pool.QueryRow(ctx, `
		SELECT id,merchant_id,name,description,venue,event_start_at,event_end_at,image_url,is_active,created_at,updated_at
		FROM merchant_events WHERE id=$1`, id).Scan(
		&evt.ID, &evt.MerchantID, &evt.Name, &evt.Description, &evt.Venue,
		&evt.EventStartAt, &evt.EventEndAt, &evt.ImageURL, &evt.IsActive,
		&evt.CreatedAt, &evt.UpdatedAt)
	return evt, err
}

func (s *Store) ListEventsByMerchantID(ctx context.Context, merchantID uuid.UUID) ([]MerchantEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,merchant_id,name,description,venue,event_start_at,event_end_at,image_url,is_active,created_at,updated_at
		FROM merchant_events
		WHERE merchant_id=$1
		ORDER BY
			CASE WHEN is_active=true AND (event_start_at IS NULL OR event_start_at > now()) THEN 0 ELSE 1 END,
			event_start_at DESC NULLS LAST, created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []MerchantEvent
	for rows.Next() {
		var evt MerchantEvent
		if err := rows.Scan(&evt.ID, &evt.MerchantID, &evt.Name, &evt.Description, &evt.Venue,
			&evt.EventStartAt, &evt.EventEndAt, &evt.ImageURL, &evt.IsActive,
			&evt.CreatedAt, &evt.UpdatedAt); err != nil {
			return nil, err
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

func (s *Store) ListActiveEventsByMerchantID(ctx context.Context, merchantID uuid.UUID) ([]MerchantEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,merchant_id,name,description,venue,event_start_at,event_end_at,image_url,is_active,created_at,updated_at
		FROM merchant_events
		WHERE merchant_id=$1 AND is_active=true AND (event_start_at IS NULL OR event_start_at > now())
		ORDER BY event_start_at ASC NULLS LAST, created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []MerchantEvent
	for rows.Next() {
		var evt MerchantEvent
		if err := rows.Scan(&evt.ID, &evt.MerchantID, &evt.Name, &evt.Description, &evt.Venue,
			&evt.EventStartAt, &evt.EventEndAt, &evt.ImageURL, &evt.IsActive,
			&evt.CreatedAt, &evt.UpdatedAt); err != nil {
			return nil, err
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

func (s *Store) ToggleEvent(ctx context.Context, eventID, merchantID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_events SET is_active=NOT is_active, updated_at=now()
		WHERE id=$1 AND merchant_id=$2`, eventID, merchantID)
	return err
}

// ---------- Ticket tiers ----------

// SetEventTiersFull replaces all tiers for an event (delete + insert in a transaction).
func (s *Store) SetEventTiersFull(ctx context.Context, eventID uuid.UUID, tiers []EventTierSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM event_ticket_tiers WHERE event_id=$1`, eventID); err != nil {
		return err
	}
	for i, t := range tiers {
		if t.Name == "" {
			continue
		}
		capacity := t.Capacity
		if capacity == 0 {
			capacity = -1
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO event_ticket_tiers(event_id,name,price_kobo,capacity,sort_order,is_active)
			VALUES($1,$2,$3,$4,$5,$6)`, eventID, t.Name, t.PriceKobo, capacity, i, t.IsActive)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// EventTierSpec is input for creating/updating a ticket tier.
type EventTierSpec struct {
	Name      string
	PriceKobo int64
	Capacity  int
	IsActive  bool
}

func (s *Store) TiersByEventID(ctx context.Context, eventID uuid.UUID) ([]EventTicketTier, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,event_id,name,price_kobo,capacity,sold,sort_order,is_active,created_at,updated_at
		FROM event_ticket_tiers
		WHERE event_id=$1
		ORDER BY sort_order`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tiers []EventTicketTier
	for rows.Next() {
		var t EventTicketTier
		if err := rows.Scan(&t.ID, &t.EventID, &t.Name, &t.PriceKobo, &t.Capacity,
			&t.Sold, &t.SortOrder, &t.IsActive, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tiers = append(tiers, t)
	}
	return tiers, rows.Err()
}

func (s *Store) TierByID(ctx context.Context, id uuid.UUID) (EventTicketTier, error) {
	var t EventTicketTier
	err := s.pool.QueryRow(ctx, `
		SELECT id,event_id,name,price_kobo,capacity,sold,sort_order,is_active,created_at,updated_at
		FROM event_ticket_tiers WHERE id=$1`, id).Scan(
		&t.ID, &t.EventID, &t.Name, &t.PriceKobo, &t.Capacity,
		&t.Sold, &t.SortOrder, &t.IsActive, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) ActiveTiersByEventID(ctx context.Context, eventID uuid.UUID) ([]EventTicketTier, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,event_id,name,price_kobo,capacity,sold,sort_order,is_active,created_at,updated_at
		FROM event_ticket_tiers
		WHERE event_id=$1 AND is_active=true
		ORDER BY sort_order`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tiers []EventTicketTier
	for rows.Next() {
		var t EventTicketTier
		if err := rows.Scan(&t.ID, &t.EventID, &t.Name, &t.PriceKobo, &t.Capacity,
			&t.Sold, &t.SortOrder, &t.IsActive, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tiers = append(tiers, t)
	}
	return tiers, rows.Err()
}

// ---------- Event ticket purchases ----------

func (s *Store) CreateEventTicketPurchase(ctx context.Context, tierID, paymentID uuid.UUID, quantity int, unitPriceKobo, totalKobo int64) (uuid.UUID, error) {
	id := uuid.New()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO event_ticket_purchases(id,tier_id,payment_id,quantity,unit_price_kobo,total_kobo)
		VALUES($1,$2,$3,$4,$5,$6)`, id, tierID, paymentID, quantity, unitPriceKobo, totalKobo)
	return id, err
}

// ConfirmEventTicketPurchase increments sold count with capacity check, idempotent per payment.
func (s *Store) ConfirmEventTicketPurchase(ctx context.Context, paymentID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The applied flag (added in 056) makes the hook idempotent: a retried
	// hook cannot increment tier sales a second time for the same purchase.
	if _, err := tx.Exec(ctx, `
		UPDATE event_ticket_tiers t
		SET sold = t.sold + etp.quantity, updated_at=now()
		FROM event_ticket_purchases etp
		WHERE etp.payment_id=$1 AND t.id=etp.tier_id
		  AND NOT etp.applied
		  AND (t.capacity < 0 OR t.capacity >= t.sold + etp.quantity)`, paymentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE event_ticket_purchases
		SET applied = true
		WHERE payment_id=$1 AND NOT applied`, paymentID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) EventTicketPurchaseQuantityByPaymentID(ctx context.Context, paymentID uuid.UUID) (int, error) {
	var qty int
	err := s.pool.QueryRow(ctx, `
		SELECT quantity FROM event_ticket_purchases WHERE payment_id=$1`, paymentID).Scan(&qty)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return qty, err
}

func (s *Store) EventTicketPurchasesByEventID(ctx context.Context, eventID uuid.UUID) ([]EventTicketPurchaseView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT etp.id,etp.tier_id,ett.name,me.name,p.id,p.user_id,
		       u.display_name,COALESCE(u.whatsapp_number,''),
		       etp.quantity,etp.unit_price_kobo,etp.total_kobo,etp.created_at
		FROM event_ticket_purchases etp
		JOIN event_ticket_tiers ett ON ett.id=etp.tier_id
		JOIN merchant_events me ON me.id=ett.event_id
		JOIN payments p ON p.id=etp.payment_id
		JOIN users u ON u.id=p.user_id
		WHERE ett.event_id=$1
		ORDER BY etp.created_at DESC`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var purchases []EventTicketPurchaseView
	for rows.Next() {
		var p EventTicketPurchaseView
		if err := rows.Scan(&p.ID, &p.TierID, &p.TierName, &p.EventName, &p.PaymentID, &p.UserID,
			&p.UserName, &p.WhatsAppNumber, &p.Quantity, &p.UnitPriceKobo, &p.TotalKobo, &p.CreatedAt); err != nil {
			return nil, err
		}
		purchases = append(purchases, p)
	}
	return purchases, rows.Err()
}

// ---------- Event custom fields ----------

func (s *Store) SetEventCustomFields(ctx context.Context, tierID uuid.UUID, fields []EventCustomFieldSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM event_custom_fields WHERE tier_id=$1`, tierID); err != nil {
		return err
	}
	for i, f := range fields {
		if f.FieldName == "" {
			continue
		}
		fieldType := f.FieldType
		if fieldType == "" {
			fieldType = "text"
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO event_custom_fields(tier_id,field_name,field_type,field_options,is_required,sort_order)
			VALUES($1,$2,$3,$4,$5,$6)`, tierID, f.FieldName, fieldType, f.FieldOptions, f.IsRequired, i)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListEventCustomFields(ctx context.Context, tierID uuid.UUID) ([]EventCustomField, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,tier_id,field_name,field_type,field_options,is_required,sort_order,created_at,updated_at
		FROM event_custom_fields
		WHERE tier_id=$1
		ORDER BY sort_order`, tierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fields []EventCustomField
	for rows.Next() {
		var f EventCustomField
		if err := rows.Scan(&f.ID, &f.TierID, &f.FieldName, &f.FieldType, &f.FieldOptions,
			&f.IsRequired, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, rows.Err()
}

func (s *Store) SaveEventPurchaseCustomData(ctx context.Context, purchaseID uuid.UUID, fieldMap map[string]string) error {
	for fieldName, fieldValue := range fieldMap {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO event_purchase_custom_data(purchase_id,field_id,field_name,field_value)
			SELECT $1, id, $3, $4 FROM event_custom_fields WHERE tier_id=(
				SELECT tier_id FROM event_ticket_purchases WHERE id=$1
			) AND field_name=$2
			LIMIT 1`, purchaseID, fieldName, fieldName, fieldValue)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EventPurchaseCustomDataByPurchaseIDs(ctx context.Context, purchaseIDs []uuid.UUID) (map[uuid.UUID][]EventPurchaseCustomDataValue, error) {
	if len(purchaseIDs) == 0 {
		return map[uuid.UUID][]EventPurchaseCustomDataValue{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT purchase_id, field_name, field_value
		FROM event_purchase_custom_data
		WHERE purchase_id = ANY($1)
		ORDER BY purchase_id, field_name`, purchaseIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[uuid.UUID][]EventPurchaseCustomDataValue)
	for rows.Next() {
		var pid uuid.UUID
		var v EventPurchaseCustomDataValue
		if err := rows.Scan(&pid, &v.FieldName, &v.FieldValue); err != nil {
			return nil, err
		}
		result[pid] = append(result[pid], v)
	}
	return result, rows.Err()
}
