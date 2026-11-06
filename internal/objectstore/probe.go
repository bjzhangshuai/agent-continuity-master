package objectstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// Probe uses a random, task-owned key only. It never lists or deletes existing
// objects. On failure the key is returned so the operator can clean it up.
func Probe(ctx context.Context, p Provider, target Target) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	key, err := target.Key("diagnostics/" + hex.EncodeToString(nonce) + "/roundtrip.txt")
	if err != nil {
		return "", err
	}
	data := []byte("Agent Continuity storage contract probe\n文字、Markdown、图片及文件均使用相同字节归档协议。\n")
	sum := sha256.Sum256(data)
	if err = p.Put(ctx, Object{Key: key, Size: int64(len(data)), ContentType: "text/plain; charset=utf-8", SHA256: "sha256:" + hex.EncodeToString(sum[:])}, bytes.NewReader(data)); err != nil {
		return key, errors.New("probe PUT failed")
	}
	body, err := p.Get(ctx, key)
	if err != nil {
		return key, errors.New("probe GET failed")
	}
	got, err := io.ReadAll(io.LimitReader(body, int64(len(data)+1)))
	body.Close()
	if err != nil || !bytes.Equal(got, data) {
		return key, errors.New("probe content integrity failed")
	}
	if err = p.Delete(ctx, key); err != nil {
		return key, errors.New("probe DELETE failed")
	}
	return key, nil
}
