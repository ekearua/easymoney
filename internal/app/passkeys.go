package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// errPasskeysUnconfigured is returned whenever a ceremony is attempted on a
// deployment whose WebAuthn relying party is not usable (any BaseURL that is
// not an origin, or no BaseURL at all).
var errPasskeysUnconfigured = errors.New("passkeys are not configured")

// webAuthnUser adapts a portal subject to the WebAuthn user interface. The
// handle is the subject's UUID, so a credential stays bound to one account.
type webAuthnUser struct {
	handle      uuid.UUID
	name        string
	displayName string
	credentials []webauthn.Credential
}

func (u webAuthnUser) WebAuthnID() []byte                         { return u.handle[:] }
func (u webAuthnUser) WebAuthnName() string                       { return u.name }
func (u webAuthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u webAuthnUser) WebAuthnIcon() string                       { return "" }
func (u webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// passkeyFactors lists a subject's confirmed passkeys, oldest first.
func (a *App) passkeyFactors(ctx context.Context, scope string, subjectID uuid.UUID) ([]store.MFAFactor, error) {
	factors, err := a.store.MFAFactors(ctx, scope, subjectID)
	if err != nil {
		return nil, err
	}
	var passkeys []store.MFAFactor
	for _, factor := range factors {
		if factor.Kind == store.MFAKindPasskey && factor.ConfirmedAt != nil {
			passkeys = append(passkeys, factor)
		}
	}
	return passkeys, nil
}

// webAuthnSubject builds the ceremony user from the subject's stored passkeys.
func (a *App) webAuthnSubject(ctx context.Context, scope string, subjectID uuid.UUID, account string) (webAuthnUser, error) {
	factors, err := a.passkeyFactors(ctx, scope, subjectID)
	if err != nil {
		return webAuthnUser{}, err
	}
	user := webAuthnUser{handle: subjectID, name: account, displayName: account}
	if strings.TrimSpace(user.name) == "" {
		user.name = subjectID.String()
		user.displayName = subjectID.String()
	}
	for _, factor := range factors {
		user.credentials = append(user.credentials, webauthn.Credential{
			ID:            factor.CredentialID,
			PublicKey:     factor.PublicKey,
			Authenticator: webauthn.Authenticator{SignCount: clampSignCount(factor.SignCount)},
		})
	}
	return user, nil
}

// clampSignCount fits a stored counter into the ceremony type without wrapping.
func clampSignCount(count int64) uint32 {
	if count <= 0 {
		return 0
	}
	if count > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(count)
}

// credentialDescriptors lists the credentials to exclude or allow in a
// ceremony.
func credentialDescriptors(credentials []webauthn.Credential) []protocol.CredentialDescriptor {
	descriptors := make([]protocol.CredentialDescriptor, 0, len(credentials))
	for _, credential := range credentials {
		descriptors = append(descriptors, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: credential.ID,
		})
	}
	return descriptors
}

// ceremonyRequest wraps a posted ceremony response as the request the WebAuthn
// library parses it from. The body is already the verbatim credential JSON the
// browser produced.
func ceremonyRequest(ctx context.Context, body []byte) *http.Request {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(body))
	if err != nil {
		return (&http.Request{}).WithContext(ctx)
	}
	request.Header.Set("Content-Type", "application/json")
	return request
}

// beginPasskeyRegistration returns the creation options for a new passkey and
// the ceremony state needed to finish it.
func (a *App) beginPasskeyRegistration(ctx context.Context, scope string, subjectID uuid.UUID, account string) (options, state []byte, err error) {
	if a.webAuthn == nil {
		return nil, nil, errPasskeysUnconfigured
	}
	user, err := a.webAuthnSubject(ctx, scope, subjectID, account)
	if err != nil {
		return nil, nil, err
	}
	creation, session, err := a.webAuthn.BeginRegistration(user, webauthn.WithExclusions(credentialDescriptors(user.credentials)))
	if err != nil {
		return nil, nil, err
	}
	if options, err = json.Marshal(creation); err != nil {
		return nil, nil, err
	}
	if state, err = json.Marshal(session); err != nil {
		return nil, nil, err
	}
	return options, state, nil
}

// finishPasskeyRegistration verifies an attestation and stores the credential.
func (a *App) finishPasskeyRegistration(ctx context.Context, scope string, subjectID uuid.UUID, account string, state, body []byte, label string) (int64, error) {
	if a.webAuthn == nil {
		return 0, errPasskeysUnconfigured
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(state, &session); err != nil {
		return 0, err
	}
	user, err := a.webAuthnSubject(ctx, scope, subjectID, account)
	if err != nil {
		return 0, err
	}
	credential, err := a.webAuthn.FinishRegistration(user, session, ceremonyRequest(ctx, body))
	if err != nil {
		return 0, err
	}
	now := time.Now()
	return a.store.InsertPasskeyFactor(ctx, store.MFAFactor{
		Scope:        scope,
		SubjectID:    subjectID,
		Kind:         store.MFAKindPasskey,
		Label:        passkeyLabel(label),
		CredentialID: credential.ID,
		PublicKey:    credential.PublicKey,
		SignCount:    int64(credential.Authenticator.SignCount),
		Transports:   transportsString(credential.Transport),
		ConfirmedAt:  &now,
	})
}

// beginPasskeyAssertion returns the assertion options for a live sign-in step.
func (a *App) beginPasskeyAssertion(ctx context.Context, challenge store.MFAChallenge, account string) (options, state []byte, err error) {
	if a.webAuthn == nil {
		return nil, nil, errPasskeysUnconfigured
	}
	user, err := a.webAuthnSubject(ctx, challenge.Scope, challenge.SubjectID, account)
	if err != nil {
		return nil, nil, err
	}
	if len(user.credentials) == 0 {
		return nil, nil, errors.New("no passkey is registered for this account")
	}
	assertion, session, err := a.webAuthn.BeginLogin(user, webauthn.WithAllowedCredentials(credentialDescriptors(user.credentials)))
	if err != nil {
		return nil, nil, err
	}
	if options, err = json.Marshal(assertion); err != nil {
		return nil, nil, err
	}
	if state, err = json.Marshal(session); err != nil {
		return nil, nil, err
	}
	return options, state, nil
}

// finishPasskeyAssertion verifies an assertion against the stored credential and
// advances its signature counter. The credential is resolved from the assertion
// itself, so a subject with several passkeys can use any of them.
func (a *App) finishPasskeyAssertion(ctx context.Context, challenge store.MFAChallenge, account string, state, body []byte) error {
	if a.webAuthn == nil {
		return errPasskeysUnconfigured
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(state, &session); err != nil {
		return err
	}
	user, err := a.webAuthnSubject(ctx, challenge.Scope, challenge.SubjectID, account)
	if err != nil {
		return err
	}
	credential, err := a.webAuthn.FinishLogin(user, session, ceremonyRequest(ctx, body))
	if err != nil {
		return err
	}
	stored, err := a.store.MFAFactorByCredential(ctx, credential.ID)
	if err != nil {
		return err
	}
	if stored == nil || stored.Scope != challenge.Scope || stored.SubjectID != challenge.SubjectID {
		return errors.New("that passkey does not belong to this account")
	}
	// A counter that stopped growing means the credential was cloned or
	// replayed. Authenticators that keep no counter always report zero, so the
	// check only applies once a counter has been seen.
	if stored.SignCount > 0 && int64(credential.Authenticator.SignCount) <= stored.SignCount {
		return fmt.Errorf("passkey signature counter did not advance: %d <= %d",
			credential.Authenticator.SignCount, stored.SignCount)
	}
	return a.store.UpdatePasskeySignCount(ctx, stored.ID, int64(credential.Authenticator.SignCount))
}

// transportsString records how the authenticator was reached, for display.
func transportsString(transports []protocol.AuthenticatorTransport) string {
	parts := make([]string, 0, len(transports))
	for _, transport := range transports {
		parts = append(parts, string(transport))
	}
	return strings.Join(parts, ",")
}

// passkeyLabel names a new credential. A browser hint is used when it is short
// and sane; otherwise the generic name keeps the list readable.
func passkeyLabel(hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return store.MFAKindLabel(store.MFAKindPasskey)
	}
	if len(hint) > 60 {
		hint = hint[:60]
	}
	return hint
}

// passkeyPublicError turns a ceremony failure into copy a subject can act on.
func passkeyPublicError(err error) string {
	if errors.Is(err, errPasskeysUnconfigured) {
		return "Passkeys are not configured on this deployment."
	}
	if strings.Contains(err.Error(), "no passkey is registered") {
		return "No passkey is registered for this account yet."
	}
	return "That passkey could not be verified. Try again or use another method."
}

// secondFactorPasskey drives the passkey half of a live sign-in step. The
// browser talks JSON here because a ceremony is not a form: it first asks for
// the assertion options, then posts the signed assertion back.
func (a *App) secondFactorPasskey(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Token      string          `json:"token"`
		Action     string          `json:"action"`
		Account    string          `json:"account"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	token := strings.TrimSpace(payload.Token)
	hash := sha256.Sum256([]byte(token))
	challenge, found, err := a.store.MFAChallengeByToken(r.Context(), hash[:])
	if err != nil {
		a.logger.ErrorContext(r.Context(), "load passkey step", "error", err)
	}
	if !found || challenge.Attempts >= mfaChallengeMaxAttempts {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "This sign-in step has expired. Please sign in again."})
		return
	}
	if challenge.Method != store.MFAKindPasskey {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "This step is not using a passkey."})
		return
	}
	switch payload.Action {
	case "begin":
		options, state, err := a.beginPasskeyAssertion(r.Context(), challenge, payload.Account)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "begin passkey assertion", "scope", challenge.Scope, "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": passkeyPublicError(err)})
			return
		}
		if err := a.store.SwitchMFAChallenge(r.Context(), hash[:], store.MFAKindPasskey, challenge.FactorID, nil, nil, state); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Security error. Please sign in again."})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(options)
	case "finish":
		if len(challenge.WebAuthnState) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Ask for the passkey prompt again."})
			return
		}
		if len(payload.Credential) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "No passkey response was sent."})
			return
		}
		if err := a.finishPasskeyAssertion(r.Context(), challenge, payload.Account, challenge.WebAuthnState, payload.Credential); err != nil {
			a.logger.WarnContext(r.Context(), "passkey assertion rejected", "scope", challenge.Scope, "error", err)
			remaining, failErr := a.store.FailMFAChallenge(r.Context(), hash[:], mfaChallengeMaxAttempts)
			if failErr != nil {
				a.logger.ErrorContext(r.Context(), "record failed passkey assertion", "error", failErr)
			}
			if remaining <= 0 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error": "Too many failed attempts. This sign-in step has expired. Please sign in again.",
				})
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": passkeyPublicError(err)})
			return
		}
		if err := a.completeSecondFactor(r, challenge, token); err != nil {
			a.logger.ErrorContext(r.Context(), "complete passkey step", "scope", challenge.Scope, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Security error. Please sign in again."})
			return
		}
		landing, err := a.issueSession(w, r, challenge.Scope, challenge.SubjectID)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "issue session after passkey", "scope", challenge.Scope, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Session error. Please sign in again."})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"redirect": landing})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
	}
}
