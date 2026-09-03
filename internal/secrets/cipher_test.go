package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func TestCipherRoundTripAndBinding(t *testing.T) {
	cipher, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := cipher.Encrypt([]byte("top-secret"), "webhook_1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envelope), "top-secret") {
		t.Fatal("ciphertext exposed plaintext")
	}
	plaintext, err := cipher.Decrypt(envelope, "webhook_1")
	if err != nil || string(plaintext) != "top-secret" {
		t.Fatalf("round trip failed: %q %v", plaintext, err)
	}
	if _, err := cipher.Decrypt(envelope, "webhook_2"); err == nil {
		t.Fatal("associated data mismatch was accepted")
	}
}
