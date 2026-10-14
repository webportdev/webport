package webui

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	hashScheme = "pbkdf2-sha256"
	// MinPasswordLength is the shortest accepted password, in characters.
	MinPasswordLength = 8
	maxPasswordBytes  = 1024
	saltBytes         = 16
	keyBytes          = 32
)

// hashIterations follows current OWASP guidance for PBKDF2-HMAC-SHA256.
// Tests lower it to keep runs fast.
var hashIterations = 600_000

var sleep = func() { time.Sleep(20 * time.Millisecond) }

type passwordHash struct {
	iterations int
	salt, key  []byte
}

// ValidatePassword checks the password policy without revealing the password.
func ValidatePassword(password string) error {
	if !utf8.ValidString(password) {
		return errors.New("password must be valid UTF-8")
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("password must contain at least %d characters", MinPasswordLength)
	}
	if len(password) > maxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", maxPasswordBytes)
	}
	return nil
}

// HashPassword returns a salted, self-describing PBKDF2 hash.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, keyBytes)
	if err != nil {
		return "", err
	}
	encoding := base64.RawStdEncoding
	return strings.Join([]string{hashScheme, strconv.Itoa(hashIterations), encoding.EncodeToString(salt), encoding.EncodeToString(key)}, "$"), nil
}

// VerifyPassword compares password with encoded in constant time.
func VerifyPassword(encoded, password string) bool {
	parsed, err := parseHash(encoded)
	if err != nil || len(password) > maxPasswordBytes {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, parsed.salt, parsed.iterations, len(parsed.key))
	return err == nil && subtle.ConstantTimeCompare(key, parsed.key) == 1
}

func parseHash(encoded string) (passwordHash, error) {
	invalid := errors.New("password hash is not a supported pbkdf2-sha256 value; set the password again")
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return passwordHash{}, invalid
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > 10_000_000 {
		return passwordHash{}, invalid
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return passwordHash{}, invalid
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(key) < 16 {
		return passwordHash{}, invalid
	}
	return passwordHash{iterations, salt, key}, nil
}
