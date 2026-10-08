package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrNoMasterKey is returned when a secret is written or read on a store
// opened without a master key. Core still starts in that state — routing needs
// no secrets — but nothing secret can be saved, and the dashboard says why.
var ErrNoMasterKey = errors.New("store: CORE_MASTER_KEY is not set")

// ErrWrongKey means a secret was sealed under a different master key than the
// one Core started with. Distinct from a corrupt value: the fix is to restore
// the old key, not to re-enter the secret.
var ErrWrongKey = errors.New("store: secret was sealed with a different master key")

// sealedPrefix versions the format, so a later change of cipher or layout can
// still read what this one wrote.
const sealedPrefix = "v1"

// sealer encrypts secrets at rest with AES-256-GCM.
//
// Each value is bound to where it lives through the additional data (aad), so
// a ciphertext copied from one row into another fails to open instead of
// quietly handing one plugin another's password.
type sealer struct {
	aead  cipher.AEAD
	keyID string
}

// ParseMasterKey decodes CORE_MASTER_KEY: 32 random bytes, base64-encoded
// (`openssl rand -base64 32`). Anything else is refused rather than stretched,
// since a short key typed by hand is the mistake this is guarding against.
func ParseMasterKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, fmt.Errorf("master key is not base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d (generate one with: openssl rand -base64 32)", len(key))
	}
	return key, nil
}

func newSealer(key []byte) (*sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// The key id is a short fingerprint, not the key: enough to tell which key
	// sealed a value, useless for recovering it.
	sum := sha256.Sum256(key)
	return &sealer{aead: aead, keyID: hex.EncodeToString(sum[:4])}, nil
}

// seal returns "v1.<key id>.<base64(nonce || ciphertext)>".
func (s *sealer) seal(plaintext, aad string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return sealedPrefix + "." + s.keyID + "." + base64.RawURLEncoding.EncodeToString(out), nil
}

// sealSecret and openSecret are what the rest of the package uses: they turn
// a missing master key into ErrNoMasterKey instead of a nil dereference.
func (s *Store) sealSecret(plaintext, aad string) (string, error) {
	if s.sealer == nil {
		return "", ErrNoMasterKey
	}
	return s.sealer.seal(plaintext, aad)
}

func (s *Store) openSecret(sealed, aad string) (string, error) {
	if s.sealer == nil {
		return "", ErrNoMasterKey
	}
	return s.sealer.open(sealed, aad)
}

func (s *sealer) open(sealed, aad string) (string, error) {
	parts := strings.SplitN(sealed, ".", 3)
	if len(parts) != 3 || parts[0] != sealedPrefix {
		return "", errors.New("store: unrecognised secret format")
	}
	if parts[1] != s.keyID {
		return "", ErrWrongKey
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("store: corrupt secret: %w", err)
	}
	n := s.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("store: corrupt secret: too short")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return "", fmt.Errorf("store: secret does not open (moved between rows, or corrupt): %w", err)
	}
	return string(plain), nil
}
