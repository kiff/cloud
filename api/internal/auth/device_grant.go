package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// A device grant lets a phone's session make the writes another KIFF service
// makes on its behalf, once each, for a minute after its phone signed the
// request that needs them (kiff/tafs#206): the gateway connecting the
// account's control server saves the account's contract and permit targets.
// It is issued only when KIFF accepts that signature (POST
// /v1/me/device-signature), to the session that signed, and is kept in the
// signed-request store, so every API process honours it. A stolen session
// alone, with no signature, gets none.

// DeviceGrantHeader carries a device grant on the writes it admits.
const DeviceGrantHeader = "KIFF-Device-Grant"

// DeviceGrantLife is how long a grant admits its writes.
const DeviceGrantLife = 60 * time.Second

// DeviceGrantRoutes are the writes a grant admits, each once: the gateway's
// own domain, which is the only one it writes, and its permit targets. An
// account with no gateway domain yet gets it created (POST), and then only
// a body that names the gateway's domain is admitted (grantedDomain).
var DeviceGrantRoutes = []string{
	"PUT /v1/me/domains/gateway",
	"POST /v1/me/domains",
	"PUT /v1/me/permit-targets",
}

// grantedDomain is the one domain a grant may create.
const grantedDomain = "gateway"

// createsGrantedDomain reports whether r's YAML body creates the gateway's
// domain and no other. The body is read and put back for the handler.
func createsGrantedDomain(r *http.Request) bool {
	if r.Body == nil {
		return false
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxSignedBody+1))
	if err != nil || len(b) > maxSignedBody {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	// One document only: a second could name another domain the handler
	// reads instead of the first.
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var d struct {
		Domain string `yaml:"domain"`
	}
	if dec.Decode(&d) != nil || d.Domain != grantedDomain {
		return false
	}
	var more any
	return errors.Is(dec.Decode(&more), io.EOF)
}

const deviceGrantPrefix = "kdg_"

func deviceGrantID(tenant, session, token string) []byte {
	sum := sha256.Sum256([]byte("kiff.device-grant.v1\n" + tenant + "\n" + session + "\n" + token))
	return sum[:]
}

func deviceGrantUseID(issued []byte, route string) []byte {
	sum := sha256.Sum256(append(append([]byte("kiff.device-grant.use\n"), issued...), []byte("\n"+route)...))
	return sum[:]
}

// IssueDeviceGrant records a new grant for id's phone session and returns
// its token and when it ends.
func IssueDeviceGrant(ctx context.Context, made SignedRequests, id Identity, now time.Time) (string, time.Time, error) {
	if len(id.DeviceKey) == 0 || id.DeviceSessionID == "" {
		return "", time.Time{}, errors.New("auth: a device grant is for a phone's session")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := deviceGrantPrefix + base64.RawURLEncoding.EncodeToString(raw)
	until := now.Add(DeviceGrantLife)
	fresh, err := made.Use(ctx, deviceGrantID(id.Tenant, id.DeviceSessionID, token), now, until)
	if err != nil {
		return "", time.Time{}, err
	}
	if !fresh {
		return "", time.Time{}, errors.New("auth: device grant collision")
	}
	return token, until, nil
}

// useGrant admits route for id's phone session when r carries a live grant
// issued to that session, not yet used for route. It writes the refusal.
func (p *RoutePolicy) useGrant(w http.ResponseWriter, r *http.Request, id Identity, route string) bool {
	token := strings.TrimSpace(r.Header.Get(DeviceGrantHeader))
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	if !strings.HasPrefix(token, deviceGrantPrefix) || id.DeviceSessionID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_grant_invalid"})
		return false
	}
	if route == "POST /v1/me/domains" && !createsGrantedDomain(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_grant_invalid",
			"message": "a grant creates only the gateway's domain"})
		return false
	}
	issued := deviceGrantID(id.Tenant, id.DeviceSessionID, token)
	live, err := p.made.Seen(r.Context(), issued, now)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device_grant_check_unavailable"})
		return false
	}
	if !live {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_grant_invalid",
			"message": "this grant isn't this session's, or it has ended; sign the request again"})
		return false
	}
	fresh, err := p.made.Use(r.Context(), deviceGrantUseID(issued, route), now, now.Add(DeviceGrantLife))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device_grant_check_unavailable"})
		return false
	}
	if !fresh {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "device_grant_used",
			"message": "this grant already made this write; sign the request again"})
		return false
	}
	return true
}
