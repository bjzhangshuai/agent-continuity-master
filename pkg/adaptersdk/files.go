package adaptersdk

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultMaxLogBytes      int64 = 64 << 20
	DefaultMaxLineBytes           = 4 << 20
	DefaultMaxArtifactBytes int64 = 64 << 20
	DefaultMaxSessions            = 1000
)

var ErrSourceChanged = errors.New("adapter source changed before the capture cursor")

type JSONLReader struct {
	Root         string
	MaxLogBytes  int64
	MaxLineBytes int
}

func (reader JSONLReader) Discover(ctx context.Context, provider string) ([]NativeSession, error) {
	root, err := canonicalRoot(reader.Root)
	if err != nil {
		return nil, err
	}
	items := make([]NativeSession, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != root && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			return nil
		}
		if len(items) >= DefaultMaxSessions {
			return fmt.Errorf("adapter session limit %d exceeded", DefaultMaxSessions)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > reader.maxLogBytes() {
			return nil
		}
		ref := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		items = append(items, NativeSession{Provider: provider, SessionRef: ref, SourcePath: path, UpdatedAt: info.ModTime().UTC(), Metadata: map[string]string{"format": "jsonl"}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].SourcePath < items[j].SourcePath
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

// Read reads only complete JSONL records after cursor.Offset. The returned
// cursor advances to the byte after the last complete line, making capture
// restart-safe even if a producer is still writing the final line.
func (reader JSONLReader) Read(ctx context.Context, source string, cursor CaptureCursor, handle func(line []byte, lineNumber uint64) error) (CaptureCursor, error) {
	root, err := canonicalRoot(reader.Root)
	if err != nil {
		return CaptureCursor{}, err
	}
	path, err := containedRegularFile(root, source)
	if err != nil {
		return CaptureCursor{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return CaptureCursor{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return CaptureCursor{}, err
	}
	if info.Size() > reader.maxLogBytes() {
		return CaptureCursor{}, fmt.Errorf("adapter log exceeds %d bytes", reader.maxLogBytes())
	}
	if cursor.Offset < 0 || cursor.Offset > info.Size() {
		return CaptureCursor{}, ErrSourceChanged
	}
	if cursor.Offset > 0 && cursor.Digest != "" {
		prefixDigest, err := digestPrefix(file, cursor.Offset)
		if err != nil {
			return CaptureCursor{}, err
		}
		if cursor.Digest != prefixDigest {
			return CaptureCursor{}, ErrSourceChanged
		}
	}
	if _, err := file.Seek(cursor.Offset, io.SeekStart); err != nil {
		return CaptureCursor{}, err
	}
	maxLine := reader.MaxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}
	buffered := bufio.NewReaderSize(file, 64*1024)
	offset := cursor.Offset
	lineNumber := cursor.Line
	for {
		if err := ctx.Err(); err != nil {
			return CaptureCursor{}, err
		}
		line, readErr := buffered.ReadBytes('\n')
		if len(line) > maxLine {
			return CaptureCursor{}, fmt.Errorf("JSONL record exceeds %d bytes", maxLine)
		}
		if errors.Is(readErr, io.EOF) {
			// The producer may still be writing this record. Leave it for the
			// next capture rather than returning a duplicate with an old cursor.
			break
		}
		if readErr != nil {
			return CaptureCursor{}, readErr
		}
		lineNumber++
		offset += int64(len(line))
		line = line[:len(line)-1]
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if err := handle(line, lineNumber); err != nil {
			return CaptureCursor{}, fmt.Errorf("%s line %d: %w", filepath.Base(path), lineNumber, err)
		}
	}
	prefixDigest, err := digestPrefix(file, offset)
	if err != nil {
		return CaptureCursor{}, err
	}
	return CaptureCursor{Offset: offset, Line: lineNumber, Digest: prefixDigest}, nil
}

func CaptureArtifact(path string, workspaceRoots []string, maxBytes int64) (CapturedArtifact, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxArtifactBytes
	}
	resolved, err := containedInAnyRoot(path, workspaceRoots)
	if err != nil {
		return CapturedArtifact{}, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return CapturedArtifact{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return CapturedArtifact{}, errors.New("artifact must be a regular non-symlink file")
	}
	if info.Size() > maxBytes {
		return CapturedArtifact{}, fmt.Errorf("artifact exceeds capture limit %d bytes", maxBytes)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return CapturedArtifact{}, err
	}
	defer file.Close()
	hash := sha256.New()
	prefix := make([]byte, 512)
	n, readErr := io.ReadFull(file, prefix)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return CapturedArtifact{}, readErr
	}
	if _, err := hash.Write(prefix[:n]); err != nil {
		return CapturedArtifact{}, err
	}
	if _, err := io.Copy(hash, io.LimitReader(file, maxBytes+1-int64(n))); err != nil {
		return CapturedArtifact{}, err
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(resolved)))
	if contentType == "" {
		contentType = http.DetectContentType(prefix[:n])
	}
	return CapturedArtifact{Path: resolved, Filename: filepath.Base(resolved), ContentType: contentType, Size: info.Size(), ContentHash: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("adapter root is not configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("adapter root must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func containedRegularFile(root, source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("session source path is required")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !pathWithin(root, resolved) {
		return "", errors.New("session source is outside the configured adapter root")
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("session source must be a regular non-symlink file")
	}
	return resolved, nil
}

func containedInAnyRoot(path string, roots []string) (string, error) {
	if len(roots) == 0 {
		return "", errors.New("at least one artifact workspace root is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	for _, candidate := range roots {
		root, err := canonicalRoot(candidate)
		if err != nil {
			continue
		}
		if pathWithin(root, resolved) {
			return resolved, nil
		}
	}
	return "", errors.New("artifact is outside configured workspace roots")
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func digestPrefix(file *os.File, stableBytes int64) (string, error) {
	limit := int64(4096)
	if stableBytes < limit {
		limit = stableBytes
	}
	if limit < 0 {
		limit = 0
	}
	buffer := make([]byte, int(limit))
	n, err := file.ReadAt(buffer, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(buffer[:n])
	return hex.EncodeToString(sum[:]), nil
}

func (reader JSONLReader) maxLogBytes() int64 {
	if reader.MaxLogBytes > 0 {
		return reader.MaxLogBytes
	}
	return DefaultMaxLogBytes
}

func ParseTimestamp(value string, fallback time.Time) time.Time {
	if value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UTC()
		}
		if milliseconds, err := time.ParseDuration(value + "ms"); err == nil {
			return time.UnixMilli(milliseconds.Milliseconds()).UTC()
		}
	}
	if fallback.IsZero() {
		return time.Now().UTC()
	}
	return fallback.UTC()
}
