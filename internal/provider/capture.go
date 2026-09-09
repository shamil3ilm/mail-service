// Capture relay: pretend to send, but actually loop the message back into
// local storage so it appears in the dashboard immediately. Used in local
// mode and by tests. Zero network activity — sent mail never leaves the box.
//
// Rationale: this is what every dev wants when running against a local
// mail service — "send" via my app, immediately see the message in my
// inbox as if it were received. Mimics Mailpit's "release" flow but from
// the sending side.

package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/router"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

// Capture is a Relay that writes to the local storage instead of sending.
type Capture struct {
	Store  storage.Store
	Raw    *rawstore.Store
	Router *router.Router
	Bus    events.Bus // may be nil
}

// Name returns the provider identifier for logs and metrics.
func (c *Capture) Name() string { return "capture" }

// Send buffers the raw message once, then for each recipient resolves the
// mailbox, writes a raw copy under a fresh id, and inserts a metadata row.
// This is the same pipeline the SMTP inbound listener uses in Data() — it's
// duplicated deliberately so a change to one path doesn't silently diverge
// the other; a future refactor can lift a shared "persist" helper if the
// duplication becomes painful.
func (c *Capture) Send(ctx context.Context, req *SendRequest) (*SendResult, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	if len(req.To)+len(req.Cc)+len(req.Bcc) == 0 {
		return nil, fmt.Errorf("no recipients")
	}

	// Buffer the message once — Raw is (a) big and (b) needs to be written
	// per-recipient so the DB row and file path are trivially 1:1.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, req.Raw); err != nil {
		return nil, fmt.Errorf("read raw: %w", err)
	}

	now := time.Now().UTC()
	result := &SendResult{
		ProviderMessageID: newProviderID(),
	}

	recipients := make([]string, 0, len(req.To)+len(req.Cc)+len(req.Bcc))
	recipients = append(recipients, req.To...)
	recipients = append(recipients, req.Cc...)
	recipients = append(recipients, req.Bcc...)

	for _, addr := range recipients {
		mb, err := c.Router.Resolve(ctx, addr)
		if err != nil {
			result.Rejected = append(result.Rejected, addr)
			continue
		}

		msgID := newProviderID()
		rawPath, size, err := c.Raw.Write(msgID, now, bytes.NewReader(buf.Bytes()))
		if err != nil {
			result.Rejected = append(result.Rejected, addr)
			continue
		}

		m := &storage.Message{
			ID:          msgID,
			MailboxID:   mb.ID,
			FromAddr:    req.From,
			ToAddrs:     []string{addr},
			Subject:     req.Subject,
			RawPath:     rawPath,
			Size:        size,
			ReceivedAt:  now,
			BodyPreview: req.BodyText,
		}
		if err := c.Store.InsertMessage(ctx, m); err != nil {
			result.Rejected = append(result.Rejected, addr)
			continue
		}

		// Persist attachments so they're queryable via /messages/:id/attachments.
		// Best-effort: DB is source of truth, orphaned files get swept later.
		for _, att := range req.Attachments {
			attID := "att_" + strings.TrimPrefix(newProviderID(), "msg_")
			apath, asize, werr := c.Raw.WriteAttachment(attID, now, bytes.NewReader(att.Content))
			if werr != nil {
				continue
			}
			mime := att.ContentType
			if mime == "" {
				mime = "application/octet-stream"
			}
			_ = c.Store.InsertAttachment(ctx, &storage.Attachment{
				ID:        attID,
				MessageID: msgID,
				Filename:  att.Filename,
				MimeType:  mime,
				Size:      asize,
				Path:      apath,
			})
		}

		result.Accepted = append(result.Accepted, addr)

		if c.Bus != nil {
			c.Bus.Publish(events.Event{
				Type:      events.MessageReceived,
				MessageID: msgID,
				MailboxID: mb.ID,
				Subject:   req.Subject,
				FromAddr:  req.From,
				ToAddr:    addr,
				At:        now,
			})
		}
	}

	if len(result.Accepted) == 0 {
		return result, fmt.Errorf("%w: all recipients rejected", ErrPermanent)
	}
	return result, nil
}

func newProviderID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "msg_" + hex.EncodeToString(b)
}
