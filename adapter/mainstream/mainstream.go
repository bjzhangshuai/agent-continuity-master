// Package mainstream implements opt-in structured-log adapters for mainstream
// coding agents. Each provider has its own identity and resume instructions,
// while sharing the hardened JSONL reader, cursor, and Artifact boundary checks.
package mainstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/agent-continuity/agent-continuity-master/pkg/adaptersdk"
)

var profiles = map[string]Profile{
	"claude": {
		Name: "claude", DisplayName: "Claude Code",
		SessionTypes: []string{"session", "session.started", "session_start", "system"},
	},
	"codebuddy": {
		Name: "codebuddy", DisplayName: "CodeBuddy",
		SessionTypes: []string{"session", "session.started", "session_start", "conversation.started"},
	},
	"gemini": {
		Name: "gemini", DisplayName: "Gemini CLI",
		SessionTypes: []string{"session", "session.started", "session_start", "init"},
	},
	"cursor": {
		Name: "cursor", DisplayName: "Cursor Agent",
		SessionTypes: []string{"session", "session.started", "session_start", "composer.started"},
	},
	"opencode": {
		Name: "opencode", DisplayName: "OpenCode",
		SessionTypes: []string{"session", "session.started", "session_start", "server.connected"},
	},
}

// Profile describes one provider-specific public adapter identity.
type Profile struct {
	Name         string
	DisplayName  string
	SessionTypes []string
}

// SupportedProviders returns the stable provider IDs accepted by the CLI.
func SupportedProviders() []string {
	return []string{"claude", "codebuddy", "gemini", "cursor", "opencode"}
}

// New constructs a provider adapter without scanning any implicit home or
// vendor state directory. Callers must pass an explicit JSONL root.
func New(provider, root string, workspaces []string) (Adapter, error) {
	profile, ok := profiles[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return Adapter{}, fmt.Errorf("unsupported mainstream adapter %q", provider)
	}
	return Adapter{Profile: profile, Root: root, WorkspaceRoots: workspaces}, nil
}

// Adapter consumes explicit JSONL exports or hook output. It deliberately
// supports Rehydrate rather than claiming vendor-private session equivalence.
type Adapter struct {
	Profile          Profile
	Root             string
	WorkspaceRoots   []string
	MaxLogBytes      int64
	MaxArtifactBytes int64
}

func (adapter Adapter) Name() string { return adapter.Profile.Name }
func (Adapter) Version() string      { return adaptersdk.ContractVersion }
func (Adapter) Capabilities() []adaptersdk.Capability {
	return []adaptersdk.Capability{adaptersdk.CapabilityDiscover, adaptersdk.CapabilityCapture, adaptersdk.CapabilityRehydrate, adaptersdk.CapabilityArtifacts}
}

func (adapter Adapter) Detect(ctx context.Context) adaptersdk.CapabilityReport {
	report := adaptersdk.CapabilityReport{Adapter: adapter.Name(), Version: adapter.Version(), Capabilities: map[adaptersdk.Capability]adaptersdk.CapabilityStatus{}}
	_, err := adapter.Discover(ctx)
	for _, capability := range adapter.Capabilities() {
		status := adaptersdk.CapabilityStatus{Available: true}
		if capability != adaptersdk.CapabilityRehydrate && err != nil {
			status = adaptersdk.CapabilityStatus{Reason: err.Error()}
		}
		report.Capabilities[capability] = status
	}
	return report
}

func (adapter Adapter) Discover(ctx context.Context) ([]adaptersdk.NativeSession, error) {
	if adapter.Name() == "" {
		return nil, errors.New("mainstream adapter profile is not configured")
	}
	return adapter.reader().Discover(ctx, adapter.Name())
}

func (adapter Adapter) Capture(ctx context.Context, request adaptersdk.CaptureRequest) (adaptersdk.CaptureResult, error) {
	if request.Session.Provider != "" && request.Session.Provider != adapter.Name() {
		return adaptersdk.CaptureResult{}, fmt.Errorf("session provider does not match %s adapter", adapter.Name())
	}
	result := adaptersdk.CaptureResult{Session: request.Session}
	result.Session.Provider = adapter.Name()
	if result.Session.Metadata == nil {
		result.Session.Metadata = map[string]string{}
	}
	result.Session.Metadata["native_agent"] = adapter.Profile.DisplayName
	seenArtifacts := map[string]bool{}
	next, err := adapter.reader().Read(ctx, request.Session.SourcePath, request.Cursor, func(line []byte, lineNumber uint64) error {
		var native map[string]any
		if err := json.Unmarshal(line, &native); err != nil {
			return err
		}
		nativeType := normalized(firstString(native, "type", "event", "kind", "event_type"))
		if adapter.isSessionRecord(nativeType, native) {
			if ref := firstNestedString(native, []string{"session_id", "sessionId", "conversation_id", "conversationId", "thread_id", "threadId", "id"}); ref != "" {
				result.Session.SessionRef = ref
			}
			if cwd := firstNestedString(native, []string{"cwd", "working_directory", "workspace", "project_path"}); cwd != "" {
				result.Session.Metadata["cwd"] = cwd
			}
			return nil
		}
		kind, ok, reason := classify(nativeType, native)
		if !ok {
			if reason != "" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("line %d %s", lineNumber, reason))
			}
			return nil
		}
		recordID := firstNestedString(native, []string{"id", "event_id", "message_id", "uuid", "request_id", "call_id"})
		if recordID == "" {
			recordID = fmt.Sprintf("line-%d", lineNumber)
		}
		timestamp := firstNestedString(native, []string{"timestamp", "created_at", "createdAt", "time"})
		record := adaptersdk.CapturedRecord{
			ID: recordID, Kind: kind,
			OccurredAt: adaptersdk.ParseTimestamp(timestamp, result.Session.UpdatedAt),
			Payload:    append(json.RawMessage(nil), line...), NativeType: nativeType,
		}
		if kind == adaptersdk.RecordArtifact {
			for _, nativePath := range artifactPaths(native) {
				path := adapter.resolveArtifactPath(nativePath, result.Session.Metadata)
				artifact, err := adaptersdk.CaptureArtifact(path, adapter.artifactRoots(result.Session.Metadata), adapter.MaxArtifactBytes)
				if err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("line %d artifact %q skipped: %v", lineNumber, nativePath, err))
					continue
				}
				if !seenArtifacts[artifact.ContentHash] {
					seenArtifacts[artifact.ContentHash] = true
					result.Artifacts = append(result.Artifacts, artifact)
				}
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

func (adapter Adapter) Resume(_ context.Context, request adaptersdk.ResumeRequest) (adaptersdk.ResumeResult, error) {
	data, err := json.MarshalIndent(map[string]any{
		"protocol_version":   adaptersdk.ContractVersion,
		"target_agent":       adapter.Name(),
		"task":               request.Task,
		"events":             request.Events,
		"execution_reports":  request.ExecutionReports,
		"artifact_manifests": request.Artifacts,
	}, "", "  ")
	if err != nil {
		return adaptersdk.ResumeResult{}, err
	}
	return adaptersdk.ResumeResult{Mode: "rehydrate", Instructions: fmt.Sprintf("Continue this portable task in %s. Preserve causal order, provenance, unresolved conflicts, decisions, and Artifact hashes. Do not infer private reasoning that is absent from the package.\n%s", adapter.Profile.DisplayName, data)}, nil
}

func (adapter Adapter) isSessionRecord(nativeType string, native map[string]any) bool {
	for _, candidate := range adapter.Profile.SessionTypes {
		if nativeType == normalized(candidate) {
			// Claude uses system records for more than session metadata; only
			// treat one as a session record when an explicit session ID exists.
			return nativeType != "system" || firstNestedString(native, []string{"session_id", "sessionId"}) != ""
		}
	}
	return false
}

func classify(nativeType string, native map[string]any) (adaptersdk.RecordKind, bool, string) {
	if containsAny(nativeType, "reasoning", "thinking", "chain_of_thought", "analysis") {
		return "", false, "ignored private reasoning record"
	}
	role := normalized(firstNestedString(native, []string{"role", "author_role", "speaker"}))
	switch nativeType {
	case "user", "user_message", "human", "human_message", "user_input", "user.input", "prompt":
		return adaptersdk.RecordUserInput, true, ""
	case "assistant", "assistant_message", "agent", "agent_message", "model", "agent_output", "agent.output", "response":
		return adaptersdk.RecordAgentOutput, true, ""
	case "tool_use", "tool_call", "tool.call", "function_call", "command", "command_execution", "action":
		return adaptersdk.RecordToolCall, true, ""
	case "tool_result", "tool_response", "tool.result", "function_result", "function_response", "command_result", "observation":
		return adaptersdk.RecordToolResult, true, ""
	case "decision", "plan", "checkpoint":
		return adaptersdk.RecordDecision, true, ""
	case "status", "progress", "task_complete", "task.completed", "turn_completed", "turn.completed", "step_finish", "step.finished", "system":
		return adaptersdk.RecordStatus, true, ""
	case "error", "agent_error", "agent.error", "failure":
		return adaptersdk.RecordError, true, ""
	case "artifact", "artifact_changed", "artifact.changed", "file_change", "file.changed", "file_write", "file_written", "patch":
		return adaptersdk.RecordArtifact, true, ""
	case "message", "message_created", "message.created", "content_block":
		switch role {
		case "user", "human":
			return adaptersdk.RecordUserInput, true, ""
		case "assistant", "agent", "model":
			return adaptersdk.RecordAgentOutput, true, ""
		case "tool":
			return adaptersdk.RecordToolResult, true, ""
		}
	}
	// Some exporters keep the semantic type in a nested content block.
	if nested := normalized(firstNestedString(native, []string{"content_type", "block_type", "action_type"})); nested != "" && nested != nativeType {
		return classify(nested, native)
	}
	return "", false, ""
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

func artifactPaths(native map[string]any) []string {
	seen := map[string]bool{}
	paths := []string{}
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				normalizedKey := normalized(key)
				if normalizedKey == "path" || normalizedKey == "file_path" {
					if path, ok := child.(string); ok && path != "" && !seen[path] {
						seen[path] = true
						paths = append(paths, path)
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(native)
	return paths
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNestedString(value any, keys []string) string {
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[normalized(key)] = true
	}
	var visit func(any) string
	visit = func(candidate any) string {
		switch typed := candidate.(type) {
		case map[string]any:
			for key, child := range typed {
				if wanted[normalized(key)] {
					if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
						return text
					}
				}
			}
			for _, key := range []string{"message", "payload", "data", "metadata", "context", "item", "content"} {
				if child, ok := typed[key]; ok {
					if found := visit(child); found != "" {
						return found
					}
				}
			}
		case []any:
			for _, child := range typed {
				if found := visit(child); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return visit(value)
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func normalized(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
