package smsprovider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// Capture is the default SMS provider. It persists the SMS to storage
// with status "sent" so the dashboard reflects it, but nothing leaves the
// process. Matches the mail-side capture semantics.
type Capture struct {
	Store storage.Store
}

func (Capture) Name() string { return "capture" }

func (c *Capture) Send(ctx context.Context, req *SendRequest) (*SendResult, error) {
	if req == nil || req.To == "" {
		return nil, ErrPermanent
	}
	segs := segmentCount(req.Body)
	now := time.Now().UTC()
	id := "sms_" + randHex(12)
	m := &storage.SMSMessage{
		ID:        id,
		Direction: "outbound",
		Provider:  "capture",
		FromAddr:  req.From,
		ToAddr:    req.To,
		Body:      req.Body,
		Status:    "sent",
		Segments:  segs,
		CreatedAt: now,
		SentAt:    &now,
	}
	if err := c.Store.InsertSMS(ctx, m); err != nil {
		return nil, err
	}
	return &SendResult{ProviderMessageID: id, Segments: segs}, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
