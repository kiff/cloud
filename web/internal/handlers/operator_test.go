package handlers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/kiff/cloud/web/internal/session"
)

func TestRequireOperator(t *testing.T) {
	t.Parallel()

	var nextCalled bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	mw := RequireOperator([]string{"ops-1", "ops-2"})(next)

	cases := []struct {
		name       string
		subject    string // "" = no session on context
		wantStatus int
		wantNext   bool
	}{
		{"operator passes", "ops-1", http.StatusOK, true},
		{"non-operator 404", "tenant-9", http.StatusNotFound, false},
		{"no session 404", "", http.StatusNotFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled = false
			req := httptest.NewRequest(http.MethodGet, "/ops", nil)
			if tc.subject != "" {
				req = req.WithContext(withSession(req.Context(), session.Session{Subject: tc.subject}))
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			if nextCalled != tc.wantNext {
				t.Errorf("next called: got %v, want %v", nextCalled, tc.wantNext)
			}
		})
	}
}

func TestRequireOperator_EmptyAllowListRejectsAll(t *testing.T) {
	t.Parallel()
	mw := RequireOperator(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next should not be called with empty allow-list")
	}))
	req := httptest.NewRequest(http.MethodGet, "/ops", nil)
	req = req.WithContext(withSession(req.Context(), session.Session{Subject: "anyone"}))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

func TestParseOperatorSubjects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,, c ", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		if got := ParseOperatorSubjects(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseOperatorSubjects(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
