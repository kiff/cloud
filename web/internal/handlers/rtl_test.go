package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/content/country"
)

// An Arabic or Hebrew page is marked right to left and in its language, on the
// document itself: <html lang="ar" dir="rtl">, <html lang="he" dir="rtl">. No other page carries a dir attribute, so no
// left-to-right page's markup changes.
func TestArabicAndHebrewPagesAreRightToLeft(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /privacy", Privacy())
	for _, tr := range TranslatedRoutes() {
		mux.Handle("GET "+tr.Path, Translated(tr.Lang, tr.Page, OfferLinks{}))
	}
	arabic, hebrew := 0, 0
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
		if tr.Lang == "he" {
			hebrew++
			if !strings.Contains(body, `<html lang="he" dir="rtl"`) {
				t.Errorf("%s: not <html lang=\"he\" dir=\"rtl\">", tr.Path)
			}
			if got := rec.Header().Get("Content-Language"); got != "he" {
				t.Errorf("%s: Content-Language %q", tr.Path, got)
			}
			continue
		}
		if strings.Contains(strings.SplitN(body, ">", 3)[1]+">", "dir=") && tr.Lang != "ar" {
			t.Errorf("%s: a left-to-right page carries dir", tr.Path)
		}
	}
	// The Arabic country sections are right to left too.
	cm := http.NewServeMux()
	cm.Handle("GET /privacy/countries/{country}/{lang}", CountrySection())
	arc := 0
	for c, langs := range country.Served {
		for _, l := range langs {
			if l != "ar" {
				continue
			}
			arc++
			body := getPage(t, cm, "/privacy/countries/"+c+"/ar").Body.String()
			if !strings.Contains(body, `<html lang="ar" dir="rtl"`) {
				t.Errorf("/privacy/countries/%s/ar: not right to left", c)
			}
		}
	}
	if arc < 9 {
		t.Errorf("only %d Arabic country sections", arc)
	}
	if arabic != 4 {
		t.Errorf("Arabic has %d routes, want its four legal pages", arabic)
	}
	if hebrew != 4 {
		t.Errorf("Hebrew has %d routes, want its four legal pages", hebrew)
	}
	if got := getPage(t, mux, "/privacy").Body.String(); strings.Contains(got, "dir=\"rtl\"") {
		t.Error("the English Privacy page is marked right to left")
	}
	if !components.IsRightToLeft("ar") || !components.IsRightToLeft("he") || components.IsRightToLeft("en") {
		t.Error("IsRightToLeft")
	}
}
