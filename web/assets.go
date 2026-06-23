// Package web embeds templates and static dashboard assets in the Go binary.
package web

import "embed"

// Assets contains the server-rendered UI and locally served HTMX runtime.
//
//go:embed templates/*.html static/*
var Assets embed.FS
