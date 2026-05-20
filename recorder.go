package mockmcp

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"time"
)

// RecordedRequest is a snapshot of an inbound HTTP request captured by the
// request recorder. Tests use these snapshots to make positive assertions
// about traffic the fixture served — "did the controller's ListTools call
// arrive?", "did this request carry the expected header?" — without
// grepping log output.
type RecordedRequest struct {
	Method     string
	Path       string
	Headers    http.Header
	Body       []byte
	ReceivedAt time.Time
}

// requestRecorder is an append-only thread-safe ring of captured requests.
// Bounded so a long-running fixture can't OOM the test process.
type requestRecorder struct {
	mu       sync.Mutex
	requests []RecordedRequest
}

func (r *requestRecorder) snapshot() []RecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RecordedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

func (r *requestRecorder) record(req RecordedRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
}

// recorderMiddleware wraps next with a handler that captures the request's
// method, path, headers, and body into rec before invoking next. The body
// is fully read into memory (so callers can assert on it later) and then
// replaced with a buffered reader so the inner handler can still read it.
//
// Body capture is bounded to the request's actual size as delivered by the
// transport; there's no per-request truncation. For SSE/streaming clients
// where the request body is empty or trivially small, the cost is
// negligible; for streamable-HTTP POSTs the JSON-RPC payload is also
// bounded by client behavior.
func recorderMiddleware(next http.Handler, rec *requestRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err == nil {
				body = b
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		// Copy headers so a later mutation by next doesn't poison the
		// recorded snapshot.
		headers := make(http.Header, len(r.Header))
		for k, v := range r.Header {
			cp := make([]string, len(v))
			copy(cp, v)
			headers[k] = cp
		}
		rec.record(RecordedRequest{
			Method:     r.Method,
			Path:       r.URL.Path,
			Headers:    headers,
			Body:       body,
			ReceivedAt: time.Now(),
		})
		next.ServeHTTP(w, r)
	})
}
