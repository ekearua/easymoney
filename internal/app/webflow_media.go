package app

import (
	"io"
	"net/http"
	"regexp"
	"strings"

	"whatsapp-payment-demo/internal/store"
)

// Media upload limits for web-flow upload/voice fields. Files are read into
// memory, OCR'd or transcribed, and only the extracted text is kept: the raw
// file is never stored, so nothing sensitive lingers on disk.
const (
	wfMaxMediaBytes = 8 << 20 // 8MB per upload
	wfMaxExtractLen = 4096    // characters kept from OCR/STT output
)

var wfFieldNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
var wfDigits11Pattern = regexp.MustCompile(`\d{11}`)

// wfFieldNameOK rejects upload field names that could not be a payload key.
func wfFieldNameOK(name string) bool {
	return wfFieldNamePattern.MatchString(name)
}

// wfMediaKind classifies an uploaded file by its declared content type into
// the provider that reads it. Anything that is not an image or audio file is
// rejected.
func wfMediaKind(mime string) (string, bool) {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image", true
	case strings.HasPrefix(mime, "audio/"):
		return "audio", true
	}
	return "", false
}

// wfIDNumberFrom returns the first 11-digit sequence found in raw (an OCR'd
// NIN/BVN slip or a transcribed voice note), or "" when none is present.
func wfIDNumberFrom(raw string) string {
	m := wfDigits11Pattern.FindString(raw)
	if m == "" {
		return ""
	}
	return m
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

// webFlowMediaUpload reads one uploaded image or voice note, extracts text
// with ports.ImageReader or ports.SpeechToText, and stores the extracted text
// in the flow payload under the submitted field name (PRG back to the same
// step, which then shows what was read). The raw file is never persisted.
func (a *App) webFlowMediaUpload(w http.ResponseWriter, r *http.Request) {
	flow, user, ok := a.webFlowContext(w, r)
	if !ok {
		return
	}
	if flow.Status != store.WebFlowOpen {
		http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "choose a file to upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	field := strings.TrimSpace(r.FormValue("field"))
	if !wfFieldNameOK(field) {
		http.Error(w, "invalid field", http.StatusBadRequest)
		return
	}
	mime := strings.ToLower(header.Header.Get("Content-Type"))
	kind, ok := wfMediaKind(mime)
	if !ok {
		a.wfMediaError(w, r, flow, user, "Upload an image (JPG, PNG, WebP) or an audio recording.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, wfMaxMediaBytes+1))
	if err != nil {
		a.wfMediaError(w, r, flow, user, "Could not read that file. Please try again.")
		return
	}
	if len(data) > wfMaxMediaBytes {
		a.wfMediaError(w, r, flow, user, "That file is too large (max 8MB). Try a smaller photo or shorter voice note.")
		return
	}

	var text string
	switch kind {
	case "image":
		if a.imageReader == nil {
			a.wfMediaError(w, r, flow, user, "Image reading is not enabled on this server.")
			return
		}
		prompt := strings.TrimSpace(r.FormValue("prompt"))
		text, err = a.imageReader.ReadImage(r.Context(), data, mime, prompt)
	case "audio":
		if a.speechToText == nil {
			a.wfMediaError(w, r, flow, user, "Voice notes are not enabled on this server.")
			return
		}
		text, err = a.speechToText.Transcribe(r.Context(), data, mime, "en")
	}
	if err != nil {
		a.logger.WarnContext(r.Context(), "web flow media extract failed", "flow", flow.ID, "kind", kind, "error", err)
		a.wfMediaError(w, r, flow, user, "We couldn't read that file. Try a clearer photo or a shorter voice note.")
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		a.wfMediaError(w, r, flow, user, "We couldn't read any text from that file. Try again with a clearer photo or louder recording.")
		return
	}
	if len([]rune(text)) > wfMaxExtractLen {
		text = truncateRunes(text, wfMaxExtractLen)
	}

	payload := clonePayload(flow.Payload)
	payload[field] = text
	if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, flow.Step, payload); err != nil {
		a.logger.WarnContext(r.Context(), "web flow media save failed", "flow", flow.ID, "error", err)
		http.Error(w, "Could not save what we read. Go back and try again.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
}

// wfMediaError re-renders the current step with the failure message so the
// customer can retry without leaving the flow.
func (a *App) wfMediaError(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, msg string) {
	page, err := a.wfRenderStep(r, flow, user)
	if err != nil {
		a.logger.WarnContext(r.Context(), "web flow media error render failed", "flow", flow.ID, "error", err)
		http.Error(w, "Something went wrong. Go back to WhatsApp and tap the link again.", http.StatusInternalServerError)
		return
	}
	a.wfPrefillMediaValues(&page, flow)
	page.Error = msg
	page.FlowType = flow.FlowType
	page.Token = flow.Token
	page.AppName = a.cfg.AppName
	page.WhatsAppLink = a.whatsappDeepLink()
	page.BaseURL = a.cfg.BaseURL
	a.renderStatus(w, "webflow.html", page, http.StatusUnprocessableEntity)
}
