// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package tlsutil creates the private CA and loopback certificate for
// Zyntra's exec listener, which Fabric Keep's broker calls over HTTPS.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type Files struct {
	CA, Cert, Key string
}

func paths(dir string) Files {
	return Files{
		CA:   filepath.Join(dir, "exec-ca.pem"),
		Cert: filepath.Join(dir, "exec.pem"),
		Key:  filepath.Join(dir, "exec-key.pem"),
	}
}

// Ensure returns a usable certificate for 127.0.0.1 signed by a private CA
// under dir, creating both if missing or expiring within 30 days. The CA
// certificate is world-readable so Keep can trust it; keys stay 0600.
func Ensure(dir string) (Files, tls.Certificate, error) {
	f := paths(dir)
	if c, err := tls.LoadX509KeyPair(f.Cert, f.Key); err == nil && fresh(c) {
		if _, err := os.Stat(f.CA); err == nil {
			return f, c, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return f, tls.Certificate{}, err
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return f, tls.Certificate{}, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Zyntra exec CA", Organization: []string{"Zyvor"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return f, tls.Certificate{}, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return f, tls.Certificate{}, err
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "zyntra-exec"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:     []string{"localhost"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return f, tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return f, tls.Certificate{}, err
	}
	if err := write(f.CA, "CERTIFICATE", caDER, 0o644); err != nil {
		return f, tls.Certificate{}, err
	}
	if err := write(f.Key, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return f, tls.Certificate{}, err
	}
	if err := write(f.Cert, "CERTIFICATE", leafDER, 0o644); err != nil {
		return f, tls.Certificate{}, err
	}
	c, err := tls.LoadX509KeyPair(f.Cert, f.Key)
	return f, c, err
}

func fresh(c tls.Certificate) bool {
	if len(c.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	return err == nil && time.Until(leaf.NotAfter) > 30*24*time.Hour
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

func write(path, typ string, der []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(fmt.Errorf("write %s", path), err)
	}
	return nil
}
