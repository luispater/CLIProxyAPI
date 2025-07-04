package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
)

const (
	codeAssistEndpoint = "https://cloudcode-pa.googleapis.com"
	apiVersion         = "v1internal"
	pluginVersion      = "0.1.9"

	glEndPoint   = "https://generativelanguage.googleapis.com/"
	glApiVersion = "v1beta"
)

var (
	previewModels = map[string][]string{
		"gemini-2.5-pro":   {"gemini-2.5-pro-preview-05-06", "gemini-2.5-pro-preview-06-05"},
		"gemini-2.5-flash": {"gemini-2.5-flash-preview-04-17", "gemini-2.5-flash-preview-05-20"},
	}
)

// Client is the main client for interacting with the CLI API.
type Client struct {
	httpClient         *http.Client
	RequestMutex       sync.Mutex
	tokenStorage       *auth.TokenStorage
	cfg                *config.Config
	modelQuotaExceeded map[string]*time.Time
	glAPIKey           string
}

// NewClient creates a new CLI API client.
func NewClient(httpClient *http.Client, ts *auth.TokenStorage, cfg *config.Config, glAPIKey ...string) *Client {
	var glKey string
	if len(glAPIKey) > 0 {
		glKey = glAPIKey[0]
	}
	return &Client{
		httpClient:         httpClient,
		tokenStorage:       ts,
		cfg:                cfg,
		modelQuotaExceeded: make(map[string]*time.Time),
		glAPIKey:           glKey,
	}
}

func (c *Client) SetProjectID(projectID string) {
	c.tokenStorage.ProjectID = projectID
}

func (c *Client) SetIsAuto(auto bool) {
	c.tokenStorage.Auto = auto
}

func (c *Client) SetIsChecked(checked bool) {
	c.tokenStorage.Checked = checked
}

func (c *Client) IsChecked() bool {
	return c.tokenStorage.Checked
}

func (c *Client) IsAuto() bool {
	return c.tokenStorage.Auto
}

func (c *Client) GetEmail() string {
	return c.tokenStorage.Email
}

func (c *Client) GetProjectID() string {
	if c.tokenStorage != nil {
		return c.tokenStorage.ProjectID
	}
	return ""
}

func (c *Client) GetGenerativeLanguageAPIKey() string {
	return c.glAPIKey
}

// SetupUser performs the initial user onboarding and setup.
func (c *Client) SetupUser(ctx context.Context, email, projectID string) error {
	c.tokenStorage.Email = email
	log.Info("Performing user onboarding...")

	// 1. LoadCodeAssist
	loadAssistReqBody := map[string]interface{}{
		"metadata": getClientMetadata(),
	}
	if projectID != "" {
		loadAssistReqBody["cloudaicompanionProject"] = projectID
	}

	var loadAssistResp map[string]interface{}
	err := c.makeAPIRequest(ctx, "loadCodeAssist", "POST", loadAssistReqBody, &loadAssistResp)
	if err != nil {
		return fmt.Errorf("failed to load code assist: %w", err)
	}

	// a, _ := json.Marshal(&loadAssistResp)
	// log.Debug(string(a))
	//
	// a, _ = json.Marshal(loadAssistReqBody)
	// log.Debug(string(a))

	// 2. OnboardUser
	var onboardTierID = "legacy-tier"
	if tiers, ok := loadAssistResp["allowedTiers"].([]interface{}); ok {
		for _, t := range tiers {
			if tier, tierOk := t.(map[string]interface{}); tierOk {
				if isDefault, isDefaultOk := tier["isDefault"].(bool); isDefaultOk && isDefault {
					if id, idOk := tier["id"].(string); idOk {
						onboardTierID = id
						break
					}
				}
			}
		}
	}

	onboardProjectID := projectID
	if p, ok := loadAssistResp["cloudaicompanionProject"].(string); ok && p != "" {
		onboardProjectID = p
	}

	onboardReqBody := map[string]interface{}{
		"tierId":   onboardTierID,
		"metadata": getClientMetadata(),
	}
	if onboardProjectID != "" {
		onboardReqBody["cloudaicompanionProject"] = onboardProjectID
	} else {
		return fmt.Errorf("failed to start user onboarding, need define a project id")
	}

	for {
		var lroResp map[string]interface{}
		err = c.makeAPIRequest(ctx, "onboardUser", "POST", onboardReqBody, &lroResp)
		if err != nil {
			return fmt.Errorf("failed to start user onboarding: %w", err)
		}
		// a, _ := json.Marshal(&lroResp)
		// log.Debug(string(a))

		// 3. Poll Long-Running Operation (LRO)
		done, doneOk := lroResp["done"].(bool)
		if doneOk && done {
			if project, projectOk := lroResp["response"].(map[string]interface{})["cloudaicompanionProject"].(map[string]interface{}); projectOk {
				if projectID != "" {
					c.tokenStorage.ProjectID = projectID
				} else {
					c.tokenStorage.ProjectID = project["id"].(string)
				}
				log.Infof("Onboarding complete. Using Project ID: %s", c.tokenStorage.ProjectID)
				return nil
			}
		} else {
			log.Println("Onboarding in progress, waiting 5 seconds...")
			time.Sleep(5 * time.Second)
		}
	}
}

// makeAPIRequest handles making requests to the CLI API endpoints.
func (c *Client) makeAPIRequest(ctx context.Context, endpoint, method string, body interface{}, result interface{}) error {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, apiVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		url = fmt.Sprintf("%s/%s", codeAssistEndpoint, endpoint)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	// Set headers
	metadataStr := getClientMetadataString()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", getUserAgent())
	req.Header.Set("Client-Metadata", metadataStr)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		if err = resp.Body.Close(); err != nil {
			log.Printf("warn: failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if result != nil {
		if err = json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to decode response body: %w", err)
		}
	}

	return nil
}

// APIRequest handles making requests to the CLI API endpoints.
func (c *Client) APIRequest(ctx context.Context, endpoint string, body interface{}, stream bool) (io.ReadCloser, *ErrorMessage) {
	var jsonBody []byte
	var err error
	if byteBody, ok := body.([]byte); ok {
		jsonBody = byteBody
	} else {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, &ErrorMessage{500, fmt.Errorf("failed to marshal request body: %w", err)}
		}
	}

	var url string
	if c.glAPIKey == "" {
		// Add alt=sse for streaming
		url = fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, apiVersion, endpoint)
		if stream {
			url = url + "?alt=sse"
		}
	} else {
		modelResult := gjson.GetBytes(jsonBody, "model")
		url = fmt.Sprintf("%s/%s/models/%s:%s", glEndPoint, glApiVersion, modelResult.String(), endpoint)
		if stream {
			url = url + "?alt=sse"
		}
		jsonBody = []byte(gjson.GetBytes(jsonBody, "request").Raw)
	}

	// log.Debug(string(jsonBody))
	reqBody := bytes.NewBuffer(jsonBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to create request: %v", err)}
	}

	// Set headers
	metadataStr := getClientMetadataString()
	req.Header.Set("Content-Type", "application/json")
	if c.glAPIKey == "" {
		token, errToken := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
		if errToken != nil {
			return nil, &ErrorMessage{500, fmt.Errorf("failed to get token: %v", errToken)}
		}
		req.Header.Set("User-Agent", getUserAgent())
		req.Header.Set("Client-Metadata", metadataStr)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	} else {
		req.Header.Set("x-goog-api-key", c.glAPIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to execute request: %v", err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			if err = resp.Body.Close(); err != nil {
				log.Printf("warn: failed to close response body: %v", err)
			}
		}()
		bodyBytes, _ := io.ReadAll(resp.Body)

		return nil, &ErrorMessage{resp.StatusCode, fmt.Errorf(string(bodyBytes))}
	}

	return resp.Body, nil
}

// SendMessageStream handles a single conversational turn, including tool calls.
func (c *Client) SendMessage(ctx context.Context, rawJson []byte, model string, contents []Content, tools []ToolDeclaration) ([]byte, *ErrorMessage) {
	request := GenerateContentRequest{
		Contents: contents,
		GenerationConfig: GenerationConfig{
			ThinkingConfig: GenerationConfigThinkingConfig{
				IncludeThoughts: true,
			},
		},
	}
	request.Tools = tools

	requestBody := map[string]interface{}{
		"project": c.GetProjectID(), // Assuming ProjectID is available
		"request": request,
		"model":   model,
	}

	byteRequestBody, _ := json.Marshal(requestBody)

	// log.Debug(string(byteRequestBody))

	reasoningEffortResult := gjson.GetBytes(rawJson, "reasoning_effort")
	if reasoningEffortResult.String() == "none" {
		byteRequestBody, _ = sjson.DeleteBytes(byteRequestBody, "request.generationConfig.thinkingConfig.include_thoughts")
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 0)
	} else if reasoningEffortResult.String() == "auto" {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
	} else if reasoningEffortResult.String() == "low" {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 1024)
	} else if reasoningEffortResult.String() == "medium" {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 8192)
	} else if reasoningEffortResult.String() == "high" {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 24576)
	} else {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
	}

	temperatureResult := gjson.GetBytes(rawJson, "temperature")
	if temperatureResult.Exists() && temperatureResult.Type == gjson.Number {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.temperature", temperatureResult.Num)
	}

	topPResult := gjson.GetBytes(rawJson, "top_p")
	if topPResult.Exists() && topPResult.Type == gjson.Number {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topP", topPResult.Num)
	}

	topKResult := gjson.GetBytes(rawJson, "top_k")
	if topKResult.Exists() && topKResult.Type == gjson.Number {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topK", topKResult.Num)
	}

	modelName := model
	// log.Debug(string(byteRequestBody))
	for {
		if c.isModelQuotaExceeded(modelName) {
			if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
				modelName = c.getPreviewModel(model)
				if modelName != "" {
					log.Debugf("Model %s is quota exceeded. Switch to preview model %s", model, modelName)
					byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "model", modelName)
					continue
				}
			}
			return nil, &ErrorMessage{
				StatusCode: 429,
				Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, model),
			}
		}

		respBody, err := c.APIRequest(ctx, "generateContent", byteRequestBody, false)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
					continue
				}
			}
			return nil, err
		}
		delete(c.modelQuotaExceeded, modelName)
		bodyBytes, errReadAll := io.ReadAll(respBody)
		if errReadAll != nil {
			return nil, &ErrorMessage{StatusCode: 500, Error: errReadAll}
		}
		return bodyBytes, nil
	}
}

// SendMessageStream handles a single conversational turn, including tool calls.
func (c *Client) SendMessageStream(ctx context.Context, rawJson []byte, model string, contents []Content, tools []ToolDeclaration) (<-chan []byte, <-chan *ErrorMessage) {
	dataTag := []byte("data: ")
	errChan := make(chan *ErrorMessage)
	dataChan := make(chan []byte)
	go func() {
		defer close(errChan)
		defer close(dataChan)

		request := GenerateContentRequest{
			Contents: contents,
			GenerationConfig: GenerationConfig{
				ThinkingConfig: GenerationConfigThinkingConfig{
					IncludeThoughts: true,
				},
			},
		}
		request.Tools = tools

		requestBody := map[string]interface{}{
			"project": c.GetProjectID(), // Assuming ProjectID is available
			"request": request,
			"model":   model,
		}

		byteRequestBody, _ := json.Marshal(requestBody)

		// log.Debug(string(byteRequestBody))

		reasoningEffortResult := gjson.GetBytes(rawJson, "reasoning_effort")
		if reasoningEffortResult.String() == "none" {
			byteRequestBody, _ = sjson.DeleteBytes(byteRequestBody, "request.generationConfig.thinkingConfig.include_thoughts")
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 0)
		} else if reasoningEffortResult.String() == "auto" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
		} else if reasoningEffortResult.String() == "low" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 1024)
		} else if reasoningEffortResult.String() == "medium" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 8192)
		} else if reasoningEffortResult.String() == "high" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 24576)
		} else {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
		}

		temperatureResult := gjson.GetBytes(rawJson, "temperature")
		if temperatureResult.Exists() && temperatureResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.temperature", temperatureResult.Num)
		}

		topPResult := gjson.GetBytes(rawJson, "top_p")
		if topPResult.Exists() && topPResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topP", topPResult.Num)
		}

		topKResult := gjson.GetBytes(rawJson, "top_k")
		if topKResult.Exists() && topKResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topK", topKResult.Num)
		}

		// log.Debug(string(byteRequestBody))
		modelName := model
		var stream io.ReadCloser
		for {
			if c.isModelQuotaExceeded(modelName) {
				if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
					modelName = c.getPreviewModel(model)
					if modelName != "" {
						log.Debugf("Model %s is quota exceeded. Switch to preview model %s", model, modelName)
						byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "model", modelName)
						continue
					}
				}
				errChan <- &ErrorMessage{
					StatusCode: 429,
					Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, model),
				}
				return
			}
			var err *ErrorMessage
			stream, err = c.APIRequest(ctx, "streamGenerateContent", byteRequestBody, true)
			if err != nil {
				if err.StatusCode == 429 {
					now := time.Now()
					c.modelQuotaExceeded[modelName] = &now
					if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
						continue
					}
				}
				errChan <- err
				return
			}
			delete(c.modelQuotaExceeded, modelName)
			break
		}

		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Bytes()
			// log.Printf("Received stream chunk: %s", line)
			if bytes.HasPrefix(line, dataTag) {
				dataChan <- line[6:]
			}
		}

		if errScanner := scanner.Err(); errScanner != nil {
			// log.Println(err)
			errChan <- &ErrorMessage{500, errScanner}
			_ = stream.Close()
			return
		}

		_ = stream.Close()
	}()

	return dataChan, errChan
}

func (c *Client) isModelQuotaExceeded(model string) bool {
	if lastExceededTime, hasKey := c.modelQuotaExceeded[model]; hasKey {
		duration := time.Now().Sub(*lastExceededTime)
		if duration > 30*time.Minute {
			return false
		}
		return true
	}
	return false
}

func (c *Client) getPreviewModel(model string) string {
	if models, hasKey := previewModels[model]; hasKey {
		for i := 0; i < len(models); i++ {
			if !c.isModelQuotaExceeded(models[i]) {
				return models[i]
			}
		}
	}
	return ""
}

func (c *Client) IsModelQuotaExceeded(model string) bool {
	if c.isModelQuotaExceeded(model) {
		if c.cfg.QuotaExceeded.SwitchPreviewModel {
			return c.getPreviewModel(model) == ""
		}
		return true
	}
	return false
}

// CheckCloudAPIIsEnabled sends a simple test request to the API to verify
// that the Cloud AI API is enabled for the user's project. It provides
// an activation URL if the API is disabled.
func (c *Client) CheckCloudAPIIsEnabled() (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		c.RequestMutex.Unlock()
		cancel()
	}()
	c.RequestMutex.Lock()

	// A simple request to test the API endpoint.
	requestBody := fmt.Sprintf(`{"project":"%s","request":{"contents":[{"role":"user","parts":[{"text":"Be concise. What is the capital of France?"}]}],"generationConfig":{"thinkingConfig":{"include_thoughts":false,"thinkingBudget":0}}},"model":"gemini-2.5-flash"}`, c.tokenStorage.ProjectID)

	stream, err := c.APIRequest(ctx, "streamGenerateContent", []byte(requestBody), true)
	if err != nil {
		// If a 403 Forbidden error occurs, it likely means the API is not enabled.
		if err.StatusCode == 403 {
			errJson := err.Error.Error()
			// Check for a specific error code and extract the activation URL.
			if gjson.Get(errJson, "error.code").Int() == 403 {
				activationUrl := gjson.Get(errJson, "error.details.0.metadata.activationUrl").String()
				if activationUrl != "" {
					log.Warnf(
						"\n\nPlease activate your account with this url:\n\n%s\n And execute this command again:\n%s --login --project_id %s",
						activationUrl,
						os.Args[0],
						c.tokenStorage.ProjectID,
					)
				}
			}
			return false, nil
		}
		return false, err.Error
	}
	defer func() {
		_ = stream.Close()
	}()

	// We only need to know if the request was successful, so we can drain the stream.
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		// Do nothing, just consume the stream.
	}

	return scanner.Err() == nil, scanner.Err()
}

// GetProjectList fetches a list of Google Cloud projects accessible by the user.
func (c *Client) GetProjectList(ctx context.Context) (*GCPProject, error) {
	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if err != nil {
		return nil, fmt.Errorf("could not create project list request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute project list request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("project list request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var project GCPProject
	if err = json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", err)
	}
	return &project, nil
}

// SaveTokenToFile serializes the client's current token storage to a JSON file.
// The filename is constructed from the user's email and project ID.
func (c *Client) SaveTokenToFile() error {
	if err := os.MkdirAll(c.cfg.AuthDir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	fileName := filepath.Join(c.cfg.AuthDir, fmt.Sprintf("%s-%s.json", c.tokenStorage.Email, c.tokenStorage.ProjectID))
	log.Infof("Saving credentials to %s", fileName)
	f, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err = json.NewEncoder(f).Encode(c.tokenStorage); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// getClientMetadata returns a map of metadata about the client environment,
// such as IDE type, platform, and plugin version.
func getClientMetadata() map[string]string {
	return map[string]string{
		"ideType":       "IDE_UNSPECIFIED",
		"platform":      getPlatform(),
		"pluginType":    "GEMINI",
		"pluginVersion": pluginVersion,
	}
}

// getClientMetadataString returns the client metadata as a single,
// comma-separated string, which is required for the 'Client-Metadata' header.
func getClientMetadataString() string {
	md := getClientMetadata()
	parts := make([]string, 0, len(md))
	for k, v := range md {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// getUserAgent constructs the User-Agent string for HTTP requests.
func getUserAgent() string {
	return fmt.Sprintf("GeminiCLI/%s (%s; %s)", pluginVersion, runtime.GOOS, runtime.GOARCH)
}

// getPlatform determines the operating system and architecture and formats
// it into a string expected by the backend API.
func getPlatform() string {
	goOS := runtime.GOOS
	arch := runtime.GOARCH
	switch goOS {
	case "darwin":
		return fmt.Sprintf("DARWIN_%s", strings.ToUpper(arch))
	case "linux":
		return fmt.Sprintf("LINUX_%s", strings.ToUpper(arch))
	case "windows":
		return fmt.Sprintf("WINDOWS_%s", strings.ToUpper(arch))
	default:
		return "PLATFORM_UNSPECIFIED"
	}
}
