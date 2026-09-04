package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

func jsonUnmarshalStringMap(raw string) (map[string]string, error) {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// wfItemHasCustomFields reports whether the selected service or event tier
// requires a custom-fields step.
func (a *App) wfItemHasCustomFields(ctx context.Context, payload map[string]string) bool {
	switch payload["item_kind"] {
	case "service":
		if id, err := uuid.Parse(payload["service_id"]); err == nil {
			fields, err := a.store.ListServiceCustomFields(ctx, id)
			return err == nil && len(fields) > 0
		}
	case "event":
		if id, err := uuid.Parse(payload["tier_id"]); err == nil {
			fields, err := a.store.ListEventCustomFields(ctx, id)
			return err == nil && len(fields) > 0
		}
	}
	return false
}

// wfCustomFieldSpecs renders the service/event custom fields as form fields.
func (a *App) wfCustomFieldSpecs(ctx context.Context, payload map[string]string) ([]webFlowField, error) {
	switch payload["item_kind"] {
	case "service":
		if id, err := uuid.Parse(payload["service_id"]); err == nil {
			fields, err := a.store.ListServiceCustomFields(ctx, id)
			if err != nil {
				return nil, err
			}
			return wfFieldsFromCustom(fields), nil
		}
	case "event":
		if id, err := uuid.Parse(payload["tier_id"]); err == nil {
			fields, err := a.store.ListEventCustomFields(ctx, id)
			if err != nil {
				return nil, err
			}
			return wfFieldsFromEventCustom(fields), nil
		}
	}
	return nil, nil
}

type customFieldSpec struct {
	Name      string
	FieldType string
	Required  bool
}

func wfFieldsFromCustom(fields []store.ServiceCustomField) []webFlowField {
	out := make([]webFlowField, 0, len(fields))
	for _, f := range fields {
		out = append(out, webFlowField{
			Name: f.FieldName, Label: f.FieldName,
			Type: wfCustomInputType(f.FieldType), Required: f.IsRequired,
		})
	}
	return out
}

func wfFieldsFromEventCustom(fields []store.EventCustomField) []webFlowField {
	out := make([]webFlowField, 0, len(fields))
	for _, f := range fields {
		out = append(out, webFlowField{
			Name: f.FieldName, Label: f.FieldName,
			Type: wfCustomInputType(f.FieldType), Required: f.IsRequired,
		})
	}
	return out
}

func wfCustomInputType(fieldType string) string {
	switch fieldType {
	case "number":
		return "number"
	case "email":
		return "email"
	case "tel", "phone":
		return "tel"
	default:
		return "text"
	}
}

// wfCollectCustomFields reads the submitted custom field values into the JSON
// blob the purchase record expects, validating required and numeric fields.
func (a *App) wfCollectCustomFields(r *http.Request, payload map[string]string) (string, error) {
	fields, err := a.wfCustomFieldSpecs(r.Context(), payload)
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	for _, field := range fields {
		value := strings.TrimSpace(r.FormValue(field.Name))
		if field.Required && value == "" {
			return "", fmt.Errorf("%s is required.", field.Label)
		}
		if field.Type == "number" && value != "" {
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return "", fmt.Errorf("%s must be a number.", field.Label)
			}
		}
		values[field.Name] = value
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
