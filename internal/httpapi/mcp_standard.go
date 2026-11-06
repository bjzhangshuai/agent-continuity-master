package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

var mcpVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"}

func validRPCID(id json.RawMessage) bool {
	if len(id) == 0 || string(id) == "null" {
		return false
	}
	var str string
	if json.Unmarshal(id, &str) == nil {
		return true
	}
	var n json.Number
	return json.Unmarshal(id, &n) == nil
}
func decodeRPC(w http.ResponseWriter, r *http.Request, max int64, q *rpcRequest) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		writeJSON(w, 400, rpcFailure(json.RawMessage("null"), -32700, "parse error", nil))
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeJSON(w, 400, rpcFailure(json.RawMessage("null"), -32700, "trailing JSON", nil))
		return false
	}
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, q) != nil {
		writeJSON(w, 400, rpcFailure(json.RawMessage("null"), -32600, "invalid request", nil))
		return false
	}
	return true
}
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		base, e2 := url.Parse(s.billing.BaseURL)
		allowed := e == nil && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/") && (u.Scheme == "https" || u.Scheme == "http")
		if base != nil && e2 == nil && base.Host != "" {
			allowed = allowed && u.Host == base.Host && u.Scheme == base.Scheme
		} else {
			allowed = allowed && u.Host == r.Host
		}
		if !allowed {
			writeJSON(w, 403, rpcFailure(nil, -32600, "origin forbidden", nil))
			return
		}
	}
	var q rpcRequest
	if !decodeRPC(w, r, s.maxBodyBytes, &q) {
		return
	}
	fail := func(status, code int, msg string, data any) {
		id := q.ID
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		writeJSON(w, status, rpcFailure(id, code, msg, data))
	}
	if q.JSONRPC != "2.0" || q.Method == "" {
		fail(400, -32600, "invalid request", nil)
		return
	}
	notification := len(q.ID) == 0
	if !notification && !validRPCID(q.ID) {
		fail(400, -32600, "invalid id", nil)
		return
	}
	var p struct {
		Version   string                     `json:"protocolVersion"`
		Meta      map[string]json.RawMessage `json:"_meta"`
		Name      string                     `json:"name"`
		Arguments json.RawMessage            `json:"arguments"`
		Cursor    string                     `json:"cursor"`
	}
	if len(q.Params) > 0 && json.Unmarshal(q.Params, &p) != nil {
		fail(400, -32602, "invalid params", nil)
		return
	}
	version := r.Header.Get("MCP-Protocol-Version")
	var bodyVersion string
	_ = json.Unmarshal(p.Meta["io.modelcontextprotocol/protocolVersion"], &bodyVersion)
	modern := bodyVersion != "" || version == "2026-07-28" || q.Method == "server/discover"
	if modern {
		if version != bodyVersion || version == "" || r.Header.Get("Mcp-Method") != q.Method {
			fail(400, -32020, "required protocol/method headers must match request", nil)
			return
		}
		if !slices.Contains(mcpVersions, version) || version != "2026-07-28" {
			fail(400, -32022, "Unsupported protocol version", map[string]any{"supported": mcpVersions, "requested": version})
			return
		}
		var info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		var caps map[string]any
		if json.Unmarshal(p.Meta["io.modelcontextprotocol/clientInfo"], &info) != nil || info.Name == "" || info.Version == "" || json.Unmarshal(p.Meta["io.modelcontextprotocol/clientCapabilities"], &caps) != nil || caps == nil {
			fail(400, -32602, "clientInfo and clientCapabilities metadata required", nil)
			return
		}
		if q.Method == "tools/call" {
			name := r.Header.Get("Mcp-Name")
			if strings.HasPrefix(name, "=?base64?") && strings.HasSuffix(name, "?=") {
				b, e := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(name, "=?base64?"), "?="))
				if e != nil {
					fail(400, -32020, "invalid encoded name", nil)
					return
				}
				name = string(b)
			}
			if name != p.Name || name == "" {
				fail(400, -32020, "Mcp-Name must match tool name", nil)
				return
			}
		}
	} else if version != "" && !slices.Contains(mcpVersions, version) {
		fail(400, -32022, "Unsupported protocol version", map[string]any{"supported": mcpVersions, "requested": version})
		return
	}
	c, ok := s.authorize(w, r)
	if !ok {
		return
	}
	if notification {
		if q.Method == "notifications/initialized" || q.Method == "notifications/cancelled" {
			w.WriteHeader(202)
		} else {
			fail(400, -32600, "unsupported notification", nil)
		}
		return
	}
	result := func(v map[string]any) {
		if modern {
			v["resultType"] = "complete"
		}
		writeJSON(w, 200, rpcSuccess(q.ID, v))
	}
	switch q.Method {
	case "server/discover":
		result(map[string]any{"supportedVersions": mcpVersions, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "_meta": map[string]any{"io.modelcontextprotocol/serverInfo": map[string]string{"name": "agent-continuity", "version": protocol.ProtocolVersion}}})
	case "initialize":
		chosen := p.Version
		if !slices.Contains(mcpVersions[1:], chosen) {
			chosen = "2025-11-25"
		}
		if p.Version == "" {
			chosen = "2025-06-18"
		}
		result(map[string]any{"protocolVersion": chosen, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "agent-continuity", "version": protocol.ProtocolVersion}})
	case "ping":
		result(map[string]any{})
	case "tools/list":
		if p.Cursor != "" {
			fail(200, -32602, "invalid cursor", nil)
			return
		}
		result(map[string]any{"tools": mcpTools()})
	case "tools/call":
		if !validToolArguments(p.Name, p.Arguments) {
			fail(200, -32602, "unknown tool or invalid arguments", nil)
			return
		}
		v, e := s.callMCPTool(r, c, p.Name, p.Arguments)
		if e != nil {
			result(map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": e.Error()}}})
			return
		}
		data, _ := json.Marshal(v)
		result(map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": string(data)}}, "structuredContent": v})
	default:
		fail(404, -32601, "method not found", nil)
	}
}
func validToolArguments(name string, raw json.RawMessage) bool {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return false
	}
	for _, tool := range mcpTools() {
		if tool["name"] != name {
			continue
		}
		schema := tool["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		for k := range args {
			if _, ok := props[k]; !ok {
				return false
			}
		}
		if required, ok := schema["required"].([]string); ok {
			for _, k := range required {
				if len(args[k]) == 0 || bytes.Equal(args[k], []byte("null")) {
					return false
				}
			}
		}
		if l, ok := args["limit"]; ok {
			var n int
			if json.Unmarshal(l, &n) != nil || n < 1 || n > 500 {
				return false
			}
		}
		return true
	}
	return false
}
