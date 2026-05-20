// Command mockmcp runs the mockmcp MCP test fixture as a standalone
// binary. It maps CLI flags onto mockmcp.Options and manages signal
// handling and graceful shutdown.
//
// Usage:
//
//	go run github.com/kagent-dev/mockmcp/cmd/mockmcp@latest --addr=:13443
//
// The streamable-HTTP handler is mounted at /mcp and the SSE handler at
// /sse; both paths are fixed. HTTPS is enabled by passing both --cert and
// --key; mismatched flags are rejected at startup.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kagent-dev/mockmcp"
)

func main() {
	addr := flag.String("addr", mockmcp.DefaultAddr, "address to listen on (e.g. :13443)")
	certPath := flag.String("cert", "", "TLS server certificate path (PEM); enables HTTPS when paired with --key")
	keyPath := flag.String("key", "", "TLS server private key path (PEM); enables HTTPS when paired with --cert")
	logHeaders := flag.Bool("log-headers", false, "log every inbound request's headers (values verbatim — test-only)")
	flag.Parse()

	server, err := mockmcp.NewServer(mockmcp.Options{
		Addr:       *addr,
		CertPath:   *certPath,
		KeyPath:    *keyPath,
		LogHeaders: *logHeaders,
	})
	if err != nil {
		log.Fatalf("mockmcp: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	baseURL, err := server.Start(ctx)
	if err != nil {
		log.Fatalf("mockmcp: start: %v", err)
	}
	log.Printf("mockmcp ready at %s", baseURL)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Stop(shutdownCtx); err != nil {
		log.Printf("mockmcp: stop: %v", err)
	}
}
