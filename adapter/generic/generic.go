package generic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/agent-continuity/agent-continuity-master/pkg/adaptersdk"
)

type Adapter struct {
	Root             string
	WorkspaceRoots   []string
	MaxLogBytes      int64
	MaxArtifactBytes int64
}

func (Adapter) Name() string    { return "generic" }
func (Adapter) Version() string { return adaptersdk.ContractVersion }
func (Adapter) Capabilities() []adaptersdk.Capability {
	return []adaptersdk.Capability{adaptersdk.CapabilityDiscover, adaptersdk.CapabilityCapture, adaptersdk.CapabilityRehydrate, adaptersdk.CapabilityArtifacts}
}

func (adapter Adapter) Detect(ctx context.Context) adaptersdk.CapabilityReport {
	report := adaptersdk.CapabilityReport{Adapter: adapter.Name(), Version: adapter.Version(), Capabilities: map[adaptersdk.Capability]adaptersdk.CapabilityStatus{}}
	_, err := adapter.Discover(ctx)
	for _, capability := range adapter.Capabilities() {
		status := adaptersdk.CapabilityStatus{Available: true}
		if (capability == adaptersdk.CapabilityDiscover || capability == adaptersdk.CapabilityCapture || capability == adaptersdk.CapabilityArtifacts) && err != nil {
			status = adaptersdk.CapabilityStatus{Reason: err.Error()}
		}
		report.Capabilities[capability] = status
	}
	return report
}

func (adapter Adapter) Discover(ctx context.Context) ([]adaptersdk.NativeSession, error) {
	return adapter.reader().Discover(ctx, adapter.Name())
}

func (adapter Adapter) Capture(ctx context.Context, request adaptersdk.CaptureRequest) (adaptersdk.CaptureResult, error) {
	if request.Session.Provider != "" && request.Session.Provider != adapter.Name() {
		return adaptersdk.CaptureResult{}, errors.New("session provider does not match generic adapter")
	}
	result := adaptersdk.CaptureResult{Session: request.Session}
	result.Session.Provider = adapter.Name()
	artifacts := map[string]bool{}
	next, err := adapter.reader().Read(ctx, request.Session.SourcePath, request.Cursor, func(line []byte, lineNumber uint64) error {
		var item genericRecord
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.Type == "session" || item.Type == "session.started" {
			if item.SessionID != "" {
				result.Session.SessionRef = item.SessionID
			}
			if result.Session.Metadata == nil {
				result.Session.Metadata = map[string]string{}
			}
			if item.CWD != "" {
				result.Session.Metadata["cwd"] = item.CWD
			}
			if item.Agent != "" {
				result.Session.Metadata["native_agent"] = item.Agent
			}
			return nil
		}
		kind, ok := genericKind(item.Type)
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("line %d ignored unknown record type %q", lineNumber, item.Type))
			return nil
		}
		payload := item.Payload
		if len(payload) == 0 || string(payload) == "null" {
			payload = append(json.RawMessage(nil), line...)
		}
		if !json.Valid(payload) {
			return errors.New("payload must be valid JSON")
		}
		recordID := item.ID
		if recordID == "" {
			recordID = fmt.Sprintf("line-%d", lineNumber)
		}
		occurredAt := adaptersdk.ParseTimestamp(item.Timestamp, result.Session.UpdatedAt)
		record := adaptersdk.CapturedRecord{ID: recordID, Kind: kind, OccurredAt: occurredAt, Payload: payload, NativeType: item.Type}
		if item.Path != "" {
			path := adapter.resolveArtifactPath(item.Path, result.Session.Metadata)
			artifact, err := adaptersdk.CaptureArtifact(path, adapter.artifactRoots(result.Session.Metadata), adapter.MaxArtifactBytes)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("line %d artifact %q skipped: %v", lineNumber, item.Path, err))
			} else if !artifacts[artifact.ContentHash] {
				artifacts[artifact.ContentHash] = true
				result.Artifacts = append(result.Artifacts, artifact)
				record.ArtifactRefs = append(record.ArtifactRefs, artifact.ContentHash)
			}
		}
		result.Records = append(result.Records, record)
		return nil
	})
	if err != nil {
		return adaptersdk.CaptureResult{}, err
	}
	result.Cursor = next
	return result, nil
}

func (Adapter) Resume(_ context.Context, request adaptersdk.ResumeRequest) (adaptersdk.ResumeResult, error) {
	data, err := json.MarshalIndent(map[string]any{
		"protocol_version":   adaptersdk.ContractVersion,
		"task":               request.Task,
		"events":             request.Events,
		"execution_reports":  request.ExecutionReports,
		"artifact_manifests": request.Artifacts,
	}, "", "  ")
	if err != nil {
		return adaptersdk.ResumeResult{}, err
	}
	return adaptersdk.ResumeResult{Mode: "rehydrate", Instructions: fmt.Sprintf("Continue the portable task from this continuity package. Preserve causal order, provenance, unresolved conflicts, and artifact hashes.\n%s", data)}, nil
}

type genericRecord struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd"`
	Agent     string          `json:"agent"`
	Path      string          `json:"path"`
	Payload   json.RawMessage `json:"payload"`
}

func genericKind(value string) (adaptersdk.RecordKind, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user_input", "user.input", "input":
		return adaptersdk.RecordUserInput, true
	case "agent_output", "agent.output", "assistant", "output":
		return adaptersdk.RecordAgentOutput, true
	case "tool_call", "tool.call":
		return adaptersdk.RecordToolCall, true
	case "tool_result", "tool.result":
		return adaptersdk.RecordToolResult, true
	case "decision":
		return adaptersdk.RecordDecision, true
	case "status", "status.changed":
		return adaptersdk.RecordStatus, true
	case "error", "agent.error":
		return adaptersdk.RecordError, true
	case "artifact", "artifact.changed":
		return adaptersdk.RecordArtifact, true
	default:
		return "", false
	}
}

func (adapter Adapter) reader() adaptersdk.JSONLReader {
	return adaptersdk.JSONLReader{Root: adapter.Root, MaxLogBytes: adapter.MaxLogBytes}
}

func (adapter Adapter) artifactRoots(metadata map[string]string) []string {
	roots := append([]string(nil), adapter.WorkspaceRoots...)
	if cwd := metadata["cwd"]; cwd != "" {
		roots = append(roots, cwd)
	}
	return roots
}

func (adapter Adapter) resolveArtifactPath(path string, metadata map[string]string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if cwd := metadata["cwd"]; cwd != "" {
		return filepath.Join(cwd, path)
	}
	if len(adapter.WorkspaceRoots) > 0 {
		return filepath.Join(adapter.WorkspaceRoots[0], path)
	}
	return path
}
