package httpapi

import (
	"net/http"

	"github.com/agent-continuity/agent-continuity-master/api"
)

func (s *Server) protocolDiscovery(w http.ResponseWriter, r *http.Request) {
	serveContract(w, r, "continuity-profile-v1.json", "application/json")
}

func (s *Server) protocolSchema(w http.ResponseWriter, r *http.Request) {
	serveContract(w, r, "schemas/"+r.PathValue("name"), "application/schema+json")
}

func (s *Server) protocolOpenAPI(w http.ResponseWriter, r *http.Request) {
	serveContract(w, r, "openapi.yaml", "application/yaml")
}

func serveContract(w http.ResponseWriter, r *http.Request, name, contentType string) {
	data, err := api.Contract(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}
