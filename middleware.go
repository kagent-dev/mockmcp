package mockmcp

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
)

// ServerIDHeader is the response header populated when Options.ServerID
// is non-empty. Tests running multiple fixture instances disambiguate
// which one handled a given request by reading this header.
const ServerIDHeader = "Mockmcp-Server-Id"

// serverIDMiddleware adds the Mockmcp-Server-Id response header to every
// outbound response. Empty id means the middleware is not installed; this
// function is never called with id == "".
func serverIDMiddleware(next http.Handler, id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ServerIDHeader, id)
		next.ServeHTTP(w, r)
	})
}

// HeaderLoggingMiddleware wraps the given handler with a logger that
// prints every inbound request's headers in sorted order with values
// verbatim. Each request produces one multi-line log entry so test runs
// have deterministic, grep-able output.
//
// Test-only. Values are logged unredacted; do not enable in any setup
// that could see real credentials.
func HeaderLoggingMiddleware(next http.Handler, logger *log.Logger) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := make([]string, 0, len(r.Header))
		for k := range r.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		fmt.Fprintf(&sb, "==> %s %s from %s\n", r.Method, r.URL.RequestURI(), r.RemoteAddr)
		for _, k := range keys {
			for _, v := range r.Header[k] {
				fmt.Fprintf(&sb, "    %s: %s\n", k, v)
			}
		}
		logger.Print(sb.String())
		next.ServeHTTP(w, r)
	})
}
