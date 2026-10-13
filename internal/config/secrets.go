package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// Token secrets live in the OS keychain (macOS Keychain via /usr/bin/security,
// Linux Secret Service over D-Bus). Where no keychain is available (headless
// Linux) they fall back to secrets.json, mode 0600, next to config.json.

const keyringService = "modalfs"

var fileMu sync.Mutex

func secretsPath() string { return filepath.Join(Dir(), "secrets.json") }

// readSecretFile fails on anything but a missing file, so a damaged
// secrets.json is never overwritten with only the newest secret.
func readSecretFile() (map[string]string, error) {
	m := map[string]string{}
	raw, err := os.ReadFile(secretsPath())
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", secretsPath(), err)
	}
	return m, nil
}

func writeSecretFile(m map[string]string) error {
	raw, _ := json.MarshalIndent(m, "", "  ")
	return WriteAtomic(secretsPath(), raw)
}

// SaveSecret stores the token secret for an account and reports where it went.
func SaveSecret(accountID, secret string) (string, error) {
	if os.Getenv("MODALFS_NO_KEYCHAIN") == "" {
		if err := keyring.Set(keyringService, accountID, secret); err == nil {
			return "keychain", nil
		}
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	m, err := readSecretFile()
	if err != nil {
		return "", err
	}
	m[accountID] = secret
	return "file", writeSecretFile(m)
}

func LoadSecret(a Account) (string, error) {
	switch a.SecretStore {
	case "keychain":
		return keyring.Get(keyringService, a.ID)
	case "file":
		fileMu.Lock()
		defer fileMu.Unlock()
		m, err := readSecretFile()
		if err != nil {
			return "", err
		}
		if s, ok := m[a.ID]; ok {
			return s, nil
		}
		return "", errors.New("secret missing from secrets.json")
	case "env":
		// Secret supplied by the environment / an injecting proxy.
		if s := os.Getenv("MODAL_TOKEN_SECRET"); s != "" {
			return s, nil
		}
		return "", errors.New("this account reads its token secret from MODAL_TOKEN_SECRET, which is not set for the modalfs daemon")
	}
	return "", errors.New("unknown secret store " + a.SecretStore)
}

func DeleteSecret(a Account) error {
	switch a.SecretStore {
	case "keychain":
		_ = keyring.Delete(keyringService, a.ID)
	case "file":
		fileMu.Lock()
		defer fileMu.Unlock()
		m, err := readSecretFile()
		if err != nil {
			return err
		}
		delete(m, a.ID)
		return writeSecretFile(m)
	}
	return nil
}

// Volume keys use a separate keychain account namespace from token secrets.
func SaveVolumeKey(v VolumeEncryption, key string) (string, error) {
	id := "volume/" + v.VolumeID
	switch v.SecretStore {
	case "":
		return SaveSecret(id, key)
	case "keychain":
		return "keychain", keyring.Set(keyringService, id, key)
	case "file":
		fileMu.Lock()
		defer fileMu.Unlock()
		m, err := readSecretFile()
		if err != nil {
			return "", err
		}
		m[id] = key
		return "file", writeSecretFile(m)
	default:
		return "", errors.New("unknown volume key store " + v.SecretStore)
	}
}

func LoadVolumeKey(v VolumeEncryption) (string, error) {
	return LoadSecret(Account{ID: "volume/" + v.VolumeID, SecretStore: v.SecretStore})
}

func DeleteVolumeKey(v VolumeEncryption) error {
	id := "volume/" + v.VolumeID
	if v.SecretStore == "keychain" {
		err := keyring.Delete(keyringService, id)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return DeleteSecret(Account{ID: id, SecretStore: v.SecretStore})
}
