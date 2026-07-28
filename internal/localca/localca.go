// Package localca manages a small, persistent certificate authority for
// private webport domains.
package localca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	caLifetime   = 10 * 365 * 24 * time.Hour
	leafLifetime = 90 * 24 * time.Hour
	caRenewal    = 30 * 24 * time.Hour
	leafRenewal  = 14 * 24 * time.Hour
)

// Paths identifies the generated certificate material.
type Paths struct {
	CACert    string
	CAKey     string
	Cert      string
	Key       string
	CreatedCA bool
	Changed   bool
}

// Ensure creates or renews a private CA and wildcard server certificate.
func Ensure(dir, baseDomain string) (Paths, error) {
	baseDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(baseDomain)), ".")
	if baseDomain == "" {
		return Paths{}, errors.New("base domain is required")
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return Paths{}, fmt.Errorf("create local CA directory: %w", err)
	}

	paths := Paths{
		CACert: filepath.Join(dir, "ca.crt"),
		CAKey:  filepath.Join(dir, "ca.key"),
		Cert:   filepath.Join(dir, "wildcard.crt"),
		Key:    filepath.Join(dir, "wildcard.key"),
	}

	caCert, caKey, err := loadCA(paths, baseDomain)
	if err != nil {
		caCert, caKey, err = createCA(paths, baseDomain)
		if err != nil {
			return Paths{}, err
		}
		paths.CreatedCA = true
		paths.Changed = true
	}

	if !validLeaf(paths, caCert, baseDomain) {
		if err := createLeaf(paths, caCert, caKey, baseDomain); err != nil {
			return Paths{}, err
		}
		paths.Changed = true
	}

	// Repair permissions on material from older versions without making the
	// CA private key available to Traefik.
	for path, mode := range map[string]os.FileMode{
		paths.CACert: 0644,
		paths.CAKey:  0600,
		paths.Cert:   0644,
		paths.Key:    0640,
	} {
		if err := os.Chmod(path, mode); err != nil {
			return Paths{}, fmt.Errorf("set permissions on %s: %w", path, err)
		}
	}
	return paths, nil
}

func loadCA(paths Paths, baseDomain string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := readCertificate(paths.CACert)
	if err != nil {
		return nil, nil, err
	}
	key, err := readECPrivateKey(paths.CAKey)
	if err != nil {
		return nil, nil, err
	}
	if !cert.IsCA || time.Until(cert.NotAfter) < caRenewal ||
		len(cert.PermittedDNSDomains) != 1 || cert.PermittedDNSDomains[0] != baseDomain {
		return nil, nil, errors.New("local CA is expired or belongs to another domain")
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !public.Equal(&key.PublicKey) {
		return nil, nil, errors.New("local CA certificate and key do not match")
	}
	return cert, key, nil
}

func createCA(paths Paths, baseDomain string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate local CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "webport local development CA for " + baseDomain,
			Organization: []string{"webport"},
		},
		NotBefore:                   now.Add(-5 * time.Minute),
		NotAfter:                    now.Add(caLifetime),
		KeyUsage:                    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{baseDomain},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create local CA certificate: %w", err)
	}
	if err := writePrivateKey(paths.CAKey, key, 0600); err != nil {
		return nil, nil, err
	}
	if err := writePEM(paths.CACert, "CERTIFICATE", der, 0644); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse generated local CA certificate: %w", err)
	}
	return cert, key, nil
}

func validLeaf(paths Paths, caCert *x509.Certificate, baseDomain string) bool {
	cert, err := readCertificate(paths.Cert)
	if err != nil || time.Until(cert.NotAfter) < leafRenewal {
		return false
	}
	key, err := readECPrivateKey(paths.Key)
	if err != nil {
		return false
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !public.Equal(&key.PublicKey) {
		return false
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	for _, name := range []string{baseDomain, "route." + baseDomain} {
		if _, err := cert.Verify(x509.VerifyOptions{
			Roots:     roots,
			DNSName:   name,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return false
		}
	}
	return true
}

func createLeaf(paths Paths, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, baseDomain string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate wildcard certificate key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "*." + baseDomain},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(leafLifetime),
		DNSNames:     []string{baseDomain, "*." + baseDomain},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create wildcard certificate: %w", err)
	}
	if err := writePrivateKey(paths.Key, key, 0640); err != nil {
		return err
	}
	return writePEM(paths.Cert, "CERTIFICATE", der, 0644)
}

func readCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func readECPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, errors.New("invalid EC private key PEM")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func writePrivateKey(path string, key *ecdsa.PrivateKey, mode os.FileMode) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	return writePEM(path, "EC PRIVATE KEY", der, mode)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".webport-pki-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary certificate file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := pem.Encode(tmp, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		tmp.Close()
		return fmt.Errorf("encode certificate material: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace certificate material: %w", err)
	}
	return nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}
