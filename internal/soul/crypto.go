package soul

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

const (
	// archiveMagic identifies a daimon soul archive on disk.
	archiveMagic = "DSOUL"
	// archiveVersion is the on-disk framing format version.
	archiveVersion = 0x01
	// saltLen is the PBKDF2 salt length in bytes.
	saltLen = 32
	// nonceLen is the AES-GCM nonce length in bytes.
	nonceLen = 12
	// kdfIterations is the PBKDF2-SHA256 iteration count (OWASP 2023
	// recommendation for SHA-256).
	kdfIterations = 600_000
	// keyLen is the AES-256 key length in bytes.
	keyLen = 32
)

// headerLen is the fixed byte length of the archive framing before ciphertext.
const headerLen = len(archiveMagic) + 1 + saltLen + nonceLen

// sealArchive encrypts plaintext with a passphrase-derived AES-256-GCM key and
// writes the framed archive (magic | version | salt | nonce | ciphertext) to w.
// GCM authentication covers the whole payload, so any tampering fails at open
// time without a separate checksum. The payload is held fully in memory: a
// personal state directory is megabytes, so chunked/streaming GCM would be
// speculative complexity (YAGNI).
func sealArchive(w io.Writer, passphrase string, plaintext []byte) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	for _, chunk := range [][]byte{[]byte(archiveMagic), {archiveVersion}, salt, nonce, ciphertext} {
		if _, err := w.Write(chunk); err != nil {
			return fmt.Errorf("write archive: %w", err)
		}
	}
	return nil
}

// openArchive decrypts a framed archive produced by sealArchive. A wrong
// passphrase and a corrupted archive are cryptographically indistinguishable
// under GCM, so both surface as one honest error.
func openArchive(r io.Reader, passphrase string) ([]byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if len(raw) < headerLen || string(raw[:len(archiveMagic)]) != archiveMagic || raw[len(archiveMagic)] != archiveVersion {
		return nil, fmt.Errorf("not a daimon soul archive (or unsupported version)")
	}
	salt := raw[len(archiveMagic)+1 : len(archiveMagic)+1+saltLen]
	nonce := raw[len(archiveMagic)+1+saltLen : headerLen]
	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, raw[headerLen:], nil)
	if err != nil {
		return nil, fmt.Errorf("wrong passphrase or corrupted archive")
	}
	return plaintext, nil
}

func newGCM(passphrase string, salt []byte) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, kdfIterations, keyLen)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return gcm, nil
}
