// Package mockmcp is a minimal MCP test fixture. It exposes a small set of
// tools (echo, add_numbers, get_time) over the streamable-HTTP transport,
// optionally with TLS, so end-to-end tests have a non-trivial MCP server
// to point at without depending on a real upstream.
//
// The library is designed for two usage shapes:
//
//   - As an in-process fixture from Go tests: construct via NewServer,
//     call Start to begin serving and obtain the base URL, defer Stop.
//   - As a standalone binary spawned as a subprocess: see the
//     cmd/mockmcp entrypoint, which wires the same Options from flags.
//
// Transport is plaintext HTTP unless both Options.CertPath and
// Options.KeyPath are set, in which case the server serves HTTPS with the
// operator-supplied cert. Asymmetric configuration (one set, the other
// empty) is rejected by NewServer.
package mockmcp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// DefaultAddr is the listen address used when Options.Addr is empty.
	DefaultAddr = ":13443"
	// MCPPath is the fixed URL path the streamable-HTTP handler binds
	// under. Not configurable — /mcp is the MCP convention and no real-
	// world test needs a different path.
	MCPPath = "/mcp"
	// SSEPath is the fixed URL path the SSE handler binds under. Not
	// configurable for the same reason as MCPPath.
	SSEPath = "/sse"
	// implementationName is reported to MCP clients during initialization.
	implementationName = "mockmcp"
	// implementationVersion is reported to MCP clients during initialization.
	implementationVersion = "0.1.0"
)

// Options configures a Server.
type Options struct {
	// Addr is the TCP address to listen on (e.g. ":13443" or "127.0.0.1:0").
	// Empty defaults to DefaultAddr. Use ":0" to bind a random free port;
	// the actual address is reflected in the base URL returned by Start.
	Addr string

	// CertPath and KeyPath enable HTTPS when both are set. Asymmetric
	// configuration is rejected by NewServer so an operator who omits one
	// by mistake gets a fast failure instead of a silent fall-through to
	// plaintext.
	CertPath string
	KeyPath  string

	// CertPEM and KeyPEM enable HTTPS via in-memory PEM-encoded certificate
	// material. Like CertPath/KeyPath they must be set together. CertPEM
	// may contain a leaf followed by intermediates; tls.X509KeyPair
	// validates the chain. Mutually exclusive with CertPath/KeyPath —
	// NewServer rejects a configuration that mixes file-based and
	// in-memory cert input.
	CertPEM []byte
	KeyPEM  []byte

	// LogHeaders enables a middleware that logs every inbound request's
	// headers in sorted order with values verbatim. Test-only — leave off
	// in any setup that could see real credentials.
	LogHeaders bool

	// RecordRequests, when true, installs a middleware that captures the
	// method, path, headers, and body of every inbound request into an
	// internal slice. Test code reads the snapshot via Server.Requests().
	// Opt-in because body capture has a per-request memory cost.
	RecordRequests bool

	// ServerID, when non-empty, is echoed on every response as the
	// `Mockmcp-Server-Id` header (see ServerIDHeader). Useful when a test
	// runs multiple fixture instances and needs to distinguish them. Empty
	// leaves the header off.
	ServerID string

	// Logger receives header logs and startup messages. Nil falls back to
	// the standard logger.
	Logger *log.Logger
}

// ErrAlreadyStarted is returned by Server.Start when called more than once.
var ErrAlreadyStarted = errors.New("mockmcp: server already started")

// Server is the running fixture. Zero value is not usable; construct with
// NewServer.
type Server struct {
	opts      Options
	mcpServer *mcp.Server
	http      *http.Server
	listener  net.Listener
	scheme    string
	// tlsCert holds the parsed certificate when CertPEM/KeyPEM is used.
	// nil when CertPath/KeyPath is the input or TLS is disabled.
	tlsCert *tls.Certificate
	// recorder captures inbound requests when Options.RecordRequests is
	// true. Nil otherwise. Server.Requests reports a snapshot.
	recorder *requestRecorder

	startOnce sync.Once
	stopMu    sync.Mutex
	stopped   bool
}

// NewServer validates options and constructs a Server. Returns an error
// when cert/key fields are set asymmetrically, when file-based and
// in-memory cert input are mixed, or when Path and SSEPath collide.
func NewServer(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}
	pathHalfSet := (opts.CertPath == "") != (opts.KeyPath == "")
	pemHalfSet := (len(opts.CertPEM) == 0) != (len(opts.KeyPEM) == 0)
	if pathHalfSet {
		return nil, errors.New("mockmcp: CertPath and KeyPath must be set together")
	}
	if pemHalfSet {
		return nil, errors.New("mockmcp: CertPEM and KeyPEM must be set together")
	}
	pathSet := opts.CertPath != "" && opts.KeyPath != ""
	pemSet := len(opts.CertPEM) > 0 && len(opts.KeyPEM) > 0
	if pathSet && pemSet {
		return nil, errors.New("mockmcp: CertPath/KeyPath and CertPEM/KeyPEM are mutually exclusive")
	}
	s := &Server{opts: opts}
	if opts.RecordRequests {
		s.recorder = &requestRecorder{}
	}
	return s, nil
}

// Requests returns a snapshot of the requests captured since the server
// started. Returns nil when Options.RecordRequests was false.
func (s *Server) Requests() []RecordedRequest {
	if s.recorder == nil {
		return nil
	}
	return s.recorder.snapshot()
}

// Start binds the listener, registers the default tool set, and begins
// serving in a background goroutine. Returns the base URL clients should
// dial (e.g. "http://127.0.0.1:13443"). Subsequent calls return
// ErrAlreadyStarted.
//
// The ctx parameter is accepted for API symmetry with related fixtures
// (notably mockllm); the server itself runs until Stop is called.
func (s *Server) Start(_ context.Context) (string, error) {
	var (
		baseURL  string
		startErr error
		started  bool
	)
	s.startOnce.Do(func() {
		started = true
		baseURL, startErr = s.start()
	})
	if !started {
		return "", ErrAlreadyStarted
	}
	if startErr != nil {
		return "", startErr
	}
	return baseURL, nil
}

func (s *Server) start() (string, error) {
	s.mcpServer = mcp.NewServer(&mcp.Implementation{
		Name:    implementationName,
		Version: implementationVersion,
	}, nil)
	RegisterDefaultTools(s.mcpServer)

	mux := http.NewServeMux()
	// DisableLocalhostProtection: the MCP SDK returns 403 when the server
	// binds to a localhost address but the Host header is non-localhost
	// (DNS-rebinding protection). The intended deployment binds on the
	// host and is dialed from a container via host.docker.internal — a
	// non-localhost Host header that would otherwise trip the rejection.
	// Acceptable for a test fixture; production MCP servers should leave
	// the protection on.
	getServer := func(_ *http.Request) *mcp.Server { return s.mcpServer }
	mux.Handle(MCPPath, mcp.NewStreamableHTTPHandler(
		getServer,
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	))
	mux.Handle(SSEPath, mcp.NewSSEHandler(getServer, nil))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Middleware chain — order matters. Innermost wrapper runs closest to
	// the mux; outermost runs first on the way in. We want:
	//
	//   ServerID  ─►  HeaderLogging  ─►  Recorder  ─►  mux/MCP
	//
	// so the recorder captures the body before the inner handlers consume
	// it, header logging happens after recording (so a recorded snapshot
	// is taken before any in-handler mutation), and the server-id header
	// is set early enough that any inner WriteHeader inherits it.
	var handler http.Handler = mux
	if s.opts.RecordRequests {
		handler = recorderMiddleware(handler, s.recorder)
	}
	if s.opts.LogHeaders {
		handler = HeaderLoggingMiddleware(handler, s.opts.Logger)
		s.logger().Print("header logging enabled (LogHeaders=true)")
	}
	if s.opts.ServerID != "" {
		handler = serverIDMiddleware(handler, s.opts.ServerID)
	}

	pathSet := s.opts.CertPath != "" && s.opts.KeyPath != ""
	pemSet := len(s.opts.CertPEM) > 0 && len(s.opts.KeyPEM) > 0
	tlsEnabled := pathSet || pemSet

	if pemSet {
		cert, err := tls.X509KeyPair(s.opts.CertPEM, s.opts.KeyPEM)
		if err != nil {
			return "", fmt.Errorf("mockmcp: parse CertPEM/KeyPEM: %w", err)
		}
		s.tlsCert = &cert
	}

	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return "", fmt.Errorf("mockmcp: listen %s: %w", s.opts.Addr, err)
	}
	s.listener = ln
	s.http = &http.Server{Handler: handler}
	if s.tlsCert != nil {
		s.http.TLSConfig = &tls.Config{Certificates: []tls.Certificate{*s.tlsCert}}
	}

	s.scheme = "http"
	if tlsEnabled {
		s.scheme = "https"
		switch {
		case pathSet:
			s.logger().Printf("mockmcp using TLS cert %s key %s", s.opts.CertPath, s.opts.KeyPath)
		case pemSet:
			s.logger().Print("mockmcp using TLS cert/key from in-memory PEM")
		}
	}
	s.logger().Printf("mockmcp listening on %s://%s (streamable-HTTP=%s, SSE=%s)",
		s.scheme, ln.Addr().String(), MCPPath, SSEPath)

	go func() {
		var err error
		switch {
		case pathSet:
			err = s.http.ServeTLS(ln, s.opts.CertPath, s.opts.KeyPath)
		case pemSet:
			// ServeTLS with empty cert/key paths uses TLSConfig.Certificates.
			err = s.http.ServeTLS(ln, "", "")
		default:
			err = s.http.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger().Printf("mockmcp serve error: %v", err)
		}
	}()

	return fmt.Sprintf("%s://%s", s.scheme, ln.Addr().String()), nil
}

// Stop gracefully shuts down the HTTP server, draining in-flight requests
// up to the ctx deadline. Safe to call multiple times; subsequent calls
// are no-ops.
func (s *Server) Stop(ctx context.Context) error {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopped || s.http == nil {
		s.stopped = true
		return nil
	}
	s.stopped = true
	return s.http.Shutdown(ctx)
}

// Addr returns the actual listener address. Useful when Options.Addr was
// ":0" and the caller needs to discover the chosen port. Returns nil if
// Start has not yet succeeded.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

func (s *Server) logger() *log.Logger {
	if s.opts.Logger != nil {
		return s.opts.Logger
	}
	return log.Default()
}
