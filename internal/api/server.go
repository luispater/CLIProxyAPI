// Package api provides the HTTP API server implementation for the CLI Proxy API.
// It includes the main server struct, routing setup, middleware for CORS and authentication,
// and integration with various AI API handlers (OpenAI, Claude, Gemini).
// The server supports hot-reloading of clients and configuration.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers/claude"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers/gemini"
	managementHandlers "github.com/luispater/CLIProxyAPI/internal/api/handlers/management"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers/openai"
	"github.com/luispater/CLIProxyAPI/internal/api/middleware"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/logging"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
)

// Server represents the main API server.
// It encapsulates the Gin engine, HTTP server, handlers, and configuration.
type Server struct {
	// engine is the Gin web framework engine instance.
	engine *gin.Engine

	// server is the underlying HTTP server.
	server *http.Server

	// handlers contains the API handlers for processing requests.
	handlers *handlers.BaseAPIHandler

	// cfg holds the current server configuration.
	cfg *config.Config

	// requestLogger is the request logger instance for dynamic configuration updates.
	requestLogger *logging.FileRequestLogger

	// configFilePath is the absolute path to the YAML config file for persistence.
	configFilePath string

	// management handler
	mgmt *managementHandlers.Handler
}

// NewServer creates and initializes a new API server instance.
// It sets up the Gin engine, middleware, routes, and handlers.
//
// Parameters:
//   - cfg: The server configuration
//   - cliClients: A slice of AI service clients
//
// Returns:
//   - *Server: A new server instance
func NewServer(cfg *config.Config, cliClients []interfaces.Client, configFilePath string) *Server {
	// Set gin mode
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create gin engine
	engine := gin.New()

	// Add middleware
	engine.Use(gin.Logger())
	engine.Use(gin.Recovery())

	// Add request logging middleware (positioned after recovery, before auth)
	// Resolve logs directory relative to the configuration file directory.
	requestLogger := logging.NewFileRequestLogger(cfg.RequestLog, "logs", filepath.Dir(configFilePath))
	engine.Use(middleware.RequestLoggingMiddleware(requestLogger))

	engine.Use(corsMiddleware())

	// Create server instance
	s := &Server{
		engine:         engine,
		handlers:       handlers.NewBaseAPIHandlers(cliClients, cfg),
		cfg:            cfg,
		requestLogger:  requestLogger,
		configFilePath: configFilePath,
	}
	// Initialize management handler
	s.mgmt = managementHandlers.NewHandler(cfg, configFilePath)

	// Setup routes
	s.setupRoutes()

	// Create HTTP server
	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: engine,
	}

	return s
}

// setupRoutes configures the API routes for the server.
// It defines the endpoints and associates them with their respective handlers.
func (s *Server) setupRoutes() {
	openaiHandlers := openai.NewOpenAIAPIHandler(s.handlers)
	geminiHandlers := gemini.NewGeminiAPIHandler(s.handlers)
	geminiCLIHandlers := gemini.NewGeminiCLIAPIHandler(s.handlers)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(s.handlers)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(s.handlers)

	// OpenAI compatible API routes
	v1 := s.engine.Group("/v1")
	v1.Use(AuthMiddleware(s.cfg))
	{
		v1.GET("/models", s.unifiedModelsHandler(openaiHandlers, claudeCodeHandlers))
		v1.POST("/chat/completions", openaiHandlers.ChatCompletions)
		v1.POST("/completions", openaiHandlers.Completions)
		v1.POST("/messages", claudeCodeHandlers.ClaudeMessages)
		v1.POST("/responses", openaiResponsesHandlers.Responses)
	}

	// Gemini compatible API routes
	v1beta := s.engine.Group("/v1beta")
	v1beta.Use(AuthMiddleware(s.cfg))
	{
		v1beta.GET("/models", geminiHandlers.GeminiModels)
		v1beta.POST("/models/:action", geminiHandlers.GeminiHandler)
		v1beta.GET("/models/:action", geminiHandlers.GeminiGetHandler)
	}

	// Root endpoint
	s.engine.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "CLI Proxy API Server",
			"version": "1.0.0",
			"endpoints": []string{
				"POST /v1/chat/completions",
				"POST /v1/completions",
				"GET /v1/models",
			},
		})
	})
	s.engine.POST("/v1internal:method", geminiCLIHandlers.CLIHandler)

	// OAuth callback endpoints (reuse main server port)
	// These endpoints receive provider redirects and persist
	// the short-lived code/state for the waiting goroutine.
	s.engine.GET("/anthropic/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		// Persist to a temporary file keyed by state
		if state != "" {
			file := fmt.Sprintf("%s/.oauth-anthropic-%s.oauth", s.cfg.AuthDir, state)
			_ = os.WriteFile(file, []byte(fmt.Sprintf(`{"code":"%s","state":"%s","error":"%s"}`, code, state, errStr)), 0o600)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, "<html><body><h1>Authentication successful!</h1><p>You can close this window.</p></body></html>")
	})

	s.engine.GET("/codex/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if state != "" {
			file := fmt.Sprintf("%s/.oauth-codex-%s.oauth", s.cfg.AuthDir, state)
			_ = os.WriteFile(file, []byte(fmt.Sprintf(`{"code":"%s","state":"%s","error":"%s"}`, code, state, errStr)), 0o600)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, "<html><body><h1>Authentication successful!</h1><p>You can close this window.</p></body></html>")
	})

	s.engine.GET("/google/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if state != "" {
			file := fmt.Sprintf("%s/.oauth-gemini-%s.oauth", s.cfg.AuthDir, state)
			_ = os.WriteFile(file, []byte(fmt.Sprintf(`{"code":"%s","state":"%s","error":"%s"}`, code, state, errStr)), 0o600)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, "<html><body><h1>Authentication successful!</h1><p>You can close this window.</p></body></html>")
	})

	// Management API routes (delegated to management handlers)
	// New logic: if remote-management-key is empty, do not expose any management endpoint (404).
	if s.cfg.RemoteManagement.SecretKey != "" {
		mgmt := s.engine.Group("/v0/management")
		mgmt.Use(s.mgmt.Middleware())
		{
			mgmt.GET("/config", s.mgmt.GetConfig)

			mgmt.GET("/debug", s.mgmt.GetDebug)
			mgmt.PUT("/debug", s.mgmt.PutDebug)
			mgmt.PATCH("/debug", s.mgmt.PutDebug)

			mgmt.GET("/proxy-url", s.mgmt.GetProxyURL)
			mgmt.PUT("/proxy-url", s.mgmt.PutProxyURL)
			mgmt.PATCH("/proxy-url", s.mgmt.PutProxyURL)
			mgmt.DELETE("/proxy-url", s.mgmt.DeleteProxyURL)

			mgmt.GET("/quota-exceeded/switch-project", s.mgmt.GetSwitchProject)
			mgmt.PUT("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)
			mgmt.PATCH("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)

			mgmt.GET("/quota-exceeded/switch-preview-model", s.mgmt.GetSwitchPreviewModel)
			mgmt.PUT("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
			mgmt.PATCH("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)

			mgmt.GET("/api-keys", s.mgmt.GetAPIKeys)
			mgmt.PUT("/api-keys", s.mgmt.PutAPIKeys)
			mgmt.PATCH("/api-keys", s.mgmt.PatchAPIKeys)
			mgmt.DELETE("/api-keys", s.mgmt.DeleteAPIKeys)

			mgmt.GET("/generative-language-api-key", s.mgmt.GetGlKeys)
			mgmt.PUT("/generative-language-api-key", s.mgmt.PutGlKeys)
			mgmt.PATCH("/generative-language-api-key", s.mgmt.PatchGlKeys)
			mgmt.DELETE("/generative-language-api-key", s.mgmt.DeleteGlKeys)

			mgmt.GET("/request-log", s.mgmt.GetRequestLog)
			mgmt.PUT("/request-log", s.mgmt.PutRequestLog)
			mgmt.PATCH("/request-log", s.mgmt.PutRequestLog)

			mgmt.GET("/request-retry", s.mgmt.GetRequestRetry)
			mgmt.PUT("/request-retry", s.mgmt.PutRequestRetry)
			mgmt.PATCH("/request-retry", s.mgmt.PutRequestRetry)

			mgmt.GET("/allow-localhost-unauthenticated", s.mgmt.GetAllowLocalhost)
			mgmt.PUT("/allow-localhost-unauthenticated", s.mgmt.PutAllowLocalhost)
			mgmt.PATCH("/allow-localhost-unauthenticated", s.mgmt.PutAllowLocalhost)

			mgmt.GET("/claude-api-key", s.mgmt.GetClaudeKeys)
			mgmt.PUT("/claude-api-key", s.mgmt.PutClaudeKeys)
			mgmt.PATCH("/claude-api-key", s.mgmt.PatchClaudeKey)
			mgmt.DELETE("/claude-api-key", s.mgmt.DeleteClaudeKey)

			mgmt.GET("/codex-api-key", s.mgmt.GetCodexKeys)
			mgmt.PUT("/codex-api-key", s.mgmt.PutCodexKeys)
			mgmt.PATCH("/codex-api-key", s.mgmt.PatchCodexKey)
			mgmt.DELETE("/codex-api-key", s.mgmt.DeleteCodexKey)

			mgmt.GET("/openai-compatibility", s.mgmt.GetOpenAICompat)
			mgmt.PUT("/openai-compatibility", s.mgmt.PutOpenAICompat)
			mgmt.PATCH("/openai-compatibility", s.mgmt.PatchOpenAICompat)
			mgmt.DELETE("/openai-compatibility", s.mgmt.DeleteOpenAICompat)

			mgmt.GET("/auth-files", s.mgmt.ListAuthFiles)
			mgmt.GET("/auth-files/download", s.mgmt.DownloadAuthFile)
			mgmt.POST("/auth-files", s.mgmt.UploadAuthFile)
			mgmt.DELETE("/auth-files", s.mgmt.DeleteAuthFile)

			mgmt.GET("/anthropic-auth-url", s.mgmt.RequestAnthropicToken)
			mgmt.GET("/codex-auth-url", s.mgmt.RequestCodexToken)
			mgmt.GET("/gemini-cli-auth-url", s.mgmt.RequestGeminiCLIToken)
			mgmt.GET("/qwen-auth-url", s.mgmt.RequestQwenToken)
			mgmt.GET("/get-auth-status", s.mgmt.GetAuthStatus)
		}
	}
}

// unifiedModelsHandler creates a unified handler for the /v1/models endpoint
// that routes to different handlers based on the User-Agent header.
// If User-Agent starts with "claude-cli", it routes to Claude handler,
// otherwise it routes to OpenAI handler.
func (s *Server) unifiedModelsHandler(openaiHandler *openai.OpenAIAPIHandler, claudeHandler *claude.ClaudeCodeAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		userAgent := c.GetHeader("User-Agent")

		// Route to Claude handler if User-Agent starts with "claude-cli"
		if strings.HasPrefix(userAgent, "claude-cli") {
			// log.Debugf("Routing /v1/models to Claude handler for User-Agent: %s", userAgent)
			claudeHandler.ClaudeModels(c)
		} else {
			// log.Debugf("Routing /v1/models to OpenAI handler for User-Agent: %s", userAgent)
			openaiHandler.OpenAIModels(c)
		}
	}
}

// Start begins listening for and serving HTTP requests.
// It's a blocking call and will only return on an unrecoverable error.
//
// Returns:
//   - error: An error if the server fails to start
func (s *Server) Start() error {
	log.Debugf("Starting API server on %s", s.server.Addr)

	// Start the HTTP server.
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to start HTTP server: %v", err)
	}

	return nil
}

// Stop gracefully shuts down the API server without interrupting any
// active connections.
//
// Parameters:
//   - ctx: The context for graceful shutdown
//
// Returns:
//   - error: An error if the server fails to stop
func (s *Server) Stop(ctx context.Context) error {
	log.Debug("Stopping API server...")

	// Shutdown the HTTP server.
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", err)
	}

	log.Debug("API server stopped")
	return nil
}

// corsMiddleware returns a Gin middleware handler that adds CORS headers
// to every response, allowing cross-origin requests.
//
// Returns:
//   - gin.HandlerFunc: The CORS middleware handler
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// UpdateClients updates the server's client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (s *Server) UpdateClients(clients map[string]interfaces.Client, cfg *config.Config) {
	clientSlice := s.clientsToSlice(clients)
	// Update request logger enabled state if it has changed
	if s.requestLogger != nil && s.cfg.RequestLog != cfg.RequestLog {
		s.requestLogger.SetEnabled(cfg.RequestLog)
		log.Debugf("request logging updated from %t to %t", s.cfg.RequestLog, cfg.RequestLog)
	}

	// Update log level dynamically when debug flag changes
	if s.cfg.Debug != cfg.Debug {
		util.SetLogLevel(cfg)
		log.Debugf("debug mode updated from %t to %t", s.cfg.Debug, cfg.Debug)
	}

	s.cfg = cfg
	s.handlers.UpdateClients(clientSlice, cfg)
	if s.mgmt != nil {
		s.mgmt.SetConfig(cfg)
	}

	// Count client types for detailed logging
	authFiles := 0
	glAPIKeyCount := 0
	claudeAPIKeyCount := 0
	codexAPIKeyCount := 0
	openAICompatCount := 0

	for _, c := range clientSlice {
		switch cl := c.(type) {
    case *client.GeminiCLIClient:
        authFiles++
    case *client.GeminiWebClient:
        authFiles++
		case *client.CodexClient:
			if cl.GetAPIKey() == "" {
				authFiles++
			} else {
				codexAPIKeyCount++
			}
		case *client.ClaudeClient:
			if cl.GetAPIKey() == "" {
				authFiles++
			} else {
				claudeAPIKeyCount++
			}
		case *client.QwenClient:
			authFiles++
		case *client.GeminiClient:
			glAPIKeyCount++
		case *client.OpenAICompatibilityClient:
			openAICompatCount++
		}
	}

	log.Infof("server clients and configuration updated: %d clients (%d auth files + %d GL API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		len(clientSlice),
		authFiles,
		glAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		openAICompatCount,
	)
}

// (management handlers moved to internal/api/handlers/management)

// AuthMiddleware returns a Gin middleware handler that authenticates requests
// using API keys. If no API keys are configured, it allows all requests.
//
// Parameters:
//   - cfg: The server configuration containing API keys
//
// Returns:
//   - gin.HandlerFunc: The authentication middleware handler
func AuthMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.AllowLocalhostUnauthenticated && strings.HasPrefix(c.Request.RemoteAddr, "127.0.0.1:") {
			c.Next()
			return
		}

		if len(cfg.APIKeys) == 0 {
			c.Next()
			return
		}

		// Get the Authorization header
		authHeader := c.GetHeader("Authorization")
		authHeaderGoogle := c.GetHeader("X-Goog-Api-Key")
		authHeaderAnthropic := c.GetHeader("X-Api-Key")

		// Get the API key from the query parameter
		apiKeyQuery, _ := c.GetQuery("key")

		if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && apiKeyQuery == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Missing API key",
			})
			return
		}

		// Extract the API key
		parts := strings.Split(authHeader, " ")
		var apiKey string
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			apiKey = parts[1]
		} else {
			apiKey = authHeader
		}

		// Find the API key in the in-memory list
		var foundKey string
		for i := range cfg.APIKeys {
			if cfg.APIKeys[i] == apiKey || cfg.APIKeys[i] == authHeaderGoogle || cfg.APIKeys[i] == authHeaderAnthropic || cfg.APIKeys[i] == apiKeyQuery {
				foundKey = cfg.APIKeys[i]
				break
			}
		}
		if foundKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid API key",
			})
			return
		}

		// Store the API key and user in the context
		c.Set("apiKey", foundKey)

		c.Next()
	}
}

func (s *Server) clientsToSlice(clientMap map[string]interfaces.Client) []interfaces.Client {
	slice := make([]interfaces.Client, 0, len(clientMap))
	for _, v := range clientMap {
		slice = append(slice, v)
	}
	return slice
}
