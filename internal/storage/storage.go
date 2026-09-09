// Package storage defines the persistence adapter interface.
// Concrete implementations live in subpackages (sqlite, later d1/postgres).
// Business logic depends on Store, never on a specific driver.
package storage

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Message is the persisted representation of a received or sent mail.
// Body/attachments are on disk; only metadata + fts fields live here.
type Message struct {
	ID         string
	ThreadID   string
	MailboxID  string
	FromAddr   string
	ToAddrs    []string
	CcAddrs    []string
	BccAddrs   []string
	Subject    string
	MessageID  string // RFC 5322 Message-ID header
	InReplyTo  string
	References []string
	RawPath    string // filesystem path to raw .eml
	Size       int64
	ReceivedAt time.Time
	ReadAt     *time.Time

	// BodyPreview is the plaintext body used to populate the FTS index.
	// Callers extract it from MIME (SMTP inbound) or already have it as
	// the caller-supplied text (capture provider). Not persisted as a
	// column — only fed into messages_fts.body at InsertMessage time.
	BodyPreview string
}

// Thread is one Gmail-style conversation.
type Thread struct {
	ID                string
	SubjectNormalized string
	LastAt            time.Time
	MessageCount      int
	Participants      []string
	CreatedAt         time.Time
}

// Attachment is one MIME part broken out as a downloadable file.
// Content bytes live on disk at Path — this row is only metadata.
type Attachment struct {
	ID        string
	MessageID string
	Filename  string
	MimeType  string
	Size      int64
	Path      string
}

// Mailbox is a routing rule + inbox identity.
type Mailbox struct {
	ID          string
	Address     string
	MatchType   string // exact | wildcard | regex
	DisplayName string
	Mode        string // receive | send | both
	OwnerType   string // user | team
	OwnerID     string
	CreatedAt   time.Time
}

// User is a person with credentials. Password_hash is opaque to storage.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	DisplayName  string
	IsAdmin      bool
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// Session is a server-side session record; the ID is the cookie value.
type Session struct {
	ID        string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
	UserAgent string
	IPAddr    string
}

// Team groups users and owns domains + mailboxes.
type Team struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Domain is a mail-sending identity owned by a team. Verification timestamps
// gate outbound send: we only sign + relay from fully-verified domains.
type Domain struct {
	ID              string
	TeamID          string
	Name            string
	DKIMSelector    string
	DKIMPublicKey   string // base64 RSA pubkey, ready for a TXT record
	DKIMPrivateKey  string // PEM-encoded PKCS#1 — never returned via API
	SPFVerifiedAt   *time.Time
	DKIMVerifiedAt  *time.Time
	DMARCVerifiedAt *time.Time
	MXVerifiedAt    *time.Time
	AutoVerified    bool // true for .test/.local auto-provisioned domains
	CreatedAt       time.Time
}

// Label is a Gmail-style tag applied per message and displayed per thread.
type Label struct {
	ID        string
	TeamID    string
	Name      string
	Color     string
	CreatedAt time.Time
}

// Suppression is a recipient we must not send to. Reason values follow the
// small vocabulary in 0004_suppressions.up.sql (bounce_hard, complaint, ...).
type Suppression struct {
	Address   string
	Reason    string
	Source    string // free-form provenance (Message-ID, "manual", etc.)
	CreatedAt time.Time
}

// APIKey is a bearer credential. Full secret is never persisted; only the
// prefix (for UI identification) and the hash (for authentication).
type APIKey struct {
	ID         string
	Prefix     string
	Hash       string
	UserID     string
	Name       string
	Scopes     []string
	MailboxID  string // "" = unscoped
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Store is the adapter interface. Implementations must be safe for concurrent
// use; typically a single instance is shared across the process.
type Store interface {
	// Lifecycle
	Ping(ctx context.Context) error
	Close() error

	// Health probes read a single row so the adapter can report readiness.
	Ready(ctx context.Context) error

	// Messages (thin surface for Day 1; grows over time)
	InsertMessage(ctx context.Context, m *Message) error
	GetMessage(ctx context.Context, id string) (*Message, error)
	ListMessages(ctx context.Context, mailboxID string, limit, offset int) ([]*Message, error)
	DeleteMessage(ctx context.Context, id string) error

	// Threads
	ListThreads(ctx context.Context, mailboxID string, limit, offset int) ([]*Thread, error)
	GetThread(ctx context.Context, id string) (*Thread, error)
	ListThreadMessages(ctx context.Context, threadID string) ([]*Message, error)

	// Full-text search — query is an FTS5 MATCH expression. Returns thread
	// IDs ordered by descending rank (most relevant first).
	SearchThreadIDs(ctx context.Context, ftsMatch string, limit int) ([]string, error)

	// Attachments
	InsertAttachment(ctx context.Context, a *Attachment) error
	ListAttachmentsForMessage(ctx context.Context, messageID string) ([]*Attachment, error)
	GetAttachment(ctx context.Context, id string) (*Attachment, error)

	// Mailboxes
	UpsertMailbox(ctx context.Context, m *Mailbox) error
	GetMailbox(ctx context.Context, id string) (*Mailbox, error)
	FindMailboxByAddress(ctx context.Context, addr string) (*Mailbox, error)
	ListMailboxes(ctx context.Context) ([]*Mailbox, error)

	// Users + Sessions
	CountUsers(ctx context.Context) (int, error)
	InsertUser(ctx context.Context, u *User) error
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	UpdateUserLastLogin(ctx context.Context, id string, at time.Time) error

	InsertSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	// API keys
	InsertAPIKey(ctx context.Context, k *APIKey) error
	GetAPIKeyByHash(ctx context.Context, hash string) (*APIKey, error)
	ListAPIKeysForUser(ctx context.Context, userID string) ([]*APIKey, error)
	RevokeAPIKey(ctx context.Context, id, userID string) error
	TouchAPIKey(ctx context.Context, id string, at time.Time) error

	// Teams
	InsertTeam(ctx context.Context, t *Team) error
	AddTeamMember(ctx context.Context, teamID, userID, role string) error
	ListTeamsForUser(ctx context.Context, userID string) ([]*Team, error)

	// Domains
	InsertDomain(ctx context.Context, d *Domain) error
	GetDomain(ctx context.Context, id string) (*Domain, error)
	ListDomainsForTeam(ctx context.Context, teamID string) ([]*Domain, error)
	DeleteDomain(ctx context.Context, id, teamID string) error
	UpdateDomainVerification(ctx context.Context, d *Domain) error

	// Suppressions
	UpsertSuppression(ctx context.Context, s *Suppression) error
	IsSuppressed(ctx context.Context, address string) (bool, error)
	ListSuppressions(ctx context.Context, limit, offset int) ([]*Suppression, error)
	RemoveSuppression(ctx context.Context, address string) error

	// Labels
	InsertLabel(ctx context.Context, l *Label) error
	ListLabelsForTeam(ctx context.Context, teamID string) ([]*Label, error)
	DeleteLabel(ctx context.Context, id, teamID string) error
	LabelMessage(ctx context.Context, messageID, labelID string) error
	UnlabelMessage(ctx context.Context, messageID, labelID string) error
	ListLabelsForMessage(ctx context.Context, messageID string) ([]*Label, error)
	ListLabelsForThread(ctx context.Context, threadID string) ([]*Label, error)
	ListThreadsForLabel(ctx context.Context, labelID string, limit, offset int) ([]*Thread, error)
}
