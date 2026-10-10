package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
)

const sealedVersion byte = 1

func hash(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func encodeToken(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeToken(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode opaque token: %w", err)
	}
	return decoded, nil
}

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return nil, fmt.Errorf("generate random value: %w", err)
	}
	return value, nil
}

func (m *Manager) Seal(purpose string, plain []byte) ([]byte, error) {
	if purpose == "" {
		return nil, fmt.Errorf("seal purpose is required")
	}
	aead, err := m.aead()
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(aead.NonceSize())
	if err != nil {
		return nil, err
	}
	sealed := make([]byte, 1, 1+len(nonce)+len(plain)+aead.Overhead())
	sealed[0] = sealedVersion
	sealed = append(sealed, nonce...)
	sealed = aead.Seal(sealed, nonce, plain, []byte(purpose))
	return sealed, nil
}

func (m *Manager) Open(purpose string, sealed []byte) ([]byte, error) {
	if purpose == "" {
		return nil, fmt.Errorf("open purpose is required")
	}
	aead, err := m.aead()
	if err != nil {
		return nil, err
	}
	if len(sealed) < 1+aead.NonceSize()+aead.Overhead() || sealed[0] != sealedVersion {
		return nil, fmt.Errorf("sealed value is invalid")
	}
	nonce := sealed[1 : 1+aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, sealed[1+aead.NonceSize():], []byte(purpose))
	if err != nil {
		return nil, fmt.Errorf("open sealed value: %w", err)
	}
	return plain, nil
}

func (m *Manager) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(m.cfg.TransactionKey[:])
	if err != nil {
		return nil, fmt.Errorf("create transaction cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create transaction AEAD: %w", err)
	}
	return aead, nil
}

func (m *Manager) csrfForSession(session []byte) string {
	mac := hmac.New(sha256.New, m.cfg.TransactionKey[:])
	_, _ = mac.Write([]byte("website-csrf\x00"))
	_, _ = mac.Write(session)
	return encodeToken(mac.Sum(nil))
}

func equalSecret(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
