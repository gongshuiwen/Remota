package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

type PasswordHash struct{ Salt, Sum []byte }

func Secret(prefix string) string {
	var b [32]byte
	rand.Read(b[:])
	return prefix + base64.RawURLEncoding.EncodeToString(b[:])
}

func ID(prefix string) string {
	var b [16]byte
	rand.Read(b[:])
	if prefix != "" {
		prefix += "_"
	}
	return prefix + hex.EncodeToString(b[:])
}

func Digest(secret string) [32]byte { return sha256.Sum256([]byte(secret)) }

func ValidSecret(secret, kind string) bool {
	prefix := "rm_" + kind + "_"
	if !strings.HasPrefix(secret, prefix) {
		return false
	}
	s := strings.TrimPrefix(secret, prefix)
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(s) == 43 && len(b) == 32
}

func HashPassword(password string) PasswordHash {
	salt := make([]byte, 16)
	rand.Read(salt)
	sum, err := pbkdf2.Key(sha256.New, password, salt, 120000, 32)
	if err != nil {
		panic("password derivation unavailable")
	}
	return PasswordHash{salt, sum}
}

func (h PasswordHash) Verify(password string) bool {
	if len(h.Salt) != 16 || len(h.Sum) != 32 || len(password) == 0 || len(password) > 256 {
		return false
	}
	sum, err := pbkdf2.Key(sha256.New, password, h.Salt, 120000, 32)
	return err == nil && subtle.ConstantTimeCompare(sum, h.Sum) == 1
}
