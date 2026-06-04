package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

const defaultAdminPassword = "admin"
const passwordHashIterations = 200000

func (h *handlers) adminPasswordRecord() (string, error) {
	bytes, err := os.ReadFile(h.adminPasswordPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes)), nil
}

func (h *handlers) passwordHash(password string) (string, error) {
	salt := make([]byte, 16)
	_, err := rand.Read(salt)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	hash := derivePasswordHash(password, salt)
	return "sha256:" + hex.EncodeToString(salt) + ":" + hex.EncodeToString(hash), nil
}

func (h *handlers) setAdminPassword(password string) error {
	password = strings.TrimSpace(password)
	if len(password) < 4 {
		return fmt.Errorf("password must be at least 4 characters")
	}

	hash, err := h.passwordHash(password)
	if err != nil {
		return err
	}

	return os.WriteFile(h.adminPasswordPath, []byte(hash+"\n"), 0o600)
}

func isPasswordHashRecord(record string) bool {
	return strings.HasPrefix(record, "sha256:")
}

func derivePasswordHash(password string, salt []byte) []byte {
	sum := sha256.Sum256(append(salt, []byte(password)...))
	hash := sum[:]
	for i := 0; i < passwordHashIterations-1; i++ {
		nextInput := make([]byte, 0, len(salt)+len(hash)+len(password))
		nextInput = append(nextInput, salt...)
		nextInput = append(nextInput, hash...)
		nextInput = append(nextInput, password...)
		nextSum := sha256.Sum256(nextInput)
		hash = nextSum[:]
	}
	finalHash := make([]byte, len(hash))
	copy(finalHash, hash)
	return finalHash
}

func (h *handlers) adminPasswordMatches(password string) (matches, migrated bool, err error) {
	record, err := h.adminPasswordRecord()
	if err != nil {
		return false, false, err
	}

	if isPasswordHashRecord(record) {
		parts := strings.Split(record, ":")
		if len(parts) != 3 {
			return false, false, fmt.Errorf("invalid password hash format")
		}
		salt, err := hex.DecodeString(parts[1])
		if err != nil {
			return false, false, fmt.Errorf("decoding password salt: %w", err)
		}
		expectedHash, err := hex.DecodeString(parts[2])
		if err != nil {
			return false, false, fmt.Errorf("decoding password hash: %w", err)
		}
		actualHash := derivePasswordHash(password, salt)
		matches := subtle.ConstantTimeCompare(expectedHash, actualHash) == 1
		return matches, false, nil
	}

	if subtle.ConstantTimeCompare([]byte(record), []byte(password)) == 1 {
		err = h.setAdminPassword(password)
		if err != nil {
			return true, false, fmt.Errorf("migrating legacy admin password: %w", err)
		}
		return true, true, nil
	}

	return false, false, nil
}
