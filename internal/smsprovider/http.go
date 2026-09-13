package smsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// HTTP is a generic JSON-over-HTTPS SMS provider. Works with:
//   - Android SMS Gateway (github.com/capcom6/android-sms-gateway) on a
//     phone with your unlimited-SMS SIM — POST /message with {phoneNumbers,
//     message}. Free per message beyond your existing plan cost.
//   - Twilio: point URL at the Messages API + set BearerToken; body shape
//     is compatible with slight adjustments (see docs/sms-provider.md).
//   - Any custom HTTP-to-SMS backend you build.
//
// Response is expected to be JSON. Providers signal success with an id
// field (name configurable via IDField). Anything else lands in Error.
type HTTP struct {
	URL         string
	Method      string        // default POST
	BearerToken string        // optional; sent as Authorization: Bearer ...
	BasicAuth   string        // optional; sent as Authorization: Basic ...
	IDField     string        // JSON key holding the provider message id (default "id")
	Timeout     time.Duration // default 15s
	Store       storage.Store // captures the row before/after send
	Client      *http.Client
}

type httpProviderReqBody struct {
	From    string   `json:"from,omitempty"`
	To      []string `json:"to"`
	Message string   `json:"message"`
}

func (HTTP) Name() string { return "http" }

// Send POSTs the JSON payload to the configured URL, then persists an
// sms_messages row with the provider response. Errors classified as
// permanent (4xx) or transient (5xx / network) so callers can retry.
func (h *HTTP) Send(ctx context.Context, req *SendRequest) (*SendResult, error) {
	if h.URL == "" || h.Store == nil {
		return nil, ErrNoProvider
	}
	if req == nil || req.To == "" {
		return nil, ErrPermanent
	}

	method := h.Method
	if method == "" {
		method = http.MethodPost
	}
	timeout := h.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	idField := h.IDField
	if idField == "" {
		idField = "id"
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	now := time.Now().UTC()
	rowID := "sms_" + randHex(12)
	segs := segmentCount(req.Body)

	// Persist first, in "queued" state, so the row exists even if the HTTP
	// call panics or the client shells out. The status field is updated
	// once we hear back.
	if err := h.Store.InsertSMS(ctx, &storage.SMSMessage{
		ID:        rowID,
		Direction: "outbound",
		Provider:  "http",
		FromAddr:  req.From,
		ToAddr:    req.To,
		Body:      req.Body,
		Status:    "queued",
		Segments:  segs,
		CreatedAt: now,
	}); err != nil {
		return nil, err
	}

	body, err := json.Marshal(httpProviderReqBody{
		From:    req.From,
		To:      []string{req.To},
		Message: req.Body,
	})
	if err != nil {
		_ = h.Store.UpdateSMSStatus(ctx, rowID, "failed", "", err.Error(), nil, nil)
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, method, h.URL, bytes.NewReader(body))
	if err != nil {
		_ = h.Store.UpdateSMSStatus(ctx, rowID, "failed", "", err.Error(), nil, nil)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if h.BearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.BearerToken)
	} else if h.BasicAuth != "" {
		httpReq.Header.Set("Authorization", "Basic "+h.BasicAuth)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		_ = h.Store.UpdateSMSStatus(ctx, rowID, "failed", "", err.Error(), nil, nil)
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 {
		errMsg := strings.TrimSpace(string(respBody))
		_ = h.Store.UpdateSMSStatus(ctx, rowID, "failed", "", errMsg, nil, nil)
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: %s: %s", ErrTransient, resp.Status, errMsg)
		}
		return nil, fmt.Errorf("%w: %s: %s", ErrPermanent, resp.Status, errMsg)
	}

	providerID := extractStringField(respBody, idField)
	sentAt := time.Now().UTC()
	if err := h.Store.UpdateSMSStatus(ctx, rowID, "sent", providerID, "", &sentAt, nil); err != nil {
		return nil, err
	}
	return &SendResult{ProviderMessageID: providerID, Segments: segs}, nil
}

// extractStringField pulls a single top-level string field from a JSON
// object without full unmarshaling. Returns "" if the field is absent or
// the response isn't a JSON object.
func extractStringField(body []byte, field string) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// Ensure interfaces compile.
var _ Relay = (*HTTP)(nil)
var _ Relay = (*Capture)(nil)
var _ Relay = Noop{}
var _ = errors.New // keeps errors import when unused in build variants
