package httpapi

import (
	"embed"
	"net/http"
	"path/filepath"
)

//go:embed static/*
var staticFiles embed.FS

func (s *Server) staticAsset(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Path)
	contentType := "application/octet-stream"
	switch filepath.Ext(name) {
	case ".js":
		contentType = "text/javascript; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	}
	s.serveStatic(w, name, contentType)
}

func (s *Server) serveStatic(w http.ResponseWriter, name, contentType string) {
	data, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
