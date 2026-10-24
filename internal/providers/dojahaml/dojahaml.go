// Package dojahaml holds the wire contract for Dojah's AML screening endpoint
// (POST {base}/api/v2/aml/screening), which screens an individual or business
// against PEP, sanctions, warning and adverse-media lists. It is shared by the
// onboarding screener, the payment-time screener and the adverse-media
// screener so the three lanes cannot drift from one another.
//
// Dojah authenticates every call with "Authorization: <secret key>" (raw, no
// Bearer prefix) and "AppId: <app id>"; the adapters that use this package set
// those headers themselves.
package dojahaml

import (
	"strings"

	"whatsapp-payment-demo/internal/kyc"
)

// Path is the AML screening endpoint path, relative to the Dojah base URL.
const Path = "/api/v2/aml/screening"

// Request is the wire body accepted by the screening endpoint.
type Request struct {
	Schema           string     `json:"schema"`
	UniqueReference  string     `json:"unique_reference,omitempty"`
	Properties       Properties `json:"properties"`
	ScreeningOptions Options    `json:"screening_options"`
}

// Properties identify the subject being screened. Names is the only required
// field; the others narrow the candidate set and strengthen match scoring.
type Properties struct {
	Names       string `json:"names"`
	Gender      string `json:"gender,omitempty"`
	DateOfBirth string `json:"date_of_birth,omitempty"`
	Nationality string `json:"nationality,omitempty"`
	// IDNumber is a government-issued identifier. Dojah documents sending ""
	// when none is available, so it is always emitted.
	IDNumber string `json:"id_number"`
}

// Options select the lists to search. Omit Watchlists (or send an empty slice)
// to search every list.
type Options struct {
	PEPCheck          bool     `json:"pep_check"`
	Sanction          bool     `json:"sanction"`
	AdverseMediaCheck bool     `json:"adverse_media_check"`
	Watchlists        []string `json:"watchlists,omitempty"`
	MatchThreshold    float64  `json:"match_threshold,omitempty"`
}

// Response is the vendor reply. Entity is a pointer so an empty or malformed
// body is distinguishable from a valid "No Match" response.
type Response struct {
	Entity *Entity `json:"entity"`
}

// Entity is the screening record.
type Entity struct {
	EntityType    string   `json:"entity_type"`
	EntityID      string   `json:"entity_id"`
	Date          string   `json:"date"`
	TotalResults  int      `json:"total_results"`
	TotalArticles int      `json:"total_articles"`
	SearchQuery   string   `json:"search_query"`
	RiskLevel     string   `json:"risk_level"`
	MatchStatus   string   `json:"match_status"`
	Results       []Result `json:"results"`
}

// Result is either a profile match (PEP, Sanctions, Warning) or an
// adverse-media group. Profile matches carry Name and Match; media groups
// carry MediaCategory and Articles.
type Result struct {
	Name          []string  `json:"name"`
	EntityType    string    `json:"entity_type"`
	SourceType    string    `json:"source_type"`
	DateOfBirth   []string  `json:"date_of_birth"`
	Gender        []string  `json:"gender"`
	Country       []string  `json:"country"`
	Positions     []string  `json:"positions"`
	Match         bool      `json:"match"`
	MediaCategory string    `json:"media_category"`
	Articles      []Article `json:"articles"`
}

// Article is one adverse-media story behind a media group.
type Article struct {
	Timestamp string `json:"timestamp"`
	Headline  string `json:"headline"`
	Source    string `json:"source"`
}

// Decision maps a Dojah match status onto the shared screening vocabulary.
// The four documented statuses are the only accepted values; anything else is
// rejected so an unrecognized reply can never be treated as clear.
func Decision(matchStatus string) (string, bool) {
	switch normalizeToken(matchStatus) {
	case "no_match":
		return kyc.ScreenClear, true
	case "partially_matched", "potential_match":
		return kyc.ScreenPossible, true
	case "confirmed_match":
		// A confirmed hit interdicts: the shared gate treats strong and blocked
		// alike, while payment screening fails blocked movements outright.
		return kyc.ScreenBlocked, true
	}
	return "", false
}

// MatchedNames returns the distinct names of profile results Dojah marked as a
// match. Adverse-media groups carry no name and are skipped.
func MatchedNames(results []Result) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, result := range results {
		if !result.Match || strings.EqualFold(result.SourceType, "ADVERSE_MEDIA") {
			continue
		}
		for _, name := range result.Name {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			names = append(names, trimmed)
		}
	}
	return names
}

// HasAdverseMedia reports whether the response contains a media group.
func HasAdverseMedia(results []Result) bool {
	for _, result := range results {
		if strings.EqualFold(result.SourceType, "ADVERSE_MEDIA") {
			return true
		}
	}
	return false
}

func normalizeToken(raw string) string {
	token := strings.ToLower(strings.TrimSpace(raw))
	return strings.NewReplacer("-", "_", " ", "_").Replace(token)
}
