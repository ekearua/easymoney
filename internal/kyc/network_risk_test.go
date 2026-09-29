package kyc

import "testing"

// TestNetworkRiskWeights pins the two Trust Fabric weights: they are modest
// next to a direct observation, they grow with how strong the link actually is
// (more co-users, more confirmations, a bigger ring), and they are capped so
// association alone can never reach the top of the scale.
func TestNetworkRiskWeights(t *testing.T) {
	t.Parallel()
	sharedDevice := []struct {
		coUsers int
		want    float64
	}{
		{coUsers: 0, want: 0},
		{coUsers: 1, want: 15},
		{coUsers: 2, want: 25},
		{coUsers: 3, want: 35},
		{coUsers: 4, want: 45},
		{coUsers: 9, want: 45},
	}
	for _, tc := range sharedDevice {
		if got := SharedDeviceRisk(tc.coUsers); got != tc.want {
			t.Errorf("SharedDeviceRisk(%d) = %v, want %v", tc.coUsers, got, tc.want)
		}
	}

	ring := []struct {
		confirmed int
		members   int
		want      float64
	}{
		{confirmed: 0, members: 2, want: 0},
		{confirmed: 1, members: 1, want: 30},
		{confirmed: 1, members: 2, want: 30},
		{confirmed: 2, members: 2, want: 40},
		{confirmed: 1, members: 3, want: 40},
		{confirmed: 2, members: 4, want: 60},
		{confirmed: 9, members: 20, want: 70},
	}
	for _, tc := range ring {
		if got := RingMembershipRisk(tc.confirmed, tc.members); got != tc.want {
			t.Errorf("RingMembershipRisk(%d, %d) = %v, want %v", tc.confirmed, tc.members, got, tc.want)
		}
	}

	// A shared browser alone stays below a single high-severity rule hit, so
	// coincidence of hardware cannot outrank conduct. Ring membership is
	// allowed above it because reaching the cap means several members were
	// separately confirmed — that is conduct, aggregated.
	if max := SharedDeviceRisk(50); max >= AlertScore(AlertHigh) {
		t.Errorf("a shared browser must stay below a high-severity rule hit: %v", max)
	}
	if low := RingMembershipRisk(1, 2); low >= AlertScore(AlertHigh) {
		t.Errorf("one confirmed alert in a pair must stay below a high-severity rule hit: %v", low)
	}
}
