package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// recordSize is the aes128gcm record size written in the header. The whole
// message is one record, so it only has to be larger than the ciphertext.
const recordSize = 4096

// headerSize is the aes128gcm header: a 16-byte salt, the 4-byte record
// size, a 1-byte key id length, and the 65-byte ephemeral public key.
const headerSize = 16 + 4 + 1 + 65

// MaxPayload is the largest plaintext one push may carry. Push services
// accept at most 4096 bytes of encrypted body: the header, the plaintext,
// one padding delimiter byte, and the 16-byte authentication tag.
const MaxPayload = 4096 - headerSize - 1 - 16

// Encrypt encrypts plaintext for the browser whose subscription keys are
// p256dh (its P-256 public key, 65 bytes) and auth (its 16-byte secret),
// with a fresh ephemeral key and salt (RFC 8291). The result is the request
// body, with the aes128gcm header.
func Encrypt(plaintext, p256dh, auth []byte) ([]byte, error) {
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(plaintext, p256dh, auth, ephemeral, salt)
}

// encrypt is Encrypt with the ephemeral key and salt given, so the RFC's
// example can be reproduced.
func encrypt(plaintext, p256dh, auth []byte, ephemeral *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("the push payload is %d bytes, more than the %d a push may carry", len(plaintext), MaxPayload)
	}
	if len(auth) != 16 {
		return nil, errors.New("the subscription's auth secret must be 16 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("the salt must be 16 bytes")
	}
	browser, err := ecdh.P256().NewPublicKey(p256dh)
	if err != nil {
		return nil, errors.New("the subscription's p256dh key is not a P-256 public key")
	}
	shared, err := ephemeral.ECDH(browser)
	if err != nil {
		return nil, fmt.Errorf("agree on the push key: %w", err)
	}
	serverPublic := ephemeral.PublicKey().Bytes()

	// IKM = HKDF(salt = auth, secret = ECDH, "WebPush: info" 0 ua_public as_public)
	keyInfo := "WebPush: info\x00" + string(p256dh) + string(serverPublic)
	ikm, err := hkdf.Key(sha256.New, shared, auth, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	// The content encryption key and nonce of RFC 8188, from the salt.
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	header := make([]byte, 0, headerSize+len(plaintext)+1+gcm.Overhead())
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(serverPublic)))
	header = append(header, serverPublic...)
	// One record, so it is the last: the plaintext, then the delimiter 2,
	// without further padding.
	record := make([]byte, 0, len(plaintext)+1)
	record = append(append(record, plaintext...), 0x02)
	return gcm.Seal(header, nonce, record, nil), nil
}
