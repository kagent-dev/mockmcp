# mockmcp — MCP test fixture

A minimal MCP server exposing three tools (`echo`, `add_numbers`, `get_time`) over both the **streamable-HTTP** and **SSE** transports, optionally with TLS. Modeled on [`mockllm`](https://github.com/kagent-dev/mockllm) — usable both as a standalone binary spawned from shell scripts and as an in-process library imported from Go tests.

Typical deployment is on a developer's host while a local container/cluster dials back via `host.docker.internal:<port>` (Mac) or `172.17.0.1:<port>` (Linux).

## Layout

| File | Purpose |
|---|---|
| `server.go` | `Server`, `Options`, `NewServer`, `Start`, `Stop`, `Requests` |
| `tools.go` | Tool parameter types and the default `echo`/`add_numbers`/`get_time` handlers (`RegisterDefaultTools`) |
| `certs.go` | `NewSelfSignedCertsForTest` — programmatic self-signed CA + leaf for HTTPS tests |
| `recorder.go` | `RecordedRequest` and the request-capture middleware |
| `middleware.go` | `HeaderLoggingMiddleware`, `ServerIDHeader`, and the server-ID middleware |
| `cmd/mockmcp/main.go` | Standalone binary entry point — flag parsing + signal handling |

## Endpoints

| Path | Handler |
|---|---|
| `/mcp` | streamable-HTTP MCP transport (constant `mockmcp.MCPPath`) |
| `/sse` | SSE MCP transport (constant `mockmcp.SSEPath`) |
| `/healthz` | 200 OK liveness probe |

Both transports are always mounted on the same listener; tests point their client at whichever path matches the protocol they're exercising. Paths are fixed to the MCP convention.

## Standalone binary

Plain HTTP on `:13443` (default — sufficient for tests that don't exercise the upstream TLS path):

```bash
go run github.com/kagent-dev/mockmcp/cmd/mockmcp@latest
```

HTTPS with a real cert (mint a CA + server cert offline first; see "TLS end-to-end" below):

```bash
go run github.com/kagent-dev/mockmcp/cmd/mockmcp@latest \
    --cert /path/to/server.crt --key /path/to/server.key
```

Flags:

| Flag | Default | Notes |
|---|---|---|
| `--addr` | `:13443` | listen address; `:0` picks a free port (logged on startup) |
| `--cert` | `` | PEM cert file; pair with `--key` to serve HTTPS |
| `--key`  | `` | PEM key file; pair with `--cert` to serve HTTPS |
| `--log-headers` | `false` | log every inbound request's headers in sorted order, values verbatim; test-only |

## In-process library

### Minimal HTTPS fixture with programmatic certs

```go
import (
    "context"

    "github.com/kagent-dev/mockmcp"
)

func TestSomething(t *testing.T) {
    caPEM, certPEM, keyPEM, err := mockmcp.NewSelfSignedCertsForTest(
        "host.docker.internal", "localhost", "127.0.0.1",
    )
    if err != nil {
        t.Fatal(err)
    }

    server, err := mockmcp.NewServer(mockmcp.Options{
        Addr:    "127.0.0.1:0",
        CertPEM: certPEM,
        KeyPEM:  keyPEM,
    })
    if err != nil {
        t.Fatal(err)
    }
    baseURL, err := server.Start(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = server.Stop(context.Background()) })

    // baseURL is e.g. "https://127.0.0.1:53210"; clients dial baseURL + "/mcp"
    // for streamable-HTTP or baseURL + "/sse" for SSE. Write `caPEM` into a
    // K8s Secret if a downstream resource (e.g. RemoteMCPServer.spec.tls.caCertSecretRef)
    // needs to verify the server cert.
    _, _ = baseURL, caPEM
}
```

### Options reference

| Field | Purpose |
|---|---|
| `Addr` | listen address (`:13443` default; `:0` picks a free port) |
| `CertPath`, `KeyPath` | HTTPS via PEM files on disk; both set together |
| `CertPEM`, `KeyPEM` | HTTPS via in-memory PEM bytes; both set together; mutually exclusive with the file pair |
| `LogHeaders` | log every inbound request's headers to `Logger` (or stderr if unset) |
| `RecordRequests` | capture inbound method/path/headers/body into an internal slice readable via `Server.Requests()` |
| `ServerID` | when non-empty, echo `Mockmcp-Server-Id: <id>` on every response (useful when a test runs multiple instances) |
| `Logger` | `*log.Logger` for startup messages and middleware logs (defaults to `log.Default()`) |

### Programmatic self-signed certificates

`mockmcp.NewSelfSignedCertsForTest(hosts ...string) (caPEM, certPEM, keyPEM []byte, err error)` mints a self-signed CA, a leaf certificate signed by that CA, and the leaf's private key — all PEM-encoded — using only the Go standard library (`crypto/ecdsa`, `crypto/x509`). Hosts that parse as IP literals land in `IPAddresses`; the rest land in `DNSNames`.

The helper is deliberately parameter-free beyond `hosts` and uses fixed cryptographic choices (ECDSA P-256, SHA-256, ~1y validity, 1h backdating to absorb verifier clock skew). Callers that need different parameters should mint their own with `crypto/x509`.

Test-only — do not embed in production code paths.

### Request recorder

```go
server, _ := mockmcp.NewServer(mockmcp.Options{
    RecordRequests: true,
})
server.Start(ctx)
// ... client makes requests ...
for _, r := range server.Requests() {
    if r.Path == "/mcp" {
        // r.Method, r.Headers, r.Body, r.ReceivedAt
    }
}
```

`Server.Requests()` returns a snapshot copy. Opt-in because body capture has a per-request memory cost; long-running fixtures that don't need it should leave `RecordRequests` false.

### Server-ID response header

```go
upA, _ := mockmcp.NewServer(mockmcp.Options{ServerID: "A", Addr: ":13443"})
upB, _ := mockmcp.NewServer(mockmcp.Options{ServerID: "B", Addr: ":13444"})
```

Every response from `upA` carries `Mockmcp-Server-Id: A` (constant `mockmcp.ServerIDHeader`); from `upB`, `Mockmcp-Server-Id: B`. Empty `ServerID` leaves the header off entirely.

### Default tool set on a custom server

`mockmcp.RegisterDefaultTools(server *mcp.Server)` is exported for callers that own their own `*mcp.Server` and want the default tool set attached to it.

## TLS end-to-end (file-based cert recipe)

For the standalone binary or for tests that prefer offline-minted certs, the openssl recipe is:

```bash
# CA private key + self-signed CA cert
openssl genrsa -out .tls/ca.key 4096
openssl req -x509 -new -nodes -key .tls/ca.key -sha256 -days 3650 \
    -out .tls/ca.crt -subj "/CN=mockmcp-ca"

# Server key + CSR + cert signed by the CA, with the SANs cluster pods
# will dial via host.docker.internal.
cat > .tls/server.ext <<'EOF'
subjectAltName = DNS:host.docker.internal, DNS:localhost, IP:127.0.0.1, IP:172.17.0.1
EOF
openssl genrsa -out .tls/server.key 2048
openssl req -new -key .tls/server.key -out .tls/server.csr \
    -subj "/CN=host.docker.internal"
openssl x509 -req -in .tls/server.csr -CA .tls/ca.crt -CAkey .tls/ca.key \
    -CAcreateserial -out .tls/server.crt -days 365 -sha256 -extfile .tls/server.ext
```

Run mockmcp with `--cert .tls/server.crt --key .tls/server.key`, create a Kubernetes Secret in the consumer's namespace from `ca.crt`, and reference that Secret from the consumer's CA bundle field.

For in-process Go tests, prefer `NewSelfSignedCertsForTest` — it avoids the openssl dependency and per-developer cert files.

## Verifying header propagation

Set `Options.LogHeaders: true` (or pass `--log-headers` to the binary) to print one log block per inbound request showing the full header set as seen at the MCP server. Useful for verifying static headers baked into client config, runtime header pass-through, or any token-rewriting layer in front of the server.

Example log line:

```
==> POST /mcp from 10.244.0.7:43210
    Accept: application/json
    Authorization: Bearer ghs_...
    Content-Type: application/json
    User-Agent: example-client/0.1.0
    X-Tenant: acme
```

Values are logged verbatim — keep `LogHeaders` off in any setup that could see real credentials. For programmatic assertions on traffic, prefer `RecordRequests` + `Server.Requests()` over grepping stderr.

## Limitations

- Three baked-in tools only; embedders that need a custom tool set should compose their own `*mcp.Server` and call `RegisterDefaultTools` (or skip it).
- DNS-rebinding protection in the MCP SDK is disabled at the streamable-HTTP handler level so cluster-pod-to-host dials via `host.docker.internal` succeed. Acceptable for a test fixture; not appropriate for a production MCP server.
- Paths are fixed: streamable-HTTP at `/mcp`, SSE at `/sse`, health at `/healthz`. Not configurable.

## License

Apache 2.0. See `LICENSE`.
