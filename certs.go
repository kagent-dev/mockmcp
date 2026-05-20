package mockmcp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// NewSelfSignedCertsForTest mints a self-signed CA, a leaf certificate
// signed by that CA, and the leaf's private key — all PEM-encoded — using
// only the Go standard library. The leaf is suitable for an HTTPS server
// whose clients verify against the returned CA.
//
// hosts are SANs embedded in the leaf certificate. Inputs that parse as
// IP literals (e.g. "127.0.0.1", "::1") land in IPAddresses; the rest
// land in DNSNames. At least one host is required.
//
// The helper is deliberately parameter-free beyond hosts and uses fixed
// cryptographic choices (ECDSA P-256, SHA-256, ~1y validity, 1h
// backdating to tolerate small verifier clock skew). Callers that need
// different parameters should mint their own certificates with
// crypto/x509 directly.
//
// Test-only. Do not embed the output into production code paths.
func NewSelfSignedCertsForTest(hosts ...string) (caPEM, certPEM, keyPEM []byte, err error) {
	if len(hosts) == 0 {
		return nil, nil, nil, fmt.Errorf("mockmcp: NewSelfSignedCertsForTest requires at least one host")
	}

	notBefore := time.Now().Add(-1 * time.Hour)
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mockmcp: generate CA key: %w", err)
	}
	caSerial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "mockmcp-test-ca"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mockmcp: sign CA: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mockmcp: parse CA: %w", err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mockmcp: generate leaf key: %w", err)
	}
	leafSerial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          leafSerial,
		Subject:               pkix.Name{CommonName: hosts[0]},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			leafTemplate.IPAddresses = append(leafTemplate.IPAddresses, ip)
			continue
		}
		leafTemplate.DNSNames = append(leafTemplate.DNSNames, h)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mockmcp: sign leaf: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mockmcp: marshal leaf key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	return caPEM, certPEM, keyPEM, nil
}

func randomSerial() (*big.Int, error) {
	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return nil, fmt.Errorf("mockmcp: random serial: %w", err)
	}
	return serial, nil
}
