package identity

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"
)

func TestOIDCVerifierValidatesSignatureIssuerAudienceAndLifetime(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	exponent := make([]byte, 4)
	binary.BigEndian.PutUint32(exponent, uint32(private.PublicKey.E))
	for len(exponent) > 1 && exponent[0] == 0 {
		exponent = exponent[1:]
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(private.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(exponent)}}})
	verifier, err := NewOIDCVerifier("https://idp.example", "continuity", jwks)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	verifier.now = func() time.Time { return now }
	token := signOIDCTestToken(t, private, map[string]any{"iss": "https://idp.example", "sub": "person-1", "aud": []string{"continuity"}, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "email": "USER@example.com", "email_verified": true, "name": "Test User"})
	claims, err := verifier.Verify(token)
	if err != nil || claims.Subject != "person-1" || claims.Email != "user@example.com" || !claims.EmailVerified {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	badAudience := signOIDCTestToken(t, private, map[string]any{"iss": "https://idp.example", "sub": "person-1", "aud": "other", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()})
	if _, err := verifier.Verify(badAudience); err == nil {
		t.Fatal("wrong audience was accepted")
	}
	parts := []byte(token)
	parts[len(parts)-1] ^= 1
	if _, err := verifier.Verify(string(parts)); err == nil {
		t.Fatal("tampered signature was accepted")
	}
}

func signOIDCTestToken(t *testing.T, private *rsa.PrivateKey, claims any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}
