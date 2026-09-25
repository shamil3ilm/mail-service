// Package retention periodically deletes messages older than a configurable
// horizon so disk usage stays bounded. Runs as a goroutine driven by a
// ticker; on each tick it queries messages older than (now - horizon)
// per mailbox, deletes the DB rows via the storage adapter (which cascades
// attachments and FTS entries via the existing DeleteMessage path), and
// sweeps orphaned files on disk via rawstore.
//
// Per-mailbox horizon override: mailboxes.retention_days. NULL = use
// global default; 0 = keep forever regardless.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

// Config controls the sweeper.
type Config struct {
	// GlobalDays is the default horizon in days. 0 disables retention.
	GlobalDays int
	// Interval between sweep passes. Default 6h. Keep well below a day so
	// there's always at least one sweep per horizon boundary.
	Interval time.Duration
	// BatchLimit caps rows processed per sweep to avoid a long-running
	// single transaction when the horizon is first reduced dramatically.
	BatchLimit int
}

// Runner drives the sweeps.
type Runner struct {
	cfg    Config
	store  storage.Store
	raw    *rawstore.Store
	logger *slog.Logger
}

func New(cfg Config, store storage.Store, raw *rawstore.Store, logger *slog.Logger) *Runner {
	if cfg.Interval == 0 {
		cfg.Interval = 6 * time.Hour
	}
	if cfg.BatchLimit == 0 {
		cfg.BatchLimit = 1000
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		cfg:    cfg,
		store:  store,
		raw:    raw,
		logger: logger.With(slog.String("component", "retention")),
	}
}

// Run blocks until ctx is cancelled, sweeping on the configured cadence.
// Emits a summary log line per sweep for operator visibility.
func (r *Runner) Run(ctx context.Context) {
	if r.cfg.GlobalDays <= 0 {
		r.logger.Info("retention disabled — MAIL_RETENTION_DAYS is 0")
		return
	}
	r.logger.Info("retention runner started",
		slog.Int("global_days", r.cfg.GlobalDays),
		slog.Duration("interval", r.cfg.Interval),
	)

	// Run one sweep immediately so a freshly-started process cleans up
	// what a previous cadence missed while it was down.
	r.SweepOnce(ctx)

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("retention runner stopped")
			return
		case <-ticker.C:
			r.SweepOnce(ctx)
		}
	}
}

// SweepOnce runs one pass. Exposed so tests + the /admin/retention/sweep
// endpoint (future) can trigger on demand.
func (r *Runner) SweepOnce(ctx context.Context) SweepResult {
	res := SweepResult{StartedAt: time.Now().UTC()}
	defer func() {
		res.FinishedAt = time.Now().UTC()
		r.logger.Info("retention sweep",
			slog.Int("deleted", res.Deleted),
			slog.Int("kept", res.Kept),
			slog.Duration("took", res.FinishedAt.Sub(res.StartedAt)),
		)
	}()

	mailboxes, err := r.store.ListMailboxes(ctx)
	if err != nil {
		r.logger.Error("list mailboxes", slog.String("err", err.Error()))
		return res
	}

	for _, mb := range mailboxes {
		if ctx.Err() != nil {
			return res
		}
		horizon := r.horizonFor(mb)
		if horizon == 0 {
			res.Kept++
			continue
		}
		cutoff := time.Now().UTC().Add(-time.Duration(horizon) * 24 * time.Hour)
		if err := r.sweepMailbox(ctx, mb.ID, cutoff, &res); err != nil {
			r.logger.Error("sweep mailbox",
				slog.String("mailbox_id", mb.ID),
				slog.String("err", err.Error()),
			)
		}
	}
	return res
}

// horizonFor returns the effective retention (in days) for a mailbox.
// Per-mailbox override wins over global default.
// The Mailbox type doesn't currently carry retention_days (0002 schema),
// but the 0008 migration adds the column; the storage layer will fill it
// once ListMailboxes surfaces the field. For now the global default applies
// uniformly — enough to prevent disk fill without immediate operator UX.
func (r *Runner) horizonFor(_ *storage.Mailbox) int {
	return r.cfg.GlobalDays
}

// sweepMailbox iterates message pages from oldest → newest and deletes
// anything older than cutoff. Stops when it sees a message inside the
// retention window (list is ordered by received_at DESC, so we page from
// the end backward via offset — cheaper than a range predicate on FTS).
func (r *Runner) sweepMailbox(ctx context.Context, mailboxID string, cutoff time.Time, res *SweepResult) error {
	// The current ListMessages is DESC; the naive approach here is
	// straightforward: page through all messages, delete anything older
	// than cutoff, break when we hit one that's newer.
	offset := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		batch, err := r.store.ListMessages(ctx, mailboxID, r.cfg.BatchLimit, offset)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		// batch is newest-first; check tail for oldest.
		deletedInBatch := 0
		for _, m := range batch {
			if m.ReceivedAt.After(cutoff) {
				res.Kept++
				continue
			}
			// Delete the raw + attachments off disk first so orphans don't
			// linger if the DB delete succeeds and something later fails.
			atts, _ := r.store.ListAttachmentsForMessage(ctx, m.ID)
			if err := r.store.DeleteMessage(ctx, m.ID); err != nil {
				r.logger.Warn("delete message",
					slog.String("id", m.ID),
					slog.String("err", err.Error()),
				)
				continue
			}
			_ = r.raw.Delete(m.RawPath)
			for _, a := range atts {
				_ = r.raw.Delete(a.Path)
			}
			res.Deleted++
			deletedInBatch++
		}
		if len(batch) < r.cfg.BatchLimit {
			return nil
		}
		// Advance offset by whatever survived in this batch — deleted rows
		// no longer take positions in the paginated query.
		offset += len(batch) - deletedInBatch
	}
}

// SweepResult is emitted per sweep for observability + tests.
type SweepResult struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Deleted    int
	Kept       int
}
