package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/client"
)

type localCredentials struct {
	Version          uint32    `json:"version"`
	AccountID        string    `json:"account_id"`
	Subject          string    `json:"subject"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

func credentialsFile(stateDir string) string {
	return filepath.Join(stateDir, "credentials.json")
}

func loadLocalCredentials(stateDir string) (localCredentials, error) {
	data, err := os.ReadFile(credentialsFile(stateDir))
	if err != nil {
		return localCredentials{}, err
	}
	var value localCredentials
	if err := json.Unmarshal(data, &value); err != nil {
		return localCredentials{}, fmt.Errorf("decode local credentials: %w", err)
	}
	if value.Version != 1 || value.AccountID == "" || value.Subject == "" || value.AccessToken == "" || value.RefreshToken == "" {
		return localCredentials{}, errors.New("local credentials are incomplete")
	}
	return value, nil
}

func saveLocalCredentials(stateDir string, value localCredentials) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(stateDir, ".credentials-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, credentialsFile(stateDir))
}

func credentialsFromToken(accountID, subject string, token client.TokenResponse, now time.Time) localCredentials {
	return localCredentials{
		Version:          1,
		AccountID:        accountID,
		Subject:          subject,
		AccessToken:      token.AccessToken,
		RefreshToken:     token.RefreshToken,
		AccessExpiresAt:  now.Add(time.Duration(token.ExpiresIn) * time.Second),
		RefreshExpiresAt: now.Add(time.Duration(token.RefreshExpiresIn) * time.Second),
	}
}
