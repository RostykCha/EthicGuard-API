package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

const testInstallerSecret = "installer-secret-32-bytes-padding-aaaa"

type fakeInstallationsLifecycle struct {
	upserted  *store.Installation
	upsertErr error
	deleted   string
	deleteErr error
}

func (f *fakeInstallationsLifecycle) Upsert(_ context.Context, cloudID, secret string) (*store.Installation, error) {
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	f.upserted = &store.Installation{CloudID: cloudID, SharedSecret: secret}
	return f.upserted, nil
}

func (f *fakeInstallationsLifecycle) DeleteByCloudID(_ context.Context, cloudID string) error {
	f.deleted = cloudID
	return f.deleteErr
}

func newLifecycleHandler(repo InstallationsRepo) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &LifecycleHandler{
		Logger:          logger,
		Installations:   repo,
		InstallerSecret: testInstallerSecret,
	}
}

func mintInstallerToken(t *testing.T, secret, cloudID string, lifetime time.Duration) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"cloudId": cloudID,
		"aud":     auth.AudienceInstaller,
		"iat":     now.Unix(),
		"exp":     now.Add(lifetime).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func doLifecycle(handler http.Handler, token string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/installations/lifecycle", bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestLifecycle_Install_Happy(t *testing.T) {
	repo := &fakeInstallationsLifecycle{}
	handler := newLifecycleHandler(repo)
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)

	rec := doLifecycle(handler, tok, lifecycleReq{
		Event:        "install",
		CloudID:      "cloud-abc",
		SharedSecret: "this-is-a-32-byte-secret-aaaaaaaa",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if repo.upserted == nil || repo.upserted.CloudID != "cloud-abc" {
		t.Errorf("upsert not called or wrong cloudID: %+v", repo.upserted)
	}
}

func TestLifecycle_Install_MissingToken(t *testing.T) {
	handler := newLifecycleHandler(&fakeInstallationsLifecycle{})
	rec := doLifecycle(handler, "", lifecycleReq{Event: "install", CloudID: "x"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLifecycle_Install_BadSignature(t *testing.T) {
	handler := newLifecycleHandler(&fakeInstallationsLifecycle{})
	wrongSecret := "wrong-secret-32-bytes-padding-aaaaa"
	tok := mintInstallerToken(t, wrongSecret, "cloud-abc", time.Minute)
	rec := doLifecycle(handler, tok, lifecycleReq{Event: "install", CloudID: "cloud-abc"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLifecycle_Install_InvalidJSON(t *testing.T) {
	handler := newLifecycleHandler(&fakeInstallationsLifecycle{})
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/v1/installations/lifecycle",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestLifecycle_Install_CloudIDMismatch(t *testing.T) {
	handler := newLifecycleHandler(&fakeInstallationsLifecycle{})
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)
	rec := doLifecycle(handler, tok, lifecycleReq{
		Event: "install", CloudID: "cloud-xyz", SharedSecret: "32-bytes-padding-aaaaaaaaaaaaaaa",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestLifecycle_Install_SecretTooShort(t *testing.T) {
	handler := newLifecycleHandler(&fakeInstallationsLifecycle{})
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)
	rec := doLifecycle(handler, tok, lifecycleReq{
		Event: "install", CloudID: "cloud-abc", SharedSecret: "short",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestLifecycle_Install_PersistFailure(t *testing.T) {
	repo := &fakeInstallationsLifecycle{upsertErr: errors.New("db down")}
	handler := newLifecycleHandler(repo)
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)
	rec := doLifecycle(handler, tok, lifecycleReq{
		Event: "install", CloudID: "cloud-abc",
		SharedSecret: "this-is-a-32-byte-secret-aaaaaaaa",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestLifecycle_Uninstall_Happy(t *testing.T) {
	repo := &fakeInstallationsLifecycle{}
	handler := newLifecycleHandler(repo)
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)

	rec := doLifecycle(handler, tok, lifecycleReq{Event: "uninstall", CloudID: "cloud-abc"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if repo.deleted != "cloud-abc" {
		t.Errorf("delete not called for cloud-abc, got %q", repo.deleted)
	}
}

func TestLifecycle_Uninstall_NotFoundIsIdempotent(t *testing.T) {
	repo := &fakeInstallationsLifecycle{deleteErr: store.ErrNotFound}
	handler := newLifecycleHandler(repo)
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)

	rec := doLifecycle(handler, tok, lifecycleReq{Event: "uninstall", CloudID: "cloud-abc"})

	// Forge retries forever on non-2xx; not-found uninstalls must be 200.
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for idempotent uninstall", rec.Code)
	}
}

func TestLifecycle_Uninstall_DeleteFailure(t *testing.T) {
	repo := &fakeInstallationsLifecycle{deleteErr: errors.New("db down")}
	handler := newLifecycleHandler(repo)
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)

	rec := doLifecycle(handler, tok, lifecycleReq{Event: "uninstall", CloudID: "cloud-abc"})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestLifecycle_UnknownEvent(t *testing.T) {
	handler := newLifecycleHandler(&fakeInstallationsLifecycle{})
	tok := mintInstallerToken(t, testInstallerSecret, "cloud-abc", time.Minute)
	rec := doLifecycle(handler, tok, lifecycleReq{Event: "rotate", CloudID: "cloud-abc"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
