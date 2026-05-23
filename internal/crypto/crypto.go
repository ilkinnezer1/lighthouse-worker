// Package crypto wraps AES-256-GCM in the exact layout Nest's CryptoService uses:
// base64(iv || ciphertext || tag), 12-byte iv, 16-byte tag, 32-byte key.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	ivBytes  = 12
	tagBytes = 16
	keyBytes = 32
)

type Cipher struct {
	aead cipher.AEAD
}

func New(base64Key string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("decode MASTER_ENCRYPTION_KEY: %w", err)
	}
	if len(key) != keyBytes {
		return nil, fmt.Errorf("MASTER_ENCRYPTION_KEY must decode to %d bytes (got %d)", keyBytes, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plain string) (string, error) {
	iv := make([]byte, ivBytes)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("read iv: %w", err)
	}
	sealed := c.aead.Seal(nil, iv, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(append(iv, sealed...)), nil
}

func (c *Cipher) Decrypt(b64 string) (string, error) {
	buf, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(buf) < ivBytes+tagBytes {
		return "", errors.New("ciphertext too short")
	}
	plain, err := c.aead.Open(nil, buf[:ivBytes], buf[ivBytes:], nil)
	if err != nil {
		return "", fmt.Errorf("aead open: %w", err)
	}
	return string(plain), nil
}
