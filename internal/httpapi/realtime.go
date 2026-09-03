package httpapi

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type realtimeSubscriber struct {
	id       string
	taskID   string
	presence protocol.Presence
	messages chan protocol.RealtimeMessage
}

type realtimeHub struct {
	mu          sync.RWMutex
	subscribers map[string]map[string]*realtimeSubscriber
	presence    map[string]protocol.Presence
}

func newRealtimeHub() *realtimeHub {
	return &realtimeHub{subscribers: map[string]map[string]*realtimeSubscriber{}, presence: map[string]protocol.Presence{}}
}

func (h *realtimeHub) subscribe(value protocol.Presence) *realtimeSubscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, _ := protocol.NewID("subscription")
	subscriber := &realtimeSubscriber{id: id, taskID: value.TaskID, presence: value, messages: make(chan protocol.RealtimeMessage, 32)}
	if h.subscribers[value.TaskID] == nil {
		h.subscribers[value.TaskID] = map[string]*realtimeSubscriber{}
	}
	h.subscribers[value.TaskID][id] = subscriber
	h.presence[presenceKey(value)] = value
	return subscriber
}

func (h *realtimeHub) unsubscribe(value *realtimeSubscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers[value.taskID], value.id)
	delete(h.presence, presenceKey(value.presence))
	if len(h.subscribers[value.taskID]) == 0 {
		delete(h.subscribers, value.taskID)
	}
}

func (h *realtimeHub) publish(value protocol.RealtimeMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, subscriber := range h.subscribers[value.TaskID] {
		select {
		case subscriber.messages <- value:
		default:
			resync := protocol.RealtimeMessage{Type: "resync.required", TaskID: value.TaskID, ServerSequence: value.ServerSequence, SentAt: time.Now().UTC()}
			select {
			case <-subscriber.messages:
			default:
			}
			select {
			case subscriber.messages <- resync:
			default:
			}
		}
	}
}

func (h *realtimeHub) listPresence(taskID string) []protocol.Presence {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]protocol.Presence, 0)
	for _, value := range h.presence {
		if value.TaskID == taskID {
			result = append(result, value)
		}
	}
	return result
}

func presenceKey(value protocol.Presence) string {
	return value.TaskID + "\x00" + value.ParticipantID + "\x00" + value.DeviceID
}

func (s *Server) publish(value protocol.RealtimeMessage) {
	if value.SentAt.IsZero() {
		value.SentAt = time.Now().UTC()
	}
	s.hub.publish(value)
}

func (s *Server) listPresence(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presence": s.hub.listPresence(r.PathValue("taskID")), "ephemeral": true})
}

func (s *Server) realtime(w http.ResponseWriter, r *http.Request) {
	if !validWebSocketOrigin(r) {
		writeError(w, http.StatusForbidden, "origin_forbidden", "websocket origin does not match request host")
		return
	}
	if r.Header.Get("Authorization") == "" {
		for _, offered := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
			offered = strings.TrimSpace(offered)
			if strings.HasPrefix(offered, "bearer.") {
				r.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(offered, "bearer."))
				break
			}
		}
	}
	claims, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	participantID := r.URL.Query().Get("participant_id")
	participant, err := s.store.GetTaskParticipant(r.Context(), r.PathValue("taskID"), participantID)
	if err != nil || participant.AccountID != claims.AccountID || participant.Status != "active" {
		writeError(w, http.StatusForbidden, "participant_forbidden", "active participant_id owned by the caller is required")
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	decodedKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decodedKey) != 16 || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerContains(r.Header.Get("Connection"), "upgrade") || r.Header.Get("Sec-WebSocket-Version") != "13" {
		writeError(w, http.StatusBadRequest, "invalid_websocket_handshake", "RFC 6455 websocket upgrade is required")
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "websocket_unavailable", "response writer does not support connection hijacking")
		return
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer connection.Close()
	if recorder, ok := w.(*statusWriter); ok {
		recorder.status = http.StatusSwitchingProtocols
	}
	acceptHash := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(acceptHash[:]) + "\r\n\r\n")
	if err := buffered.Flush(); err != nil {
		return
	}
	presence := protocol.Presence{TaskID: participant.TaskID, ParticipantID: participant.ID, AccountID: claims.AccountID, DeviceID: r.URL.Query().Get("device_id"), AgentID: participant.AgentID, Status: "online", LastSeenAt: time.Now().UTC()}
	subscriber := s.hub.subscribe(presence)
	defer s.hub.unsubscribe(subscriber)
	s.publish(protocol.RealtimeMessage{Type: "presence.changed", TaskID: presence.TaskID, ResourceID: presence.ParticipantID, Payload: mustJSON(presence), SentAt: time.Now().UTC()})
	defer s.publish(protocol.RealtimeMessage{Type: "presence.changed", TaskID: presence.TaskID, ResourceID: presence.ParticipantID, Payload: mustJSON(map[string]any{"status": "offline"}), SentAt: time.Now().UTC()})
	if err := writeWebSocketJSON(connection, protocol.RealtimeMessage{Type: "connected", TaskID: presence.TaskID, Payload: mustJSON(map[string]any{"cursor_fallback": "/v1/tasks/" + presence.TaskID + "/events"}), SentAt: time.Now().UTC()}); err != nil {
		return
	}
	readErrors := make(chan error, 1)
	pongs := make(chan []byte, 1)
	go readWebSocket(connection, pongs, readErrors)
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			_ = writeWebSocketFrame(connection, 0x8, nil)
			return
		case <-readErrors:
			return
		case payload := <-pongs:
			if err := writeWebSocketFrame(connection, 0xA, payload); err != nil {
				return
			}
		case message := <-subscriber.messages:
			if !s.realtimeAccessValid(r, claims, presence.TaskID) {
				return
			}
			if err := writeWebSocketJSON(connection, message); err != nil {
				return
			}
		case <-ping.C:
			if !s.realtimeAccessValid(r, claims, presence.TaskID) {
				return
			}
			if err := writeWebSocketFrame(connection, 0x9, []byte("continuity")); err != nil {
				return
			}
		}
	}
}

// A socket must not preserve stale tenant membership, revoked API keys, or an
// expired browser session. Check before sending any subsequent task payload.
func (s *Server) realtimeAccessValid(r *http.Request, c auth.Claims, taskID string) bool {
	if c.ExpiresAt > 0 && time.Now().Unix() >= c.ExpiresAt {
		return false
	}
	if c.APIKeyID != "" {
		token, err := auth.Bearer(r.Header.Get("Authorization"))
		if err != nil {
			return false
		}
		if _, err = s.store.AuthenticateAPIKey(r.Context(), auth.HashOpaque(token)); err != nil {
			return false
		}
	}
	if r.Header.Get("Authorization") == "" {
		cookie, err := r.Cookie(portalCookie)
		if err != nil {
			return false
		}
		if _, err = s.store.GetPortalSession(r.Context(), auth.HashOpaque(cookie.Value)); err != nil {
			return false
		}
	}
	allowed, err := s.taskAccessAllowed(r, c, taskID, protocol.PermissionRead)
	return err == nil && allowed
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	return hijacker.Hijack()
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeWebSocketJSON(connection net.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeWebSocketFrame(connection, 0x1, payload)
}

func writeWebSocketFrame(connection net.Conn, opcode byte, payload []byte) error {
	if len(payload) > 1<<20 {
		return errors.New("websocket frame exceeds 1 MiB")
	}
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(len(payload)>>24), byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload)))
	}
	if _, err := connection.Write(header); err != nil {
		return err
	}
	_, err := connection.Write(payload)
	return err
}

func readWebSocket(connection net.Conn, pings chan<- []byte, result chan<- error) {
	for {
		header := make([]byte, 2)
		if _, err := io.ReadFull(connection, header); err != nil {
			result <- err
			return
		}
		opcode := header[0] & 0x0f
		if header[0]&0x80 == 0 || header[1]&0x80 == 0 {
			result <- errors.New("fragmented or unmasked client frame")
			return
		}
		length := uint64(header[1] & 0x7f)
		if length == 126 {
			buffer := make([]byte, 2)
			if _, err := io.ReadFull(connection, buffer); err != nil {
				result <- err
				return
			}
			length = uint64(binary.BigEndian.Uint16(buffer))
		} else if length == 127 {
			buffer := make([]byte, 8)
			if _, err := io.ReadFull(connection, buffer); err != nil {
				result <- err
				return
			}
			length = binary.BigEndian.Uint64(buffer)
		}
		if length > 64<<10 {
			result <- errors.New("client websocket frame exceeds 64 KiB")
			return
		}
		mask := make([]byte, 4)
		if _, err := io.ReadFull(connection, mask); err != nil {
			result <- err
			return
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(connection, payload); err != nil {
			result <- err
			return
		}
		for index := range payload {
			payload[index] ^= mask[index%4]
		}
		switch opcode {
		case 0x8:
			result <- io.EOF
			return
		case 0x9:
			select {
			case pings <- payload:
			default:
			}
		case 0xA, 0x1, 0x2:
		default:
			result <- errors.New("unsupported websocket opcode")
			return
		}
	}
}

func validWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}

func headerContains(value, wanted string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(item), wanted) {
			return true
		}
	}
	return false
}
