package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-continuity/agent-continuity-master/adapter/codex"
	"github.com/agent-continuity/agent-continuity-master/adapter/generic"
	"github.com/agent-continuity/agent-continuity-master/adapter/mainstream"
	"github.com/agent-continuity/agent-continuity-master/internal/daemon"
	"github.com/agent-continuity/agent-continuity-master/pkg/adaptersdk"
	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func adapterCommand(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, outbox *daemon.Outbox, control *daemon.SyncControl, checkpoints *daemon.AdapterCheckpoints, stateDir string, args []string) error {
	if len(args) == 0 {
		return adapterUsage()
	}
	switch args[0] {
	case "discover":
		if len(args) < 3 {
			return errors.New("adapter discover requires PROVIDER and LOG_ROOT")
		}
		adapter, err := configuredAdapter(args[1], args[2], optionValue(args[3:], "--workspace"))
		if err != nil {
			return err
		}
		items, err := adapter.Discover(ctx)
		if err != nil {
			return err
		}
		printJSON(map[string]any{"adapter": adapter.Name(), "version": adapter.Version(), "capabilities": adapter.Detect(ctx), "sessions": items})
		return nil
	case "capture":
		if len(args) < 4 {
			return errors.New("adapter capture requires PROVIDER, LOG_ROOT and SOURCE_JSONL")
		}
		return captureAdapter(ctx, api, stateStore, keyring, outbox, control, checkpoints, stateDir, args[1], args[2], args[3], args[4:])
	case "resume":
		if len(args) < 3 {
			return errors.New("adapter resume requires PROVIDER and TASK_ID")
		}
		return resumeAdapter(ctx, api, stateStore, keyring, args[1], args[2], args[3:])
	default:
		return adapterUsage()
	}
}

func captureAdapter(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, outbox *daemon.Outbox, control *daemon.SyncControl, checkpoints *daemon.AdapterCheckpoints, stateDir, provider, root, source string, options []string) error {
	workspace := optionValue(options, "--workspace")
	adapter, err := configuredAdapter(provider, root, workspace)
	if err != nil {
		return err
	}
	source, err = canonicalAdapterSource(source)
	if err != nil {
		return err
	}
	sessions, err := adapter.Discover(ctx)
	if err != nil {
		return err
	}
	var session adaptersdk.NativeSession
	for _, candidate := range sessions {
		candidatePath, _ := canonicalAdapterSource(candidate.SourcePath)
		if candidatePath == source {
			session = candidate
			break
		}
	}
	if session.SourcePath == "" {
		return errors.New("source JSONL was not discovered under the configured root")
	}
	if workspace != "" {
		if session.Metadata == nil {
			session.Metadata = map[string]string{}
		}
		session.Metadata["cwd"] = workspace
	}
	cursor, err := checkpoints.Get(session)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	result, err := adapter.Capture(ctx, adaptersdk.CaptureRequest{Session: session, Cursor: cursor})
	if err != nil {
		return err
	}
	if !contains(options, "--commit") {
		printJSON(result)
		return nil
	}
	taskID := optionValue(options, "--task")
	if taskID == "" {
		return errors.New("adapter capture --commit requires --task TASK_ID")
	}
	if len(result.Records) == 0 {
		if err := checkpoints.Put(result.Session, result.Cursor); err != nil {
			return err
		}
		fmt.Println("adapter capture is already up to date")
		return nil
	}
	plaintext := contains(options, "--plaintext")
	state, err := stateStore.Load()
	if err != nil {
		return fmt.Errorf("initialize a device first: %w", err)
	}
	task, err := api.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if !plaintext {
		if _, _, err := ensureAdapterTaskKey(ctx, api, stateStore, keyring, state, taskID); err != nil {
			return err
		}
	}
	artifactIDs, err := commitCapturedArtifacts(ctx, api, stateStore, keyring, stateDir, taskID, result.Artifacts, plaintext)
	if err != nil {
		return err
	}
	for index := range result.Records {
		for refIndex, reference := range result.Records[index].ArtifactRefs {
			if artifactID := artifactIDs[reference]; artifactID != "" {
				result.Records[index].ArtifactRefs[refIndex] = artifactID
			}
		}
	}
	reserved, err := stateStore.ReserveSequences(uint64(len(result.Records)))
	if err != nil {
		return err
	}
	privateKey, err := reserved.Private()
	if err != nil {
		return err
	}
	events, err := adaptersdk.Envelope(result.Records, adaptersdk.EnvelopeOptions{
		TaskID: taskID, AccountID: reserved.AccountID, DeviceID: reserved.DeviceID,
		AgentID: optionValue(options, "--agent-id"), ProviderID: provider,
		SessionRef: result.Session.SessionRef, StartSequence: reserved.Sequence - uint64(len(result.Records)),
		ParentEventIDs: task.HeadEventIDs, PrivateKey: privateKey,
	})
	if err != nil {
		return err
	}
	if !plaintext {
		key, version, err := ensureAdapterTaskKey(ctx, api, stateStore, keyring, reserved, taskID)
		if err != nil {
			return err
		}
		if err := encryptCapturedEvents(events, key, version, privateKey); err != nil {
			return err
		}
	}
	for _, event := range events {
		if err := outbox.Enqueue(event); err != nil {
			return err
		}
	}
	if err := checkpoints.Put(result.Session, result.Cursor); err != nil {
		return err
	}
	if !contains(options, "--offline") {
		settings, err := control.Load()
		if err != nil {
			return err
		}
		if !settings.Paused {
			if err := syncOutbox(ctx, api, outbox); err != nil {
				return err
			}
		}
	}
	printJSON(map[string]any{"adapter": provider, "session_ref": result.Session.SessionRef, "captured_records": len(result.Records), "captured_artifacts": len(result.Artifacts), "encrypted": !plaintext, "checkpoint": result.Cursor})
	return nil
}

func canonicalAdapterSource(source string) (string, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func resumeAdapter(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, provider, taskID string, options []string) error {
	adapter, err := configuredAdapter(provider, ".", optionValue(options, "--cwd"))
	if err != nil {
		return err
	}
	pack, err := api.ContinuityPackage(ctx, taskID, provider)
	if err != nil {
		return err
	}
	if err := decryptContinuityPackage(ctx, api, stateStore, keyring, &pack); err != nil {
		return err
	}
	var native *adaptersdk.NativeSession
	if reference := optionValue(options, "--session"); reference != "" {
		metadata := map[string]string{}
		if cwd := optionValue(options, "--cwd"); cwd != "" {
			metadata["cwd"] = cwd
		}
		if contains(options, "--exec") {
			metadata["session_mode"] = "exec"
		}
		native = &adaptersdk.NativeSession{Provider: provider, SessionRef: reference, Metadata: metadata}
	}
	result, err := adapter.Resume(ctx, adaptersdk.ResumeRequest{Task: pack.Task, Events: pack.Events, Artifacts: pack.Artifacts, Native: native})
	if err != nil {
		return err
	}
	printJSON(result)
	return nil
}

func configuredAdapter(provider, root, workspace string) (adaptersdk.Adapter, error) {
	workspaces := []string{}
	if workspace != "" {
		workspaces = append(workspaces, workspace)
	}
	var adapter adaptersdk.Adapter
	switch provider {
	case "generic":
		adapter = generic.Adapter{Root: root, WorkspaceRoots: workspaces}
	case "codex":
		adapter = codex.Adapter{Root: root, WorkspaceRoots: workspaces}
	default:
		mainstreamAdapter, err := mainstream.New(provider, root, workspaces)
		if err != nil {
			return nil, fmt.Errorf("unsupported adapter %q; supported providers: codex, generic, %s", provider, strings.Join(mainstream.SupportedProviders(), ", "))
		}
		adapter = mainstreamAdapter
	}
	if err := adaptersdk.Validate(adapter); err != nil {
		return nil, err
	}
	return adapter, nil
}

func commitCapturedArtifacts(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, stateDir, taskID string, artifacts []adaptersdk.CapturedArtifact, plaintext bool) (map[string]string, error) {
	result := map[string]string{}
	existing, err := api.ListArtifacts(ctx, taskID)
	if err != nil {
		return nil, err
	}
	byID := map[string]protocol.Artifact{}
	for _, artifact := range existing {
		byID[artifact.ID] = artifact
	}
	for _, captured := range artifacts {
		hexDigest := strings.TrimPrefix(captured.ContentHash, "sha256:")
		if len(hexDigest) < 32 {
			return nil, errors.New("captured artifact has an invalid content hash")
		}
		artifactID := "artifact_" + hexDigest[:32]
		if current, ok := byID[artifactID]; ok && artifactPlaintextDigest(current) == captured.ContentHash {
			result[captured.ContentHash] = artifactID
			continue
		}
		options := []string{}
		if current, ok := byID[artifactID]; ok {
			options = append(options, "--parent", current.VersionID)
		}
		if !plaintext {
			options = append(options, "--encrypt")
		}
		if err := uploadArtifact(ctx, api, stateStore, keyring, stateDir, taskID, artifactID, captured.Path, options); err != nil {
			return nil, err
		}
		result[captured.ContentHash] = artifactID
	}
	return result, nil
}

func artifactPlaintextDigest(value protocol.Artifact) string {
	if len(value.EncryptionMetadata) == 0 {
		return value.ContentHash
	}
	var metadata protocol.ArtifactEncryptionMetadata
	if json.Unmarshal(value.EncryptionMetadata, &metadata) == nil {
		return metadata.PlaintextDigest
	}
	return ""
}

func ensureAdapterTaskKey(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, state daemon.DeviceState, taskID string) ([]byte, uint32, error) {
	key, version, err := ensureTaskKey(ctx, api, state, keyring, taskID, 0)
	if err == nil {
		return key, version, nil
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		return nil, 0, err
	}
	if err := rotateTaskKey(ctx, api, stateStore, keyring, taskID); err != nil {
		return nil, 0, err
	}
	return ensureTaskKey(ctx, api, state, keyring, taskID, 0)
}

func encryptCapturedEvents(events []protocol.Event, key []byte, version uint32, privateKey ed25519.PrivateKey) error {
	for index := range events {
		encrypted, err := e2ee.EncryptPayload(key, version, events[index].ContentType, events[index].Payload, []byte(events[index].EventID))
		if err != nil {
			return err
		}
		payload, err := json.Marshal(encrypted)
		if err != nil {
			return err
		}
		events[index].Payload = payload
		events[index].ContentType = "application/vnd.agent-continuity.encrypted+json"
		events[index].ContentDigest = protocol.Digest(payload)
		if err := protocol.SignPortableEvent(&events[index], privateKey); err != nil {
			return err
		}
	}
	return nil
}

func adapterUsage() error {
	return errors.New("usage: agentcloud adapter discover PROVIDER LOG_ROOT [--workspace DIR] | adapter capture PROVIDER LOG_ROOT SOURCE_JSONL [--workspace DIR] [--task ID --commit] [--agent-id ID] [--offline] [--plaintext] | adapter resume PROVIDER TASK_ID [--session ID] [--exec] [--cwd DIR]")
}
