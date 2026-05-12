package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

// fakeInstallations implements MiddlewareDeps. Behavior is scripted per test
// — return the configured installation, force a not-found, or return a
// generic error.
type fakeInstallations struct {
	inst   *store.Installation
	err    error
	calls  int
	gotCID string
}

func (f *fakeInstallations) GetByCloudID(_ context.Context, cloudID string) (*store.Installation, error) {
	f.calls++
	f.gotCID = cloudID
	if f.err != nil {
		return nil, f.err
	}
	if f.inst == nil {
		return nil, store.ErrNotFound
	}
	return f.inst, nil
}

const testAudience = "ethicguard-api"

// signToken makes a valid HS256 JWT with the given claims for tests.
func signToken(t *testing.T, secret, cloudID, audience string, lifetime time.Duration) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"cloudId": cloudID,
		"aud":     audience,
		"iat":     now.Unix(),
		"exp":     now.Add(lifetime).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// nextStub returns a handler that records that it was called and the
// installation it saw in context.
func nextStub() (http.Handler, *bool, **store.Installation) {
	called := false
	var inst *store.Installation
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		inst = InstallationFromContext(r.Context())
	})
	return h, &called, &inst
}

func TestMiddleware_MissingToken(t *testing.T) {
	fi := &fakeInstallations{}
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, _ := nextStub()
	h := mw(next)

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if *called {
		t.Errorf("next handler ran without auth")
	}
	if fi.calls != 0 {
		t.Errorf("installations lookup called %d times; want 0", fi.calls)
	}
}

func TestMiddleware_MalformedToken(t *testing.T) {
	fi := &fakeInstallations{}
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, _ := nextStub()
	h := mw(next)

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer not.a.jwt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if *called {
		t.Errorf("next handler ran with malformed token")
	}
}

func TestMiddleware_UnknownInstallation(t *testing.T) {
	fi := &fakeInstallations{} // ErrNotFound by default
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, _ := nextStub()
	h := mw(next)

	tok := signToken(t, "secret-32-bytes-long-padding-aaaa", "cloud-xyz", testAudience, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if *called {
		t.Errorf("next handler ran for unknown installation")
	}
	if fi.gotCID != "cloud-xyz" {
		t.Errorf("lookup cloudID = %q, want cloud-xyz", fi.gotCID)
	}
}

func TestMiddleware_InstallationsLookupError(t *testing.T) {
	fi := &fakeInstallations{err: errors.New("db down")}
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, _ := nextStub()
	h := mw(next)

	tok := signToken(t, "secret", "cloud-xyz", testAudience, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (non-NotFound store error)", rec.Code)
	}
	if *called {
		t.Errorf("next handler ran on store error")
	}
}

func TestMiddleware_WrongSignature(t *testing.T) {
	secret := "stored-secret-32-bytes-padding-aaa"
	fi := &fakeInstallations{
		inst: &store.Installation{ID: 1, CloudID: "cloud-xyz", SharedSecret: secret},
	}
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, _ := nextStub()
	h := mw(next)

	// Sign with a different secret than what's stored.
	tok := signToken(t, "different-secret-32-bytes-padding-x", "cloud-xyz", testAudience, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 on wrong signature", rec.Code)
	}
	if *called {
		t.Errorf("next handler ran with wrong signature")
	}
}

func TestMiddleware_WrongAudience(t *testing.T) {
	secret := "stored-secret-32-bytes-padding-aaa"
	fi := &fakeInstallations{
		inst: &store.Installation{ID: 1, CloudID: "cloud-xyz", SharedSecret: secret},
	}
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, _ := nextStub()
	h := mw(next)

	tok := signToken(t, secret, "cloud-xyz", "some-other-aud", time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 on wrong audience", rec.Code)
	}
	if *called {
		t.Errorf("next handler ran with wrong audience")
	}
}

func TestMiddleware_Success(t *testing.T) {
	secret := "stored-secret-32-bytes-padding-aaa"
	want := &store.Installation{ID: 42, CloudID: "cloud-xyz", SharedSecret: secret}
	fi := &fakeInstallations{inst: want}
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, gotInst := nextStub()
	h := mw(next)

	tok := signToken(t, secret, "cloud-xyz", testAudience, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !*called {
		t.Fatalf("next handler did not run")
	}
	if *gotInst == nil || (*gotInst).CloudID != want.CloudID || (*gotInst).ID != want.ID {
		t.Errorf("installation in ctx = %+v, want %+v", *gotInst, want)
	}
}

func TestMiddleware_CloudIDMismatch(t *testing.T) {
	// The token claims one cloudId; the looked-up installation has a
	// different one. Should reject (defense-in-depth — peekCloudID and
	// Verify both check, but a future bug shouldn't slip).
	secret := "stored-secret-32-bytes-padding-aaa"
	fi := &fakeInstallations{
		inst: &store.Installation{ID: 1, CloudID: "cloud-actual", SharedSecret: secret},
	}
	mw := Middleware(discardLogger(), fi, testAudience)
	next, called, _ := nextStub()
	h := mw(next)

	// Token says cloud-claimed, lookup returns cloud-actual.
	tok := signToken(t, secret, "cloud-claimed", testAudience, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 on cloudId mismatch", rec.Code)
	}
	if *called {
		t.Errorf("next handler ran on cloudId mismatch")
	}
}
