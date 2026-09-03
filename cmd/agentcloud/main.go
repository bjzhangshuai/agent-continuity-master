package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/daemon"
	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	baseURL := env("AGENTCLOUD_API_URL", "http://127.0.0.1:8080")
	token := os.Getenv("AGENTCLOUD_TOKEN")
	stateDir := env("AGENTCLOUD_STATE_DIR", defaultStateDir())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	api := client.New(baseURL, token)
	if args[0] == "login" {
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: agentcloud login ACCOUNT_ID [SUBJECT]")
		}
		bootstrapToken := os.Getenv("AGENTCLOUD_BOOTSTRAP_TOKEN")
		if bootstrapToken == "" {
			return errors.New("AGENTCLOUD_BOOTSTRAP_TOKEN is required for local bootstrap login")
		}
		subject := "agentcloud"
		if len(args) == 3 {
			subject = strings.TrimSpace(args[2])
		}
		if subject == "" {
			return errors.New("login subject cannot be empty")
		}
		issued, err := api.IssueToken(ctx, bootstrapToken, args[1], subject)
		if err != nil {
			return err
		}
		if err := saveLocalCredentials(stateDir, credentialsFromToken(args[1], subject, issued, time.Now())); err != nil {
			return err
		}
		fmt.Printf("logged in as %s; credentials stored with mode 0600\n", args[1])
		return nil
	}
	var localCredential localCredentials
	if token == "" {
		if value, err := loadLocalCredentials(stateDir); err == nil {
			localCredential = value
			if time.Until(value.AccessExpiresAt) <= time.Minute {
				if time.Now().After(value.RefreshExpiresAt) {
					return errors.New("local session expired; run `agentcloud login` again")
				}
				refreshed, err := api.RefreshToken(ctx, value.RefreshToken)
				if err != nil {
					return fmt.Errorf("refresh local session: %w", err)
				}
				localCredential = credentialsFromToken(value.AccountID, value.Subject, refreshed, time.Now())
				if err := saveLocalCredentials(stateDir, localCredential); err != nil {
					return err
				}
			}
			token = localCredential.AccessToken
			api.Token = token
		}
	}
	if args[0] == "logout" {
		if localCredential.RefreshToken != "" {
			if err := api.Logout(ctx, localCredential.RefreshToken); err != nil {
				return err
			}
		}
		if err := os.Remove(credentialsFile(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("logged out; local credentials removed")
		return nil
	}
	if args[0] == "mcp" {
		if len(args) != 2 || args[1] != "stdio" {
			return errors.New("usage: agentcloud mcp stdio")
		}
		if token == "" {
			return errors.New("login or AGENTCLOUD_TOKEN is required for MCP")
		}
		return serveMCPStdio(context.Background(), api, os.Stdin, os.Stdout)
	}
	stateStore, err := daemon.OpenState(stateDir)
	if err != nil {
		return err
	}
	outbox, err := daemon.OpenOutbox(stateDir)
	if err != nil {
		return err
	}
	keyring, err := daemon.OpenKeyring(stateDir)
	if err != nil {
		return err
	}
	control, err := daemon.OpenSyncControl(stateDir)
	if err != nil {
		return err
	}
	cache, err := daemon.OpenArtifactCache(stateDir, 1<<30)
	if err != nil {
		return err
	}
	checkpoints, err := daemon.OpenAdapterCheckpoints(stateDir)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if err := api.Health(ctx); err != nil {
			return err
		}
		fmt.Println("server: ready")
		if state, sizeErr := stateStore.Load(); sizeErr == nil {
			length, _ := outbox.Len()
			fmt.Printf("device: %s (%s)\noutbox: %d\n", state.DeviceID, state.Name, length)
		}
		return nil
	case "doctor":
		return doctor(ctx, api, stateStore, outbox, baseURL, token)
	case "device":
		if len(args) == 2 && args[1] == "list" {
			items, err := api.ListDevices(ctx, false)
			if err != nil {
				return err
			}
			printJSON(items)
			return nil
		}
		if len(args) >= 3 && args[1] == "init" {
			if token == "" {
				return errors.New("login or AGENTCLOUD_TOKEN is required")
			}
			accountID := os.Getenv("AGENTCLOUD_ACCOUNT_ID")
			if accountID == "" {
				accountID = localCredential.AccountID
			}
			if accountID == "" {
				return errors.New("AGENTCLOUD_ACCOUNT_ID is required when using an environment token")
			}
			state, publicKey, err := daemon.NewDeviceState(accountID, args[2])
			if err != nil {
				return err
			}
			device, err := api.RegisterDevice(ctx, state.DeviceID, state.Name, publicKey, state.EncryptionPublicKey)
			if err != nil {
				return err
			}
			state.DeviceID = device.ID
			if err := stateStore.Save(state); err != nil {
				return err
			}
			printJSON(device)
			return nil
		}
	case "task":
		return taskCommand(ctx, api, stateStore, keyring, args[1:])
	case "agent":
		if len(args) == 2 && args[1] == "list" {
			items, err := api.ListAgents(ctx)
			if err != nil {
				return err
			}
			printJSON(items)
			return nil
		}
	case "conflict":
		if len(args) == 3 && args[1] == "list" {
			items, err := api.ListConflicts(ctx, args[2], true)
			if err != nil {
				return err
			}
			printJSON(items)
			return nil
		}
		if len(args) >= 5 && args[1] == "resolve" {
			value, err := api.ResolveConflict(ctx, args[2], args[3], args[4], strings.Join(args[5:], " "))
			if err != nil {
				return err
			}
			printJSON(value)
			return nil
		}
	case "event":
		if len(args) >= 5 && args[1] == "append" {
			return appendEvent(ctx, api, stateStore, keyring, outbox, args[2], args[3], args[4], contains(args[5:], "--offline"), contains(args[5:], "--encrypt"))
		}
	case "sync":
		return syncCommand(ctx, api, stateStore, outbox, control, stateDir, args[1:])
	case "artifact":
		return artifactCommand(ctx, api, stateStore, keyring, cache, stateDir, args[1:])
	case "adapter":
		return adapterCommand(ctx, api, stateStore, keyring, outbox, control, checkpoints, stateDir, args[1:])
	case "invite":
		if len(args) == 3 && args[1] == "accept" {
			return api.AcceptInvitation(ctx, args[2])
		}
	}
	return usage()
}

func taskCommand(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "list":
		items, err := api.ListTasks(ctx)
		if err != nil {
			return err
		}
		printJSON(items)
		return nil
	case "create":
		if len(args) < 3 {
			return errors.New("task create requires TITLE and OBJECTIVE")
		}
		state, err := stateStore.Load()
		if err != nil {
			return fmt.Errorf("initialize a device first: %w", err)
		}
		value, err := api.CreateTask(ctx, args[1], args[2], state.DeviceID)
		if err != nil {
			return err
		}
		printJSON(value)
		return nil
	case "inspect":
		if len(args) != 2 {
			return errors.New("task inspect requires TASK_ID")
		}
		value, err := api.GetTask(ctx, args[1])
		if err != nil {
			return err
		}
		printJSON(value)
		return nil
	case "resume":
		if len(args) < 2 {
			return errors.New("task resume requires TASK_ID")
		}
		agent := "generic"
		if len(args) > 2 {
			agent = args[2]
		}
		value, err := api.ContinuityPackage(ctx, args[1], agent)
		if err != nil {
			return err
		}
		if err := decryptContinuityPackage(ctx, api, stateStore, keyring, &value); err != nil {
			return err
		}
		printJSON(value)
		return nil
	case "key":
		if len(args) == 3 && args[1] == "rotate" {
			return rotateTaskKey(ctx, api, stateStore, keyring, args[2])
		}
		return errors.New("task key rotate requires TASK_ID")
	case "share":
		if len(args) < 2 {
			return errors.New("task share requires TASK_ID")
		}
		permission, qrPath, err := parseShareOptions(args[2:])
		if err != nil {
			return err
		}
		if qrPath != "" {
			return createQRShare(ctx, api, stateStore, args[1], permission, qrPath)
		}
		token, err := api.CreateInvitation(ctx, args[1], permission, 900)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	}
	return usage()
}

func parseShareOptions(args []string) (protocol.Permission, string, error) {
	permission := protocol.PermissionRead
	qrPath := ""
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case string(protocol.PermissionRead), string(protocol.PermissionWrite):
			permission = protocol.Permission(args[index])
		case "--qr":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return "", "", errors.New("--qr requires an output SVG file")
			}
			qrPath = args[index]
		default:
			return "", "", fmt.Errorf("unknown task share option %q", args[index])
		}
	}
	return permission, qrPath, nil
}

func createQRShare(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, taskID string, permission protocol.Permission, outputPath string) error {
	state, err := stateStore.Load()
	if err != nil {
		return fmt.Errorf("initialize a device first: %w", err)
	}
	privateKey, err := state.Private()
	if err != nil {
		return fmt.Errorf("load device signing key: %w", err)
	}
	absolute, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve QR output path: %w", err)
	}
	directory := filepath.Dir(absolute)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create QR output directory: %w", err)
	}
	output, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create QR output without overwriting existing data: %w", err)
	}
	committed := false
	defer func() {
		_ = output.Close()
		if !committed {
			_ = os.Remove(absolute)
		}
	}()

	invitationID, err := protocol.NewID("invite")
	if err != nil {
		return err
	}
	tokenBytes := make([]byte, 32)
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate invitation token: %w", err)
	}
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("generate invitation nonce: %w", err)
	}
	token := "qri_" + base64.RawURLEncoding.EncodeToString(tokenBytes)
	value := protocol.QRInvitation{
		Version:              1,
		InvitationID:         invitationID,
		TaskID:               taskID,
		InviterAccountID:     state.AccountID,
		InviterDeviceID:      state.DeviceID,
		Permission:           permission,
		ExpiresAt:            time.Now().UTC().Add(15 * time.Minute),
		Nonce:                base64.RawURLEncoding.EncodeToString(nonceBytes),
		TokenDigest:          protocol.Digest([]byte(token)),
		KeyExchangeReference: json.RawMessage(`{"type":"task_key_exchange","mode":"post_accept"}`),
		Token:                token,
	}
	if err := protocol.SignQRInvitation(&value, privateKey); err != nil {
		return fmt.Errorf("sign QR invitation: %w", err)
	}
	rendered, err := api.CreateQRInvitation(ctx, value)
	if err != nil {
		return err
	}
	if rendered.Invitation.InvitationID != invitationID || rendered.Content != "agentcontinuity://invite?token="+token || !strings.HasPrefix(rendered.SVG, "<svg") {
		return errors.New("server returned an invalid QR invitation rendering")
	}
	if _, err := output.WriteString(rendered.SVG); err != nil {
		return fmt.Errorf("write QR output: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync QR output: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close QR output: %w", err)
	}
	committed = true
	fmt.Printf("created signed one-time QR invitation %s (expires %s)\n", absolute, value.ExpiresAt.Format(time.RFC3339))
	return nil
}

func appendEvent(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, outbox *daemon.Outbox, taskID, eventType, payloadText string, offline, encrypt bool) error {
	if !json.Valid([]byte(payloadText)) {
		return errors.New("payload must be valid JSON")
	}
	state, err := stateStore.NextSequence()
	if err != nil {
		return fmt.Errorf("load device state: %w", err)
	}
	task, err := api.GetTask(ctx, taskID)
	if err != nil && !offline {
		return err
	}
	parents := task.HeadEventIDs
	eventID, err := protocol.NewID("event")
	if err != nil {
		return err
	}
	payload := json.RawMessage(payloadText)
	contentType := "application/json"
	if encrypt {
		key, version, err := ensureTaskKey(ctx, api, state, keyring, taskID, 0)
		if err != nil {
			return fmt.Errorf("load task encryption key: %w", err)
		}
		encrypted, err := e2ee.EncryptPayload(key, version, contentType, payload, []byte(eventID))
		if err != nil {
			return err
		}
		payload, err = json.Marshal(encrypted)
		if err != nil {
			return err
		}
		contentType = "application/vnd.agent-continuity.encrypted+json"
	}
	value := protocol.Event{EventID: eventID, TaskID: taskID, BranchID: "main", AccountID: state.AccountID, DeviceID: state.DeviceID, ParentEventIDs: parents, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: state.DeviceID}, DeviceSequence: state.Sequence, Type: eventType, SchemaVersion: protocol.SchemaVersion, ContentType: contentType, ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: eventID}
	privateKey, err := state.Private()
	if err != nil {
		return err
	}
	if err := protocol.SignPortableEvent(&value, privateKey); err != nil {
		return err
	}
	if offline {
		if err := outbox.Enqueue(value); err != nil {
			return err
		}
		fmt.Println("queued", eventID)
		return nil
	}
	result, err := api.AppendEvent(ctx, value)
	if err != nil && client.IsTemporary(err) {
		if queueErr := outbox.Enqueue(value); queueErr != nil {
			return fmt.Errorf("append failed (%v) and queue failed: %w", err, queueErr)
		}
		fmt.Println("queued after temporary failure", eventID)
		return nil
	}
	if err != nil {
		return err
	}
	printJSON(result)
	return nil
}

func rotateTaskKey(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, taskID string) error {
	state, err := stateStore.Load()
	if err != nil {
		return fmt.Errorf("initialize a device first: %w", err)
	}
	if _, err := state.EncryptionPrivate(); err != nil {
		return err
	}
	devices, err := api.ListTaskDevices(ctx, taskID)
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return errors.New("task has no active encryption-capable devices")
	}
	version := uint32(1)
	if latest, err := api.GetTaskKeySet(ctx, taskID, state.DeviceID, 0); err == nil {
		version = latest.Version + 1
	} else {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 404 {
			return err
		}
	}
	key, err := e2ee.GenerateTaskKey()
	if err != nil {
		return err
	}
	wrapped := make([]protocol.WrappedTaskKey, 0, len(devices))
	for _, device := range devices {
		envelope, err := e2ee.WrapTaskKey(taskID, version, key, device.EncryptionPublicKey, device.ID)
		if err != nil {
			return fmt.Errorf("wrap key for %s: %w", device.ID, err)
		}
		wrapped = append(wrapped, envelope)
	}
	keySet := protocol.TaskKeySet{TaskID: taskID, Version: version, Algorithm: e2ee.Algorithm, CreatedBy: state.DeviceID, WrappedKeys: wrapped, CreatedAt: time.Now().UTC()}
	if err := api.PutTaskKeySet(ctx, keySet); err != nil {
		return err
	}
	if err := keyring.Put(taskID, version, key); err != nil {
		return err
	}
	fmt.Printf("rotated task key to version %d for %d device(s)\n", version, len(devices))
	return nil
}

func ensureTaskKey(ctx context.Context, api *client.Client, state daemon.DeviceState, keyring *daemon.Keyring, taskID string, version uint32) ([]byte, uint32, error) {
	if version == 0 {
		if key, foundVersion, err := keyring.Latest(taskID); err == nil {
			return key, foundVersion, nil
		}
	} else if key, err := keyring.Get(taskID, version); err == nil {
		return key, version, nil
	}
	privateKey, err := state.EncryptionPrivate()
	if err != nil {
		return nil, 0, err
	}
	keySet, err := api.GetTaskKeySet(ctx, taskID, state.DeviceID, version)
	if err != nil {
		return nil, 0, err
	}
	if len(keySet.WrappedKeys) != 1 {
		return nil, 0, errors.New("server returned an invalid device key envelope")
	}
	key, err := e2ee.UnwrapTaskKey(taskID, keySet.Version, keySet.WrappedKeys[0], privateKey)
	if err != nil {
		return nil, 0, err
	}
	if err := keyring.Put(taskID, keySet.Version, key); err != nil {
		return nil, 0, err
	}
	return key, keySet.Version, nil
}

func decryptContinuityPackage(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, keyring *daemon.Keyring, value *protocol.ContinuityPackage) error {
	var state daemon.DeviceState
	loadedState := false
	for i := range value.Events {
		event := &value.Events[i]
		if event.ContentType != "application/vnd.agent-continuity.encrypted+json" {
			continue
		}
		if !loadedState {
			var err error
			state, err = stateStore.Load()
			if err != nil {
				return fmt.Errorf("encrypted continuity package requires local device state: %w", err)
			}
			loadedState = true
		}
		var encrypted protocol.EncryptedPayload
		if err := json.Unmarshal(event.Payload, &encrypted); err != nil {
			return fmt.Errorf("decode encrypted event %s: %w", event.EventID, err)
		}
		key, _, err := ensureTaskKey(ctx, api, state, keyring, event.TaskID, encrypted.KeyVersion)
		if err != nil {
			return fmt.Errorf("fetch key for event %s: %w", event.EventID, err)
		}
		plaintext, err := e2ee.DecryptPayload(key, encrypted, []byte(event.EventID))
		if err != nil {
			return fmt.Errorf("decrypt event %s: %w", event.EventID, err)
		}
		event.Payload = plaintext
		event.ContentType = encrypted.OriginalContentType
		event.ContentDigest = encrypted.PlaintextDigest
	}
	return nil
}
func syncOutbox(ctx context.Context, api *client.Client, outbox *daemon.Outbox) error {
	items, retryAfter, err := outbox.Ready(100, time.Now())
	if err != nil {
		return err
	}
	if retryAfter > 0 {
		fmt.Printf("sync deferred by retry backoff for %s\n", retryAfter.Round(time.Millisecond))
		return nil
	}
	for _, value := range items {
		if _, err := api.AppendEvent(ctx, value); err != nil {
			retry, retryErr := outbox.RecordFailure(value.EventID, err, time.Now())
			if retryErr != nil {
				return fmt.Errorf("sync failed (%v) and retry state failed: %w", err, retryErr)
			}
			fmt.Printf("sync deferred after attempt %d until %s: %v\n", retry.Attempts, retry.NextAttempt.Format(time.RFC3339), err)
			return nil
		}
		if err := outbox.Ack(value.EventID); err != nil {
			return err
		}
	}
	fmt.Printf("synced %d event(s)\n", len(items))
	return nil
}

func syncCommand(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, outbox *daemon.Outbox, control *daemon.SyncControl, stateDir string, args []string) error {
	if len(args) == 0 {
		settings, err := control.Load()
		if err != nil {
			return err
		}
		if settings.Paused {
			return errors.New("sync is paused; run `agentcloud sync resume`")
		}
		localStore, err := daemon.OpenLocalStore(stateDir)
		if err != nil {
			return fmt.Errorf("open local sync database (is the daemon already running?): %w", err)
		}
		defer localStore.Close()
		engine := daemon.SyncEngine{API: api, State: stateStore, Outbox: outbox, Local: localStore}
		report, err := engine.SyncOnce(ctx, settings.BandwidthBytesPerSecond)
		if err != nil {
			return err
		}
		printJSON(report)
		return nil
	}
	switch args[0] {
	case "pause":
		if err := control.SetPaused(true); err != nil {
			return err
		}
		fmt.Println("sync paused")
		return nil
	case "resume":
		if err := control.SetPaused(false); err != nil {
			return err
		}
		fmt.Println("sync resumed")
		return nil
	case "status":
		settings, err := control.Load()
		if err != nil {
			return err
		}
		pending, err := outbox.Len()
		if err != nil {
			return err
		}
		printJSON(map[string]any{"paused": settings.Paused, "bandwidth_bytes_per_second": settings.BandwidthBytesPerSecond, "outbox_pending": pending})
		return nil
	case "bandwidth":
		if len(args) != 2 {
			return errors.New("sync bandwidth requires BYTES_PER_SECOND; use 0 for unlimited")
		}
		value, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return err
		}
		if err := control.SetBandwidth(value); err != nil {
			return err
		}
		fmt.Printf("sync bandwidth set to %d bytes/second\n", value)
		return nil
	}
	return errors.New("usage: agentcloud sync [pause|resume|status|bandwidth BYTES_PER_SECOND]")
}
func doctor(ctx context.Context, api *client.Client, stateStore *daemon.StateStore, outbox *daemon.Outbox, baseURL, token string) error {
	checks := map[string]any{"api_url": baseURL, "token_configured": token != ""}
	checks["server_ready"] = api.Health(ctx) == nil
	if state, err := stateStore.Load(); err == nil {
		checks["device_id"] = state.DeviceID
		checks["device_state"] = "ok"
	} else {
		checks["device_state"] = err.Error()
	}
	if length, err := outbox.Len(); err == nil {
		checks["outbox_pending"] = length
	} else {
		checks["outbox_error"] = err.Error()
	}
	printJSON(checks)
	if checks["server_ready"] != true {
		return errors.New("server is not ready")
	}
	return nil
}
func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".agent-continuity"
	}
	return filepath.Join(dir, "agent-continuity")
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func printJSON(value any) { data, _ := json.MarshalIndent(value, "", "  "); fmt.Println(string(data)) }
func usage() error {
	return errors.New("usage: agentcloud login ACCOUNT [SUBJECT]|logout|status|doctor|mcp stdio|device list|device init NAME|agent list|task list|task create TITLE OBJECTIVE|task inspect ID|task resume ID [AGENT]|task share ID [read|write] [--qr FILE]|task key rotate ID|event append TASK TYPE JSON [--offline] [--encrypt]|conflict list TASK|conflict resolve TASK CONFLICT STRATEGY [NOTES]|artifact upload TASK ARTIFACT FILE [--parent VERSION] [--encrypt] [--upload-id ID]|artifact download TASK ARTIFACT DEST|artifact peer-serve FILE ADDRESS [--ttl SECONDS] [--allow-public]|artifact peer-download URL TOKEN SHA256 DEST|adapter discover|capture|resume ...|sync [pause|resume|status|bandwidth BPS]|invite accept TOKEN")
}
