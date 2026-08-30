package handler

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestTMEOALoginDisabledInLegacyMode(t *testing.T) {
	h := *testHandler
	h.cfg.AuthMode = auth.AuthModeLegacy

	testutil.Call(t, h.TMEOALogin, httptest.NewRequest(http.MethodGet, "/auth/hg-sso", nil)).
		Want(http.StatusNotFound)
}

func TestTMEOALoginRejectsInvalidGatewayHeaders(t *testing.T) {
	h := *testHandler
	h.cfg.AuthMode = auth.AuthModeTMEOA
	h.cfg.TPPAppSecret = "test-tpp-secret"
	h.cfg.TMEOAMaxClockSkew = auth.DefaultTMEOAMaxClockSkew

	res := testutil.Call(t, h.TMEOALogin, httptest.NewRequest(http.MethodGet, "/auth/hg-sso?cli_state=abc", nil)).
		Want(http.StatusSeeOther)
	if got := res.Header().Get("Location"); got != "/auth/hg-sso/callback?cli_state=abc&error=authentication_failed" {
		t.Fatalf("Location = %q", got)
	}
	if cookies := res.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("invalid gateway request set %d cookies", len(cookies))
	}
}

func TestTMEOALoginCreatesNewUserForOnboarding(t *testing.T) {
	const (
		email  = "tmeoa-new@tencentmusic.com"
		secret = "test-tpp-secret"
	)
	dbfx.Cleanup(t, `DELETE FROM "user" WHERE email = $1`, email)

	h := *testHandler
	h.cfg = Config{
		AuthMode:            auth.AuthModeTMEOA,
		TPPAppSecret:        secret,
		TMEOAMaxClockSkew:   auth.DefaultTMEOAMaxClockSkew,
		AllowSignup:         false,
		AllowedEmailDomains: []string{"tencentmusic.com"},
	}

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	token := encryptTMEOAHandlerToken(t, map[string]string{
		"ename": "tmeoa-new",
		"id":    "employee-new",
		"cname": "企业新用户",
		"email": email,
	}, timestamp, secret)
	req := httptest.NewRequest(http.MethodGet, "/auth/hg-sso", nil)
	req.Header.Set("X-Token", token)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Request-ID", "request-new")

	res := testutil.Call(t, h.TMEOALogin, req).Want(http.StatusSeeOther)
	if got := res.Header().Get("Location"); got != "/auth/hg-sso/callback" {
		t.Fatalf("Location = %q", got)
	}

	var name, storedEmail string
	var onboardedAt pgtype.Timestamptz
	dbfx.QueryRow(t, `SELECT name, email, onboarded_at FROM "user" WHERE email = $1`, email).
		Scan(&name, &storedEmail, &onboardedAt)
	if name != "企业新用户" || storedEmail != email || onboardedAt.Valid {
		t.Fatalf("created user = name %q, email %q, onboarded_at %v", name, storedEmail, onboardedAt)
	}
}

func TestFindOrCreateTMEOAUserHandlesConcurrentFirstLogin(t *testing.T) {
	const email = "tmeoa-concurrent@tencentmusic.com"
	dbfx.Cleanup(t, `DELETE FROM "user" WHERE email = $1`, email)

	h := *testHandler
	h.cfg = Config{
		AllowSignup:         false,
		AllowedEmailDomains: []string{"tencentmusic.com"},
	}

	type result struct {
		id    string
		isNew bool
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			user, isNew, err := h.findOrCreateUserWithName(t.Context(), email, "并发用户")
			results <- result{id: uuidToString(user.ID), isNew: isNew, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	created := 0
	var userID string
	for got := range results {
		if got.err != nil {
			t.Fatalf("findOrCreateUserWithName() error = %v", got.err)
		}
		if got.isNew {
			created++
		}
		if userID == "" {
			userID = got.id
		} else if got.id != userID {
			t.Fatalf("concurrent logins returned different users: %q and %q", userID, got.id)
		}
	}
	if created != 1 {
		t.Fatalf("created count = %d, want 1", created)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM "user" WHERE email = $1`, email); got != 1 {
		t.Fatalf("stored user count = %d, want 1", got)
	}
}

func TestTMEOALoginReusesExistingUserAndSetsSessionCookies(t *testing.T) {
	const (
		email  = "tmeoa-existing@tencentmusic.com"
		secret = "test-tpp-secret"
	)
	dbfx.User(t, "Existing Profile Name", email)

	h := *testHandler
	h.cfg.AuthMode = auth.AuthModeTMEOA
	h.cfg.TPPAppSecret = secret
	h.cfg.TMEOAMaxClockSkew = auth.DefaultTMEOAMaxClockSkew

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	token := encryptTMEOAHandlerToken(t, map[string]string{
		"ename": "tmeoa-existing",
		"id":    "employee-existing",
		"cname": "网关名称",
		"email": email,
	}, timestamp, secret)
	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/hg-sso?cli_callback=http%3A%2F%2Flocalhost%3A9876%2Fcallback&cli_state=abc",
		nil,
	)
	req.Header.Set("X-Token", token)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Request-ID", "request-existing")

	res := testutil.Call(t, h.TMEOALogin, req).Want(http.StatusSeeOther)
	if got := res.Header().Get("Location"); got != "/auth/hg-sso/callback?cli_callback=http%3A%2F%2Flocalhost%3A9876%2Fcallback&cli_state=abc" {
		t.Fatalf("Location = %q", got)
	}

	cookies := res.Result().Cookies()
	seen := map[string]bool{}
	for _, cookie := range cookies {
		seen[cookie.Name] = true
	}
	if !seen[auth.AuthCookieName] || !seen[auth.CSRFCookieName] {
		t.Fatalf("session cookies = %v", seen)
	}

	var name string
	dbfx.QueryRow(t, `SELECT name FROM "user" WHERE email = $1`, email).Scan(&name)
	if name != "Existing Profile Name" {
		t.Fatalf("existing profile name = %q, want preserved", name)
	}
}

func TestTMEOADisablesLegacyLoginEndpoints(t *testing.T) {
	h := *testHandler
	h.cfg.AuthMode = auth.AuthModeTMEOA

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{"email", h.SendCode, "/auth/send-code"},
		{"code", h.VerifyCode, "/auth/verify-code"},
		{"google", h.GoogleLogin, "/auth/google"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.Call(t, tc.handler, testutil.JSONRequest(http.MethodPost, tc.path, map[string]string{})).
				Want(http.StatusNotFound)
		})
	}
}

func TestTMEOANewUserDisplayName(t *testing.T) {
	for _, tc := range []struct {
		cname string
		ename string
		want  string
	}{
		{"小根刘", "xiaogenliu", "小根刘"},
		{"", "xiaogenliu", "xiaogenliu"},
	} {
		if got := tmeoaDisplayName(auth.TMEOAIdentity{CName: tc.cname, EName: tc.ename}); got != tc.want {
			t.Fatalf("tmeoaDisplayName(%q, %q) = %q, want %q", tc.cname, tc.ename, got, tc.want)
		}
	}
}

func encryptTMEOAHandlerToken(t *testing.T, payload map[string]string, timestamp, secret string) string {
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
