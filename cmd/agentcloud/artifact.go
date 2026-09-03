package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/daemon"
	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/p2p"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

const artifactPlainChunkSize int64 = 4 << 20

func artifactCommand(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, cache *daemon.ArtifactCache, stateDir string, args []string) error {
	if len(args) >= 3 && args[0] == "peer-serve" {
		return servePeerArtifact(ctx, args[1], args[2], args[3:])
	}
	if len(args) == 5 && args[0] == "peer-download" {
		return downloadPeerArtifact(ctx, args[1], args[2], args[3], args[4])
	}
	if len(args) >= 4 && args[0] == "upload" {
		return uploadArtifact(ctx, api, stateStore, keyring, stateDir, args[1], args[2], args[3], args[4:])
	}
	if len(args) == 4 && args[0] == "download" {
		return downloadArtifact(ctx, api, stateStore, keyring, cache, stateDir, args[1], args[2], args[3])
	}
	return errors.New("usage: agentcloud artifact upload TASK ARTIFACT FILE [--parent VERSION] [--encrypt] [--upload-id ID] | artifact download TASK ARTIFACT DEST | artifact peer-serve FILE ADDRESS [--ttl SECONDS] [--allow-public] | artifact peer-download URL TOKEN SHA256 DEST")
}

func servePeerArtifact(ctx context.Context, sourcePath, address string, options []string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("peer address must be HOST:PORT: %w", err)
	}
	if host == "" {
		host = "0.0.0.0"
	}
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	certificatePath, keyPath := optionValue(options, "--tls-cert"), optionValue(options, "--tls-key")
	useTLS := certificatePath != "" || keyPath != ""
	if useTLS && (certificatePath == "" || keyPath == "") {
		return errors.New("both --tls-cert and --tls-key are required")
	}
	if !loopback && (!contains(options, "--allow-public") || !useTLS) {
		return errors.New("non-loopback peer listener requires --allow-public plus --tls-cert and --tls-key")
	}
	ttl := 15 * time.Minute
	if value := optionValue(options, "--ttl"); value != "" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
		ttl = time.Duration(seconds) * time.Second
	}
	digest, _, err := hashFile(sourcePath)
	if err != nil {
		return err
	}
	sender, offer, err := p2p.NewSender(sourcePath, digest, ttl, p2p.DefaultMaxBytes)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	publicHost := host
	if publicHost == "0.0.0.0" || publicHost == "::" {
		publicHost = "DEVICE_IP"
	}
	_, actualPort, _ := net.SplitHostPort(listener.Addr().String())
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	printJSON(map[string]any{"endpoint": scheme + "://" + net.JoinHostPort(publicHost, actualPort) + "/v1/artifacts/" + strings.TrimPrefix(digest, "sha256:"), "offer": offer, "warning": "share this short-lived capability only through an authorized task channel"})
	server := &http.Server{Handler: sender, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	stopped := make(chan error, 1)
	go func() {
		if useTLS {
			stopped <- server.ServeTLS(listener, certificatePath, keyPath)
			return
		}
		stopped <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func downloadPeerArtifact(ctx context.Context, endpoint, token, digest, destination string) error {
	if err := (p2p.Receiver{}).Download(ctx, endpoint, token, digest, destination); err != nil {
		return err
	}
	fmt.Println(destination)
	return nil
}

func uploadArtifact(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, stateDir, taskID, artifactID, sourcePath string, options []string) error {
	uploadID := optionValue(options, "--upload-id")
	parentVersion := optionValue(options, "--parent")
	encrypt := contains(options, "--encrypt")
	var status protocol.ArtifactUploadStatus
	preparedPath := sourcePath
	stagedEncrypted := false
	if uploadID != "" {
		var err error
		status, err = api.GetArtifactUpload(ctx, uploadID)
		if err != nil {
			return err
		}
		if status.Session.TaskID != taskID || status.Session.ArtifactID != artifactID {
			return errors.New("upload ID does not match task and artifact")
		}
		if len(status.Session.EncryptionMetadata) > 0 {
			preparedPath = filepath.Join(stateDir, "upload-staging", uploadID+".bin")
			stagedEncrypted = true
		}
	} else {
		versionID, _ := protocol.NewID("version")
		contentType := mime.TypeByExtension(filepath.Ext(sourcePath))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		var size int64
		var digest string
		var encryptionMetadata json.RawMessage
		var temporaryPath string
		var err error
		if encrypt {
			temporaryPath, size, digest, contentType, encryptionMetadata, err = prepareEncryptedArtifact(ctx, api, stateStore, keyring, stateDir, taskID, artifactID, versionID, sourcePath, contentType)
			if err != nil {
				return err
			}
			preparedPath = temporaryPath
			stagedEncrypted = true
		} else {
			digest, size, err = hashFile(sourcePath)
			if err != nil {
				return err
			}
		}
		input := map[string]any{"artifact_id": artifactID, "version_id": versionID, "parent_version_id": parentVersion, "filename": filepath.Base(sourcePath), "content_type": contentType, "total_size": size, "chunk_size": artifactPlainChunkSize, "content_hash": digest, "encryption_metadata": encryptionMetadata}
		if encrypt {
			input["chunk_size"] = artifactPlainChunkSize + 16
		}
		status, err = api.CreateArtifactUpload(ctx, taskID, input)
		if err != nil {
			if temporaryPath != "" {
				_ = os.Remove(temporaryPath)
			}
			return err
		}
		uploadID = status.Session.ID
		fmt.Printf("upload session: %s\n", uploadID)
		if stagedEncrypted {
			directory := filepath.Join(stateDir, "upload-staging")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return err
			}
			stable := filepath.Join(directory, uploadID+".bin")
			if err := os.Rename(preparedPath, stable); err != nil {
				return err
			}
			preparedPath = stable
		}
	}
	digest, size, err := hashFile(preparedPath)
	if err != nil {
		return err
	}
	if digest != status.Session.ContentHash || size != status.Session.TotalSize {
		return errors.New("local upload source no longer matches the upload manifest")
	}
	file, err := os.Open(preparedPath)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, index := range status.MissingChunks {
		offset := int64(index) * status.Session.ChunkSize
		size := status.Session.ChunkSize
		if index == status.Session.TotalChunks-1 {
			size = status.Session.TotalSize - offset
		}
		chunk := make([]byte, size)
		if _, err := file.ReadAt(chunk, offset); err != nil {
			return fmt.Errorf("read chunk %d: %w", index, err)
		}
		if _, err := api.PutArtifactChunk(ctx, uploadID, index, chunk, protocol.Digest(chunk)); err != nil {
			return fmt.Errorf("upload %s interrupted at chunk %d; resume with --upload-id %s: %w", uploadID, index, uploadID, err)
		}
		fmt.Printf("uploaded chunk %d/%d\n", index+1, status.Session.TotalChunks)
	}
	artifact, err := api.CompleteArtifactUpload(ctx, uploadID)
	if err != nil {
		return fmt.Errorf("complete upload %s: %w", uploadID, err)
	}
	if stagedEncrypted {
		_ = os.Remove(preparedPath)
	}
	printJSON(artifact)
	return nil
}

func prepareEncryptedArtifact(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, stateDir, taskID, artifactID, versionID, sourcePath, contentType string) (string, int64, string, string, json.RawMessage, error) {
	state, err := stateStore.Load()
	if err != nil {
		return "", 0, "", "", nil, err
	}
	key, keyVersion, err := ensureTaskKey(ctx, api, state, keyring, taskID, 0)
	if err != nil {
		return "", 0, "", "", nil, err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", 0, "", "", nil, err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Join(stateDir, "upload-staging"), 0o700); err != nil {
		return "", 0, "", "", nil, err
	}
	target, err := os.CreateTemp(filepath.Join(stateDir, "upload-staging"), ".encrypt-*")
	if err != nil {
		return "", 0, "", "", nil, err
	}
	targetPath := target.Name()
	failed := true
	defer func() {
		_ = target.Close()
		if failed {
			_ = os.Remove(targetPath)
		}
	}()
	_ = target.Chmod(0o600)
	plaintextHash := sha256.New()
	ciphertextHash := sha256.New()
	metadata := protocol.ArtifactEncryptionMetadata{Version: 1, Algorithm: "A256GCM-chunked", KeyVersion: keyVersion, OriginalContentType: contentType, PlaintextChunkSize: artifactPlainChunkSize}
	buffer := make([]byte, artifactPlainChunkSize)
	for index := 0; ; index++ {
		read, readErr := io.ReadFull(source, buffer)
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return "", 0, "", "", nil, readErr
		}
		plaintext := buffer[:read]
		_, _ = plaintextHash.Write(plaintext)
		metadata.PlaintextSize += int64(read)
		ciphertext, chunkMetadata, err := e2ee.EncryptArtifactChunk(key, keyVersion, artifactID, versionID, index, plaintext)
		if err != nil {
			return "", 0, "", "", nil, err
		}
		if _, err := target.Write(ciphertext); err != nil {
			return "", 0, "", "", nil, err
		}
		_, _ = ciphertextHash.Write(ciphertext)
		metadata.Chunks = append(metadata.Chunks, chunkMetadata)
		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	if len(metadata.Chunks) == 0 {
		ciphertext, chunkMetadata, err := e2ee.EncryptArtifactChunk(key, keyVersion, artifactID, versionID, 0, nil)
		if err != nil {
			return "", 0, "", "", nil, err
		}
		if _, err := target.Write(ciphertext); err != nil {
			return "", 0, "", "", nil, err
		}
		_, _ = ciphertextHash.Write(ciphertext)
		metadata.Chunks = append(metadata.Chunks, chunkMetadata)
	}
	metadata.PlaintextDigest = "sha256:" + hex.EncodeToString(plaintextHash.Sum(nil))
	if err := target.Sync(); err != nil {
		return "", 0, "", "", nil, err
	}
	if err := target.Close(); err != nil {
		return "", 0, "", "", nil, err
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return "", 0, "", "", nil, err
	}
	raw, _ := json.Marshal(metadata)
	failed = false
	return targetPath, info.Size(), "sha256:" + hex.EncodeToString(ciphertextHash.Sum(nil)), "application/vnd.agent-continuity.encrypted-artifact", raw, nil
}

func downloadArtifact(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, cache *daemon.ArtifactCache, stateDir, taskID, artifactID, destination string) error {
	items, err := api.ListArtifacts(ctx, taskID)
	if err != nil {
		return err
	}
	var manifest protocol.Artifact
	for _, item := range items {
		if item.ID == artifactID {
			manifest = item
			break
		}
	}
	if manifest.ID == "" {
		return errors.New("artifact not found in task manifest")
	}
	cached, err := cache.Open(manifest.ContentHash)
	if err != nil {
		cached, err = fetchArtifactIntoCache(ctx, api, cache, stateDir, manifest)
		if err != nil {
			return err
		}
	}
	defer cached.Close()
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(directory, ".artifact-download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	_ = tmp.Chmod(0o600)
	if len(manifest.EncryptionMetadata) == 0 {
		_, err = io.Copy(tmp, cached)
	} else {
		err = decryptArtifactTo(ctx, api, stateStore, keyring, manifest, cached, tmp)
	}
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return err
	}
	fmt.Println(destination)
	return nil
}

func fetchArtifactIntoCache(ctx context.Context, api *client.Client, cache *daemon.ArtifactCache, stateDir string, manifest protocol.Artifact) (*os.File, error) {
	directory := filepath.Join(stateDir, "download-partials")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	partial := filepath.Join(directory, strings.TrimPrefix(manifest.ContentHash, "sha256:")+".part")
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.Size() > manifest.Size {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, err
		}
		info, _ = file.Stat()
	}
	if _, err := file.Seek(info.Size(), io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := api.DownloadArtifact(ctx, manifest.TaskID, manifest.ID, file, info.Size()); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("download interrupted at byte %d; rerun to resume: %w", info.Size(), err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	_, _, putErr := cache.Put(manifest.ContentHash, file)
	_ = file.Close()
	if putErr != nil {
		_ = os.Remove(partial)
		return nil, putErr
	}
	_ = os.Remove(partial)
	return cache.Open(manifest.ContentHash)
}

func decryptArtifactTo(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, manifest protocol.Artifact, source io.Reader, destination io.Writer) error {
	var metadata protocol.ArtifactEncryptionMetadata
	if err := json.Unmarshal(manifest.EncryptionMetadata, &metadata); err != nil || metadata.Version != 1 || metadata.Algorithm != "A256GCM-chunked" {
		return errors.New("unsupported artifact encryption metadata")
	}
	state, err := stateStore.Load()
	if err != nil {
		return err
	}
	key, _, err := ensureTaskKey(ctx, api, state, keyring, manifest.TaskID, metadata.KeyVersion)
	if err != nil {
		return err
	}
	plaintextHash := sha256.New()
	var plaintextSize int64
	for index, chunkMetadata := range metadata.Chunks {
		plainSize := metadata.PlaintextChunkSize
		if index == len(metadata.Chunks)-1 {
			plainSize = metadata.PlaintextSize - int64(index)*metadata.PlaintextChunkSize
		}
		ciphertext := make([]byte, plainSize+16)
		if _, err := io.ReadFull(source, ciphertext); err != nil {
			return err
		}
		plaintext, err := e2ee.DecryptArtifactChunk(key, manifest.ID, manifest.VersionID, chunkMetadata, ciphertext)
		if err != nil {
			return err
		}
		if _, err := destination.Write(plaintext); err != nil {
			return err
		}
		_, _ = plaintextHash.Write(plaintext)
		plaintextSize += int64(len(plaintext))
	}
	if plaintextSize != metadata.PlaintextSize || "sha256:"+hex.EncodeToString(plaintextHash.Sum(nil)) != metadata.PlaintextDigest {
		return errors.New("decrypted artifact integrity mismatch")
	}
	return nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, err
}

func optionValue(values []string, option string) string {
	for index, value := range values {
		if value == option && index+1 < len(values) {
			return values[index+1]
		}
	}
	return ""
}
