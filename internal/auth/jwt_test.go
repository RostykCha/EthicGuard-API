package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func mint(t *testing.T, secret, audience, cloudID string, exp time.Duration) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"cloudId": cloudID,
		"aud":     audience,
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(exp).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestVerifyHappyPath(t *testing.T) {
	secret := "s3cr3t"
	token := mint(t, secret, AudienceAPI, "cloud-abc", 2*time.Minute)

	claims, err := Verify(token, secret, AudienceAPI)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.CloudID != "cloud-abc" {
		t.Errorf("CloudID = %q, want cloud-abc", claims.CloudID)
	}
	if claims.Audience != AudienceAPI {
		t.Errorf("Audience = %q, want %q", claims.Audience, AudienceAPI)
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	secret := "s3cr3t"
	token := mint(t, secret, AudienceInstaller, "cloud-abc", 2*time.Minute)

	if _, err := Verify(token, secret, AudienceAPI); err == nil {
		t.Fatal("expected Verify to reject wrong audience")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	token := mint(t, "correct", AudienceAPI, "cloud-abc", 2*time.Minute)

	if _, err := Verify(token, "wrong", AudienceAPI); err == nil {
		t.Fatal("expected Verify to reject wrong secret")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	secret := "s3cr3t"
	token := mint(t, secret, AudienceAPI, "cloud-abc", -1*time.Minute)

	if _, err := Verify(token, secret, AudienceAPI); err == nil {
		t.Fatal("expected Verify to reject expired token")
	}
}

func TestVerifyRejectsEmpty(t *testing.T) {
	if _, err := Verify("", "secret", AudienceAPI); err == nil {
		t.Fatal("expected Verify to reject empty token")
	}
}
