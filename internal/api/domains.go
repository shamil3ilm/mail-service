package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/dnsx"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

type domainDTO struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	TeamID          string         `json:"team_id"`
	DKIMSelector    string         `json:"dkim_selector"`
	SPFVerifiedAt   *time.Time     `json:"spf_verified_at,omitempty"`
	DKIMVerifiedAt  *time.Time     `json:"dkim_verified_at,omitempty"`
	DMARCVerifiedAt *time.Time     `json:"dmarc_verified_at,omitempty"`
	MXVerifiedAt    *time.Time     `json:"mx_verified_at,omitempty"`
	AutoVerified    bool           `json:"auto_verified"`
	CreatedAt       time.Time      `json:"created_at"`
	Records         []dnsx.Record  `json:"records,omitempty"`
	Verdicts        []dnsx.Verdict `json:"verdicts,omitempty"`
}

func toDomainDTO(d *storage.Domain, records []dnsx.Record, verdicts []dnsx.Verdict) domainDTO {
	return domainDTO{
		ID: d.ID, Name: d.Name, TeamID: d.TeamID,
		DKIMSelector:    d.DKIMSelector,
		SPFVerifiedAt:   d.SPFVerifiedAt,
		DKIMVerifiedAt:  d.DKIMVerifiedAt,
		DMARCVerifiedAt: d.DMARCVerifiedAt,
		MXVerifiedAt:    d.MXVerifiedAt,
		AutoVerified:    d.AutoVerified,
		CreatedAt:       d.CreatedAt,
		Records:         records,
		Verdicts:        verdicts,
	}
}

type createDomainReq struct {
	Name string `json:"name"`
}

// currentTeamID resolves the primary team for the request's user. Today
// every user has exactly one team (the personal one seeded at bootstrap /
// signup) so we return the first. Multi-team UX is a Day 9+ concern.
func (s *Server) currentTeamID(ctx context.Context, u *storage.User) (string, error) {
	teams, err := s.Store.ListTeamsForUser(ctx, u.ID)
	if err != nil {
		return "", err
	}
	if len(teams) == 0 {
		return "", errors.New("user has no team")
	}
	return teams[0].ID, nil
}

// listDomains returns every domain belonging to the caller's team, with
// their target DNS records pre-computed so the UI can show them without
// a second round-trip.
func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	teamID, err := s.currentTeamID(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	list, err := s.Store.ListDomainsForTeam(r.Context(), teamID)
	if err != nil {
		s.Logger.Error("list domains", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	out := make([]domainDTO, 0, len(list))
	for _, d := range list {
		records := dnsx.RecordsFor(d.Name, keyFromDomain(d), s.dnsPolicy())
		out = append(out, toDomainDTO(d, records, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

// createDomain provisions a new domain: generates a DKIM keypair, stores
// the private key, and returns the records the user must publish.
func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req createDomainReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}

	teamID, err := s.currentTeamID(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	key, err := dnsx.GenerateDKIM("ms1")
	if err != nil {
		s.Logger.Error("dkim gen", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "dkim generation failed")
		return
	}

	d := &storage.Domain{
		ID:             newAPIID("dom"),
		TeamID:         teamID,
		Name:           name,
		DKIMSelector:   key.Selector,
		DKIMPublicKey:  key.PublicB64,
		DKIMPrivateKey: key.PrivatePEM,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.Store.InsertDomain(r.Context(), d); err != nil {
		s.Logger.Error("insert domain", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	records := dnsx.RecordsFor(d.Name, key, s.dnsPolicy())

	// Auto-publish to the configured DNS backend if one is wired up.
	// Best-effort: publish failures log a warning but don't fail the
	// request — the domain row exists, the operator can retry manually.
	if s.DNSPublisher != nil && s.DNSPublisher.Name() != "manual" {
		if err := s.DNSPublisher.Publish(r.Context(), d.Name, records); err != nil {
			s.Logger.Warn("dns publish",
				slog.String("publisher", s.DNSPublisher.Name()),
				slog.String("domain", d.Name),
				slog.String("err", err.Error()),
			)
		} else {
			s.Logger.Info("dns published",
				slog.String("publisher", s.DNSPublisher.Name()),
				slog.String("domain", d.Name),
				slog.Int("records", len(records)),
			)
		}
	}

	writeJSON(w, http.StatusCreated, toDomainDTO(d, records, nil))
}

// verifyDomain runs a live DNS check for each expected record and persists
// the timestamp for every one that passes. Returns per-record verdicts so
// the UI can show a green/red row.
func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	teamID, err := s.currentTeamID(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	id := chi.URLParam(r, "id")
	d, err := s.Store.GetDomain(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) || (d != nil && d.TeamID != teamID) {
		writeErr(w, http.StatusNotFound, "domain not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	records := dnsx.RecordsFor(d.Name, keyFromDomain(d), s.dnsPolicy())

	// Two-stage verification:
	//   1. Ask the DNS publisher (privatedns) whether it has the records.
	//      This confirms our OWN authoritative DNS is correct.
	//   2. Fall back to a live public DNS lookup to confirm the world
	//      actually sees the records (registrar delegation is working +
	//      DNS caches have propagated).
	// Merge: a record is considered verified if EITHER stage confirms it.
	verdicts := dnsx.Verify(r.Context(), nil /* net.DefaultResolver */, records)
	if s.DNSPublisher != nil && s.DNSPublisher.Name() != "manual" {
		if pubVerdicts, err := s.DNSPublisher.Verify(r.Context(), d.Name, records); err == nil {
			verdicts = mergeVerdicts(verdicts, pubVerdicts)
		} else {
			s.Logger.Debug("dns publisher verify",
				slog.String("publisher", s.DNSPublisher.Name()),
				slog.String("err", err.Error()),
			)
		}
	}

	now := time.Now().UTC()
	for i, v := range verdicts {
		if !v.Verified {
			continue
		}
		switch records[i].Kind {
		case "spf":
			d.SPFVerifiedAt = &now
		case "dkim":
			d.DKIMVerifiedAt = &now
		case "dmarc":
			d.DMARCVerifiedAt = &now
		case "mx":
			d.MXVerifiedAt = &now
		}
	}
	if err := s.Store.UpdateDomainVerification(r.Context(), d); err != nil {
		s.Logger.Error("update verification", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	writeJSON(w, http.StatusOK, toDomainDTO(d, records, verdicts))
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	teamID, err := s.currentTeamID(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteDomain(r.Context(), id, teamID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "domain not found")
			return
		}
		s.Logger.Error("delete domain", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dnsPolicy returns the current server's DNS policy defaults. Kept as a
// method so future config-driven overrides (custom MX target, DMARC ruf)
// flow through without changing every handler.
func (s *Server) dnsPolicy() dnsx.PolicyConfig {
	return dnsx.PolicyConfig{
		// Left empty: dnsx defaults kick in ("mail.<domain>", p=quarantine, etc).
	}
}

// keyFromDomain reconstructs the DKIM key struct we need for RecordsFor.
// We don't include the private key in the shape that's exposed to callers.
func keyFromDomain(d *storage.Domain) *dnsx.DKIMKey {
	return &dnsx.DKIMKey{
		Selector:   d.DKIMSelector,
		PublicB64:  d.DKIMPublicKey,
		PrivatePEM: "", // never returned
	}
}

// mergeVerdicts combines two verdict slices (same order, same length).
// A record is Verified when EITHER slice says so — we want the union of
// public-DNS and publisher-side observations. Kind + Observed carry
// through from whichever slice reported them.
func mergeVerdicts(a, b []dnsx.Verdict) []dnsx.Verdict {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	out := make([]dnsx.Verdict, n)
	for i := 0; i < n; i++ {
		out[i] = a[i]
		if !out[i].Verified && b[i].Verified {
			out[i].Verified = true
			if len(out[i].Observed) == 0 {
				out[i].Observed = b[i].Observed
			}
		}
	}
	return out
}
