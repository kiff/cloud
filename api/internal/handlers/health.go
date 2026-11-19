package handlers

import "net/http"

// Health returns 200 OK with a tiny body. Used by the load balancer
// and uptime checks; does not depend on any external state.
func Health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}
