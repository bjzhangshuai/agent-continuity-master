package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/agent-continuity/agent-continuity-master/pkg/adaptersdk"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

// Adapter consumes JSONL emitted by `codex exec --json` or an explicitly
// exported Codex rollout. Root is opt-in: the adapter never scans a user's
// private Codex state directory implicitly.
type Adapter struct {
	Root             string
	WorkspaceRoots   []string
	MaxLogBytes      int64
	MaxArtifactBytes int64
}

func (Adapter) Name() string    { return "codex" }
func (Adapter) Version() string { return adaptersdk.ContractVersion }
func (Adapter) Capabilities() []adaptersdk.Capability {
	return []adaptersdk.Capability{adaptersdk.CapabilityDiscover, adaptersdk.CapabilityCapture, adaptersdk.CapabilityNativeResume, adaptersdk.CapabilityRehydrate, adaptersdk.CapabilityArtifacts}
}

func (adapter Adapter) Detect(ctx context.Context) adaptersdk.CapabilityReport {
	report := adaptersdk.CapabilityReport{Adapter: adapter.Name(), Version: adapter.Version(), Capabilities: map[adaptersdk.Capability]adaptersdk.CapabilityStatus{}}
	_, sourceErr := adapter.Discover(ctx)
	for _, capability := range adapter.Capabilities() {
		status := adaptersdk.CapabilityStatus{Available: true}
		if (capability == adaptersdk.CapabilityDiscover || capability == adaptersdk.CapabilityCapture || capability == adaptersdk.CapabilityArtifacts) && sourceErr != nil {
			status = adaptersdk.CapabilityStatus{Reason: sourceErr.Error()}
		}
		report.Capabilities[capability] = status
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		report.Capabilities[adaptersdk.CapabilityNativeResume] = adaptersdk.CapabilityStatus{Reason: "codex executable was not found in PATH"}
		return report
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").CombinedOutput()
	if err != nil {
		reason := err.Error()
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			reason = "codex version probe timed out"
		}
		report.Capabilities[adaptersdk.CapabilityNativeResume] = adaptersdk.CapabilityStatus{Reason: reason}
		return report
	}
	report.Capabilities[adaptersdk.CapabilityNativeResume] = adaptersdk.CapabilityStatus{Available: true, Version: strings.TrimSpace(string(output))}
	return report
}

func (adapter Adapter) Discover(ctx context.Context) ([]adaptersdk.NativeSession, error) {
	return adapter.reader().Discover(ctx, adapter.Name())
}

func (adapter Adapter) Capture(ctx context.Context, request adaptersdk.CaptureRequest) (adaptersdk.CaptureResult, error) {
	if request.Session.Provider != "" && request.Session.Provider != adapter.Name() {
		return adaptersdk.CaptureResult{}, errors.New("session provider does not match Codex adapter")
	}
	result := adaptersdk.CaptureResult{Session: request.Session}
	result.Session.Provider = adapter.Name()
	if result.Session.Metadata == nil {
		result.Session.Metadata = map[string]string{}
	}
	seenRecords := map[string]bool{}
	seenArtifacts := map[string]bool{}
	next, err := adapter.reader().Read(ctx, request.Session.SourcePath, request.Cursor, func(line []byte, lineNumber uint64) error {
		var native map[string]any
		if err := json.Unmarshal(line, &native); err != nil {
			return err
		}
		nativeType := stringValue(native, "type")
		timestamp := stringValue(native, "timestamp")
		payload, _ := native["payload"].(map[string]any)
		item, _ := native["item"].(map[string]any)
		switch nativeType {
		case "thread.started":
			if ref := stringValue(native, "thread_id"); ref != "" {
				result.Session.SessionRef = ref
				result.Session.Metadata["session_mode"] = "exec"
			}
			return nil
		case "session_meta":
			if ref := stringValue(payload, "id"); ref != "" {
				result.Session.SessionRef = ref
				result.Session.Metadata["session_mode"] = "interactive"
			}
			if cwd := stringValue(payload, "cwd"); cwd != "" {
				result.Session.Metadata["cwd"] = cwd
			}
			return nil
		}

		kind, capture, skipReason := classify(nativeType, payload, item)
		if !capture {
			if skipReason != "" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("line %d %s", lineNumber, skipReason))
			}
			return nil
		}
		recordID := firstNonEmpty(stringValue(native, "id"), stringValue(item, "id"), stringValue(payload, "id"), fmt.Sprintf("line-%d", lineNumber))
		identity := string(kind) + "\x00" + recordID + "\x00" + string(line)
		identity = strings.TrimPrefix(adaptersdkDigest(identity), "sha256:")
		if seenRecords[identity] {
			return nil
		}
		seenRecords[identity] = true
		record := adaptersdk.CapturedRecord{
			ID: recordID, Kind: kind,
			OccurredAt: adaptersdk.ParseTimestamp(timestamp, result.Session.UpdatedAt),
			Payload:    append(json.RawMessage(nil), line...), NativeType: nativeType,
		}
		if kind == adaptersdk.RecordArtifact {
			for _, nativePath := range artifactPaths(item, payload) {
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

func (Adapter) Resume(_ context.Context, request adaptersdk.ResumeRequest) (adaptersdk.ResumeResult, error) {
	if request.Native != nil && request.Native.Provider == "codex" && request.Native.SessionRef != "" {
		if err := validateSessionRef(request.Native.SessionRef); err != nil {
			return adaptersdk.ResumeResult{}, err
		}
		command := []string{"codex"}
		if cwd := request.Native.Metadata["cwd"]; cwd != "" {
			command = append(command, "--cd", cwd)
		}
		if request.Native.Metadata["session_mode"] == "exec" {
			command = append(command, "exec", "resume", request.Native.SessionRef)
		} else {
			command = append(command, "resume", request.Native.SessionRef)
		}
		return adaptersdk.ResumeResult{Mode: "native", SessionRef: request.Native.SessionRef, Command: command}, nil
	}
	summary := map[string]any{
		"protocol_version":   adaptersdk.ContractVersion,
		"task":               request.Task,
		"events":             request.Events,
		"artifact_manifests": request.Artifacts,
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return adaptersdk.ResumeResult{}, err
	}
	return adaptersdk.ResumeResult{Mode: "rehydrate", Instructions: fmt.Sprintf("Resume this vendor-neutral task in Codex. Preserve provenance, causal order, unresolved branches, and artifact hashes.\n%s", data)}, nil
}

func classify(nativeType string, payload, item map[string]any) (adaptersdk.RecordKind, bool, string) {
	switch nativeType {
	case "turn.started":
		return adaptersdk.RecordStatus, true, ""
	case "turn.completed":
		return adaptersdk.RecordStatus, true, ""
	case "error":
		return adaptersdk.RecordError, true, ""
	case "item.started", "item.completed":
		itemType := stringValue(item, "type")
		switch itemType {
		case "agent_message":
			return adaptersdk.RecordAgentOutput, nativeType == "item.completed", ""
		case "command_execution", "mcp_tool_call", "web_search":
			if nativeType == "item.started" {
				return adaptersdk.RecordToolCall, true, ""
			}
			return adaptersdk.RecordToolResult, true, ""
		case "file_change":
			return adaptersdk.RecordArtifact, nativeType == "item.completed", ""
		case "error":
			return adaptersdk.RecordError, true, ""
		case "reasoning":
			return "", false, "ignored reasoning item; private chain-of-thought is not continuity state"
		default:
			return "", false, ""
		}
	case "response_item":
		switch stringValue(payload, "type") {
		case "message":
			switch stringValue(payload, "role") {
			case "user":
				return adaptersdk.RecordUserInput, true, ""
			case "assistant":
				return adaptersdk.RecordAgentOutput, true, ""
			}
		case "function_call", "custom_tool_call":
			return adaptersdk.RecordToolCall, true, ""
		case "function_call_output", "custom_tool_call_output":
			return adaptersdk.RecordToolResult, true, ""
		}
	case "event_msg":
		switch stringValue(payload, "type") {
		case "user_message":
			return adaptersdk.RecordUserInput, true, ""
		case "agent_message":
			return adaptersdk.RecordAgentOutput, true, ""
		case "task_complete", "turn_aborted":
			return adaptersdk.RecordStatus, true, ""
		case "error":
			return adaptersdk.RecordError, true, ""
		}
	}
	return "", false, ""
}

func artifactPaths(values ...map[string]any) []string {
	seen := map[string]bool{}
	result := []string{}
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "path" || key == "file_path" {
					if path, ok := child.(string); ok && path != "" && !seen[path] {
						seen[path] = true
						result = append(result, path)
					}
				}
				if key == "changes" || key == "files" || key == "artifacts" {
					visit(child)
				}
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	for _, value := range values {
		visit(value)
	}
	return result
}

func stringValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func validateSessionRef(value string) error {
	if len(value) > 512 || strings.HasPrefix(value, "-") || strings.TrimSpace(value) != value {
		return errors.New("invalid Codex session reference")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("invalid Codex session reference")
		}
	}
	return nil
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

func adaptersdkDigest(value string) string {
	return protocol.Digest([]byte(value))
}
