package service

import (
	"context"
	"errors"
	"testing"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

type fakeDownloader struct {
	data  []byte
	mime  string
	calls []string
	err   error
}

func (f *fakeDownloader) Download(ctx context.Context, channel, mediaID, mediaURL string) ([]byte, string, error) {
	f.calls = append(f.calls, channel+"|"+mediaID+"|"+mediaURL)
	return f.data, f.mime, f.err
}

type recordingImageReader struct {
	contents [][]byte
	mimes    []string
	text     string
	err      error
}

func (r *recordingImageReader) ReadImage(ctx context.Context, imageData []byte, mimeType string, prompt string) (string, error) {
	r.contents = append(r.contents, imageData)
	r.mimes = append(r.mimes, mimeType)
	return r.text, r.err
}

type recordingSTT struct {
	audio [][]byte
	mimes []string
	text  string
	err   error
}

func (s *recordingSTT) Transcribe(ctx context.Context, audioData []byte, mimeType string, language string) (string, error) {
	s.audio = append(s.audio, audioData)
	s.mimes = append(s.mimes, mimeType)
	return s.text, s.err
}

func TestProcessMediaFeedsDownloadedBytesToImageReader(t *testing.T) {
	reader := &recordingImageReader{text: "reference XGO-777"}
	dl := &fakeDownloader{data: []byte("real-jpeg"), mime: "image/jpeg"}
	s := &ConversationService{imageReader: reader, mediaDownloader: dl}

	got := s.processMedia(context.Background(), store.InboundMessage{
		Channel: ChannelWhatsApp, MediaType: "image", MediaID: "media-1",
	}, "media:u1")
	if got != "reference XGO-777" {
		t.Fatalf("unexpected media text: %q", got)
	}
	if len(dl.calls) != 1 || dl.calls[0] != "whatsapp|media-1|" {
		t.Fatalf("unexpected download calls: %#v", dl.calls)
	}
	if len(reader.contents) != 1 || string(reader.contents[0]) != "real-jpeg" {
		t.Fatalf("image reader must receive real bytes, got %d calls %q", len(reader.contents), reader.contents)
	}
	if len(reader.mimes) != 1 || reader.mimes[0] != "image/jpeg" {
		t.Fatalf("image reader must receive the downloaded mime, got %#v", reader.mimes)
	}
}

func TestProcessMediaFallsBackToEnqueuedMime(t *testing.T) {
	reader := &recordingImageReader{text: "ok"}
	dl := &fakeDownloader{data: []byte("bytes")}
	s := &ConversationService{imageReader: reader, mediaDownloader: dl}

	s.processMedia(context.Background(), store.InboundMessage{
		Channel: ChannelInstagram, MediaType: "photo", MediaURL: "https://cdn.ig/x", MediaMime: "image/png",
	}, "media:u1")
	if dl.calls[0] != "instagram||https://cdn.ig/x" {
		t.Fatalf("unexpected download calls: %#v", dl.calls)
	}
	if reader.mimes[0] != "image/png" {
		t.Fatalf("expected enqueued mime fallback, got %q", reader.mimes[0])
	}
}

func TestProcessMediaTranscribesAudioBytes(t *testing.T) {
	stt := &recordingSTT{text: "make payment"}
	dl := &fakeDownloader{data: []byte("real-ogg"), mime: "audio/ogg"}
	s := &ConversationService{speechToText: stt, mediaDownloader: dl}

	got := s.processMedia(context.Background(), store.InboundMessage{
		Channel: ChannelTelegram, MediaType: "audio", MediaID: "file-1",
	}, "media:u1")
	if got != "make payment" {
		t.Fatalf("unexpected transcription: %q", got)
	}
	if len(stt.audio) != 1 || string(stt.audio[0]) != "real-ogg" || stt.mimes[0] != "audio/ogg" {
		t.Fatalf("STT must receive real bytes, got %d calls", len(stt.audio))
	}
}

func TestProcessMediaDegradesOnDownloadFailure(t *testing.T) {
	reader := &recordingImageReader{text: "should not be used"}
	dl := &fakeDownloader{err: errors.New("provider 403")}
	s := &ConversationService{imageReader: reader, mediaDownloader: dl}

	if got := s.processMedia(context.Background(), store.InboundMessage{
		Channel: ChannelWhatsApp, MediaType: "image", MediaID: "media-1",
	}, "media:u1"); got != "" {
		t.Fatalf("download failure must degrade to empty text, got %q", got)
	}
	if len(reader.contents) != 0 {
		t.Fatalf("image reader must not be called after download failure")
	}
}

func TestProcessMediaDegradesOnEmptyBytes(t *testing.T) {
	reader := &recordingImageReader{text: "ignored"}
	dl := &fakeDownloader{data: nil}
	s := &ConversationService{imageReader: reader, mediaDownloader: dl}

	if got := s.processMedia(context.Background(), store.InboundMessage{
		Channel: ChannelWhatsApp, MediaType: "image", MediaID: "media-1",
	}, "media:u1"); got != "" {
		t.Fatalf("empty download must degrade, got %q", got)
	}
}

func TestProcessMediaSkipsDownloadWithoutFloor(t *testing.T) {
	dl := &fakeDownloader{data: []byte("bytes")}
	s := &ConversationService{mediaDownloader: dl}

	if got := s.processMedia(context.Background(), store.InboundMessage{
		Channel: ChannelWhatsApp, MediaType: "image", MediaID: "media-1",
	}, "media:u1"); got != "" {
		t.Fatalf("missing image reader must degrade, got %q", got)
	}
	if len(dl.calls) != 0 {
		t.Fatalf("no download should happen when the AI floor is nil")
	}
}

func TestProcessMediaIgnoresUnsupportedMediaType(t *testing.T) {
	s := &ConversationService{imageReader: &recordingImageReader{}}
	if got := s.processMedia(context.Background(), store.InboundMessage{
		Channel: ChannelWhatsApp, MediaType: "video", MediaID: "media-1",
	}, "media:u1"); got != "" {
		t.Fatalf("unsupported media type must degrade, got %q", got)
	}
}

var _ ports.MediaDownloader = (*fakeDownloader)(nil)
