package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var ErrHashMismatch = errors.New("artifact hash mismatch")

type Service struct {
	root       string
	maxBytes   int64
	remote     *s3Backend
	quotaBytes int64
	quotaMu    sync.Mutex
}

type S3Config struct {
	Endpoint     string
	Bucket       string
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	AutoCreate   bool
}

type s3Backend struct {
	client *minio.Client
	bucket string
}

func New(root string, maxBytes int64) (*Service, error) {
	if root == "" || maxBytes <= 0 {
		return nil, errors.New("artifact root and positive maxBytes are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	return &Service{root: absolute, maxBytes: maxBytes}, nil
}

func NewS3(root string, maxBytes int64, config S3Config) (*Service, error) {
	service, err := New(root, maxBytes)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("S3 endpoint must be an absolute HTTP(S) origin")
	}
	if config.Bucket == "" || config.AccessKey == "" || config.SecretKey == "" {
		return nil, errors.New("S3 bucket and credentials are required")
	}
	client, err := minio.New(parsed.Host, &minio.Options{Creds: credentials.NewStaticV4(config.AccessKey, config.SecretKey, config.SessionToken), Secure: parsed.Scheme == "https", Region: config.Region, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		return nil, err
	}
	client.SetAppInfo("agent-continuity", "0.1.0")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exists, err := client.BucketExists(ctx, config.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check S3 bucket: %w", err)
	}
	if !exists {
		if !config.AutoCreate {
			return nil, errors.New("S3 bucket does not exist")
		}
		if err := client.MakeBucket(ctx, config.Bucket, minio.MakeBucketOptions{Region: config.Region}); err != nil {
			return nil, fmt.Errorf("create S3 bucket: %w", err)
		}
	}
	service.remote = &s3Backend{client: client, bucket: config.Bucket}
	return service, nil
}

func (s *Service) Put(reader io.Reader, expected string) (storageRef string, size int64, err error) {
	return s.putLimited(reader, expected, s.maxBytes)
}

func (s *Service) putLimited(reader io.Reader, expected string, limit int64) (storageRef string, size int64, err error) {
	release, err := s.reserveSpace(limit + 1)
	if err != nil {
		return "", 0, err
	}
	defer release()
	if !strings.HasPrefix(expected, "sha256:") || len(expected) != len("sha256:")+64 {
		return "", 0, errors.New("expected a sha256 content hash")
	}
	tmp, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(reader, limit+1))
	if copyErr != nil {
		_ = tmp.Close()
		return "", written, copyErr
	}
	if written > limit {
		_ = tmp.Close()
		return "", written, fmt.Errorf("artifact exceeds %d byte limit", limit)
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		_ = tmp.Close()
		return "", written, ErrHashMismatch
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", written, err
	}
	if err := tmp.Close(); err != nil {
		return "", written, err
	}
	hexHash := strings.TrimPrefix(actual, "sha256:")
	directory := filepath.Join(s.root, "sha256", hexHash[:2])
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", written, err
	}
	destination := filepath.Join(directory, hexHash)
	objectKey := "sha256/" + hexHash[:2] + "/" + hexHash
	if _, err := os.Stat(destination); err == nil {
		if err := s.uploadRemote(destination, objectKey, written, actual); err != nil {
			return "", written, err
		}
		return s.storageReference(objectKey), written, nil
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return "", written, err
	}
	if err := os.Chmod(destination, 0o640); err != nil {
		return "", written, err
	}
	if err := syncDirectory(directory); err != nil {
		return "", written, err
	}
	if err := s.uploadRemote(destination, objectKey, written, actual); err != nil {
		return "", written, err
	}
	return s.storageReference(objectKey), written, nil
}

func (s *Service) PutChunk(uploadID string, index int, reader io.Reader, expected string, expectedSize int64) (string, int64, error) {
	if !safeSegment(uploadID) || index < 0 || expectedSize < 0 || expectedSize > s.maxBytes {
		return "", 0, errors.New("invalid upload chunk parameters")
	}
	release, err := s.reserveSpace(expectedSize + 1)
	if err != nil {
		return "", 0, err
	}
	defer release()
	if !strings.HasPrefix(expected, "sha256:") || len(expected) != len("sha256:")+64 {
		return "", 0, errors.New("expected a sha256 content hash")
	}
	directory := filepath.Join(s.root, "uploads", uploadID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(directory, ".chunk-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(reader, expectedSize+1))
	if err != nil {
		_ = tmp.Close()
		return "", written, err
	}
	if written != expectedSize {
		_ = tmp.Close()
		return "", written, errors.New("chunk size mismatch")
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		_ = tmp.Close()
		return "", written, ErrHashMismatch
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", written, err
	}
	if err := tmp.Close(); err != nil {
		return "", written, err
	}
	name := fmt.Sprintf("%08d.chunk", index)
	destination := filepath.Join(directory, name)
	if existing, err := os.Open(destination); err == nil {
		existingHash := sha256.New()
		existingSize, readErr := io.Copy(existingHash, existing)
		_ = existing.Close()
		if readErr != nil || existingSize != written || "sha256:"+hex.EncodeToString(existingHash.Sum(nil)) != expected {
			return "", written, errors.New("chunk index already contains different content")
		}
		return s.publishChunk(destination, uploadID, name, expected, written)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return "", written, err
	}
	if err := os.Chmod(destination, 0o640); err != nil {
		return "", written, err
	}
	if err := syncDirectory(directory); err != nil {
		return "", written, err
	}
	return s.publishChunk(destination, uploadID, name, expected, written)
}

func (s *Service) publishChunk(localPath, uploadID, name, digest string, size int64) (string, int64, error) {
	localRef := filepath.ToSlash(filepath.Join("uploads", uploadID, name))
	if s.remote == nil {
		return localRef, size, nil
	}
	objectKey := filepath.ToSlash(filepath.Join("uploads", uploadID, name, strings.TrimPrefix(digest, "sha256:")))
	if err := s.uploadRemote(localPath, objectKey, size, digest); err != nil {
		return "", size, err
	}
	return "s3://" + s.remote.bucket + "/" + objectKey, size, nil
}

func (s *Service) Assemble(uploadID string, storageRefs []string, expected string, totalSize int64) (string, int64, error) {
	if !safeSegment(uploadID) || len(storageRefs) == 0 || totalSize < 0 || totalSize > 1<<30 {
		return "", 0, errors.New("invalid upload assembly parameters")
	}
	closers := make([]io.Closer, 0, len(storageRefs))
	cancels := make([]context.CancelFunc, 0, len(storageRefs))
	readers := make([]io.Reader, 0, len(storageRefs))
	defer func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
		for _, cancel := range cancels {
			cancel()
		}
	}()
	prefix := filepath.ToSlash(filepath.Join("uploads", uploadID)) + "/"
	for _, ref := range storageRefs {
		if strings.HasPrefix(ref, "s3://") {
			if s.remote == nil {
				return "", 0, errors.New("S3 chunk cannot be assembled by a local backend")
			}
			remotePrefix := "s3://" + s.remote.bucket + "/" + prefix
			if !strings.HasPrefix(ref, remotePrefix) {
				return "", 0, errors.New("S3 chunk reference does not belong to upload")
			}
			objectKey := strings.TrimPrefix(ref, "s3://"+s.remote.bucket+"/")
			if clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(objectKey))); clean != objectKey || strings.Contains(clean, "..") {
				return "", 0, errors.New("invalid S3 chunk reference")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			object, err := s.remote.client.GetObject(ctx, s.remote.bucket, objectKey, minio.GetObjectOptions{})
			if err != nil {
				cancel()
				return "", 0, fmt.Errorf("open S3 upload chunk: %w", err)
			}
			closers = append(closers, object)
			cancels = append(cancels, cancel)
			readers = append(readers, object)
			continue
		}
		if !strings.HasPrefix(ref, prefix) {
			return "", 0, errors.New("chunk reference does not belong to upload")
		}
		path := filepath.Join(s.root, filepath.FromSlash(ref))
		if !strings.HasPrefix(path, s.root+string(os.PathSeparator)) {
			return "", 0, errors.New("chunk reference escapes root")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", 0, err
		}
		closers = append(closers, file)
		readers = append(readers, file)
	}
	ref, size, err := s.putLimited(io.MultiReader(readers...), expected, totalSize)
	if err != nil {
		return "", size, err
	}
	if size != totalSize {
		return "", size, errors.New("assembled artifact size mismatch")
	}
	return ref, size, nil
}

func (s *Service) RemoveUpload(uploadID string) error {
	if !safeSegment(uploadID) {
		return errors.New("invalid upload ID")
	}
	if err := os.RemoveAll(filepath.Join(s.root, "uploads", uploadID)); err != nil {
		return err
	}
	if s.remote == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	prefix := filepath.ToSlash(filepath.Join("uploads", uploadID)) + "/"
	for object := range s.remote.client.ListObjects(ctx, s.remote.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return object.Err
		}
		if err := s.remote.client.RemoveObject(ctx, s.remote.bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func safeSegment(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`)
}

func (s *Service) Open(storageRef string) (*os.File, error) {
	if strings.HasPrefix(storageRef, "s3://") {
		return s.openRemote(storageRef)
	}
	clean := filepath.Clean(storageRef)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || !strings.HasPrefix(clean, "sha256/") {
		return nil, errors.New("invalid storage reference")
	}
	path := filepath.Join(s.root, clean)
	if !strings.HasPrefix(path, s.root+string(os.PathSeparator)) {
		return nil, errors.New("storage reference escapes root")
	}
	return os.Open(path)
}

func (s *Service) storageReference(objectKey string) string {
	if s.remote == nil {
		return objectKey
	}
	return "s3://" + s.remote.bucket + "/" + objectKey
}

func (s *Service) uploadRemote(localPath, objectKey string, size int64, digest string) error {
	if s.remote == nil {
		return nil
	}
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err = s.remote.client.PutObject(ctx, s.remote.bucket, objectKey, file, size, minio.PutObjectOptions{ContentType: "application/octet-stream", UserMetadata: map[string]string{"content-digest": digest}})
	if err != nil {
		return fmt.Errorf("upload artifact to S3: %w", err)
	}
	return nil
}

func (s *Service) openRemote(storageRef string) (*os.File, error) {
	if s.remote == nil {
		return nil, errors.New("S3 storage reference cannot be opened by a local artifact backend")
	}
	prefix := "s3://" + s.remote.bucket + "/sha256/"
	if !strings.HasPrefix(storageRef, prefix) {
		return nil, errors.New("invalid S3 storage reference")
	}
	objectKey := strings.TrimPrefix(storageRef, "s3://"+s.remote.bucket+"/")
	clean := filepath.Clean(filepath.FromSlash(objectKey))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return nil, errors.New("S3 storage reference escapes cache root")
	}
	localPath := filepath.Join(s.root, clean)
	if file, err := os.Open(localPath); err == nil {
		hash := sha256.New()
		_, hashErr := io.Copy(hash, file)
		_ = file.Close()
		expectedHex := filepath.Base(clean)
		if hashErr == nil && hex.EncodeToString(hash.Sum(nil)) == expectedHex {
			return os.Open(localPath)
		}
		if removeErr := os.Remove(localPath); removeErr != nil {
			return nil, fmt.Errorf("discard corrupt S3 cache entry: %w", removeErr)
		}
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	object, err := s.remote.client.GetObject(ctx, s.remote.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("download artifact from S3: %w", err)
	}
	defer object.Close()
	temporary, err := os.CreateTemp(filepath.Dir(localPath), ".download-*")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), object); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("download artifact from S3: %w", err)
	}
	expectedHex := filepath.Base(clean)
	if hex.EncodeToString(hash.Sum(nil)) != expectedHex {
		_ = temporary.Close()
		return nil, ErrHashMismatch
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, localPath); err != nil {
		return nil, err
	}
	return os.Open(localPath)
}

func (s *Service) Verify(storageRef, expected string) error {
	file, err := s.Open(storageRef)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return ErrHashMismatch
	}
	return nil
}
