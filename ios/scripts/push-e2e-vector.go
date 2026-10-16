//go:build ignore

// Run with `go run scripts/push-e2e-vector.go` from ios/. The build tag keeps
// it out of the daemon module's `go build ./...` and `go test ./...`.
//
// Seals a fixed notification exactly like herdr-sheepdog internal/push.Seal
// (AES-256-GCM, 12-byte nonce, tag appended, no AD), with a fixed key and nonce.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
)

func main() {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	nonce := []byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab}
	pt := []byte(`{"aps":{"alert":{"title":"auth","body":"auth asks to run git push. Yes or no?"},"sound":"default","thread-id":"w5:p1","interruption-level":"time-sensitive","category":"APPROVAL"},"sheepdog":{"kind":"approval","pane_id":"w5:p1","agent":"auth","pending_id":"ap-tm9bu0-1","event_id":"evt-12","options":[{"index":1,"label":"Yes, proceed"},{"index":2,"label":"No"}]}}`)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	c := gcm.Seal(nil, nonce, pt, nil)
	b := base64.StdEncoding
	fmt.Printf("key=%s\nnonce=%s\nciphertext=%s\nplaintext=%s\n", b.EncodeToString(key), b.EncodeToString(nonce), b.EncodeToString(c), pt)
}
