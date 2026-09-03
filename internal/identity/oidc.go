package identity

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"
)

type OIDCClaims struct {
	Issuer        string
	Subject       string
	Email         string
	Name          string
	EmailVerified bool
}

type OIDCVerifier struct {
	issuer   string
	audience string
	keys     map[string]*rsa.PublicKey
	now      func() time.Time
}

func NewOIDCVerifier(issuer, audience string, jwks []byte) (*OIDCVerifier, error) {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("OIDC issuer must be an absolute HTTPS URL")
	}
	if strings.TrimSpace(audience) == "" {
		return nil, errors.New("OIDC audience is required")
	}
	var document struct {
		Keys []struct {
			KID string   `json:"kid"`
			KTY string   `json:"kty"`
			Use string   `json:"use"`
			Alg string   `json:"alg"`
			N   string   `json:"n"`
			E   string   `json:"e"`
			X5C []string `json:"x5c"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(jwks, &document); err != nil {
		return nil, errors.New("invalid OIDC JWKS")
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, value := range document.Keys {
		if value.KID == "" || value.KTY != "RSA" || value.Use != "sig" || value.Alg != "RS256" {
			continue
		}
		key, err := rsaJWK(value.N, value.E, value.X5C)
		if err == nil && key.N.BitLen() >= 2048 {
			keys[value.KID] = key
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("OIDC JWKS has no usable 2048-bit RS256 signing key")
	}
	return &OIDCVerifier{issuer: strings.TrimRight(issuer, "/"), audience: audience, keys: keys, now: time.Now}, nil
}

func (v *OIDCVerifier) Verify(raw string) (OIDCClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || len(raw) > 64<<10 {
		return OIDCClaims{}, errors.New("invalid OIDC token")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return OIDCClaims{}, errors.New("invalid OIDC header")
	}
	var header struct {
		Alg string `json:"alg"`
		KID string `json:"kid"`
	}
	if json.Unmarshal(headerRaw, &header) != nil || header.Alg != "RS256" || header.KID == "" {
		return OIDCClaims{}, errors.New("unsupported OIDC signature")
	}
	key := v.keys[header.KID]
	if key == nil {
		return OIDCClaims{}, errors.New("unknown OIDC signing key")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return OIDCClaims{}, errors.New("invalid OIDC signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return OIDCClaims{}, errors.New("OIDC signature verification failed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return OIDCClaims{}, errors.New("invalid OIDC claims")
	}
	var claims struct {
		Issuer        string          `json:"iss"`
		Subject       string          `json:"sub"`
		Audience      json.RawMessage `json:"aud"`
		Expires       int64           `json:"exp"`
		IssuedAt      int64           `json:"iat"`
		Email         string          `json:"email"`
		Name          string          `json:"name"`
		EmailVerified bool            `json:"email_verified"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Subject == "" || strings.TrimRight(claims.Issuer, "/") != v.issuer || !hasAudience(claims.Audience, v.audience) {
		return OIDCClaims{}, errors.New("OIDC issuer, subject, or audience is invalid")
	}
	now := v.now().Unix()
	if claims.Expires <= now || claims.Expires > now+24*60*60 || claims.IssuedAt == 0 || claims.IssuedAt > now+120 {
		return OIDCClaims{}, errors.New("OIDC token lifetime is invalid")
	}
	return OIDCClaims{Issuer: claims.Issuer, Subject: claims.Subject, Email: strings.ToLower(strings.TrimSpace(claims.Email)), Name: strings.TrimSpace(claims.Name), EmailVerified: claims.EmailVerified}, nil
}

func hasAudience(raw json.RawMessage, expected string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == expected
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	for _, value := range many {
		if value == expected {
			return true
		}
	}
	return false
}

func rsaJWK(n, e string, certificateChain []string) (*rsa.PublicKey, error) {
	if len(certificateChain) > 0 {
		der, err := base64.StdEncoding.DecodeString(certificateChain[0])
		if err != nil {
			return nil, err
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		key, ok := certificate.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("certificate is not RSA")
		}
		return key, nil
	}
	modulus, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil || len(modulus) == 0 {
		return nil, errors.New("invalid RSA modulus")
	}
	exponentRaw, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil || len(exponentRaw) == 0 || len(exponentRaw) > 4 {
		return nil, errors.New("invalid RSA exponent")
	}
	padded := make([]byte, 4)
	copy(padded[4-len(exponentRaw):], exponentRaw)
	exponent := int(binary.BigEndian.Uint32(padded))
	if exponent < 3 || exponent%2 == 0 {
		return nil, errors.New("invalid RSA exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: exponent}, nil
}
