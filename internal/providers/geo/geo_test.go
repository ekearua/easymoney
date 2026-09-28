package geo

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestSimulatedVerifyLocationContract(t *testing.T) {
	t.Parallel()
	s := NewSimulated()
	cases := []struct {
		name string
		req  ports.LocationRequest
		want string
	}{
		{"no position", ports.LocationRequest{Address: "12 Marina, Lagos"}, ports.VerifyNoLocation},
		{"precise fix", ports.LocationRequest{Latitude: 6.45, Longitude: 3.39, AccuracyMeters: 25}, ports.VerifyMatched},
		{"unknown accuracy", ports.LocationRequest{Latitude: 6.45, Longitude: 3.39}, ports.VerifyReview},
		{"coarse fix", ports.LocationRequest{Latitude: 6.45, Longitude: 3.39, AccuracyMeters: 50_000}, ports.VerifyReview},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.VerifyLocation(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("VerifyLocation: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
			// The simulator never geocodes, so it must never claim a distance.
			if got.DistanceMeters != -1 {
				t.Fatalf("distance = %v, want -1 from a simulator", got.DistanceMeters)
			}
		})
	}
}

func TestDistanceMeters(t *testing.T) {
	t.Parallel()
	// Lagos Island to Ikeja is roughly 18km; allow a wide tolerance since the
	// point of this assertion is that the units and order of magnitude hold.
	got := DistanceMeters(6.4531, 3.3958, 6.6018, 3.3515)
	if got < 12_000 || got > 25_000 {
		t.Fatalf("distance = %.0fm, want roughly 18km", got)
	}
	if d := DistanceMeters(6.4531, 3.3958, 6.4531, 3.3958); d > 0.001 {
		t.Fatalf("distance to itself = %v, want 0", d)
	}
}

func TestHTTPVerifyLocationDerivesStatusFromDistance(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"distance_m":812.4,"resolved_address":"12 Marina Rd","reference":"G-1"}`))
	}))
	defer srv.Close()

	client := NewHTTP(srv.URL, "", 5000, 0)
	close, err := client.VerifyLocation(context.Background(), ports.LocationRequest{Address: "12 Marina, Lagos", Latitude: 6.45, Longitude: 3.39})
	if err != nil {
		t.Fatalf("VerifyLocation: %v", err)
	}
	if close.Status != ports.VerifyMatched || math.Abs(close.DistanceMeters-812.4) > 0.01 {
		t.Fatalf("within-tolerance result = %+v", close)
	}
}

func TestHTTPVerifyLocationRejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"probably"}`))
	}))
	defer srv.Close()

	if _, err := NewHTTP(srv.URL, "", 0, 0).VerifyLocation(context.Background(), ports.LocationRequest{Latitude: 1, Longitude: 1}); err == nil {
		t.Fatal("an unrecognized status must be rejected, never silently treated as matched")
	}
}
