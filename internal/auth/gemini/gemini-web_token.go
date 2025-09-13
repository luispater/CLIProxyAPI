// Package gemini provides authentication and token management functionality
// for Google's Gemini AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Gemini API.
package gemini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

// GeminiAppTokenStorage stores cookie information for Google Gemini App authentication.
type GeminiAppTokenStorage struct {
	Secure1PSID   string `json:"secure_1psid"`
	Secure1PSIDTS string `json:"secure_1psidts"`
	Type          string `json:"type"`
}

// SaveTokenToFile serializes the Gemini App token storage to a JSON file.
func (ts *GeminiAppTokenStorage) SaveTokenToFile(authFilePath string) error {
    ts.Type = "gemini-web"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("failed to close file: %v", errClose)
		}
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}
