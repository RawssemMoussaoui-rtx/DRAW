package api

import (
	"embed"
	"net/http"
)

// chatboxFS embeds the Chatbox SPA assets (static/index.html, static/app.js,
// static/app.css). The embed root mirrors the on-disk static/ directory, so
// every entry is stored under the "static/" path prefix.
//
//go:embed static/*
var chatboxFS embed.FS

// ChatboxHandler is the http.Handler that serves the embedded Chatbox SPA.
// It is intentionally self-contained: it imports only the standard library
// (net/http) plus the embed directive, and never references internal/master,
// internal/storage, internal/model, internal/auth, or the SSE/handlers
// modules. It is designed to compile independently alongside sse.go in
// package api and to share the package namespace without symbol collisions
// (SSE module owns SSEHandler; this file owns ChatboxHandler).
type ChatboxHandler struct {
	mux *http.ServeMux
}

// NewChatbox returns an http.Handler that mounts the Chatbox SPA:
//
//	GET /            -> static/index.html
//	GET /static/*    -> static assets (app.js, app.css) via http.FileServer
//	any other path   -> 404 Not Found
//
// The parent router (T2 wiring) mounts the returned handler at "/". Because the
// embed root stores entries under "static/", the "/static/" URL prefix already
// matches the "static/" filesystem prefix, so http.FileServer needs no
// http.StripPrefix.
func NewChatbox() http.Handler {
	h := &ChatboxHandler{}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(chatboxFS)))
	mux.Handle("/", http.HandlerFunc(h.serveIndex))
	h.mux = mux
	return h
}

// ServeHTTP dispatches to the internal mux.
func (h *ChatboxHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// serveIndex serves the embedded index.html for the root path only; any other
// non-static path returns 404 so the SPA shell stays the single entry point.
func (h *ChatboxHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	const indexFile = "static/index.html"
	data, err := chatboxFS.ReadFile(indexFile)
	if err != nil {
		http.Error(w, "chatbox index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
