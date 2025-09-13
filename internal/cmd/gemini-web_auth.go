// Package cmd provides command-line interface functionality for the CLI Proxy API.
package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luispater/CLIProxyAPI/internal/auth/gemini"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
)

// DoGeminiWebAuth handles the process of creating a Gemini Web token file.
// It prompts the user for their cookie values and saves them to a JSON file.
func DoGeminiWebAuth(cfg *config.Config) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter your __Secure-1PSID cookie value: ")
	secure1psid, _ := reader.ReadString('\n')
	secure1psid = strings.TrimSpace(secure1psid)

	if secure1psid == "" {
		log.Fatal("The __Secure-1PSID value cannot be empty.")
		return
	}

	fmt.Print("Enter your __Secure-1PSIDTS cookie value: ")
	secure1psidts, _ := reader.ReadString('\n')
	secure1psidts = strings.TrimSpace(secure1psidts)

	if secure1psidts == "" {
		log.Fatal("The __Secure-1PSIDTS value cannot be empty.")
		return
	}

	tokenStorage := &gemini.GeminiAppTokenStorage{
		Secure1PSID:   secure1psid,
		Secure1PSIDTS: secure1psidts,
	}

	// Generate a filename based on the SHA256 hash of the PSID
	hasher := sha256.New()
	hasher.Write([]byte(secure1psid))
	hash := hex.EncodeToString(hasher.Sum(nil))
    fileName := fmt.Sprintf("gemini-web-%s.json", hash[:16])
	filePath := filepath.Join(cfg.AuthDir, fileName)

	err := tokenStorage.SaveTokenToFile(filePath)
    if err != nil {
        log.Fatalf("Failed to save Gemini Web token to file: %v", err)
        return
    }

    log.Infof("Successfully saved Gemini Web token to: %s", filePath)
}
