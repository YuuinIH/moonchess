package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func RandomHex(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}
	return hex.EncodeToString(value), nil
}
