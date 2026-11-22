package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
)

// An Arabic page is marked right to left and in Arabic, on the document itself:
// <html lang="ar" dir="rtl">. No other page carries a dir attribute, so no
// left-to-right page's markup changes.
func TestArabicPagesAreRightToLeft(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /privacy", Privacy())
	for _, tr := range TranslatedRoutes() {
		mux.Handle("GET "+tr.Path, Translated(tr.Lang, tr.Page, OfferLinks{}))
	}
	arabic := 0
	for _, tr := range TranslatedRoutes() {
		rec := getPage(t, mux, tr.Path)
		body := rec.Body.String()
		rtl := strings.Contains(body, `<html lang="ar" dir="rtl"`)
		if tr.Lang == "ar" {
			arabic++
			if !rtl {
				t.Errorf("%s: not <html lang=\"ar\" dir=\"rtl\">", tr.Path)
			}
			if got := rec.Header().Get("Content-Language"); got != "ar" {
				t.Errorf("%s: Content-Language %q", tr.Path, got)
			}
			continue
		}
		if strings.Contains(strings.SplitN(body, ">", 3)[1]+">", "dir=") && tr.Lang != "ar" {
			t.Errorf("%s: a left-to-right page carries dir", tr.Path)
		}
	}
	if arabic != 4 {
		t.Errorf("Arabic has %d routes, want its four legal pages", arabic)
	}
	if got := getPage(t, mux, "/privacy").Body.String(); strings.Contains(got, "dir=\"rtl\"") {
		t.Error("the English Privacy page is marked right to left")
	}
	if !components.IsRightToLeft("ar") || components.IsRightToLeft("en") {
		t.Error("IsRightToLeft")
	}
}
