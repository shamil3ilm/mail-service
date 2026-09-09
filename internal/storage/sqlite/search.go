package sqlite

import (
	"context"
	"strings"
)

// SearchThreadIDs runs an FTS5 MATCH query against messages_fts, groups the
// hit rows by thread, and returns thread IDs in descending relevance order.
//
// ftsMatch is an FTS5 query expression — the *caller* has already applied
// any operator rewriting (e.g. from: → from_addr:) and validated syntax.
// An invalid query bubbles up as an SQL error; the API layer turns that
// into an empty result rather than a 500.
func (s *Store) SearchThreadIDs(ctx context.Context, ftsMatch string, limit int) ([]string, error) {
	ftsMatch = strings.TrimSpace(ftsMatch)
	if ftsMatch == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	// FTS5's rank pseudo-column is the negative BM25 score; MIN keeps the
	// best hit per thread. Nested GROUP BY + ORDER BY gives us thread-level
	// ordering by "most relevant hit within the thread".
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.thread_id, MIN(f.rank) AS r
		FROM messages_fts f
		INNER JOIN messages m ON m.rowid = f.rowid
		WHERE messages_fts MATCH ?
		  AND m.thread_id != ''
		GROUP BY m.thread_id
		ORDER BY r ASC
		LIMIT ?`, ftsMatch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var id string
		var r float64
		if err := rows.Scan(&id, &r); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
