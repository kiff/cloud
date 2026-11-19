package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/handlers"
)

func TestKiffDevVouchesForTheConfiguredApps(t *testing.T) {
	fp := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	ids, err := handlers.ParseAppleAppIDs(" ABCDE12345.dev.kiff.tafs , ABCDE12345.dev.kiff.tafs.mac")
	if err != nil || len(ids) != 2 {
		t.Fatalf("%v %v", ids, err)
	}
	apps, err := handlers.ParseAndroidApps("dev.kiff.tafs=" + strings.ToLower(fp))
	if err != nil || len(apps) != 1 || apps[0].Fingerprint != fp {
		t.Fatalf("%v %v", apps, err)
	}
	get := func(h http.Handler) (*httptest.ResponseRecorder, any) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		var v any
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return rec, v
	}
	rec, v := get(handlers.AppleAppSiteAssociation(ids))
	apple, _ := v.(map[string]any)["webcredentials"].(map[string]any)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || len(apple["apps"].([]any)) != 2 ||
		apple["apps"].([]any)[1] != "ABCDE12345.dev.kiff.tafs.mac" {
		t.Fatalf("AASA: %d %s", rec.Code, rec.Body.String())
	}
	rec, v = get(handlers.AssetLinks(apps))
	links, _ := v.([]any)
	if rec.Code != http.StatusOK || len(links) != 1 {
		t.Fatalf("assetlinks: %d %s", rec.Code, rec.Body.String())
	}
	s := links[0].(map[string]any)
	target := s["target"].(map[string]any)
	if s["relation"].([]any)[0] != "delegate_permission/common.get_login_creds" || target["namespace"] != "android_app" ||
		target["package_name"] != "dev.kiff.tafs" || target["sha256_cert_fingerprints"].([]any)[0] != fp {
		t.Fatalf("assetlinks: %s", rec.Body.String())
	}
	// Nothing configured: nothing vouched for.
	if rec, _ := get(handlers.AppleAppSiteAssociation(nil)); rec.Code != http.StatusNotFound {
		t.Errorf("AASA with no apps: %d", rec.Code)
	}
	if rec, _ := get(handlers.AssetLinks(nil)); rec.Code != http.StatusNotFound {
		t.Errorf("assetlinks with no apps: %d", rec.Code)
	}
	for _, bad := range []string{"dev.kiff.tafs", "abcde12345.dev.kiff.tafs", "ABCDE1234.dev.kiff.tafs", "ABCDE12345.tafs"} {
		if _, err := handlers.ParseAppleAppIDs(bad); err == nil {
			t.Errorf("Apple %q parsed", bad)
		}
	}
	for _, bad := range []string{"dev.kiff.tafs", "tafs=" + fp, "dev.kiff.tafs=" + fp[3:], "dev.kiff.tafs=" + strings.ReplaceAll(fp, ":", "")} {
		if _, err := handlers.ParseAndroidApps(bad); err == nil {
			t.Errorf("Android %q parsed", bad)
		}
	}
}
