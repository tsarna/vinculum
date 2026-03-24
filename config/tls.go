package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
)

// TLSConfig holds TLS configuration for client connections. It is designed
// to be embedded in any client block that needs TLS (Kafka, future VWS TLS, etc.)
// and decoded directly by gohcl.
//
//	tls {
//	  enabled              = true
//	  ca_cert              = "/etc/certs/ca.crt"
//	  client_cert          = "/etc/certs/client.crt"   # optional, for mTLS
//	  client_key           = "/etc/certs/client.key"   # optional, for mTLS
//	  insecure_skip_verify = false
//	}
type TLSConfig struct {
	Enabled            bool      `hcl:"enabled,optional"`
	CACert             string    `hcl:"ca_cert,optional"`
	ClientCert         string    `hcl:"client_cert,optional"`
	ClientKey          string    `hcl:"client_key,optional"`
	InsecureSkipVerify bool      `hcl:"insecure_skip_verify,optional"`
	DefRange           hcl.Range `hcl:",def_range"`
}

// BuildTLSClientConfig constructs a *tls.Config from the block. Returns nil
// if Enabled is false. Relative file paths are resolved against baseDir.
func (t *TLSConfig) BuildTLSClientConfig(baseDir string) (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil
	}

	cfg := &tls.Config{
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // controlled by explicit config
	}

	if t.CACert != "" {
		caPath := resolvePath(baseDir, t.CACert)
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("tls: read ca_cert %q: %w", caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls: no valid certificates found in ca_cert %q", caPath)
		}
		cfg.RootCAs = pool
	}

	if t.ClientCert != "" || t.ClientKey != "" {
		if t.ClientCert == "" || t.ClientKey == "" {
			return nil, fmt.Errorf("tls: client_cert and client_key must both be set for mTLS")
		}
		certPath := resolvePath(baseDir, t.ClientCert)
		keyPath := resolvePath(baseDir, t.ClientKey)
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("tls: load client cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}

// resolvePath returns path as-is if absolute, otherwise joins it with baseDir.
func resolvePath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}
