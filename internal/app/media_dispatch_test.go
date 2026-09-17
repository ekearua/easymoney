package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/providers/instagram"
	"whatsapp-payment-demo/internal/providers/telegram"
	"whatsapp-payment-demo/internal/providers/tiktok"
	"whatsapp-payment-demo/internal/service"
)

func TestAppDownloadDispatchesByChannel(t *testing.T) {
	ctx := context.Background()

	t.Run("telegram", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/bottoken/getFile" {
				_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"f1","file_path":"note.ogg"}}`))
				return
			}
			if r.URL.Path == "/file/bottoken/note.ogg" {
				_, _ = w.Write([]byte("ogg-data"))
				return
			}
			http.Error(w, "unexpected", http.StatusNotFound)
		}))
		defer server.Close()

		a := &App{telegram: telegram.New("token", server.URL, "secret")}
		data, mime, err := a.Download(ctx, service.ChannelTelegram, "f1", "")
		if err != nil || string(data) != "ogg-data" || mime != "audio/ogg" {
			t.Fatalf("telegram dispatch failed: err=%v data=%q mime=%q", err, data, mime)
		}
	})

	t.Run("instagram", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("ig-data"))
		}))
		defer server.Close()

		a := &App{instagram: instagram.New("sec", "token", "ig-1", "v23.0")}
		data, mime, err := a.Download(ctx, service.ChannelInstagram, "", server.URL)
		if err != nil || string(data) != "ig-data" || mime != "image/jpeg" {
			t.Fatalf("instagram dispatch failed: err=%v data=%q mime=%q", err, data, mime)
		}
	})

	t.Run("tiktok", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("tt-data"))
		}))
		defer server.Close()

		a := &App{tiktok: tiktok.New("sec", "token", "")}
		data, mime, err := a.Download(ctx, service.ChannelTikTok, server.URL, "")
		if err != nil || string(data) != "tt-data" || mime != "image/png" {
			t.Fatalf("tiktok dispatch failed: err=%v data=%q mime=%q", err, data, mime)
		}
	})

	t.Run("non-media channels error", func(t *testing.T) {
		a := &App{}
		for _, channel := range []string{service.ChannelSMS, service.ChannelAPI, service.ChannelCheckout} {
			if _, _, err := a.Download(ctx, channel, "x", ""); err == nil {
				t.Fatalf("channel %s must reject media downloads", channel)
			}
		}
	})
}
