package docauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestSimulatedAuthenticityContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{"a clean reading is genuine", "RC1234567 ACME LIMITED", ports.AuthenticityGenuine},
		{"tampered is flagged", "RC1234567 ACME LIMITED (tampered seal)", ports.AuthenticityTampered},
		{"forged is flagged", "forged CAC certificate", ports.AuthenticityTampered},
		{"an illegible scan is inconclusive", "blur scan", ports.AuthenticityInconclusive},
		{"an empty reading is inconclusive", "   ", ports.AuthenticityInconclusive},
	}
	s := NewSimulated()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
				EvidenceKey: "cac_verified",
				Document:    tc.document,
			})
			if err != nil {
				t.Fatalf("VerifyDocumentAuthenticity: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func TestSimulatedAuthenticityFaceComparison(t *testing.T) {
	t.Parallel()
	s := NewSimulated()
	withFace, err := s.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
		EvidenceKey:     "cac_verified",
		Document:        "RC1234567 ACME LIMITED",
		FaceDocumentRef: "selfie-ref",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !withFace.FaceMatched {
		t.Fatal("a supplied face reference must report a face-to-document match")
	}
	noFace, err := s.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
		EvidenceKey: "cac_verified",
		Document:    "RC1234567 ACME LIMITED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if noFace.FaceMatched {
		t.Fatal("no face reference means no face comparison")
	}
}

func TestHTTPVerifyDocumentAuthenticityMapsStatus(t *testing.T) {
	t.Parallel()
	var gotBody httpAuthenticityRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/document/analysis" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"genuine","confidence":0.95,"face_matched":true,"reference":"V-9"}`))
	}))
	defer srv.Close()

	got, err := NewHTTP(srv.URL, "key", 0).VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
		EvidenceKey: "cac_verified",
		Document:    "RC1234567 ACME LIMITED",
		LegalName:   "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyDocumentAuthenticity: %v", err)
	}
	if gotBody.EvidenceKey != "cac_verified" || gotBody.Document == "" || gotBody.LegalName != "Acme Ltd" {
		t.Fatalf("request body = %+v", gotBody)
	}
	if got.Status != ports.AuthenticityGenuine || !got.FaceMatched || got.ProviderRef != "V-9" || got.Confidence != 0.95 {
		t.Fatalf("result = %+v", got)
	}
}

func TestHTTPVerifyDocumentAuthenticityRejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"maybe"}`))
	}))
	defer srv.Close()

	if _, err := NewHTTP(srv.URL, "", 0).VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{Document: "RC1234567"}); err == nil {
		t.Fatal("an unrecognized status must be rejected, never silently treated as genuine")
	}
}

func TestHTTPVerifyDocumentAuthenticityNeedsReading(t *testing.T) {
	t.Parallel()
	if _, err := NewHTTP("http://example.invalid", "", 0).VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{}); err == nil {
		t.Fatal("an empty reading must be an error, not a verdict")
	}
}
