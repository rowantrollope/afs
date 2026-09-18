package main

import (
	"net"
	"net/http"
	"strings"
)

// A loopback listener restricts remote sockets, but an arbitrary DNS name can
// still resolve to that listener. Without a bearer token, also restrict Host so
// a remote web origin cannot rebind its own name to the local management API.
func guardUnauthenticatedLoopbackHost(next http.Handler, token string) http.Handler {
	if token != "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		}
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			http.Error(w, "untrusted Host header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
