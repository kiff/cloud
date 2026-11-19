package handlers

import (
	"net/http"
	"strings"

	"github.com/kiff/cloud/web/internal/pages"
)

// Download serves /tafs/download (and /download redirects there): tafs for
// each device, the visitor's own first. The App Store badge links
// only when appStoreLive is set (KIFF_APP_STORE_LIVE); until then the iOS side
// says it's coming, with no link. A computer's app says "Coming soon"
// until pages.DesktopApps gives it a file to download.
func Download(o OfferLinks, appStoreLive bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The order depends on the device, so a cache must keep each apart.
		w.Header().Add("Vary", "User-Agent")
		v := pages.DownloadView{PlayURL: o.PlayURL, AppStoreLive: appStoreLive, Device: deviceOf(r.UserAgent()), Desktop: pages.DesktopApps()}
		_ = pages.Download(v).Render(r.Context(), w)
	})
}

// deviceOf names the device asking, from its User-Agent: "ios" for an iPhone
// or iPad that says so, "android", "macos", "windows" or "linux", or "" for
// one it can't name. The order matters: an iPhone says "like Mac OS X" and an
// Android phone says "Linux". An iPad asking for the desktop site says it's a
// Mac, and gets the Mac's page, QR code included. A Chromebook ("CrOS") runs
// none of the apps as a computer, so it isn't named.
func deviceOf(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "ios"
	case strings.Contains(ua, "Android"):
		return "android"
	case strings.Contains(ua, "Windows"):
		return "windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		return "macos"
	case strings.Contains(ua, "CrOS"):
		return ""
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "linux"
	}
	return ""
}
