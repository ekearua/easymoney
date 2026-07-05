package vtpass

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

func TestFulfilDataPostsVTPassPayRequest(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pay" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("api-key") != "api" || r.Header.Get("secret-key") != "secret" {
			t.Fatalf("missing VTPass auth headers")
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response_description": "TRANSACTION SUCCESSFUL",
			"content": map[string]any{"transactions": map[string]any{
				"status":        "delivered",
				"transactionId": "txn-123",
			}},
		})
	}))
	defer server.Close()

	client := New(server.URL, "api", "public", "secret")
	result, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:      "XG-DATA-8K2Q",
		NetworkCode:      "MTN",
		PlanCode:         "MTN1GB",
		ProviderSKU:      "mtn-1gb-code",
		BeneficiaryPhone: "+2348031234567",
		AmountKobo:       50_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fulfilled" || result.ProviderReference == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if captured["serviceID"] != "mtn-data" || captured["variation_code"] != "mtn-1gb-code" || captured["billersCode"] != "08031234567" {
		t.Fatalf("unexpected payload: %#v", captured)
	}
	if captured["amount"].(float64) != 500 {
		t.Fatalf("unexpected amount: %#v", captured["amount"])
	}
}

func TestFulfilDataReusesExistingProviderReference(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response_description": "TRANSACTION SUCCESSFUL",
			"requestId":            "provider-response-id",
		})
	}))
	defer server.Close()

	result, err := New(server.URL, "api", "public", "secret").FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:       "XG-DATA-8K2Q",
		ProviderReference: "202607041230XGDATA8K2Q",
		NetworkCode:       "MTN",
		ProviderSKU:       "sku",
		BeneficiaryPhone:  "+2348031234567",
		AmountKobo:        50_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured["request_id"] != "202607041230XGDATA8K2Q" {
		t.Fatalf("expected retry to reuse provider reference, got %#v", captured["request_id"])
	}
	if result.ProviderReference != "provider-response-id" {
		t.Fatalf("unexpected provider reference: %#v", result)
	}
}

func TestFulfilDataTimeoutReturnsPendingResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"response_description": "TRANSACTION SUCCESSFUL"})
	}))
	defer server.Close()

	result, err := NewWithTimeout(server.URL, "api", "public", "secret", 10*time.Millisecond).FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:      "XG-DATA-8K2Q",
		NetworkCode:      "MTN",
		ProviderSKU:      "sku",
		BeneficiaryPhone: "+2348031234567",
		AmountKobo:       50_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" || result.ProviderReference == "" {
		t.Fatalf("expected timeout to become pending with provider reference, got %#v", result)
	}
}

func TestCheckDataStatusPostsRequery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/requery" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"requestId":            "req-1",
			"response_description": "PENDING",
			"content":              map[string]any{"transactions": map[string]any{"status": "pending"}},
		})
	}))
	defer server.Close()
	result, err := New(server.URL, "api", "public", "secret").CheckDataStatus(context.Background(), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" || result.ProviderReference != "req-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFulfilDataMapsResponseCodeSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":                 "000",
			"response_description": "TRANSACTION SUCCESSFUL",
			"requestId":            "req-123",
		})
	}))
	defer server.Close()
	result, err := New(server.URL, "api", "public", "secret").FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:      "XG-DATA-8K2Q",
		NetworkCode:      "MTN",
		ProviderSKU:      "sku",
		BeneficiaryPhone: "+2348031234567",
		AmountKobo:       50_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fulfilled" || result.ProviderReference != "req-123" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFulfilDataMapsProviderErrorCodeAsFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":                 "010",
			"response_description": "VARIATION CODE DOES NOT EXIST FOR SELECTED PRODUCT",
			"requestId":            "req-010",
		})
	}))
	defer server.Close()

	result, err := New(server.URL, "api", "public", "secret").FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:      "XG-DATA-8K2Q",
		NetworkCode:      "GLO",
		ProviderSKU:      "bad-variation",
		BeneficiaryPhone: "+2347061975340",
		AmountKobo:       100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.Message != "VARIATION CODE DOES NOT EXIST FOR SELECTED PRODUCT" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestParseWebhook(t *testing.T) {
	event, err := ParseWebhook([]byte(`{"requestId":"req-123","response_description":"TRANSACTION SUCCESSFUL","content":{"transactions":{"status":"delivered"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.Reference != "req-123" || event.Status != "fulfilled" {
		t.Fatalf("unexpected event: %#v", event)
	}
}
