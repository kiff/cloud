package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// kiff.dev vouches for the apps allowed to make and use its passkeys (the
// desktop client contract, §2.6): Apple's apps through
// /.well-known/apple-app-site-association (webcredentials), and Android's
// through /.well-known/assetlinks.json (get_login_creds). Without them, an app
// can't make a passkey for kiff.dev, and KIFF refuses one whose origin isn't
// on its list. The apps come from the service's configuration, never the
// request: KIFF_APPLE_APP_IDS ("<Team ID>.<bundle ID>", comma separated) and
// KIFF_ANDROID_APPS ("<package>=<signing certificate SHA-256 as AA:BB:…>",
// the API's same value). Unset, the file is 404.

var (
	appleAppID     = regexp.MustCompile(`^[A-Z0-9]{10}\.[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
	androidPackage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
	certSHA256     = regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`)
)

// AndroidApp is an Android app kiff.dev vouches for.
type AndroidApp struct {
	Package     string
	Fingerprint string // the signing certificate's SHA-256, AA:BB:…
}

// ParseAppleAppIDs reads KIFF_APPLE_APP_IDS.
func ParseAppleAppIDs(s string) ([]string, error) {
	var out []string
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if !appleAppID.MatchString(id) {
			return nil, fmt.Errorf("KIFF_APPLE_APP_IDS: %q is not <Team ID>.<bundle ID>", id)
		}
		out = append(out, id)
	}
	return out, nil
}

// ParseAndroidApps reads KIFF_ANDROID_APPS.
func ParseAndroidApps(s string) ([]AndroidApp, error) {
	var out []AndroidApp
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		pkg, fp, ok := strings.Cut(part, "=")
		pkg, fp = strings.TrimSpace(pkg), strings.ToUpper(strings.TrimSpace(fp))
		if !ok || !androidPackage.MatchString(pkg) || !certSHA256.MatchString(fp) {
			return nil, fmt.Errorf("KIFF_ANDROID_APPS: %q is not <package>=<SHA-256 as AA:BB:…>", part)
		}
		out = append(out, AndroidApp{Package: pkg, Fingerprint: fp})
	}
	return out, nil
}

// AppleAppSiteAssociation serves /.well-known/apple-app-site-association for
// the apps' passkeys: 404 when no app is configured.
func AppleAppSiteAssociation(ids []string) http.Handler {
	body, _ := json.Marshal(map[string]any{"webcredentials": map[string]any{"apps": ids}})
	return wellKnownJSON(len(ids) > 0, body)
}

// AssetLinks serves /.well-known/assetlinks.json for the apps' passkeys: 404
// when no app is configured.
func AssetLinks(apps []AndroidApp) http.Handler {
	statements := make([]any, 0, len(apps))
	for _, a := range apps {
		statements = append(statements, map[string]any{
			"relation": []string{"delegate_permission/common.get_login_creds"},
			"target":   map[string]any{"namespace": "android_app", "package_name": a.Package, "sha256_cert_fingerprints": []string{a.Fingerprint}},
		})
	}
	body, _ := json.Marshal(statements)
	return wellKnownJSON(len(apps) > 0, body)
}

func wellKnownJSON(configured bool, body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !configured {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	})
}
