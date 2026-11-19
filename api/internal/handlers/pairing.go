package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/oklog/ulid/v2"
)

// Pairing a computer from a phone (the desktop-client design, §5):
//
//	POST /v1/me/pairings   the phone approves the computer's QR, signed by its approval key
//	POST /v1/pair/claim    (unauthenticated) the computer claims it with its device key's proof
//
// The session is minted at the claim, bound to the keys the phone approved,
// never to keys the claim sends.

// claimInterval is how often a computer may poll a claim.
const claimInterval = 3 * time.Second

// pairingPlatforms are the computers that pair.
var pairingPlatforms = map[string]bool{"windows": true, "macos": true, "linux": true}

// phonePairingKinds are the phones and tablets that pair from another phone
// (the desktop-client design, §5.8), each with the kind of key store its approval
// key is in: the Secure Enclave on iOS and iPadOS, the hardware-backed
// keystore on Android.
var phonePairingKinds = map[string]string{"ios": "enclave", "ipados": "enclave", "android": "keystore"}

// pairingDeviceKinds are the device kinds a pairing's QR may name, by platform.
var pairingDeviceKinds = map[string][]string{
	"ios":     {auth.DeviceIPhone},
	"ipados":  {auth.DeviceIPad},
	"android": {auth.DeviceAndroidPhone, auth.DeviceAndroidTablet},
}

// WithDevicePairings keeps the pairings a phone approves (§5). Without it,
// or without a device store, the pairing routes aren't served.
func WithDevicePairings(p auth.PairingStore) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.pairings = p }
}

// WithDevicePasskeys keeps a passkey computer's own credential (§2.6): its
// pairing registers it, and removing the device removes it. Without it, a
// passkey computer doesn't pair.
func WithDevicePasskeys(p auth.PasskeyStore) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.passkeys = p }
}

// WithDevicePasskeyRP sets who passkeys are for and the origins a
// registration may come from (auth.PasskeyRelyingParty); auth.KiffRelyingParty
// when not set.
func WithDevicePasskeyRP(rp auth.RelyingParty) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.passkeyRP = rp }
}

// WithDevicePairingMembers checks, at a claim, that the person who approved
// the pairing is still a member of the account.
func WithDevicePairingMembers(m interface {
	Live(ctx context.Context, tenantID, subject string) (membership.Member, error)
}) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.pairingMembers = m }
}

// refusePairing writes a refusal the phone shows: words is one plain
// sentence for the person.
func refusePairing(w http.ResponseWriter, status int, code, message, words string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message, "words": words})
}

const pairingAgain = " Show a new code on your computer and scan it again."

func (h *DeviceAuthHandler) approvePairing(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	// The route policy checks the approval key's signature once the session
	// has one; a session with none (or no device) can't pair at all.
	if with, signed := auth.SignedWithFrom(r.Context()); !signed || !with.Critical() || len(id.ApprovalKey) == 0 || id.DeviceID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
			"message": "pairing a computer needs a phone that approves, signed with its approval key behind its fingerprint"})
		return
	}
	var req struct {
		V           string  `json:"v"`
		Pub         string  `json:"pub"`
		ApprovalPub string  `json:"approval_pub"`
		Name        string  `json:"name"`
		Platform    string  `json:"platform"`
		Kind        string  `json:"kind"`
		Nonce       string  `json:"nonce"`
		Exp         string  `json:"exp"`
		KeyStore    *string `json:"key_store"`
		// DeviceKind is a phone or tablet's (iphone, ipad, android_phone,
		// android_tablet); a computer leaves it out.
		DeviceKind string `json:"device_kind"`
		// Credential is a passkey computer's own platform passkey (§5.1).
		Credential *struct {
			ID        string `json:"id"`
			PublicKey string `json:"public_key"`
		} `json:"credential"`
	}
	invalid := func(message string) {
		refusePairing(w, http.StatusBadRequest, "invalid_pairing", message, "This code can't be used to pair."+pairingAgain)
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid("the body is the QR's JSON, at most 4 KiB, with no other fields")
		return
	}
	// A computer whose device key is in software (a Mac with no Secure
	// Enclave) pairs as a view device, whatever its kind: it shows the
	// person's tafs and sends every approval to the phone, and never approves
	// itself, not even with the person's passkeys (§5.1, §2.6). It brings no
	// approval key and no credential, and its check code is over pub alone.
	software, keyStore := req.KeyStore != nil, ""
	if software {
		keyStore = auth.KeyStoreSoftware
		switch {
		case *req.KeyStore != auth.KeyStoreSoftware:
			invalid(`key_store is "software", or left out when the device key is in hardware`)
			return
		case phonePairingKinds[req.Platform] != "":
			invalid("a phone or tablet pairs with its keys in hardware: key_store is for a computer")
			return
		case req.ApprovalPub != "" || req.Credential != nil:
			invalid("a computer with key_store software pairs view-only: no approval_pub and no credential")
			return
		}
	}
	// A passkey computer pairs with its own platform passkey (Windows Hello),
	// which the pairing registers for it, or with none: it then approves
	// with the passkeys the person registered for the account (a phone's
	// passkey, a security key: POST /v1/me/passkeys).
	if !software && req.Kind == "passkey" && h.passkeys == nil {
		refusePairing(w, http.StatusBadRequest, "invalid_pairing", "key_store isn't taken yet, and passkey pairing needs KIFF's passkeys",
			"This computer approves with a passkey, which KIFF can't pair yet.")
		return
	}
	name, nameOK := cleanDeviceName(req.Name)
	switch {
	case req.V != auth.PairingMessage:
		invalid("v is " + auth.PairingMessage)
		return
	case phonePairingKinds[req.Platform] != "" && req.Kind != phonePairingKinds[req.Platform]:
		invalid("a phone or tablet pairs with kind enclave (ios, ipados) or keystore (android)")
		return
	case phonePairingKinds[req.Platform] == "" && req.Kind != "hello" && req.Kind != "enclave" && req.Kind != "passkey":
		invalid("kind is hello, enclave or passkey")
		return
	case req.DeviceKind != "" && !slices.Contains(pairingDeviceKinds[req.Platform], req.DeviceKind):
		invalid("device_kind is the phone or tablet's: iphone (ios), ipad (ipados), android_phone or android_tablet (android); a computer leaves it out")
		return
	case req.Kind != "passkey" && req.Credential != nil:
		invalid("credential comes with kind passkey")
		return
	case !pairingPlatforms[req.Platform] && phonePairingKinds[req.Platform] == "":
		invalid("platform is windows, macos, linux, ios, ipados or android")
		return
	case !nameOK || name == "":
		invalid("name is 1 to 64 characters, without control characters")
		return
	}
	pub, err := auth.ParsePairingKey(req.Pub)
	if err != nil {
		invalid("pub is a P-256 key, base64url SPKI DER without padding")
		return
	}
	// approval_pub is required for hello and enclave, and absent for
	// passkey, whose credential takes its place (in the code too, §5.2).
	var approvalPub, credentialID, credentialKey []byte
	if req.Kind == "passkey" {
		if req.ApprovalPub != "" {
			invalid("approval_pub is absent with kind passkey: its credential approves")
			return
		}
	}
	if !software && req.Kind == "passkey" && req.Credential != nil {
		var ok bool
		if credentialID, ok = auth.ParsePairingCredentialID(req.Credential.ID); !ok {
			invalid("credential.id is the WebAuthn credential id, base64url without padding, at most 1023 bytes")
			return
		}
		if credentialKey, err = auth.ParsePairingKey(req.Credential.PublicKey); err != nil {
			invalid("credential.public_key is a P-256 (ES256) key, base64url SPKI DER without padding")
			return
		}
		if bytes.Equal(pub, credentialKey) {
			invalid("credential.public_key is the passkey's own key, not pub")
			return
		}
	} else if !software && req.Kind != "passkey" {
		if approvalPub, err = auth.ParsePairingKey(req.ApprovalPub); err != nil {
			invalid("approval_pub is a P-256 key, base64url SPKI DER without padding, sent with kind hello or enclave")
			return
		}
		if bytes.Equal(pub, approvalPub) {
			invalid("approval_pub is a second key, not pub")
			return
		}
	}
	nonceHash, ok := auth.PairingNonceHash(req.Nonce)
	if !ok {
		invalid("nonce is 32 bytes, base64url without padding")
		return
	}
	exp, err := time.Parse(time.RFC3339, req.Exp)
	if err != nil {
		invalid("exp is RFC 3339")
		return
	}
	now := h.clock()
	if now.After(exp) {
		refusePairing(w, http.StatusBadRequest, "pairing_expired", "exp has passed", "This code has expired."+pairingAgain)
		return
	}
	if signedAt, ok := auth.SignatureTime(r.Header.Get(auth.DeviceSignatureHeader)); !ok || exp.After(signedAt.Add(auth.MaxPairingAhead)) {
		invalid("exp is at most 5 minutes after the signature's signed_at")
		return
	}
	used := func() {
		refusePairing(w, http.StatusConflict, "pairing_used", "this nonce was already approved", "This code was already used."+pairingAgain)
	}
	switch _, err := h.pairings.ByNonce(r.Context(), nonceHash); {
	case err == nil:
		used()
		return
	case !errors.Is(err, auth.ErrPairingNotFound):
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the pairings"})
		return
	}
	// A refusal that ends this pairing is kept against the nonce and the
	// computer's device key until the QR's exp, so that computer's claim,
	// proving that key, says so (pairing_refused) instead of waiting for a
	// code nobody will approve (§5.4). Best effort: the phone is told either
	// way.
	refuseNonce := func(reason string) {
		pubHash := sha256.Sum256(pub)
		if err := h.pairings.Refuse(r.Context(), auth.PairingRefusal{NonceHash: nonceHash, PubHash: pubHash[:], Reason: reason,
			Expires: exp.UTC(), CreatedAt: now}); err != nil {
			h.logger.WarnContext(r.Context(), "pairing: keeping the refusal failed", slog.String("reason", reason), slog.String("err", err.Error()))
		}
	}
	// registered refuses with key_registered. Only when the device key
	// itself (pub) is a device's is the refusal kept for the computer: the
	// QR isn't signed by the computer, so whoever sees it can send its nonce
	// and pub with an approval_pub or credential of their own account's, and
	// that must never tell the real computer it's already paired.
	registered := func(keep bool) {
		if keep {
			refuseNonce("key_registered")
		}
		refusePairing(w, http.StatusConflict, "key_registered", "a key is already a device's on this account",
			"This computer is already paired with this account.")
	}
	if credentialID != nil {
		switch _, err := h.passkeys.Find(r.Context(), id.Tenant, credentialID); {
		case err == nil:
			registered(false)
			return
		case !errors.Is(err, auth.ErrPasskeyNotFound):
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the passkeys"})
			return
		}
	}
	for _, key := range [][]byte{pub, approvalPub} {
		if key == nil {
			continue
		}
		switch _, err := h.devices.FindByKeyInTenant(r.Context(), id.Tenant, key); {
		case err == nil:
			registered(bytes.Equal(key, pub))
			return
		case !errors.Is(err, auth.ErrDeviceNotFound):
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the devices"})
			return
		}
	}
	// The proof runner's test phone pairs nothing outside the test tenants.
	if !h.keys.Allows(r.Context(), id.Tenant, pub, approvalPub) {
		auth.WriteKeyRestricted(w)
		return
	}
	p := auth.Pairing{ID: auth.NewPairingID(), Tenant: id.Tenant, Subject: id.Subject, ApprovedBy: id.DeviceID,
		PublicKey: pub, ApprovalKey: approvalPub, Name: name, Platform: req.Platform, Kind: req.Kind,
		CredentialID: credentialID, CredentialKey: credentialKey, DeviceKind: req.DeviceKind,
		KeyStore: keyStore, NonceHash: nonceHash, Expires: exp.UTC(), CreatedAt: now}
	switch err := h.pairings.Insert(r.Context(), p); {
	case errors.Is(err, auth.ErrPairingUsed):
		used()
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "pairing: insert failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not keep the pairing"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"pairing": map[string]string{
		"id": p.ID, "code": auth.PairingCode(pub, pairingSecondKey(p)), "expires": p.Expires.Format(time.RFC3339)}})
}

// pairedKind is the device kind a claimed pairing's device is listed with:
// the phone or tablet's own, a computer's "other".
func pairedKind(p auth.Pairing) string {
	if p.DeviceKind != "" {
		return p.DeviceKind
	}
	if p.Platform == "android" {
		return auth.DeviceAndroidPhone
	}
	if k := pairingDeviceKinds[p.Platform]; len(k) == 1 {
		return k[0]
	}
	return auth.DeviceOther
}

// credentialProven checks a claim's credential_proof against the pairing, and
// writes the refusal when it fails. Only a pairing with its own credential (a
// passkey computer's, §5.1) takes one; it must be both fields, and verify
// (auth.VerifyPairingCredential) to exactly the credential id and key the phone
// approved. With none, it's taken until PairingProofRequiredFrom. A refused
// proof is logged by its reason alone, never its bodies.
func (h *DeviceAuthHandler) credentialProven(w http.ResponseWriter, r *http.Request, p auth.Pairing, nonce, pub string, proof *struct {
	ClientDataJSON    string `json:"client_data_json"`
	AttestationObject string `json:"attestation_object"`
}, now time.Time) bool {
	refuse := func(reason, message string) bool {
		h.logger.WarnContext(r.Context(), "pairing: credential proof refused", slog.String("pairing", p.ID), slog.String("reason", reason))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential_proof_invalid", "reason": reason, "message": message})
		return false
	}
	hasCredential := p.CredentialID != nil
	switch {
	case proof == nil && !hasCredential:
		return true
	case proof != nil && !hasCredential:
		return refuse("no_credential", "this pairing has no credential of its own, so a credential_proof isn't taken")
	case proof == nil && !now.Before(PairingProofRequiredFrom):
		return refuse("required", "a passkey computer's claim carries credential_proof: {client_data_json, attestation_object}")
	case proof == nil:
		return true
	case proof.ClientDataJSON == "" || proof.AttestationObject == "":
		return refuse("malformed", "credential_proof is both client_data_json and attestation_object, base64url")
	}
	client, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(proof.ClientDataJSON, "="))
	object, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(proof.AttestationObject, "="))
	if err1 != nil || err2 != nil {
		return refuse("malformed", "credential_proof's fields are base64url")
	}
	rp := h.passkeyRP
	if rp.ID == "" {
		rp = auth.KiffRelyingParty
	}
	reg, err := auth.VerifyPairingCredential(rp, nonce, pub, client, object)
	if err != nil {
		return refuse(registrationReason(err), err.Error())
	}
	if !bytes.Equal(reg.CredentialID, p.CredentialID) {
		return refuse("credential", "the proof's credential isn't the one the phone approved")
	}
	if !bytes.Equal(reg.PublicKey, p.CredentialKey) {
		return refuse("key", "the proof's key isn't the credential key the phone approved")
	}
	return true
}

// pairingSecondKey is the key the check code covers after pub: the approval
// key, or a passkey computer's credential key (§5.2).
func pairingSecondKey(p auth.Pairing) []byte {
	if p.KeyStore == auth.KeyStoreSoftware {
		return nil
	}
	if p.Kind == "passkey" {
		return p.CredentialKey
	}
	return p.ApprovalKey
}

// claimTooSoon reports whether this nonce was polled within claimInterval,
// and records the poll. Best effort and in this process only, so it paces
// a polling computer and is never a lockout: a 32-byte nonce is what keeps
// a pairing from being guessed. Stale polls are pruned as the map grows.
func (h *DeviceAuthHandler) claimTooSoon(key string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.claimPolls) > 1024 {
		for k, at := range h.claimPolls {
			if now.Sub(at) >= claimInterval {
				delete(h.claimPolls, k)
			}
		}
	}
	last, seen := h.claimPolls[key]
	h.claimPolls[key] = now
	return seen && now.Sub(last) < claimInterval
}

// refusalProven reports whether a claim proves the computer key a refusal was
// kept for: its pub parses, sha256(pub) is the refusal's PubHash, and the
// proof verifies for that key and nonce.
func (h *DeviceAuthHandler) refusalProven(ref auth.PairingRefusal, nonce, pubB64, proof string) bool {
	pub, err := auth.ParsePairingKey(pubB64)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(pub)
	return subtle.ConstantTimeCompare(sum[:], ref.PubHash) == 1 && auth.VerifyPairClaim(pub, nonce, proof) == nil
}

// PairingProofRequiredFrom is when a passkey computer's claim must carry its
// credential's creation proof (credential_proof). Before it, a claim with no
// proof at all is still taken, one release for the desktop apps to send it; a
// proof that's sent is always checked, and a bad one is never taken as none.
var PairingProofRequiredFrom = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

func (h *DeviceAuthHandler) claimPairing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nonce string `json:"nonce"`
		Pub   string `json:"pub"`
		Proof string `json:"proof"`
		// CredentialProof is a passkey computer's creation response for its
		// own credential (the desktop client contract, §5.1), its challenge
		// over PairingCredentialMessage.
		CredentialProof *struct {
			ClientDataJSON    string `json:"client_data_json"`
			AttestationObject string `json:"attestation_object"`
		} `json:"credential_proof"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	nonceHash, ok := auth.PairingNonceHash(req.Nonce)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "message": "nonce is 32 bytes, base64url without padding"})
		return
	}
	now := h.clock()
	if h.claimTooSoon(hex.EncodeToString(nonceHash), now) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slow_down"})
		return
	}
	p, err := h.pairings.ByNonce(r.Context(), nonceHash)
	switch {
	case errors.Is(err, auth.ErrPairingNotFound):
		// Not approved. If a phone's approve of it was refused, the computer
		// whose key it named is told so, with the reason, once its claim
		// proves that key; anyone else, or a claim that proves nothing, reads
		// authorization_pending as if the phone hadn't answered. A pairing
		// that was approved after all is found above and wins.
		switch ref, rerr := h.pairings.RefusalByNonce(r.Context(), nonceHash, now); {
		case rerr == nil && h.refusalProven(ref, req.Nonce, req.Pub, req.Proof):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "pairing_refused", "reason": ref.Reason,
				"message": "the phone's approval of this code was refused (" + ref.Reason + "): show a new code and scan it again"})
		case rerr == nil || errors.Is(rerr, auth.ErrPairingNotRefused):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the pairing"})
		}
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the pairing"})
		return
	case now.After(p.Expires) || p.ClaimedAt != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	}
	// The claim's pub must be the one the phone approved, and the proof that
	// key's: a refusal here leaves the pairing to its own computer.
	pub, err := auth.ParsePairingKey(req.Pub)
	if err != nil || !bytes.Equal(pub, p.PublicKey) || auth.VerifyPairClaim(p.PublicKey, req.Nonce, req.Proof) != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid_proof",
			"message": "proof signs kiff.pair.claim.v1, the nonce and sha256(pub) hex, with the device key the phone approved"})
		return
	}
	// A passkey computer's credential proves it was made for this pairing,
	// before anything is claimed: a refusal leaves the pairing to be claimed
	// again with a good proof.
	if !h.credentialProven(w, r, p, req.Nonce, req.Pub, req.CredentialProof, now) {
		return
	}
	// Checked again at the claim, for a pairing made before the key was listed.
	if !h.keys.Allows(r.Context(), p.Tenant, p.PublicKey, p.ApprovalKey) {
		auth.WriteKeyRestricted(w)
		return
	}
	// The phone that approved it must still be one of the person's devices: a
	// stolen phone removed after approving never hands its pairing on.
	if d, err := h.devices.Get(r.Context(), p.ApprovedBy); err != nil || d.IsRevoked() || d.Tenant != p.Tenant || d.Subject != p.Subject {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approver_removed",
			"message": "the device that approved this pairing was removed: pair again from a device that approves"})
		return
	}
	// And they must still belong to the account: someone removed in those
	// minutes never gets a session.
	if h.pairingMembers != nil {
		switch m, err := h.pairingMembers.Live(r.Context(), p.Tenant, p.Subject); {
		case errors.Is(err, membership.ErrNotFound) || err == nil && !m.Active():
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "member_removed",
				"message": "the person who approved this pairing is no longer an active member of the account"})
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the account's members"})
			return
		}
	}
	switch err := h.pairings.Claim(r.Context(), p.ID, now); {
	case errors.Is(err, auth.ErrPairingClaimed):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not claim the pairing"})
		return
	}
	// Past the claim, a failure gives it back so the computer can retry.
	fail := func(what string, err error, device string) {
		h.revokeDevice(r, device, now)
		if uerr := h.pairings.Unclaim(r.Context(), p.ID); uerr != nil {
			h.logger.WarnContext(r.Context(), "pairing: unclaim failed", slog.String("pairing", p.ID), slog.String("err", uerr.Error()))
		}
		h.logger.ErrorContext(r.Context(), "pairing: "+what+" failed", slog.String("tenant", p.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not finish pairing; claim again"})
	}
	// A software-keyed computer is a view device: it approves nothing, and
	// doesn't count as one that approves.
	view := p.KeyStore == auth.KeyStoreSoftware
	passkey := p.Kind == "passkey" && !view
	device := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: p.Tenant, Subject: p.Subject, Name: p.Name,
		Platform: p.Platform, Kind: pairedKind(p), PublicKey: p.PublicKey, HasApprovalKey: !view && (len(p.ApprovalKey) > 0 || passkey),
		KeyStore: p.KeyStore, CreatedAt: now}
	if err := h.devices.Insert(r.Context(), device); err != nil {
		fail("insert device", err, "")
		return
	}
	// A passkey computer's own credential is registered for this device
	// alone, and goes with it (§2.6).
	mode := auth.ApprovalModeKey
	switch {
	case view:
		mode = auth.ApprovalModeView
	case passkey:
		mode = auth.ApprovalModePasskey
	}
	if passkey && p.CredentialID != nil {
		if h.passkeys == nil {
			fail("register passkey", errors.New("no passkey store"), device.ID)
			return
		}
		key := auth.StoredPasskey{Passkey: auth.Passkey{ID: p.CredentialID, Tenant: p.Tenant, PublicKey: p.CredentialKey},
			Subject: p.Subject, DeviceID: device.ID, CreatedAt: now}
		if err := h.passkeys.Insert(r.Context(), key); err != nil {
			fail("register passkey", err, device.ID)
			return
		}
	}
	gen, err := auth.NewDevSessionToken()
	if err != nil {
		fail("mint session", err, device.ID)
		return
	}
	sess := auth.DeveloperSession{
		ID:        ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String(),
		Subject:   p.Subject,
		Tenant:    p.Tenant,
		Label:     p.Name,
		TokenHash: gen.Hash,
		CreatedAt: now,
		ExpiresAt: now.Add(h.sessionTTL),
		DeviceID:  device.ID,

		DevicePublicKey:   p.PublicKey,
		ApprovalPublicKey: p.ApprovalKey,
		// A hello or enclave computer approves with its approval key, a
		// passkey one with its own passkey, never with its device key (§1.1).
		ApprovalMode: mode,
	}
	if err := h.sessions.Insert(r.Context(), sess); err != nil {
		fail("insert session", err, device.ID)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		deviceTokenResponse
		DeviceID string `json:"device_id"`
	}{deviceTokenResponse{AccessToken: gen.Plaintext, TokenType: "Bearer", ExpiresIn: int(h.sessionTTL.Seconds()),
		Tenant: p.Tenant, Subject: p.Subject, ApprovalMode: auth.ShownApprovalMode(mode, len(p.ApprovalKey) > 0, "")}, device.ID})
}
