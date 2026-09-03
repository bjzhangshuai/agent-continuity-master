package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PortableSignaturePrefix identifies signatures made over the language-neutral
// AC-EVENT-1 canonical representation. Legacy Go JSON signatures remain valid.
const PortableSignaturePrefix = "ac1."

func NewID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

func Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func SigningBytes(event Event) ([]byte, error) {
	event.Signature = ""
	event.ServerSequence = 0
	event.ReceivedAtServer = time.Time{}
	return json.Marshal(event)
}

func SignEvent(event *Event, privateKey ed25519.PrivateKey) error {
	b, err := SigningBytes(*event)
	if err != nil {
		return err
	}
	event.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, b))
	return nil
}

// SignPortableEvent signs an event using a representation that is stable across
// Go, JavaScript, Python, Swift, Kotlin, and .NET JSON implementations.
func SignPortableEvent(event *Event, privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key length")
	}
	b, err := PortableEventSigningBytes(*event)
	if err != nil {
		return err
	}
	event.Signature = PortableSignaturePrefix + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, b))
	return nil
}

func VerifyEventSignature(event Event, encodedPublicKey string) error {
	publicKey, err := base64.RawURLEncoding.DecodeString(encodedPublicKey)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid Ed25519 public key length")
	}
	signatureValue := event.Signature
	portable := strings.HasPrefix(signatureValue, PortableSignaturePrefix)
	if portable {
		signatureValue = strings.TrimPrefix(signatureValue, PortableSignaturePrefix)
	}
	signature, err := base64.RawURLEncoding.DecodeString(signatureValue)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	var b []byte
	if portable {
		b, err = PortableEventSigningBytes(event)
	} else {
		b, err = SigningBytes(event)
	}
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), b, signature) {
		return errors.New("invalid event signature")
	}
	return nil
}

// PortableEventSigningBytes produces the canonical AC-EVENT-1 wire form. Each
// free-form string is base64url encoded before fields are newline-delimited, so
// embedded delimiters cannot make two events share the same signing bytes.
func PortableEventSigningBytes(event Event) ([]byte, error) {
	payload, err := portableJSON(event.Payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalize payload: %w", err)
	}
	extensions, err := portableJSON(event.Extensions)
	if err != nil {
		return nil, fmt.Errorf("canonicalize extensions: %w", err)
	}
	fields := []string{
		"AC-EVENT-1",
		portableString(event.EventID),
		portableString(event.TaskID),
		portableString(event.BranchID),
		portableString(event.AccountID),
		portableString(event.ParticipantID),
		portableString(event.AgentID),
		portableString(event.ProviderID),
		portableString(event.DeviceID),
		portableString(event.SessionRef),
		portableStrings(event.ParentEventIDs),
		strconv.FormatInt(event.LogicalTime.WallTimeMS, 10),
		strconv.FormatUint(uint64(event.LogicalTime.Counter), 10),
		portableString(event.LogicalTime.Node),
		strconv.FormatUint(event.DeviceSequence, 10),
		portableString(event.Type),
		portableString(event.SchemaVersion),
		portableString(event.ContentType),
		portableString(event.ContentDigest),
		base64.RawURLEncoding.EncodeToString(payload),
		portableStrings(event.ArtifactRefs),
		strconv.FormatInt(event.CreatedAtClient.UnixMilli(), 10),
		portableString(event.IdempotencyKey),
		base64.RawURLEncoding.EncodeToString(extensions),
	}
	return []byte(strings.Join(fields, "\n")), nil
}

func portableString(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func portableStrings(values []string) string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		encoded = append(encoded, portableString(value))
	}
	return strings.Join(encoded, ".")
}

func portableJSON(value json.RawMessage) ([]byte, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if !json.Valid(value) {
		return nil, errors.New("invalid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func QRInvitationSigningBytes(value QRInvitation) ([]byte, error) {
	value.Signature = ""
	value.Token = ""
	return json.Marshal(value)
}

func SignQRInvitation(value *QRInvitation, privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key length")
	}
	data, err := QRInvitationSigningBytes(*value)
	if err != nil {
		return err
	}
	value.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, data))
	return nil
}

func VerifyQRInvitation(value QRInvitation, encodedPublicKey string) error {
	publicKey, err := base64.RawURLEncoding.DecodeString(encodedPublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid Ed25519 public key")
	}
	signature, err := base64.RawURLEncoding.DecodeString(value.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid QR invitation signature")
	}
	data, err := QRInvitationSigningBytes(value)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), data, signature) {
		return errors.New("invalid QR invitation signature")
	}
	return nil
}
