package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
	"testing"
)

// TestWeComDecryptRoundTrip encrypts a payload with the official WeCom rule
// (AES-256-CBC, PKCS#7, 16 random bytes + 4-byte length + message + receiveid)
// and checks that weComDecrypt and weComSignature recover it.
func TestWeComDecryptRoundTrip(t *testing.T) {
	// A real EncodingAESKey is the base64 of 32 random bytes minus the '=' pad.
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		t.Fatal(err)
	}
	encodingAESKey := strings.TrimRight(base64.StdEncoding.EncodeToString(rawKey), "=")
	if len(encodingAESKey) != 43 {
		t.Fatalf("test key setup failed: key length %d", len(encodingAESKey))
	}
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil || len(key) != 32 {
		t.Fatalf("test key setup failed: %v", err)
	}
	message := []byte("hello echostr 你好")
	receiveID := []byte("corp-id-123")
	plaintext := make([]byte, 0, 16+4+len(message)+len(receiveID))
	plaintext = append(plaintext, make([]byte, 16)...) // random prefix
	if _, err := rand.Read(plaintext[:16]); err != nil {
		t.Fatal(err)
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(message)))
	plaintext = append(plaintext, length[:]...)
	plaintext = append(plaintext, message...)
	plaintext = append(plaintext, receiveID...)
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	for i := 0; i < pad; i++ {
		plaintext = append(plaintext, byte(pad))
	}
	block, _ := aes.NewCipher(key)
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	ivKey := make([]byte, 16)
	copy(ivKey, key[:16]) // WeCom uses the first 16 bytes of the key as IV
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, ivKey).CryptBlocks(ciphertext, plaintext)
	encrypted := base64.StdEncoding.EncodeToString(ciphertext)

	gotMessage, gotID, err := weComDecrypt(encrypted, encodingAESKey)
	if err != nil {
		t.Fatalf("weComDecrypt: %v", err)
	}
	if string(gotMessage) != string(message) {
		t.Fatalf("message mismatch: got %q want %q", gotMessage, message)
	}
	if string(gotID) != string(receiveID) {
		t.Fatalf("receive id mismatch: got %q want %q", gotID, receiveID)
	}

	token := "token-value"
	timestamp, nonce := "1788338000", "nonce123"
	parts := []string{token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	want := sha1.Sum([]byte(strings.Join(parts, "")))
	if got := weComSignature(token, timestamp, nonce, encrypted); got != hex.EncodeToString(want[:]) {
		t.Fatalf("signature mismatch: got %s want %s", got, hex.EncodeToString(want[:]))
	}
}
