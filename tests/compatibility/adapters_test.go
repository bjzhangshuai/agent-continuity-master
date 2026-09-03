package compatibility

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/adapter/codex"
	"github.com/agent-continuity/agent-continuity-master/adapter/generic"
	"github.com/agent-continuity/agent-continuity-master/adapter/mainstream"
	"github.com/agent-continuity/agent-continuity-master/pkg/adaptersdk"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestGenericAdapterContinuityContract(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	artifactPath := filepath.Join(workspace, "result.txt")
	if err := os.WriteFile(artifactPath, []byte("portable result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "generic-session.jsonl")
	writeJSONL(t, logPath,
		map[string]any{"type": "session", "session_id": "generic-123", "cwd": workspace, "agent": "fixture"},
		map[string]any{"type": "user_input", "id": "one", "timestamp": "2026-08-30T01:00:00Z", "payload": map[string]any{"text": "build it"}},
		map[string]any{"type": "agent_output", "id": "two", "payload": map[string]any{"text": "working"}},
		map[string]any{"type": "tool_call", "id": "three", "payload": map[string]any{"name": "write_file"}},
		map[string]any{"type": "tool_result", "id": "four", "payload": map[string]any{"ok": true}},
		map[string]any{"type": "artifact", "id": "five", "path": "result.txt", "payload": map[string]any{"operation": "created"}},
	)
	adapter := generic.Adapter{Root: root, WorkspaceRoots: []string{workspace}}
	assertContract(t, adapter)
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("discover: sessions=%d err=%v", len(sessions), err)
	}
	result, err := adapter.Capture(context.Background(), adaptersdk.CaptureRequest{Session: sessions[0]})
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, result.Records, adaptersdk.RecordUserInput, adaptersdk.RecordAgentOutput, adaptersdk.RecordToolCall, adaptersdk.RecordToolResult, adaptersdk.RecordArtifact)
	if result.Session.SessionRef != "generic-123" || len(result.Artifacts) != 1 || result.Artifacts[0].ContentHash != protocol.Digest([]byte("portable result\n")) {
		t.Fatalf("incomplete capture: %+v", result)
	}
	empty, err := adapter.Capture(context.Background(), adaptersdk.CaptureRequest{Session: result.Session, Cursor: result.Cursor})
	if err != nil || len(empty.Records) != 0 {
		t.Fatalf("incremental duplicate: records=%d err=%v", len(empty.Records), err)
	}
	appendJSONL(t, logPath, map[string]any{"type": "status", "id": "six", "payload": map[string]any{"status": "complete"}})
	incremental, err := adapter.Capture(context.Background(), adaptersdk.CaptureRequest{Session: result.Session, Cursor: result.Cursor})
	if err != nil || len(incremental.Records) != 1 || incremental.Records[0].Kind != adaptersdk.RecordStatus {
		t.Fatalf("incremental capture: %+v err=%v", incremental, err)
	}
	resume, err := adapter.Resume(context.Background(), adaptersdk.ResumeRequest{Task: protocol.Task{ID: "task_1", Objective: "finish"}})
	if err != nil || resume.Mode != "rehydrate" || resume.Instructions == "" {
		t.Fatalf("rehydrate: %+v err=%v", resume, err)
	}
}

func TestCodexAdapterContinuityContract(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	artifactPath := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(artifactPath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "codex-rollout.jsonl")
	writeJSONL(t, logPath,
		map[string]any{"type": "session_meta", "timestamp": "2026-08-30T01:00:00Z", "payload": map[string]any{"id": "019-session", "cwd": workspace}},
		map[string]any{"type": "response_item", "timestamp": "2026-08-30T01:00:01Z", "payload": map[string]any{"id": "user-1", "type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "implement"}}}},
		map[string]any{"type": "response_item", "timestamp": "2026-08-30T01:00:02Z", "payload": map[string]any{"id": "call-1", "type": "function_call", "name": "exec_command", "arguments": "{}"}},
		map[string]any{"type": "response_item", "timestamp": "2026-08-30T01:00:03Z", "payload": map[string]any{"id": "result-1", "type": "function_call_output", "output": "ok"}},
		map[string]any{"type": "response_item", "timestamp": "2026-08-30T01:00:04Z", "payload": map[string]any{"id": "assistant-1", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}},
		map[string]any{"type": "item.completed", "timestamp": "2026-08-30T01:00:05Z", "item": map[string]any{"id": "file-1", "type": "file_change", "changes": []any{map[string]any{"path": "main.go", "kind": "update"}}}},
	)
	adapter := codex.Adapter{Root: root, WorkspaceRoots: []string{workspace}}
	assertContract(t, adapter)
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("discover: sessions=%d err=%v", len(sessions), err)
	}
	result, err := adapter.Capture(context.Background(), adaptersdk.CaptureRequest{Session: sessions[0]})
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, result.Records, adaptersdk.RecordUserInput, adaptersdk.RecordToolCall, adaptersdk.RecordToolResult, adaptersdk.RecordAgentOutput, adaptersdk.RecordArtifact)
	if result.Session.SessionRef != "019-session" || result.Session.Metadata["cwd"] != workspace || len(result.Artifacts) != 1 {
		t.Fatalf("incomplete Codex capture: %+v", result)
	}
	resume, err := adapter.Resume(context.Background(), adaptersdk.ResumeRequest{Native: &result.Session})
	if err != nil {
		t.Fatal(err)
	}
	wantCommand := []string{"codex", "--cd", workspace, "resume", "019-session"}
	if resume.Mode != "native" || !reflect.DeepEqual(resume.Command, wantCommand) {
		t.Fatalf("native resume: got=%+v want=%v", resume, wantCommand)
	}
	if _, err := adapter.Resume(context.Background(), adaptersdk.ResumeRequest{Native: &adaptersdk.NativeSession{Provider: "codex", SessionRef: "--last"}}); err == nil {
		t.Fatal("expected option-like session reference to be rejected")
	}
}

func TestCodexExecJSONNativeResume(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "exec.jsonl")
	writeJSONL(t, logPath,
		map[string]any{"type": "thread.started", "thread_id": "019-exec"},
		map[string]any{"type": "turn.started"},
		map[string]any{"type": "item.started", "item": map[string]any{"id": "cmd", "type": "command_execution", "command": "go test ./..."}},
		map[string]any{"type": "item.completed", "item": map[string]any{"id": "cmd", "type": "command_execution", "aggregated_output": "ok", "exit_code": 0}},
		map[string]any{"type": "item.completed", "item": map[string]any{"id": "answer", "type": "agent_message", "text": "done"}},
		map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 10}},
	)
	adapter := codex.Adapter{Root: root}
	sessions, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Capture(context.Background(), adaptersdk.CaptureRequest{Session: sessions[0]})
	if err != nil {
		t.Fatal(err)
	}
	resume, err := adapter.Resume(context.Background(), adaptersdk.ResumeRequest{Native: &result.Session})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resume.Command, []string{"codex", "exec", "resume", "019-exec"}) {
		t.Fatalf("unexpected exec resume command: %v", resume.Command)
	}
}

func TestMainstreamAdaptersContinuityContract(t *testing.T) {
	providers := map[string]string{
		"claude":    "system",
		"codebuddy": "conversation.started",
		"gemini":    "init",
		"cursor":    "composer.started",
		"opencode":  "server.connected",
	}
	for provider, sessionType := range providers {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			workspace := t.TempDir()
			artifactPath := filepath.Join(workspace, provider+"-result.txt")
			if err := os.WriteFile(artifactPath, []byte(provider+" portable result\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, provider+"-session.jsonl")
			writeJSONL(t, logPath,
				map[string]any{"type": sessionType, "session_id": provider + "-session-1", "cwd": workspace},
				map[string]any{"type": "message", "id": "user-1", "role": "user", "timestamp": "2026-08-31T01:00:00Z", "payload": map[string]any{"text": "build it"}},
				map[string]any{"type": "assistant_message", "id": "agent-1", "payload": map[string]any{"text": "working"}},
				map[string]any{"type": "tool_use", "id": "call-1", "payload": map[string]any{"name": "write_file"}},
				map[string]any{"type": "tool_result", "id": "result-1", "payload": map[string]any{"ok": true}},
				map[string]any{"type": "decision", "id": "decision-1", "payload": map[string]any{"choice": "safe path"}},
				map[string]any{"type": "progress", "id": "status-1", "payload": map[string]any{"percent": 80}},
				map[string]any{"type": "file_change", "id": "file-1", "file_path": artifactPath, "payload": map[string]any{"operation": "updated"}},
				map[string]any{"type": "thinking", "id": "private-1", "payload": map[string]any{"text": "must not persist"}},
			)
			adapter, err := mainstream.New(provider, root, []string{workspace})
			if err != nil {
				t.Fatal(err)
			}
			assertContract(t, adapter)
			sessions, err := adapter.Discover(context.Background())
			if err != nil || len(sessions) != 1 {
				t.Fatalf("discover: sessions=%d err=%v", len(sessions), err)
			}
			result, err := adapter.Capture(context.Background(), adaptersdk.CaptureRequest{Session: sessions[0]})
			if err != nil {
				t.Fatal(err)
			}
			assertKinds(t, result.Records,
				adaptersdk.RecordUserInput,
				adaptersdk.RecordAgentOutput,
				adaptersdk.RecordToolCall,
				adaptersdk.RecordToolResult,
				adaptersdk.RecordDecision,
				adaptersdk.RecordStatus,
				adaptersdk.RecordArtifact,
			)
			if result.Session.SessionRef != provider+"-session-1" || result.Session.Metadata["cwd"] != workspace || len(result.Artifacts) != 1 || len(result.Warnings) != 1 {
				t.Fatalf("incomplete %s capture: %+v", provider, result)
			}
			resume, err := adapter.Resume(context.Background(), adaptersdk.ResumeRequest{Task: protocol.Task{ID: "task_1", Objective: "finish"}})
			if err != nil || resume.Mode != "rehydrate" || !strings.Contains(resume.Instructions, `"target_agent": "`+provider+`"`) {
				t.Fatalf("%s rehydrate: %+v err=%v", provider, resume, err)
			}
		})
	}
}

func assertContract(t *testing.T, adapter adaptersdk.Adapter) {
	t.Helper()
	if err := adaptersdk.Validate(adapter); err != nil {
		t.Fatal(err)
	}
}

func assertKinds(t *testing.T, records []adaptersdk.CapturedRecord, want ...adaptersdk.RecordKind) {
	t.Helper()
	got := make([]adaptersdk.RecordKind, len(records))
	for index := range records {
		got[index] = records[index].Kind
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record kinds: got=%v want=%v", got, want)
	}
}

func writeJSONL(t *testing.T, path string, values ...any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func appendJSONL(t *testing.T, path string, value any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
