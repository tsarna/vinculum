package config

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
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/hcl/v2"
)

// TLSConfig holds TLS configuration for client or server connections. It is
// designed to be embedded in any block that needs TLS and decoded by gohcl.
//
// For clients:
//
//	tls {
//	  enabled              = true
//	  ca_cert              = "/etc/certs/ca.crt"   # verify server cert
//	  cert                 = "/etc/certs/client.crt"  # optional, for mTLS
//	  key                  = "/etc/certs/client.key"  # optional, for mTLS
//	  insecure_skip_verify = false
//	}
//
// For servers:
//
//	tls {
//	  enabled             = true
//	  cert                = "/etc/certs/server.crt"
//	  key                 = "/etc/certs/server.key"
//	  ca_cert             = "/etc/certs/ca.crt"  # optional, require client certs
//	  require_client_cert = true                  # optional, enforce mTLS
//	}
type TLSConfig struct {
	Enabled            bool      `hcl:"enabled,optional"`
	CACert             string    `hcl:"ca_cert,optional"`
	Cert               string    `hcl:"cert,optional"`
	Key                string    `hcl:"key,optional"`
	InsecureSkipVerify bool      `hcl:"insecure_skip_verify,optional"`
	RequireClientCert  bool      `hcl:"require_client_cert,optional"`
	SelfSigned         bool      `hcl:"self_signed,optional"`
	DefRange           hcl.Range `hcl:",def_range"`
}

// BuildTLSClientConfig constructs a *tls.Config for use as a TLS client.
// Returns nil if Enabled is false. Relative paths are resolved against baseDir.
//
// ca_cert sets the trusted CA pool (verifies the server certificate).
// cert + key provide a client certificate for mTLS.
// insecure_skip_verify disables server certificate verification.
func (t *TLSConfig) BuildTLSClientConfig(baseDir string) (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil
	}

	cfg := &tls.Config{
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // controlled by explicit config
	}

	if t.CACert != "" {
		pool, err := loadCertPool(baseDir, t.CACert)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}

	if err := loadCertKeyPair(baseDir, t.Cert, t.Key, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// BuildTLSServerConfig constructs a *tls.Config for use as a TLS server.
// Returns nil if Enabled is false. Relative paths are resolved against baseDir.
//
// cert + key are required (the server's certificate).
// ca_cert sets the CA pool used to verify client certificates.
// require_client_cert enables mTLS (RequireAndVerifyClientCert).
func (t *TLSConfig) BuildTLSServerConfig(baseDir string) (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil
	}

	cfg := &tls.Config{}

	if t.SelfSigned {
		if t.Cert != "" || t.Key != "" {
			return nil, fmt.Errorf("tls: self_signed is mutually exclusive with cert and key")
		}
		cert, err := generateSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("tls: generate self-signed cert: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	} else {
		if t.Cert == "" || t.Key == "" {
			return nil, fmt.Errorf("tls: cert and key are required for server TLS (or use self_signed = true)")
		}
		if err := loadCertKeyPair(baseDir, t.Cert, t.Key, cfg); err != nil {
			return nil, err
		}
	}

	if t.CACert != "" {
		pool, err := loadCertPool(baseDir, t.CACert)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
	}

	if t.RequireClientCert {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return cfg, nil
}

// generateSelfSignedCert creates an ephemeral ECDSA P-256 certificate valid for
// localhost. It is intended for development and testing only.
func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		NotBefore:    now,
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return tls.X509KeyPair(certPEM, keyPEM)
}

// loadCertPool reads a PEM file and returns a certificate pool.
func loadCertPool(baseDir, caPath string) (*x509.CertPool, error) {
	path := resolvePath(baseDir, caPath)
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tls: read ca_cert %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tls: no valid certificates found in ca_cert %q", path)
	}
	return pool, nil
}

// loadCertKeyPair loads cert+key into cfg.Certificates. No-op if both are empty.
func loadCertKeyPair(baseDir, certPath, keyPath string, cfg *tls.Config) error {
	if certPath == "" && keyPath == "" {
		return nil
	}
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("tls: cert and key must both be set")
	}
	cert, err := tls.LoadX509KeyPair(resolvePath(baseDir, certPath), resolvePath(baseDir, keyPath))
	if err != nil {
		return fmt.Errorf("tls: load cert/key: %w", err)
	}
	cfg.Certificates = []tls.Certificate{cert}
	return nil
}

// resolvePath returns path as-is if absolute, otherwise joins it with baseDir.
func resolvePath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}
