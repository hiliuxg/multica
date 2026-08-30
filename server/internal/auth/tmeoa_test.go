package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestParseTMEOAIdentity(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	secret := "test-tpp-secret"

	tests := []struct {
		name      string
		timestamp string
		payload   map[string]string
		requestID string
		secret    string
		want      TMEOAIdentity
		wantErr   error
	}{
		{
			name:      "seconds timestamp",
			timestamp: strconv.FormatInt(now.Unix(), 10),
			payload:   map[string]string{"ename": "XiaoGenLiu", "id": "employee-1", "cname": "小根刘", "email": "xiaogenliu@tencentmusic.com"},
			requestID: "request-1",
			secret:    secret,
			want:      TMEOAIdentity{EName: "xiaogenliu", ID: "employee-1", CName: "小根刘", Email: "xiaogenliu@tencentmusic.com"},
		},
		{
			name:      "milliseconds timestamp and email fallback",
			timestamp: strconv.FormatInt(now.UnixMilli(), 10),
			payload:   map[string]string{"ename": "alice.z", "id": "employee-2", "cname": "", "email": ""},
			requestID: "request-2",
			secret:    secret,
			want:      TMEOAIdentity{EName: "alice.z", ID: "employee-2", Email: "alice.z@tencentmusic.com"},
		},
		{
			name:      "expired timestamp",
			timestamp: strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10),
			payload:   map[string]string{"ename": "alice", "id": "employee-2", "cname": "", "email": "alice@tencentmusic.com"},
			requestID: "request-3",
			secret:    secret,
			wantErr:   ErrTMEOATimestamp,
		},
		{
			name:      "future timestamp",
			timestamp: strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10),
			payload:   map[string]string{"ename": "alice", "id": "employee-2", "cname": "", "email": "alice@tencentmusic.com"},
			requestID: "request-future",
			secret:    secret,
			wantErr:   ErrTMEOATimestamp,
		},
		{
			name:      "wrong secret",
			timestamp: strconv.FormatInt(now.Unix(), 10),
			payload:   map[string]string{"ename": "alice", "id": "employee-2", "cname": "", "email": "alice@tencentmusic.com"},
			requestID: "request-secret",
			secret:    "wrong-secret",
			wantErr:   ErrTMEOAToken,
		},
		{
			name:      "email does not match ename",
			timestamp: strconv.FormatInt(now.Unix(), 10),
			payload:   map[string]string{"ename": "alice", "id": "employee-2", "cname": "", "email": "bob@tencentmusic.com"},
			requestID: "request-4",
			secret:    secret,
			wantErr:   ErrTMEOAIdentity,
		},
		{
			name:      "invalid ename",
			timestamp: strconv.FormatInt(now.Unix(), 10),
			payload:   map[string]string{"ename": "../alice", "id": "employee-2", "cname": "", "email": ""},
			requestID: "request-5",
			secret:    secret,
			wantErr:   ErrTMEOAIdentity,
		},
		{
			name:      "missing request id",
			timestamp: strconv.FormatInt(now.Unix(), 10),
			payload:   map[string]string{"ename": "alice", "id": "employee-2", "cname": "", "email": ""},
			secret:    secret,
			wantErr:   ErrTMEOAHeaders,
		},
		{
			name:      "missing secret",
			timestamp: strconv.FormatInt(now.Unix(), 10),
			payload:   map[string]string{"ename": "alice", "id": "employee-2", "cname": "", "email": ""},
			requestID: "request-6",
			wantErr:   ErrTMEOAConfiguration,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := encryptTMEOATestToken(t, tt.payload, tt.timestamp, secret)
			identity, err := ParseTMEOAIdentity(token, tt.timestamp, tt.requestID, tt.secret, now, DefaultTMEOAMaxClockSkew)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseTMEOAIdentity() error = %v, want %v", err, tt.wantErr)
			}
			if identity != tt.want {
				t.Fatalf("ParseTMEOAIdentity() = %#v, want %#v", identity, tt.want)
			}
		})
	}
}

func TestParseTMEOAIdentityMatchesOpenCodeGoldenToken(t *testing.T) {
	const token = "SKpZD5tO6/E9n8+N0Sj64uskjlC3X55uPVYLvN38hX0Xv7lkwPA/VklAlpovPhMpFDE1MVIKhTvv1gH0ZJYwQp+LQ2Ged6MLZ0xMwkxuNRDXVeMjRc7B7olZKGrv2JBX0BdmJUvL9avWp0EDv9KUUg=="
	timestamp := "1788000000"
	now := time.Unix(1_788_000_000, 0)

	identity, err := ParseTMEOAIdentity(token, timestamp, "golden-request", "golden-tpp-secret", now, DefaultTMEOAMaxClockSkew)
	if err != nil {
		t.Fatalf("ParseTMEOAIdentity() error = %v", err)
	}
	want := TMEOAIdentity{
		EName: "golden.user",
		ID:    "employee-golden",
		CName: "金牌用户",
		Email: "golden.user@tencentmusic.com",
	}
	if identity != want {
		t.Fatalf("ParseTMEOAIdentity() = %#v, want %#v", identity, want)
	}
}

func TestParseTMEOAIdentityRequiresAllStringFields(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	secret := "test-tpp-secret"
	for _, payload := range []map[string]string{
		{"ename": "alice", "id": "employee-1", "email": "alice@tencentmusic.com"},
		{"ename": "alice", "id": "employee-1", "cname": ""},
	} {
		token := encryptTMEOATestToken(t, payload, timestamp, secret)
		_, err := ParseTMEOAIdentity(token, timestamp, "request-schema", secret, now, DefaultTMEOAMaxClockSkew)
		if !errors.Is(err, ErrTMEOAIdentity) {
			t.Fatalf("ParseTMEOAIdentity() error = %v, want %v", err, ErrTMEOAIdentity)
		}
	}
}

func TestParseTMEOAIdentityRejectsInvalidToken(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	_, err := ParseTMEOAIdentity("not-base64", timestamp, "request-1", "secret", now, DefaultTMEOAMaxClockSkew)
	if !errors.Is(err, ErrTMEOAToken) {
		t.Fatalf("ParseTMEOAIdentity() error = %v, want %v", err, ErrTMEOAToken)
	}
}

func TestParseTMEOAIdentityRejectsInvalidPadding(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	secret := "test-tpp-secret"
	token := encryptTMEOATestToken(t, map[string]string{
		"ename": "alice",
		"id":    "employee-1",
		"cname": "",
		"email": "alice@tencentmusic.com",
	}, timestamp, secret)
	ciphertext, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)-aes.BlockSize-1] ^= 1
	broken := base64.StdEncoding.EncodeToString(ciphertext)

	_, err = ParseTMEOAIdentity(broken, timestamp, "request-padding", secret, now, DefaultTMEOAMaxClockSkew)
	if !errors.Is(err, ErrTMEOAToken) {
		t.Fatalf("ParseTMEOAIdentity() error = %v, want %v", err, ErrTMEOAToken)
	}
}

func TestValidateTMEOAConfiguration(t *testing.T) {
	for _, secret := range []string{"", "CHANGE_ME_TPP_APPSECRET"} {
		if err := ValidateTMEOAConfiguration(AuthModeTMEOA, secret); !errors.Is(err, ErrTMEOAConfiguration) {
			t.Fatalf("ValidateTMEOAConfiguration(tmeoa, %q) error = %v", secret, err)
		}
	}
	if err := ValidateTMEOAConfiguration(AuthModeTMEOA, "configured-secret"); err != nil {
		t.Fatalf("ValidateTMEOAConfiguration() error = %v", err)
	}
}

func TestNormalizeAuthMode(t *testing.T) {
	for _, tt := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"", AuthModeLegacy, false},
		{"legacy", AuthModeLegacy, false},
		{" TMEOA ", AuthModeTMEOA, false},
		{"other", "", true},
	} {
		got, err := NormalizeAuthMode(tt.raw)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Fatalf("NormalizeAuthMode(%q) = %q, %v; want %q, wantErr=%v", tt.raw, got, err, tt.want, tt.wantErr)
		}
	}
}

func encryptTMEOATestToken(t *testing.T, payload map[string]string, timestamp, secret string) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(raw)%aes.BlockSize
	for range padding {
		raw = append(raw, byte(padding))
	}
	digest := md5.Sum([]byte(timestamp + secret + timestamp))
	hash := hex.EncodeToString(digest[:])
	block, err := aes.NewCipher([]byte(hash[:16]))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(raw))
	cipher.NewCBCEncrypter(block, []byte(hash[16:])).CryptBlocks(encrypted, raw)
	return base64.StdEncoding.EncodeToString(encrypted)
}
