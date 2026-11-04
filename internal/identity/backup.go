package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// BackupPrefix starts every exported identity backup.
const BackupPrefix = "lanfolk-backup1:"

const maxNicknameLen = 32

// ExportBackup encrypts the identity seed and nickname with a password using
// Argon2id and XChaCha20-Poly1305. The result is safe to store offline.
func ExportBackup(k *PrivateKey, nickname, password string) (string, error) {
	if password == "" {
		return "", errors.New("backup password is required")
	}
	salt := make([]byte, 16)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.NewX(backupKey(password, salt))
	if err != nil {
		return "", err
	}
	plain := append(k.Seed(), []byte(nickname)...)
	out := append(append(salt, nonce...), aead.Seal(nil, nonce, plain, []byte(BackupPrefix))...)
	return BackupPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// ImportBackup reverses ExportBackup.
func ImportBackup(s, password string) (*PrivateKey, string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, BackupPrefix) {
		return nil, "", errors.New("not a Lanfolk backup")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(BackupPrefix):])
	if err != nil || len(raw) < 16+chacha20poly1305.NonceSizeX+32+chacha20poly1305.Overhead {
		return nil, "", errors.New("corrupted backup")
	}
	salt, nonce, ct := raw[:16], raw[16:16+chacha20poly1305.NonceSizeX], raw[16+chacha20poly1305.NonceSizeX:]
	aead, err := chacha20poly1305.NewX(backupKey(password, salt))
	if err != nil {
		return nil, "", err
	}
	plain, err := aead.Open(nil, nonce, ct, []byte(BackupPrefix))
	if err != nil {
		return nil, "", errors.New("wrong password or corrupted backup")
	}
	k, err := FromSeed(plain[:32])
	if err != nil {
		return nil, "", err
	}
	nick := string(plain[32:])
	if len(nick) > maxNicknameLen {
		nick = nick[:maxNicknameLen]
	}
	return k, nick, nil
}

func backupKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, chacha20poly1305.KeySize)
}
