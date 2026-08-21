package service

import (
	"log/slog"
	"math"
	"sync"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// providerStats tracks health and latency EWMA for one payment gateway.
type providerStats struct {
	healthy           bool
	consecutiveErrors int
	latencyEWMA       float64 // milliseconds
	lastError         error
}

// ProviderRouter selects the healthiest available payment gateway
// automatically based on availability and latency performance.
type ProviderRouter struct {
	mu       sync.RWMutex
	stats    map[string]*providerStats
	gateways map[string]ports.PaymentGateway
	logger   *slog.Logger

	// ewmaAlpha controls the EWMA smoothing factor for latency.
	// Higher values weight recent measurements more heavily.
	ewmaAlpha float64
}

// NewProviderRouter creates a router with initial health for each gateway.
func NewProviderRouter(gateways map[string]ports.PaymentGateway, logger *slog.Logger) *ProviderRouter {
	stats := make(map[string]*providerStats, len(gateways))
	for name := range gateways {
		stats[name] = &providerStats{healthy: true, latencyEWMA: 1000} // start with 1s default
	}
	return &ProviderRouter{
		stats:     stats,
		gateways:  gateways,
		logger:    logger,
		ewmaAlpha: 0.3,
	}
}

// PickProvider returns the name of the healthiest available gateway.
// Returns the first healthy provider sorted by lowest latency EWMA.
// If all providers are unhealthy, returns any configured provider (best-effort).
func (r *ProviderRouter) PickProvider() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	best := ""
	bestLatency := math.MaxFloat64
	for name, st := range r.stats {
		if !st.healthy {
			continue
		}
		if st.latencyEWMA < bestLatency {
			best = name
			bestLatency = st.latencyEWMA
		}
	}
	if best != "" {
		return best
	}
	// All unhealthy: pick the one with fewest consecutive errors (best-effort).
	fewestErrors := math.MaxInt32
	for name, st := range r.stats {
		if st.consecutiveErrors < fewestErrors {
			best = name
			fewestErrors = st.consecutiveErrors
		}
	}
	return best
}

// ReportResult feeds the outcome of a gateway call back into the router.
func (r *ProviderRouter) ReportResult(provider string, latency time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	st, ok := r.stats[provider]
	if !ok {
		return
	}
	ms := float64(latency.Milliseconds())
	st.latencyEWMA = r.ewmaAlpha*ms + (1-r.ewmaAlpha)*st.latencyEWMA

	if err != nil {
		st.consecutiveErrors++
		st.lastError = err
		st.healthy = st.consecutiveErrors < 3
		if !st.healthy {
			r.logger.Warn("provider marked unhealthy",
				"provider", provider, "consecutive_errors", st.consecutiveErrors, "error", err)
		}
	} else {
		st.consecutiveErrors = 0
		st.healthy = true
	}
}

// SetHealthy manually overrides a provider's health (for admin recovery).
func (r *ProviderRouter) SetHealthy(provider string, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.stats[provider]; ok {
		st.healthy = healthy
		if healthy {
			st.consecutiveErrors = 0
		}
	}
}

// Gateway returns the raw PaymentGateway for a provider name.
func (r *ProviderRouter) Gateway(name string) ports.PaymentGateway {
	return r.gateways[name]
}

// Providers returns the names of all configured providers.
func (r *ProviderRouter) Providers() []string {
	names := make([]string, 0, len(r.stats))
	for name := range r.stats {
		names = append(names, name)
	}
	return names
}
