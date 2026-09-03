package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/client"
)

const maxMCPMessageBytes = 4 << 20

func serveMCPStdio(ctx context.Context, api *client.Client, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxMCPMessageBytes)
	writer := bufio.NewWriter(output)
	defer writer.Flush()
	for scanner.Scan() {
		message := append(json.RawMessage(nil), scanner.Bytes()...)
		if len(message) == 0 {
			continue
		}
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			ID      json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(message, &envelope); err != nil || envelope.JSONRPC != "2.0" || envelope.Method == "" {
			if err := writeMCPError(writer, envelope.ID, -32600, "invalid JSON-RPC request"); err != nil {
				return err
			}
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		response, err := api.MCP(requestCtx, message)
		cancel()
		if err != nil {
			if len(envelope.ID) == 0 || string(envelope.ID) == "null" {
				continue
			}
			if writeErr := writeMCPError(writer, envelope.ID, -32000, err.Error()); writeErr != nil {
				return writeErr
			}
			continue
		}
		if len(response) == 0 {
			continue
		}
		if _, err := writer.Write(response); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("MCP message exceeds %d bytes", maxMCPMessageBytes)
		}
		return err
	}
	return nil
}

func writeMCPError(writer *bufio.Writer, id json.RawMessage, code int, message string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	value := map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}
