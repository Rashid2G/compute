package pki

import (
	"crypto/ed25519"
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
)

// CA represents an internal Certificate Authority used for master-node mTLS.
type CA struct {
	cert       *x509.Certificate
	certPEM    []byte
	privateKey ed25519.PrivateKey
	keyPEM     []byte
}

// LoadOrCreateCA loads the CA certificate and private key from dataDir, or creates a new one.
func LoadOrCreateCA(dataDir string) (*CA, error) {
	caCertPath := filepath.Join(dataDir, "ca.crt")
	caKeyPath := filepath.Join(dataDir, "ca.key")

	if certPEM, err := os.ReadFile(caCertPath); err == nil {
		keyPEM, err := os.ReadFile(caKeyPath)
		if err != nil {
			return nil, fmt.Errorf("found ca.crt but failed to read ca.key: %w", err)
		}

		block, _ := pem.Decode(certPEM)
		if block == nil {
			return nil, fmt.Errorf("failed to decode ca.crt pem block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse ca certificate: %w", err)
		}

		keyBlock, _ := pem.Decode(keyPEM)
		if keyBlock == nil {
			return nil, fmt.Errorf("failed to decode ca.key pem block")
		}
		privKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse ca private key: %w", err)
		}

		edKey, ok := privKey.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("expected ed25519 private key for CA")
		}

		return &CA{
			cert:       cert,
			certPEM:    certPEM,
			privateKey: edKey,
			keyPEM:     keyPEM,
		}, nil
	}

	// Create new CA
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 key for CA: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("failed to generate CA serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "Compute Internal Root CA",
			Organization: []string{"Compute"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 years
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal CA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create CA directory: %w", err)
	}
	if err := os.WriteFile(caCertPath, certPEM, 0644); err != nil {
		return nil, fmt.Errorf("failed to write ca.crt: %w", err)
	}
	if err := os.WriteFile(caKeyPath, keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("failed to write ca.key: %w", err)
	}

	parsedCert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated CA cert: %w", err)
	}

	return &CA{
		cert:       parsedCert,
		certPEM:    certPEM,
		privateKey: priv,
		keyPEM:     keyPEM,
	}, nil
}

// CACertPEM returns the CA certificate PEM encoded.
func (ca *CA) CACertPEM() []byte {
	return ca.certPEM
}

// IssueCert creates a signed x509 certificate for a node or server with both ClientAuth and ServerAuth.
func (ca *CA) IssueCert(commonName string, ips []net.IP, dnsNames []string, validityDays int) (certPEM []byte, keyPEM []byte, serial *big.Int, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate ed25519 key for cert: %w", err)
	}

	serial, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	if validityDays <= 0 {
		validityDays = 90
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"Compute"},
		},
		NotBefore:   time.Now().Add(-10 * time.Minute),
		NotAfter:    time.Now().AddDate(0, 0, validityDays),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IPAddresses: ips,
		DNSNames:    dnsNames,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, ca.cert, pub, ca.privateKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to issue certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM, serial, nil
}

// TLSConfig creates a mutual TLS config requiring certificates signed by this CA.
func (ca *CA) TLSConfig(certPEM, keyPEM []byte, isServer bool) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to load keypair: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.certPEM) {
		return nil, fmt.Errorf("failed to append CA cert to pool")
	}

	if isServer {
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS13,
		}, nil
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
