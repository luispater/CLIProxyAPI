// Package watcher provides file system monitoring functionality for the CLI Proxy API.
// It watches configuration files and authentication directories for changes,
// automatically reloading clients and configuration when files are modified.
// The package handles cross-platform file system events and supports hot-reloading.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/luispater/CLIProxyAPI/internal/auth/claude"
	"github.com/luispater/CLIProxyAPI/internal/auth/codex"
	"github.com/luispater/CLIProxyAPI/internal/auth/gemini"
	"github.com/luispater/CLIProxyAPI/internal/auth/qwen"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Watcher manages file watching for configuration and authentication files
type Watcher struct {
	configPath     string
	authDir        string
	config         *config.Config
	clients        map[string]interfaces.Client
	apiKeyClients  map[string]interfaces.Client // New field for caching API key clients
	clientsMutex   sync.RWMutex
	reloadCallback func(map[string]interfaces.Client, *config.Config)
	watcher        *fsnotify.Watcher
	lastAuthHashes map[string]string
	lastConfigHash string
}

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func(map[string]interfaces.Client, *config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}

	return &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: reloadCallback,
		watcher:        watcher,
		clients:        make(map[string]interfaces.Client),
		apiKeyClients:  make(map[string]interfaces.Client),
		lastAuthHashes: make(map[string]string),
	}, nil
}

// Start begins watching the configuration file and authentication directory
func (w *Watcher) Start(ctx context.Context) error {
	// Watch the config file
	if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
		log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
		return errAddConfig
	}
	log.Debugf("watching config file: %s", w.configPath)

	// Watch the auth directory
	if errAddAuthDir := w.watcher.Add(w.authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", w.authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory: %s", w.authDir)

	// Start the event processing goroutine
	go w.processEvents(ctx)

	return nil
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	return w.watcher.Close()
}

// SetConfig updates the current configuration
func (w *Watcher) SetConfig(cfg *config.Config) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cfg
}

// SetClients sets the file-based clients.
func (w *Watcher) SetClients(clients map[string]interfaces.Client) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.clients = clients
}

// SetAPIKeyClients sets the API key-based clients.
func (w *Watcher) SetAPIKeyClients(apiKeyClients map[string]interfaces.Client) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.apiKeyClients = apiKeyClients
}

// processEvents handles file system events
func (w *Watcher) processEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case errWatch, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Errorf("file watcher error: %v", errWatch)
		}
	}
}

// handleEvent processes individual file system events
func (w *Watcher) handleEvent(event fsnotify.Event) {
	now := time.Now()
	log.Debugf("file system event detected: %s %s", event.Op.String(), event.Name)

	// Handle config file changes
	if event.Name == w.configPath && (event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
		log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
		data, err := os.ReadFile(w.configPath)
		if err != nil {
			log.Errorf("failed to read config file for hash check: %v", err)
			return
		}
		if len(data) == 0 {
			log.Debugf("ignoring empty config file write event")
			return
		}
		sum := sha256.Sum256(data)
		newHash := hex.EncodeToString(sum[:])

		w.clientsMutex.RLock()
		currentHash := w.lastConfigHash
		w.clientsMutex.RUnlock()

		if currentHash != "" && currentHash == newHash {
			log.Debugf("config file content unchanged (hash match), skipping reload")
			return
		}
		log.Infof("config file changed, reloading: %s", w.configPath)
		if w.reloadConfig() {
			w.clientsMutex.Lock()
			w.lastConfigHash = newHash
			w.clientsMutex.Unlock()
		}
		return
	}

	// Handle auth directory changes incrementally
	if strings.HasPrefix(event.Name, w.authDir) && strings.HasSuffix(event.Name, ".json") {
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		if event.Op&fsnotify.Create == fsnotify.Create || event.Op&fsnotify.Write == fsnotify.Write {
			w.addOrUpdateClient(event.Name)
		} else if event.Op&fsnotify.Remove == fsnotify.Remove {
			w.removeClient(event.Name)
		}
	}
}

// reloadConfig reloads the configuration and triggers a full reload
func (w *Watcher) reloadConfig() bool {
	log.Debugf("starting config reload from: %s", w.configPath)

	newConfig, errLoadConfig := config.LoadConfig(w.configPath)
	if errLoadConfig != nil {
		log.Errorf("failed to reload config: %v", errLoadConfig)
		return false
	}

	w.clientsMutex.Lock()
	oldConfig := w.config
	w.config = newConfig
	w.clientsMutex.Unlock()

	// Always apply the current log level based on the latest config.
	// This ensures logrus reflects the desired level even if change detection misses.
	util.SetLogLevel(newConfig)
	// Additional debug for visibility when the flag actually changes.
	if oldConfig != nil && oldConfig.Debug != newConfig.Debug {
		log.Debugf("log level updated - debug mode changed from %t to %t", oldConfig.Debug, newConfig.Debug)
	}

	// Log configuration changes in debug mode
	if oldConfig != nil {
		log.Debugf("config changes detected:")
		if oldConfig.Port != newConfig.Port {
			log.Debugf("  port: %d -> %d", oldConfig.Port, newConfig.Port)
		}
		if oldConfig.AuthDir != newConfig.AuthDir {
			log.Debugf("  auth-dir: %s -> %s", oldConfig.AuthDir, newConfig.AuthDir)
		}
		if oldConfig.Debug != newConfig.Debug {
			log.Debugf("  debug: %t -> %t", oldConfig.Debug, newConfig.Debug)
		}
		if oldConfig.ProxyURL != newConfig.ProxyURL {
			log.Debugf("  proxy-url: %s -> %s", oldConfig.ProxyURL, newConfig.ProxyURL)
		}
		if oldConfig.RequestLog != newConfig.RequestLog {
			log.Debugf("  request-log: %t -> %t", oldConfig.RequestLog, newConfig.RequestLog)
		}
		if oldConfig.RequestRetry != newConfig.RequestRetry {
			log.Debugf("  request-retry: %d -> %d", oldConfig.RequestRetry, newConfig.RequestRetry)
		}
		if len(oldConfig.APIKeys) != len(newConfig.APIKeys) {
			log.Debugf("  api-keys count: %d -> %d", len(oldConfig.APIKeys), len(newConfig.APIKeys))
		}
		if len(oldConfig.GlAPIKey) != len(newConfig.GlAPIKey) {
			log.Debugf("  generative-language-api-key count: %d -> %d", len(oldConfig.GlAPIKey), len(newConfig.GlAPIKey))
		}
		if len(oldConfig.ClaudeKey) != len(newConfig.ClaudeKey) {
			log.Debugf("  claude-api-key count: %d -> %d", len(oldConfig.ClaudeKey), len(newConfig.ClaudeKey))
		}
		if len(oldConfig.CodexKey) != len(newConfig.CodexKey) {
			log.Debugf("  codex-api-key count: %d -> %d", len(oldConfig.CodexKey), len(newConfig.CodexKey))
		}
		if oldConfig.AllowLocalhostUnauthenticated != newConfig.AllowLocalhostUnauthenticated {
			log.Debugf("  allow-localhost-unauthenticated: %t -> %t", oldConfig.AllowLocalhostUnauthenticated, newConfig.AllowLocalhostUnauthenticated)
		}
		if oldConfig.RemoteManagement.AllowRemote != newConfig.RemoteManagement.AllowRemote {
			log.Debugf("  remote-management.allow-remote: %t -> %t", oldConfig.RemoteManagement.AllowRemote, newConfig.RemoteManagement.AllowRemote)
		}
	}

	log.Infof("config successfully reloaded, triggering client reload")
	// Reload clients with new config
	w.reloadClients()
	return true
}

// reloadClients performs a full scan and reload of all clients.
func (w *Watcher) reloadClients() {
	log.Debugf("starting full client reload process")

	w.clientsMutex.RLock()
	cfg := w.config
	oldFileClientCount := len(w.clients)
	oldAPIKeyClientCount := len(w.apiKeyClients)
	w.clientsMutex.RUnlock()

	if cfg == nil {
		log.Error("config is nil, cannot reload clients")
		return
	}

	// Unregister all old API key clients before creating new ones
	log.Debugf("unregistering %d old API key clients", oldAPIKeyClientCount)
	for _, oldClient := range w.apiKeyClients {
		if u, ok := oldClient.(interface{ UnregisterClient() }); ok {
			u.UnregisterClient()
		}
	}

	// Create new API key clients based on the new config
	newAPIKeyClients, glAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount := buildAPIKeyClients(cfg)
	log.Debugf("created %d new API key clients", len(newAPIKeyClients))

	// Load file-based clients
	newFileClients, successfulAuthCount := w.loadFileClients(cfg)
	log.Debugf("loaded %d new file-based clients", len(newFileClients))

	// Unregister all old file-based clients
	log.Debugf("unregistering %d old file-based clients", oldFileClientCount)
	for _, oldClient := range w.clients {
		if u, ok := any(oldClient).(interface{ UnregisterClient() }); ok {
			u.UnregisterClient()
		}
	}

	// Update client maps
	w.clientsMutex.Lock()
	w.clients = newFileClients
	w.apiKeyClients = newAPIKeyClients

	// Rebuild auth file hash cache for current clients
	w.lastAuthHashes = make(map[string]string, len(newFileClients))
	for path := range newFileClients {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			sum := sha256.Sum256(data)
			w.lastAuthHashes[path] = hex.EncodeToString(sum[:])
		}
	}
	w.clientsMutex.Unlock()

	totalNewClients := len(newFileClients) + len(newAPIKeyClients)

	log.Infof("full client reload complete - old: %d clients, new: %d clients (%d auth files + %d GL API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		oldFileClientCount+oldAPIKeyClientCount,
		totalNewClients,
		successfulAuthCount,
		glAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		openAICompatCount,
	)

	// Trigger the callback to update the server
	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback")
		combinedClients := w.buildCombinedClientMap()
		w.reloadCallback(combinedClients, cfg)
	}
}

// createClientFromFile creates a single client instance from a given token file path.
func (w *Watcher) createClientFromFile(path string, cfg *config.Config) (interfaces.Client, error) {
	data, errReadFile := os.ReadFile(path)
	if errReadFile != nil {
		return nil, errReadFile
	}

	// If the file is empty, it's likely an intermediate state (e.g., after touch, before write).
	// Silently ignore it and wait for a subsequent write event with content.
	if len(data) == 0 {
		return nil, nil // Not an error, just nothing to process yet.
	}

	tokenType := ""
	typeResult := gjson.GetBytes(data, "type")
	if typeResult.Exists() {
		tokenType = typeResult.String()
	}

	var err error
	if tokenType == "gemini" {
		var ts gemini.GeminiTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			clientCtx := context.Background()
			geminiAuth := gemini.NewGeminiAuth()
			httpClient, errGetClient := geminiAuth.GetAuthenticatedClient(clientCtx, &ts, cfg)
			if errGetClient != nil {
				return nil, errGetClient
			}
			return client.NewGeminiCLIClient(httpClient, &ts, cfg), nil
		}
	} else if tokenType == "codex" {
		var ts codex.CodexTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			return client.NewCodexClient(cfg, &ts)
		}
	} else if tokenType == "claude" {
		var ts claude.ClaudeTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			return client.NewClaudeClient(cfg, &ts), nil
		}
	} else if tokenType == "qwen" {
		var ts qwen.QwenTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			return client.NewQwenClient(cfg, &ts), nil
		}
    } else if tokenType == "gemini-web" {
        var ts gemini.GeminiAppTokenStorage
        if err = json.Unmarshal(data, &ts); err == nil {
            return client.NewGeminiWebClient(cfg, &ts, path)
        }
    }

	return nil, err
}

// clientsToSlice converts the client map to a slice.
func (w *Watcher) clientsToSlice(clientMap map[string]interfaces.Client) []interfaces.Client {
	s := make([]interfaces.Client, 0, len(clientMap))
	for _, v := range clientMap {
		s = append(s, v)
	}
	return s
}

// addOrUpdateClient handles the addition or update of a single client.
func (w *Watcher) addOrUpdateClient(path string) {
	w.clientsMutex.Lock()

	cfg := w.config
	if cfg == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}

	// Read file to check for emptiness and calculate hash
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		log.Errorf("failed to read auth file %s: %v", filepath.Base(path), errRead)
		w.clientsMutex.Unlock()
		return
	}
	if len(data) == 0 {
		// Empty file: ignore (wait for a subsequent WRITE)
		log.Debugf("ignoring empty auth file: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Calculate a hash of the current content and compare with the cache
	sum := sha256.Sum256(data)
	curHash := hex.EncodeToString(sum[:])
	if prev, ok := w.lastAuthHashes[path]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// If an old client exists, unregister it first
	if oldClient, ok := w.clients[path]; ok {
		if u, canUnregister := any(oldClient).(interface{ UnregisterClient() }); canUnregister {
			log.Debugf("unregistering old client for updated file: %s", filepath.Base(path))
			u.UnregisterClient()
		}
	}

	// Create new client (reads the file again internally; this is acceptable as the files are small and it keeps the change minimal)
	newClient, err := w.createClientFromFile(path, cfg)
	if err != nil {
		log.Errorf("failed to create/update client for %s: %v", filepath.Base(path), err)
		// If creation fails, ensure the old client is removed from the map; don't update hash, let a subsequent change retry
		delete(w.clients, path)
		w.clientsMutex.Unlock()
		return
	}
	if newClient == nil {
		// This branch should not be reached normally (empty files are handled above); a fallback
		log.Debugf("ignoring auth file with no client created: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Update client and hash cache
	log.Debugf("successfully created/updated client for %s", filepath.Base(path))
	w.clients[path] = newClient
	w.lastAuthHashes[path] = curHash

	w.clientsMutex.Unlock() // Unlock before the callback

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after add/update")
		combinedClients := w.buildCombinedClientMap()
		w.reloadCallback(combinedClients, cfg)
	}
}

// removeClient handles the removal of a single client.
func (w *Watcher) removeClient(path string) {
	w.clientsMutex.Lock()

	cfg := w.config
	var clientRemoved bool

	// Unregister client if it exists
	if oldClient, ok := w.clients[path]; ok {
		if u, canUnregister := any(oldClient).(interface{ UnregisterClient() }); canUnregister {
			log.Debugf("unregistering client for removed file: %s", filepath.Base(path))
			u.UnregisterClient()
		}
		delete(w.clients, path)
		delete(w.lastAuthHashes, path)
		log.Debugf("removed client for %s", filepath.Base(path))
		clientRemoved = true
	}

	w.clientsMutex.Unlock() // Release the lock before the callback

	if clientRemoved && w.reloadCallback != nil {
		log.Debugf("triggering server update callback after removal")
		combinedClients := w.buildCombinedClientMap()
		w.reloadCallback(combinedClients, cfg)
	}
}

// buildCombinedClientMap merges file-based clients with API key clients from the cache.
func (w *Watcher) buildCombinedClientMap() map[string]interfaces.Client {
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()

	combined := make(map[string]interfaces.Client)

	// Add file-based clients
	for k, v := range w.clients {
		combined[k] = v
	}

	// Add cached API key-based clients
	for k, v := range w.apiKeyClients {
		combined[k] = v
	}

	return combined
}

// loadFileClients scans the auth directory and creates clients from .json files.
func (w *Watcher) loadFileClients(cfg *config.Config) (map[string]interfaces.Client, int) {
	newClients := make(map[string]interfaces.Client)
	authFileCount := 0
	successfulAuthCount := 0

	authDir := cfg.AuthDir
	if strings.HasPrefix(authDir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("failed to get home directory: %v", err)
			return newClients, 0
		}
		authDir = filepath.Join(home, authDir[1:])
	}

	errWalk := filepath.Walk(authDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			log.Debugf("error accessing path %s: %v", path, err)
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			authFileCount++
			log.Debugf("processing auth file %d: %s", authFileCount, filepath.Base(path))
			if cliClient, errCreate := w.createClientFromFile(path, cfg); errCreate == nil && cliClient != nil {
				newClients[path] = cliClient
				successfulAuthCount++
			} else if errCreate != nil {
				log.Errorf("failed to create client from file %s: %v", path, errCreate)
			}
		}
		return nil
	})

	if errWalk != nil {
		log.Errorf("error walking auth directory: %v", errWalk)
	}
	log.Debugf("auth directory scan complete - found %d .json files, %d successful authentications", authFileCount, successfulAuthCount)
	return newClients, successfulAuthCount
}

// buildAPIKeyClients creates clients from API keys in the config.
func buildAPIKeyClients(cfg *config.Config) (map[string]interfaces.Client, int, int, int, int) {
	apiKeyClients := make(map[string]interfaces.Client)
	glAPIKeyCount := 0
	claudeAPIKeyCount := 0
	codexAPIKeyCount := 0
	openAICompatCount := 0

	if len(cfg.GlAPIKey) > 0 {
		for _, key := range cfg.GlAPIKey {
			httpClient := util.SetProxy(cfg, &http.Client{})
			cliClient := client.NewGeminiClient(httpClient, cfg, key)
			apiKeyClients[cliClient.GetClientID()] = cliClient
			glAPIKeyCount++
		}
	}
	if len(cfg.ClaudeKey) > 0 {
		for i := range cfg.ClaudeKey {
			cliClient := client.NewClaudeClientWithKey(cfg, i)
			apiKeyClients[cliClient.GetClientID()] = cliClient
			claudeAPIKeyCount++
		}
	}
	if len(cfg.CodexKey) > 0 {
		for i := range cfg.CodexKey {
			cliClient := client.NewCodexClientWithKey(cfg, i)
			apiKeyClients[cliClient.GetClientID()] = cliClient
			codexAPIKeyCount++
		}
	}
	if len(cfg.OpenAICompatibility) > 0 {
		for _, compatConfig := range cfg.OpenAICompatibility {
			for i := 0; i < len(compatConfig.APIKeys); i++ {
				compatClient, errClient := client.NewOpenAICompatibilityClient(cfg, &compatConfig, i)
				if errClient != nil {
					log.Errorf("failed to create OpenAI-compatibility client for %s: %v", compatConfig.Name, errClient)
					continue
				}
				apiKeyClients[compatClient.GetClientID()] = compatClient
				openAICompatCount++
			}
		}
	}
	return apiKeyClients, glAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount
}
