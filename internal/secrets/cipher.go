package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const envelopeVersion byte = 1

// Cipher encrypts service-managed secrets before they enter persistent storage.
// The caller must provide a stable 256-bit master key from an external secret.
type Cipher struct {
	aead     cipher.AEAD
	envelope interface {
		Encrypt([]byte, string) ([]byte, error)
		Decrypt([]byte, string) ([]byte, error)
	}
}

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("master key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// WithEnvelope enables external key wrapping; version-1 reads retain migration compatibility.
func (c *Cipher) WithEnvelope(e interface {
	Encrypt([]byte, string) ([]byte, error)
	Decrypt([]byte, string) ([]byte, error)
}) *Cipher {
	c.envelope = e
	return c
}
func DecodeKey(value string) ([]byte, error) {
	key, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("master key must be a base64url-encoded 32-byte value")
	}
	return key, nil
}

func (c *Cipher) Encrypt(plaintext []byte, associatedData string) ([]byte, error) {
	if c != nil && c.envelope != nil {
		return c.envelope.Encrypt(plaintext, associatedData)
	}
	if c == nil || c.aead == nil {
		return nil, errors.New("secret cipher is not configured")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	envelope := make([]byte, 1, 1+len(nonce)+len(plaintext)+c.aead.Overhead())
	envelope[0] = envelopeVersion
	envelope = append(envelope, nonce...)
	envelope = c.aead.Seal(envelope, nonce, plaintext, []byte(associatedData))
	return envelope, nil
}

func (c *Cipher) Decrypt(envelope []byte, associatedData string) ([]byte, error) {
	if c != nil && len(envelope) > 0 && envelope[0] == 2 {
		if c.envelope == nil {
			return nil, errors.New("KMS not configured")
		}
		return c.envelope.Decrypt(envelope, associatedData)
	}
	if c == nil || c.aead == nil {
		return nil, errors.New("secret cipher is not configured")
	}
	if len(envelope) < 1+c.aead.NonceSize()+c.aead.Overhead() || envelope[0] != envelopeVersion {
		return nil, errors.New("invalid secret envelope")
	}
	nonce := envelope[1 : 1+c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, envelope[1+c.aead.NonceSize():], []byte(associatedData))
	if err != nil {
		return nil, errors.New("decrypt secret envelope")
	}
	return plaintext, nil
}
