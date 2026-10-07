package app

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

func (a *App) merchantBulkCenters(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	centers, err := a.store.ListMerchantCenters(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_bulk_centers.html", r, "Payment locations", map[string]any{
		"Centers": centers, "DefaultRadiusM": a.cfg.BulkRadiusM,
		"Created": r.URL.Query().Get("created") == "1",
	})
}

func (a *App) merchantBulkCenterCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	lat, latErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("latitude")), 64)
	lng, lngErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("longitude")), 64)
	radius, _ := strconv.Atoi(r.FormValue("radius_m"))
	if name == "" || latErr != nil || lngErr != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		http.Error(w, "provide a name and valid coordinates", http.StatusBadRequest)
		return
	}
	if radius <= 0 {
		radius = a.cfg.BulkRadiusM
	}
	if _, err := a.store.CreateMerchantCenter(r.Context(), merchantID, name, lat, lng, radius); err != nil {
		http.Error(w, "could not save location", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/bulk/centers?created=1", http.StatusSeeOther)
}

func (a *App) merchantBulkCenterDetail(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	centerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid location id", http.StatusBadRequest)
		return
	}
	center, err := a.store.MerchantCenterByID(r.Context(), merchantID, centerID)
	if err != nil {
		http.Error(w, "location not found", http.StatusNotFound)
		return
	}
	users, err := a.store.ListCenterUsers(r.Context(), centerID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	balance, _, err := a.store.BusinessWalletBalance(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_bulk_center_detail.html", r, center.Name, map[string]any{
		"Center": center, "Users": users, "Balance": balance,
		"Error": r.URL.Query().Get("error"), "Saved": r.URL.Query().Get("saved") == "1",
	})
}

func (a *App) merchantBulkCenterDelete(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	centerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid location id", http.StatusBadRequest)
		return
	}
	if err := a.store.DeleteMerchantCenter(r.Context(), merchantID, centerID); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/bulk/centers", http.StatusSeeOther)
}

func (a *App) merchantBulkRecipientAdd(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	centerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid location id", http.StatusBadRequest)
		return
	}
	if _, err := a.store.MerchantCenterByID(r.Context(), merchantID, centerID); err != nil {
		http.Error(w, "location not found", http.StatusNotFound)
		return
	}
	phone := strings.TrimSpace(r.FormValue("phone"))
	if phone == "" {
		http.Redirect(w, r, "/merchant/bulk/centers/"+centerID.String()+"?error=Enter+a+phone+number", http.StatusSeeOther)
		return
	}
	if _, err := a.store.UpsertCenterUser(r.Context(), merchantID, centerID, phone, strings.TrimSpace(r.FormValue("name")), "manual"); err != nil {
		http.Error(w, "could not add recipient", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/bulk/centers/"+centerID.String()+"?saved=1", http.StatusSeeOther)
}

func (a *App) merchantBulkRecipientUpload(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	centerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid location id", http.StatusBadRequest)
		return
	}
	if _, err := a.store.MerchantCenterByID(r.Context(), merchantID, centerID); err != nil {
		http.Error(w, "location not found", http.StatusNotFound)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/merchant/bulk/centers/"+centerID.String()+"?error=Choose+a+CSV+or+XLSX+file", http.StatusSeeOther)
		return
	}
	defer file.Close()
	rows, err := parseRecipientFile(file, header.Filename)
	if err != nil {
		http.Redirect(w, r, "/merchant/bulk/centers/"+centerID.String()+"?error=Could+not+read+the+file", http.StatusSeeOther)
		return
	}
	added := 0
	for _, row := range rows {
		if row.phone == "" {
			continue
		}
		if _, err := a.store.UpsertCenterUser(r.Context(), merchantID, centerID, row.phone, row.name, "upload"); err != nil {
			a.logger.WarnContext(r.Context(), "bulk recipient upload row failed", "phone", row.phone, "error", err)
			continue
		}
		added++
	}
	http.Redirect(w, r, "/merchant/bulk/centers/"+centerID.String()+"?saved="+strconv.Itoa(added), http.StatusSeeOther)
}

func (a *App) merchantBulkRecipientDelete(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	centerID := chi.URLParam(r, "id")
	recipientID, err := uuid.Parse(chi.URLParam(r, "recipientID"))
	if err != nil {
		http.Error(w, "invalid recipient id", http.StatusBadRequest)
		return
	}
	if err := a.store.DeleteCenterUser(r.Context(), merchantID, recipientID); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/bulk/centers/"+centerID+"?saved=1", http.StatusSeeOther)
}

func (a *App) merchantBulkBatches(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	batches, err := a.store.ListBulkBatches(r.Context(), merchantID, 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_bulk_batches.html", r, "Bulk payments", map[string]any{
		"Batches": batches, "Error": r.URL.Query().Get("error"),
	})
}

func (a *App) merchantBulkBatchCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	centerID, err := uuid.Parse(strings.TrimSpace(r.FormValue("center_id")))
	if err != nil {
		http.Error(w, "invalid location id", http.StatusBadRequest)
		return
	}
	amountKobo, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("amount_kobo")), 10, 64)
	if err != nil || amountKobo <= 0 {
		http.Redirect(w, r, "/merchant/bulk/centers/"+centerID.String()+"?error=Enter+a+valid+amount", http.StatusSeeOther)
		return
	}
	operatorID := merchantID
	batch, err := a.bulkPayments.CreateBatch(r.Context(), merchantID, centerID, amountKobo, operatorID)
	if err != nil {
		http.Redirect(w, r, "/merchant/bulk/centers/"+centerID.String()+"?error="+url.QueryEscape(urlMessage(err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/merchant/bulk/batches/"+batch.ID.String(), http.StatusSeeOther)
}

func (a *App) merchantBulkBatchDetail(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid batch id", http.StatusBadRequest)
		return
	}
	batch, err := a.store.BulkBatchByID(r.Context(), merchantID, batchID)
	if err != nil {
		http.Error(w, "batch not found", http.StatusNotFound)
		return
	}
	items, err := a.store.ListBulkBatchItems(r.Context(), batchID, "", 1000)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	center, _ := a.store.MerchantCenterByID(r.Context(), merchantID, batch.CenterID)
	a.renderMerchant(w, "merchant_bulk_batch_detail.html", r, "Batch "+batch.BatchNo, map[string]any{
		"Batch": batch, "Items": bulkItemRows(items), "Center": center,
	})
}

// bulkItemRow is one recipient formatted for display.
type bulkItemRow struct {
	Phone    string
	Channel  string
	Status   string
	Distance string
	Accuracy string
	Reason   string
}

func bulkItemRows(items []store.BulkBatchItem) []bulkItemRow {
	rows := make([]bulkItemRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, bulkItemRow{
			Phone:    item.PhoneE164,
			Channel:  item.Channel,
			Status:   item.Status,
			Distance: formatMetres(item.DistanceM),
			Accuracy: formatMetres(item.AccuracyM),
			Reason:   item.FailureReason,
		})
	}
	return rows
}

func formatMetres(value *float64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatFloat(*value, 'f', 1, 64) + " m"
}

// publicBulkLocationPage renders the browser-geolocation fallback capture form.
func (a *App) publicBulkLocationPage(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(r.URL.Query().Get("id"))
	w.Header().Set("Permissions-Policy", "geolocation=(self)")
	a.render(w, "bulk_location.html", map[string]any{
		"AppName": a.cfg.AppName, "ItemID": itemID,
	})
}

type bulkLocationPayload struct {
	ID        string  `json:"id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Accuracy  float64 `json:"accuracy"`
}

// publicBulkLocationCapture stores a fallback location submission.
func (a *App) publicBulkLocationCapture(w http.ResponseWriter, r *http.Request) {
	var payload bulkLocationPayload
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid payload"})
			return
		}
	} else {
		_ = r.ParseForm()
		payload.ID = r.FormValue("id")
		payload.Latitude, _ = strconv.ParseFloat(r.FormValue("latitude"), 64)
		payload.Longitude, _ = strconv.ParseFloat(r.FormValue("longitude"), 64)
		payload.Accuracy, _ = strconv.ParseFloat(r.FormValue("accuracy"), 64)
	}
	itemID, err := uuid.Parse(strings.TrimSpace(payload.ID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	handled, err := a.bulkPayments.RecordLocationForItem(r.Context(), itemID, payload.Latitude, payload.Longitude, payload.Accuracy)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not record location"})
		return
	}
	if !handled {
		writeJSON(w, http.StatusGone, map[string]any{"error": "this location request is no longer open"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "recorded"})
}

// recipientRow is one parsed CSV/XLSX recipient.
type recipientRow struct {
	phone string
	name  string
}

// parseRecipientFile reads phone/name pairs from a CSV or XLSX upload. Column A
// is the phone number; column B (optional) is the display name. A header row is
// skipped when its first cell is not phone-like.
func parseRecipientFile(file io.Reader, filename string) ([]recipientRow, error) {
	if strings.HasSuffix(strings.ToLower(filename), ".xlsx") {
		return parseRecipientXLSX(file)
	}
	return parseRecipientCSV(file)
}

func parseRecipientCSV(file io.Reader) ([]recipientRow, error) {
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	var rows []recipientRow
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if row, ok := recipientFromCells(record, len(rows) == 0); ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func parseRecipientXLSX(file io.Reader) ([]recipientRow, error) {
	book, err := excelize.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer book.Close()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("workbook has no sheets")
	}
	records, err := book.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	var rows []recipientRow
	for i, record := range records {
		if row, ok := recipientFromCells(record, i == 0); ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func recipientFromCells(cells []string, first bool) (recipientRow, bool) {
	if len(cells) == 0 {
		return recipientRow{}, false
	}
	phone := strings.TrimSpace(cells[0])
	name := ""
	if len(cells) > 1 {
		name = strings.TrimSpace(cells[1])
	}
	if phone == "" {
		return recipientRow{}, false
	}
	if first && !looksLikePhone(phone) {
		return recipientRow{}, false
	}
	return recipientRow{phone: phone, name: name}, true
}

func looksLikePhone(value string) bool {
	digits := 0
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == '-' || r == ' ' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return digits >= 7
}

// urlMessage maps a batch-start failure to the sentence the merchant sees on
// the location page. The result is passed through url.QueryEscape, so it is
// plain text rather than a pre-encoded query fragment.
func urlMessage(err error) string {
	switch {
	case errors.Is(err, service.ErrBulkInsufficientFunds):
		return "Business wallet balance is too low for this batch"
	case errors.Is(err, service.ErrBulkTooManyTargets):
		return "This location has too many recipients"
	case errors.Is(err, store.ErrInsufficientWalletBalance):
		return "Business wallet balance is too low"
	default:
		return "Could not start the batch"
	}
}