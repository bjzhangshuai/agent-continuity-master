package payments

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
)

func privateKey(raw []byte) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, errors.New("private key must be PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		k, err = x509.ParsePKCS1PrivateKey(b.Bytes)
	}
	if err != nil {
		return nil, err
	}
	r, ok := k.(*rsa.PrivateKey)
	if !ok || r.N.BitLen() < 2048 {
		return nil, errors.New("RSA key must be at least 2048 bits")
	}
	return r, nil
}
func publicKey(raw []byte) (*rsa.PublicKey, error) {
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, errors.New("public key must be PEM")
	}
	var k any
	var err error
	if b.Type == "CERTIFICATE" {
		var c *x509.Certificate
		c, err = x509.ParseCertificate(b.Bytes)
		if err == nil {
			k = c.PublicKey
		}
	} else {
		k, err = x509.ParsePKIXPublicKey(b.Bytes)
	}
	if err != nil {
		return nil, err
	}
	r, ok := k.(*rsa.PublicKey)
	if !ok || r.N.BitLen() < 2048 {
		return nil, errors.New("RSA public key must be at least 2048 bits")
	}
	return r, nil
}
func signRSA(k *rsa.PrivateKey, s string) (string, error) {
	d := sha256.Sum256([]byte(s))
	v, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
	return base64.StdEncoding.EncodeToString(v), err
}
func verifyRSA(k *rsa.PublicKey, s, signature string) error {
	if k == nil {
		return ErrSignature
	}
	b, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return ErrSignature
	}
	d := sha256.Sum256([]byte(s))
	if rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], b) != nil {
		return ErrSignature
	}
	return nil
}
