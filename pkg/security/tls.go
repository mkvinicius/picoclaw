// tls.go — TLS configuration for the PicoClaw gateway.
//
// Supports three modes:
//
//  1. Disabled (default) — plain HTTP, suitable for local-only deployments
//     behind a reverse proxy (nginx, Caddy).
//
//  2. Provided certificates — bring your own cert + key (Let's Encrypt,
//     self-signed, corporate CA).
//
//  3. Self-signed (auto) — generates a self-signed cert on first boot and
//     persists it to workspace/tls/ so it survives restarts. Suitable for
//     Raspberry Pi / SBC home deployments where getting a real cert is hard.
//
// # Configuration (config.json)
//
//	"gateway": {
//	  "tls": {
//	    "enabled": true,
//	    "cert_file": "/etc/picoclaw/tls/cert.pem",  // optional
//	    "key_file":  "/etc/picoclaw/tls/key.pem",   // optional
//	    "auto_self_signed": true                     // generate if no cert_file
//	  }
//	}
package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// TLSConfig holds TLS configuration for the gateway HTTP server.
type TLSConfig struct {
	Enabled        bool   `json:"enabled"`
	CertFile       string `json:"cert_file"`
	KeyFile        string `json:"key_file"`
	AutoSelfSigned bool   `json:"auto_self_signed"`
	// WorkspaceDir is used to persist auto-generated certs.
	WorkspaceDir string `json:"-"`
}

// BuildTLSConfig returns a *tls.Config ready to use with http.Server,
// or nil if TLS is disabled.
func BuildTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	certFile, keyFile := cfg.CertFile, cfg.KeyFile

	// Auto-generate self-signed cert if requested and no cert provided.
	if (certFile == "" || keyFile == "") && cfg.AutoSelfSigned {
		var err error
		certFile, keyFile, err = ensureSelfSignedCert(cfg.WorkspaceDir)
		if err != nil {
			return nil, fmt.Errorf("tls: auto-generate self-signed cert: %w", err)
		}
	}

	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("tls: enabled but no cert_file/key_file configured and auto_self_signed is false")
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: load cert/key: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		PreferServerCipherSuites: true,
	}, nil
}

// ApplyTLS wraps an existing http.Server with TLS, or returns it unchanged
// if TLS is disabled. It also sets up an HTTP→HTTPS redirect on httpPort if
// tlsPort != httpPort.
func ApplyTLS(srv *http.Server, cfg TLSConfig) (*http.Server, *http.Server, error) {
	tlsCfg, err := BuildTLSConfig(cfg)
	if err != nil {
		return srv, nil, err
	}
	if tlsCfg == nil {
		return srv, nil, nil // TLS disabled
	}

	srv.TLSConfig = tlsCfg

	// Build a redirect server on the plain HTTP port.
	var redirectSrv *http.Server
	if srv.Addr != "" {
		host, _, _ := net.SplitHostPort(srv.Addr)
		_, tlsPort, _ := net.SplitHostPort(srv.Addr)
		redirectSrv = &http.Server{
			Addr: fmt.Sprintf("%s:%d", host, 80),
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				target := "https://" + r.Host
				if tlsPort != "443" {
					target += ":" + tlsPort
				}
				target += r.RequestURI
				http.Redirect(w, r, target, http.StatusMovedPermanently)
			}),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
	}

	return srv, redirectSrv, nil
}

// ensureSelfSignedCert generates (or loads existing) a self-signed ECDSA cert
// in workspaceDir/tls/. Returns (certFile, keyFile, error).
func ensureSelfSignedCert(workspaceDir string) (string, string, error) {
	tlsDir := filepath.Join(workspaceDir, "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		return "", "", err
	}

	certFile := filepath.Join(tlsDir, "cert.pem")
	keyFile := filepath.Join(tlsDir, "key.pem")

	// If both files already exist and are readable, reuse them.
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
		return certFile, keyFile, nil
	}

	// Generate a new ECDSA P-256 key (fast even on Pi Zero).
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"PicoClaw Home"},
			CommonName:   "picoclaw.local",
		},
		DNSNames:  []string{"picoclaw.local", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return "", "", fmt.Errorf("create certificate: %w", err)
	}

	// Write cert.pem
	certOut, err := os.OpenFile(certFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		certOut.Close()
		return "", "", err
	}
	certOut.Close()

	// Write key.pem
	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", err
	}
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		keyOut.Close()
		return "", "", err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}); err != nil {
		keyOut.Close()
		return "", "", err
	}
	keyOut.Close()

	return certFile, keyFile, nil
}
