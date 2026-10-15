package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ContentKeySize is the length of a phone's end-to-end content key
// (AES-256-GCM).
const ContentKeySize = 32

// maxPlaintext bounds the sealed notification JSON: its base64
// ciphertext (plus the 16-byte tag) must stay under the relay's 3500
// characters, which keeps the APNs payload under Apple's 4096 bytes.
const maxPlaintext = 2400

// NewContentKey returns a fresh random content key, base64 (standard,
// padded), as the registration answer hands it to the app.
func NewContentKey() (string, error) {
	k := make([]byte, ContentKeySize)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k), nil
}

// ErrBadContentKey is a content key that is not 32 bytes of base64.
var ErrBadContentKey = errors.New("push: content key must be 32 bytes of base64")

func decodeKey(b64 string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(k) != ContentKeySize {
		return nil, ErrBadContentKey
	}
	return k, nil
}

// Seal encrypts plaintext with AES-256-GCM under the base64 content key
// and a fresh random 12-byte nonce, with no associated data. The
// ciphertext carries the 16-byte tag at its end (CryptoKit's
// AES.GCM.SealedBox(nonce:ciphertext:tag:) takes the two apart).
func Seal(contentKey string, plaintext []byte) (ciphertext, nonce []byte, err error) {
	k, err := decodeKey(contentKey)
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, nil), nonce, nil
}

// Open decrypts what Seal produced (the app's side; tests).
func Open(contentKey string, ciphertext, nonce []byte) ([]byte, error) {
	k, err := decodeKey(contentKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// SealedPayload is the notification JSON the relay path encrypts: the
// same {aps, sheepdog} object the direct path sends (Payload), so the
// app's extension can hand it to the code that already reads it. A text
// too long for the relay is shortened with an ellipsis, then the options
// are dropped.
func (n Notification) SealedPayload() ([]byte, error) {
	for {
		b, err := json.Marshal(n.Payload())
		if err != nil || len(b) <= maxPlaintext {
			return b, err
		}
		over := len(b) - maxPlaintext
		switch {
		case n.Text != "":
			n.Text = trimBytes(n.Text, len(n.Text)-over-len("…"))
		case len(n.Options) > 0:
			n.Options = nil
		default:
			return nil, errors.New("push: notification too large to seal")
		}
	}
}

// trimBytes cuts s to at most max bytes on a rune boundary and adds an
// ellipsis ("" when nothing is left).
func trimBytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "…"
}
