// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package decisions exports decision records as signed JSON so they can be
// handed to auditors and checked without access to the Zyntra server.
package decisions

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
)

const Format = "zyntra.decision/v1"

// Signer holds the Ed25519 key exports are signed with.
type Signer struct {
	key ed25519.PrivateKey
}

// LoadSigner reads a 32-byte seed from path, creating it (0600) if it does
// not exist. An empty path gives a key that lasts until restart.
func LoadSigner(path string) (*Signer, error) {
	if path == "" {
		_, k, err := ed25519.GenerateKey(rand.Reader)
		return &Signer{key: k}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
			return nil, err
		}
		return &Signer{key: ed25519.NewKeyFromSeed(seed)}, nil
	}
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(string(trim(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key %s must be a hex-encoded 32-byte seed", path)
	}
	return &Signer{key: ed25519.NewKeyFromSeed(seed)}, nil
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// PublicKey returns the base64 public key.
func (s *Signer) PublicKey() string {
	return base64.StdEncoding.EncodeToString(s.key.Public().(ed25519.PublicKey))
}

// Payload is the signed part of an export.
type Payload struct {
	Format     string                 `json:"format"`
	ExportedAt time.Time              `json:"exported_at"`
	ExportedBy string                 `json:"exported_by"`
	Decision   approvals.Proposal     `json:"decision"`
	Audit      []approvals.Event      `json:"audit"`
	Chain      approvals.Verification `json:"chain"`
}

// Export is a signed decision record.
type Export struct {
	Payload
	Digest    string `json:"digest"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

func digest(p Payload) ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

// Sign builds a signed export of one decision and its audit events.
func (s *Signer) Sign(p approvals.Proposal, events []approvals.Event, chain approvals.Verification, by string, at time.Time) (Export, error) {
	pl := Payload{Format: Format, ExportedAt: at.UTC(), ExportedBy: by, Decision: p, Audit: events, Chain: chain}
	d, err := digest(pl)
	if err != nil {
		return Export{}, err
	}
	return Export{
		Payload: pl, Digest: hex.EncodeToString(d), Algorithm: "ed25519",
		PublicKey: s.PublicKey(), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(s.key, d)),
	}, nil
}

// Verify checks an export's digest and signature against its embedded
// public key; callers should also compare the key with the one they trust.
func Verify(e Export) error {
	if e.Format != Format || e.Algorithm != "ed25519" {
		return fmt.Errorf("unsupported export format %q/%q", e.Format, e.Algorithm)
	}
	d, err := digest(e.Payload)
	if err != nil {
		return err
	}
	if hex.EncodeToString(d) != e.Digest {
		return errors.New("digest does not match the decision content")
	}
	pub, err := base64.StdEncoding.DecodeString(e.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("bad public key")
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || !ed25519.Verify(pub, d, sig) {
		return errors.New("signature does not verify")
	}
	return nil
}
