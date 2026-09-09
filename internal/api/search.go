package api

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// search: GET /api/v1/search?q=hello&limit=50
//
// Query syntax combines free text with column-scoped operators:
//   subject:invoice   — matches only in Subject
//   from:alice        — matches only in From
//   to:orders         — matches only in To
//   receipt paid      — free text, both terms must be present
//
// Operator rewriting maps the friendly names to FTS5 column names
// (from_addr, to_addrs) so the caller never has to know our schema.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50, 1, 200)

	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"query":   "",
			"threads": []threadDTO{},
		})
		return
	}

	ftsQuery := rewriteQuery(q)

	threadIDs, err := s.Store.SearchThreadIDs(r.Context(), ftsQuery, limit)
	if err != nil {
		// FTS5 rejects malformed queries with an error; degrade to empty
		// results rather than 500 so the UI shows a "no matches" state
		// while the user is still typing.
		s.Logger.Debug("fts query failed",
			slog.String("q", q),
			slog.String("fts", ftsQuery),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"query":   q,
			"threads": []threadDTO{},
		})
		return
	}

	out := make([]threadDTO, 0, len(threadIDs))
	for _, id := range threadIDs {
		t, err := s.Store.GetThread(r.Context(), id)
		if err != nil {
			continue
		}
		msgs, _ := s.Store.ListThreadMessages(r.Context(), id)
		var last *storage.Message
		if n := len(msgs); n > 0 {
			last = msgs[n-1]
		}
		labels, _ := s.Store.ListLabelsForThread(r.Context(), id)
		out = append(out, toThreadDTO(t, last, labels))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   q,
		"threads": out,
	})
}

// operatorRewrite maps user-facing operator names to the FTS5 column names
// we actually indexed. Kept as a table so adding new operators is trivial.
var operatorRewrite = map[string]string{
	"from":    "from_addr",
	"to":      "to_addrs",
	"subject": "subject",
	"body":    "body",
}

// operatorRE matches "<op>:<value>" where value is either a bare token or
// a quoted string. Captures op in group 1, quoted-value in group 2, and
// bare-value in group 3 (exactly one of 2 or 3 will be non-empty).
var operatorRE = regexp.MustCompile(`(\w+):(?:"([^"]*)"|(\S+))`)

// rewriteQuery translates user-friendly operators to FTS5 syntax and quotes
// any bare token containing an "@" (email addresses tokenise poorly without
// quoting). Unknown operators are left in place so they surface as regular
// terms — no silent behavior changes.
func rewriteQuery(q string) string {
	rewritten := operatorRE.ReplaceAllStringFunc(q, func(match string) string {
		parts := operatorRE.FindStringSubmatch(match)
		op := strings.ToLower(parts[1])
		col, ok := operatorRewrite[op]
		if !ok {
			return match // unknown operator, leave for FTS to treat as literal
		}
		// The regex has two mutually-exclusive value groups: quoted (2)
		// vs. bare (3). If the char right after the colon is a quote,
		// the user typed a phrase — preserve the quoting verbatim so FTS5
		// treats it as a phrase match, not two AND'd terms.
		wasQuoted := len(parts[0]) > len(op)+1 && parts[0][len(op)+1] == '"'
		if wasQuoted {
			return col + `:"` + strings.ReplaceAll(parts[2], `"`, `""`) + `"`
		}
		return col + ":" + ftsQuoteIfNeeded(parts[3])
	})

	// Also auto-quote any bare token containing @ so email addresses match
	// even outside operators (e.g. plain "alice@example.com" query).
	rewritten = quoteEmailTokens(rewritten)
	return rewritten
}

// ftsQuoteIfNeeded wraps s in double quotes when it contains characters
// FTS5 would tokenise inconveniently (@, ., :).
func ftsQuoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "@.:") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// quoteEmailTokens finds bare tokens containing @ and wraps them in quotes.
// Skips tokens that are already inside an operator: prefix (crude but works —
// we only look at the start of the token to detect an already-processed one).
func quoteEmailTokens(q string) string {
	fields := strings.Fields(q)
	for i, f := range fields {
		if strings.HasPrefix(f, `"`) {
			continue
		}
		if strings.Contains(f, ":") {
			continue // operator form already handled
		}
		if strings.Contains(f, "@") {
			fields[i] = `"` + f + `"`
		}
	}
	return strings.Join(fields, " ")
}
