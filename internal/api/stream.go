package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// stream is a Server-Sent Events endpoint that pushes every bus event to the
// client until the client disconnects or the process shuts down.
//
// Why SSE over WebSocket:
//   - One-way (server → client) is exactly what live inbox updates need.
//   - Pure HTTP — passes through corporate proxies that block WS upgrades.
//   - Browsers auto-reconnect via EventSource with no client code.
//   - Zero external dependencies; net/http gives us everything.
//
// Wire format (per event):
//   event: message.received
//   data: {"type":"message.received", ...}
//
// Blank line terminates each event. Comments (":\n") act as keep-alive
// pings to keep the connection open through idle proxies.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if s.Bus == nil {
		writeErr(w, http.StatusServiceUnavailable, "event bus disabled")
		return
	}

	// http.ResponseController + Flush requires the underlying writer to
	// support flushing. All modern Go HTTP servers do; the type assertion
	// is defensive.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// SSE headers per WHATWG spec. X-Accel-Buffering off tells nginx (and
	// similar reverse proxies) not to buffer — otherwise events pool and
	// arrive in bursts instead of live.
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // emit headers immediately so client's onopen fires

	ctx := r.Context()
	events, unsub := s.Bus.Subscribe()
	defer unsub()

	// Keep-alive pings prevent intermediaries (browsers, proxies) from
	// timing the connection out during idle periods. SSE comment lines
	// (`: keepalive`) are ignored by the client's EventSource parser.
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	s.Logger.Debug("sse client connected", slog.String("remote", r.RemoteAddr))
	defer s.Logger.Debug("sse client disconnected", slog.String("remote", r.RemoteAddr))

	// Announce the connection with a hello event so clients can log "connected".
	if err := writeSSE(w, flusher, "hello", map[string]any{
		"at": time.Now().UTC(),
	}); err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case evt, ok := <-events:
			if !ok {
				return // bus closed
			}
			if err := writeSSE(w, flusher, string(evt.Type), evt); err != nil {
				return
			}
		}
	}
}

// writeSSE emits one SSE frame: an event: line, a data: line with the JSON
// payload, and a terminating blank line. Any write error means the client
// went away.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
