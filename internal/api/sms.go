package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/smsprovider"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

type smsDTO struct {
	ID          string     `json:"id"`
	Direction   string     `json:"direction"`
	Provider    string     `json:"provider"`
	ProviderID  string     `json:"provider_id,omitempty"`
	FromAddr    string     `json:"from"`
	ToAddr      string     `json:"to"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	Segments    int        `json:"segments"`
	CreatedAt   time.Time  `json:"created_at"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

func toSMSDTO(m *storage.SMSMessage) smsDTO {
	return smsDTO{
		ID: m.ID, Direction: m.Direction,
		Provider: m.Provider, ProviderID: m.ProviderID,
		FromAddr: m.FromAddr, ToAddr: m.ToAddr,
		Body: m.Body, Status: m.Status, Error: m.Error,
		Segments:    m.Segments,
		CreatedAt:   m.CreatedAt,
		SentAt:      m.SentAt,
		DeliveredAt: m.DeliveredAt,
	}
}

type sendSMSReq struct {
	From string `json:"from"`
	To   string `json:"to"`
	Body string `json:"body"`
}

// sendSMS: POST /api/v1/sms — hand off to the configured SMS provider,
// return the created row.
func (s *Server) sendSMS(w http.ResponseWriter, r *http.Request) {
	if s.SMS == nil {
		writeErr(w, http.StatusServiceUnavailable, "no SMS provider configured")
		return
	}
	var req sendSMSReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.To = strings.TrimSpace(req.To)
	req.Body = strings.TrimSpace(req.Body)
	if req.To == "" || req.Body == "" {
		writeErr(w, http.StatusBadRequest, "to and body required")
		return
	}

	result, err := s.SMS.Send(r.Context(), &smsprovider.SendRequest{
		From: req.From, To: req.To, Body: req.Body,
	})
	if err != nil {
		s.Logger.Warn("sms send",
			slog.String("provider", s.SMS.Name()),
			slog.String("err", err.Error()),
		)
		code := http.StatusBadGateway
		if errors.Is(err, smsprovider.ErrPermanent) {
			code = http.StatusUnprocessableEntity
		}
		writeErr(w, code, err.Error())
		return
	}
	// Return the freshly-persisted row so the dashboard doesn't need a
	// second fetch to render it.
	m, err := s.Store.GetSMS(r.Context(), result.ProviderMessageID)
	if err != nil {
		// Provider-side id may differ from row id (Capture uses row id;
		// HTTP uses provider's). Fall back to listing the latest outbound.
		list, _ := s.Store.ListSMS(r.Context(), "outbound", 1, 0)
		if len(list) > 0 {
			m = list[0]
		}
	}
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": result.ProviderMessageID})
		return
	}
	writeJSON(w, http.StatusOK, toSMSDTO(m))
}

// listSMS: GET /api/v1/sms?direction=outbound|inbound&limit=&offset=
func (s *Server) listSMS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	direction := q.Get("direction")
	limit := parseIntDefault(q.Get("limit"), 50, 1, 500)
	offset := parseIntDefault(q.Get("offset"), 0, 0, 1_000_000)

	list, err := s.Store.ListSMS(r.Context(), direction, limit, offset)
	if err != nil {
		s.Logger.Error("list sms", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]smsDTO, 0, len(list))
	for _, m := range list {
		out = append(out, toSMSDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sms":    out,
		"limit":  limit,
		"offset": offset,
	})
}

// getSMS: GET /api/v1/sms/:id
func (s *Server) getSMS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.Store.GetSMS(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "sms not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, toSMSDTO(m))
}
