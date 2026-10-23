package membership

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Status is a membership's lifecycle state. Suspension is reversible;
// removal is not (M4).
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusRemoved   Status = "removed"
)

// Member is one (tenant, subject) membership. The role belongs to the
// relationship, not to the person: the same subject can be admin of one
// tenant and approver of another.
type Member struct {
	ID        string
	TenantID  string
	Subject   string
	Role      Role
	Status    Status
	InvitedBy string // subject; "" for the founding admin
	CreatedAt time.Time
	UpdatedAt time.Time
	RemovedAt *time.Time
	Version   int64
}

// Active reports whether the membership grants authority now (M1, M4).
func (m Member) Active() bool { return m.Status == StatusActive }

// Invite is a pending offer of a membership. The token is never stored,
// only its SHA-256 (M6). Email says where the link went; it is not an
// authority (M5).
type Invite struct {
	ID         string
	TenantID   string
	Email      string
	Role       Role
	InvitedBy  string
	TokenHash  [32]byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	AcceptedBy string
	RevokedAt  *time.Time
}

// InviteState is where an invite is in pending → accepted | expired |
// revoked.
type InviteState string

const (
	InvitePending  InviteState = "pending"
	InviteAccepted InviteState = "accepted"
	InviteExpired  InviteState = "expired"
	InviteRevoked  InviteState = "revoked"
)

// StateAt is the invite's state as of now.
func (i Invite) StateAt(now time.Time) InviteState {
	switch {
	case i.AcceptedAt != nil:
		return InviteAccepted
	case i.RevokedAt != nil:
		return InviteRevoked
	case !now.Before(i.ExpiresAt):
		return InviteExpired
	}
	return InvitePending
}

// Governance is a tenant's separation-of-duties policy.
type Governance struct {
	TenantID           string
	SeparationOfDuties bool
	UpdatedBy          string
	UpdatedAt          time.Time
}

// Audit actions (M8).
const (
	AuditInvited        = "member.invited"
	AuditInviteRevoked  = "member.invite_revoked"
	AuditInviteAccepted = "member.invite_accepted"
	AuditRoleChanged    = "member.role_changed"
	AuditSuspended      = "member.suspended"
	AuditReactivated    = "member.reactivated"
	AuditRemoved        = "member.removed"
	AuditKeysRevoked    = "member.keys_revoked"
	AuditSoDEnabled     = "governance.sod_enabled"
	AuditSoDDisabled    = "governance.sod_disabled"
	AuditBackfilled     = "member.backfilled"
	AuditFounded        = "member.founded"
)

// AuditEvent is one row of the append-only membership trail.
type AuditEvent struct {
	ID         string
	TenantID   string
	Actor      string
	Action     string
	Target     string
	OldValue   string
	NewValue   string
	RequestID  string
	AuthMethod string
	ReauthAgeS *int
	At         time.Time
}

// Actor is who performs a membership change, as the audit trail
// records it.
type Actor struct {
	Subject    string
	AuthMethod string // session | developer
	RequestID  string
	// ReauthAgeS is the step-up age when the action required it; nil
	// when it did not.
	ReauthAgeS *int
}

// HoldAnswer records who answered a hold, with their role and whether
// separation of duties was on, so a statement line can say "approved by
// an approver" or "approved by an editor, separation of duties off".
type HoldAnswer struct {
	TenantID           string
	HoldID             string
	Answer             string
	Answerer           string
	Role               Role // "" when a key answered
	SeparationOfDuties bool
	At                 time.Time
	// Device is the phone's signed proof of the answer, nil when
	// it was answered without one (the dashboard, a key).
	Device *DeviceProof `json:",omitempty"`
}

// DeviceProof is what a phone signed to answer a hold: the device's name
// and id (KeyID: its approval device, or its session for a phone
// not yet linked to one), its public key (SPKI DER), the exact message KIFF
// verified, and the signature (base64 DER ECDSA P-256 over its SHA-256).
type DeviceProof struct {
	Name      string `json:"name,omitempty"`
	KeyID     string `json:"key_id"`
	PublicKey []byte `json:"public_key"`
	Message   string `json:"message"`
	Signature string `json:"signature"`
}

// Errors. Handlers map these onto status codes and stable error codes.
var (
	ErrNotFound          = errors.New("membership: not found")
	ErrNotMember         = errors.New("membership: not a member of this account")
	ErrInactive          = errors.New("membership: membership is not active")
	ErrAlreadyMember     = errors.New("membership: already a member of this account")
	ErrInviteNotPending  = errors.New("membership: the invitation was already used, revoked or has expired")
	ErrLastAdmin         = errors.New("membership: an account must keep at least one active admin")
	ErrLastApprover      = errors.New("membership: separation of duties is on, so the account must keep at least one active approver")
	ErrNoApprover        = errors.New("membership: separation of duties needs at least one active approver")
	ErrSelfRoleChange    = errors.New("membership: members cannot change their own role, except to step down from admin while another admin remains")
	ErrInvalidRole       = errors.New("membership: unknown role")
	ErrInvalidLifetime   = errors.New("membership: invitation lifetime is out of range for this role")
	ErrInvalidEmail      = errors.New("membership: a valid email address is required")
	ErrNoChange          = errors.New("membership: nothing to change")
	ErrInvalidTransition = errors.New("membership: that change does not apply to this membership's state")
)

// Invite lifetimes.
const (
	InviteMinLifetime          = time.Hour
	InviteMaxLifetime          = 90 * 24 * time.Hour
	InviteDefaultLifetime      = 90 * 24 * time.Hour
	AdminInviteMaxLifetime     = 72 * time.Hour
	AdminInviteDefaultLifetime = 72 * time.Hour
)

// InviteLifetime validates a requested lifetime for role, applying the
// default when requested is zero.
func InviteLifetime(role Role, requested time.Duration) (time.Duration, error) {
	maxL, def := InviteMaxLifetime, InviteDefaultLifetime
	if role == RoleAdmin {
		maxL, def = AdminInviteMaxLifetime, AdminInviteDefaultLifetime
	}
	if requested == 0 {
		return def, nil
	}
	if requested < InviteMinLifetime || requested > maxL {
		return 0, ErrInvalidLifetime
	}
	return requested, nil
}

// NewInviteToken returns a random token (32 bytes, URL-safe) and the
// SHA-256 that is stored in its place.
func NewInviteToken() (string, [32]byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [32]byte{}, err
	}
	tok := "kiff_inv_" + base64.RawURLEncoding.EncodeToString(raw[:])
	return tok, HashInviteToken(tok), nil
}

// HashInviteToken is the stored form of a token.
func HashInviteToken(token string) [32]byte {
	return sha256.Sum256([]byte(strings.TrimSpace(token)))
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func validEmail(s string) bool {
	s = strings.TrimSpace(s)
	at := strings.LastIndex(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n<>,;") && len(s) <= 320
}
