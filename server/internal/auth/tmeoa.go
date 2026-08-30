package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	AuthModeLegacy = "legacy"
	AuthModeTMEOA  = "tmeoa"

	DefaultTMEOAMaxClockSkew = 5 * time.Minute
	tmeoaEmailDomain         = "tencentmusic.com"
	maxTMEOATokenLength      = 16 * 1024
	maxTMEOATimestampLength  = 20
	maxTMEOARequestIDLength  = 128
)

var (
	ErrTMEOAConfiguration = errors.New("tmeoa authentication is not configured")
	ErrTMEOAHeaders       = errors.New("invalid tmeoa gateway headers")
	ErrTMEOATimestamp     = errors.New("invalid tmeoa gateway timestamp")
	ErrTMEOAToken         = errors.New("invalid tmeoa gateway token")
	ErrTMEOAIdentity      = errors.New("invalid tmeoa gateway identity")
)

type TMEOAIdentity struct {
	EName string
	ID    string
	CName string
	Email string
}

type tmeoaPayload struct {
	EName *string `json:"ename"`
	ID    *string `json:"id"`
	CName *string `json:"cname"`
	Email *string `json:"email"`
}

func NormalizeAuthMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", AuthModeLegacy:
		return AuthModeLegacy, nil
	case AuthModeTMEOA:
		return AuthModeTMEOA, nil
	default:
		return "", fmt.Errorf("AUTH_MODE must be %q or %q", AuthModeLegacy, AuthModeTMEOA)
	}
}

func ValidateTMEOAConfiguration(mode, secret string) error {
	normalized, err := NormalizeAuthMode(mode)
	if err != nil {
		return err
	}
	if normalized == AuthModeTMEOA {
		value := strings.TrimSpace(secret)
		if value == "" || value == "CHANGE_ME_TPP_APPSECRET" {
			return fmt.Errorf("%w: TPP_APPSECRET is required when AUTH_MODE=tmeoa", ErrTMEOAConfiguration)
		}
	}
	return nil
}

func ParseTMEOAIdentity(token, timestamp, requestID, secret string, now time.Time, maxClockSkew time.Duration) (TMEOAIdentity, error) {
	if strings.TrimSpace(secret) == "" {
		return TMEOAIdentity{}, ErrTMEOAConfiguration
	}
	if token == "" || len(token) > maxTMEOATokenLength || timestamp == "" || len(timestamp) > maxTMEOATimestampLength || !validTMEOARequestID(requestID) {
		return TMEOAIdentity{}, ErrTMEOAHeaders
	}
	if timestamp != strings.TrimSpace(timestamp) {
		return TMEOAIdentity{}, ErrTMEOATimestamp
	}
	issuedAt, err := parseTMEOATimestamp(timestamp)
	if err != nil {
		return TMEOAIdentity{}, ErrTMEOATimestamp
	}
	if maxClockSkew <= 0 {
		maxClockSkew = DefaultTMEOAMaxClockSkew
	}
	if issuedAt.Before(now.Add(-maxClockSkew)) || issuedAt.After(now.Add(maxClockSkew)) {
		return TMEOAIdentity{}, ErrTMEOATimestamp
	}

	raw, err := decryptTMEOAToken(token, timestamp, secret)
	if err != nil {
		return TMEOAIdentity{}, ErrTMEOAToken
	}
	var payload tmeoaPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return TMEOAIdentity{}, ErrTMEOAToken
	}

	if payload.EName == nil || payload.ID == nil || payload.CName == nil || payload.Email == nil {
		return TMEOAIdentity{}, ErrTMEOAIdentity
	}
	ename := strings.ToLower(strings.TrimSpace(*payload.EName))
	id := strings.TrimSpace(*payload.ID)
	cname := strings.TrimSpace(*payload.CName)
	if !validTMEOAEName(ename) || id == "" || utf8.RuneCountInString(id) > 128 || utf8.RuneCountInString(cname) > 200 {
		return TMEOAIdentity{}, ErrTMEOAIdentity
	}
	canonicalEmail := ename + "@" + tmeoaEmailDomain
	if *payload.Email != "" && !strings.EqualFold(strings.TrimSpace(*payload.Email), canonicalEmail) {
		return TMEOAIdentity{}, ErrTMEOAIdentity
	}

	return TMEOAIdentity{
		EName: ename,
		ID:    id,
		CName: cname,
		Email: canonicalEmail,
	}, nil
}

func parseTMEOATimestamp(raw string) (time.Time, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return time.Time{}, ErrTMEOATimestamp
	}
	if value >= 1_000_000_000_000 {
		return time.UnixMilli(value), nil
	}
	return time.Unix(value, 0), nil
}

func decryptTMEOAToken(value, timestamp, secret string) ([]byte, error) {
	digest := md5.Sum([]byte(timestamp + secret + timestamp))
	hash := hex.EncodeToString(digest[:])
	block, err := aes.NewCipher([]byte(hash[:16]))
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrTMEOAToken
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, []byte(hash[16:])).CryptBlocks(plaintext, ciphertext)
	return unpadPKCS7(plaintext, aes.BlockSize)
}

func unpadPKCS7(value []byte, blockSize int) ([]byte, error) {
	if len(value) == 0 || len(value)%blockSize != 0 {
		return nil, ErrTMEOAToken
	}
	padding := int(value[len(value)-1])
	if padding == 0 || padding > blockSize || padding > len(value) {
		return nil, ErrTMEOAToken
	}
	for _, b := range value[len(value)-padding:] {
		if int(b) != padding {
			return nil, ErrTMEOAToken
		}
	}
	return value[:len(value)-padding], nil
}

func validTMEOARequestID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxTMEOARequestIDLength {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validTMEOAEName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for i, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		if i > 0 && (r == '.' || r == '_' || r == '-') {
			continue
		}
		return false
	}
	last := value[len(value)-1]
	return last != '.' && last != '_' && last != '-'
}
