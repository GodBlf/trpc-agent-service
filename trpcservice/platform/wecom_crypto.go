package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

// WeCom signs callbacks with SHA1 over the sorted token, timestamp, nonce and
// encrypted payload. The same rule covers both the plaintext and safe modes:
// in plaintext mode the encrypted payload is the echostr itself, while in
// safe mode it is the Encrypt field of the XML envelope.
func weComSignature(token, timestamp, nonce, encrypted string) string {
	parts := []string{token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

// weComDecrypt decrypts one AES-256-CBC block produced by WeCom with the
// 43-character EncodingAESKey. The plaintext layout is 16 random bytes, a
// big-endian 4-byte message length, the message, and the receive id.
func weComDecrypt(encrypted, encodingAESKey string) (message, receiveID []byte, err error) {
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, nil, errors.New("platform: invalid encoding aes key")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(ciphertext) < 32 || len(ciphertext)%16 != 0 {
		return nil, nil, errors.New("platform: invalid encrypted payload")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	plaintext := make([]byte, len(ciphertext)-aes.BlockSize)
	cipher.NewCBCDecrypter(block, ciphertext[:aes.BlockSize]).CryptBlocks(plaintext, ciphertext[aes.BlockSize:])
	if len(plaintext) == 0 {
		return nil, nil, errors.New("platform: invalid encrypted payload")
	}
	padding := int(plaintext[len(plaintext)-1])
	if padding <= 0 || padding > len(plaintext) {
		return nil, nil, errors.New("platform: invalid encrypted payload")
	}
	plaintext = plaintext[:len(plaintext)-padding]
	if len(plaintext) < 4 {
		return nil, nil, errors.New("platform: invalid encrypted payload")
	}
	size := int(binary.BigEndian.Uint32(plaintext[:4]))
	if size < 0 || 4+size > len(plaintext) {
		return nil, nil, errors.New("platform: invalid encrypted payload")
	}
	return plaintext[4 : 4+size], plaintext[4+size:], nil
}
