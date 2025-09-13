package client

import (
    "bytes"
    "context"
    "crypto/sha256"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "net/http/cookiejar"
    "os"
    "path/filepath"
    "regexp"
    "strings"
    "sync"
    "time"

    "github.com/gin-gonic/gin"
    gemweb "github.com/luispater/CLIProxyAPI/internal/api/geminiwebapi"
    "github.com/luispater/CLIProxyAPI/internal/auth/gemini"
    "github.com/luispater/CLIProxyAPI/internal/config"
    . "github.com/luispater/CLIProxyAPI/internal/constant"
    "github.com/luispater/CLIProxyAPI/internal/interfaces"
    "github.com/luispater/CLIProxyAPI/internal/registry"
    "github.com/luispater/CLIProxyAPI/internal/translator/translator"
    "github.com/luispater/CLIProxyAPI/internal/util"
    log "github.com/sirupsen/logrus"
    "github.com/tidwall/gjson"
)

const (
	geminiAppUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var (
	errGeminiUsageLimitExceeded = errors.New("usage limit exceeded")
	errGeminiModelInconsistent  = errors.New("model inconsistent")
	errGeminiModelInvalid       = errors.New("model invalid")
	errGeminiTemporarilyBlocked = errors.New("temporarily blocked")
	errGeminiAPI                = errors.New("API error")
)

func normalizeRole(role string) string {
    r := strings.ToLower(role)
    if r == "model" {
        return "assistant"
    }
    return r
}

type GeminiWebClient struct {
    ClientBase
    gwc           *gemweb.GeminiClient
    tokenFilePath string
    convStore     map[string][]string
    convMutex     sync.RWMutex

    // JSON-based conversation persistence
    convData  map[string]conversationRecord
    convIndex map[string]string

    // restart-stable id for conversation hashing/lookup
    stableClientID string

    cookieRotationStarted bool
    cookiePersistCancel   context.CancelFunc
}

func (c *GeminiWebClient) UnregisterClient() {
    if c.cookiePersistCancel != nil {
        c.cookiePersistCancel()
        c.cookiePersistCancel = nil
    }
    if c.gwc != nil {
        c.gwc.Close(0)
        c.gwc = nil
    }
    c.ClientBase.UnregisterClient()
}

func NewGeminiWebClient(cfg *config.Config, ts *gemini.GeminiAppTokenStorage, tokenFilePath string) (*GeminiWebClient, error) {
    jar, _ := cookiejar.New(nil)
    httpClient := util.SetProxy(cfg, &http.Client{Jar: jar})

    // derive a restart-stable id from tokens (sha256 of 1PSID, hex prefix)
    stableSuffix := sha256Hex(ts.Secure1PSID)
    if len(stableSuffix) > 16 { stableSuffix = stableSuffix[:16] }
    // runtime registry id (not used for hashing)
    idPrefix := stableSuffix
    if len(idPrefix) > 8 { idPrefix = idPrefix[:8] }
    clientID := fmt.Sprintf("gemini-web-%s-%d", idPrefix, time.Now().UnixNano())
    client := &GeminiWebClient{
        ClientBase: ClientBase{
            RequestMutex:       &sync.Mutex{},
            httpClient:         httpClient,
            cfg:                cfg,
            tokenStorage:       ts,
            modelQuotaExceeded: make(map[string]*time.Time),
        },
        tokenFilePath: tokenFilePath,
        convStore:     make(map[string][]string),
        convData:      make(map[string]conversationRecord),
        convIndex:     make(map[string]string),
        stableClientID: "gemini-web-" + stableSuffix,
    }
    _ = client.loadConvStore()
    _ = client.loadConvData()

    client.InitializeModelRegistry(clientID)
    client.RegisterModels(GEMINI, getGeminiWebAliasedModels())

    client.gwc = gemweb.NewGeminiClient(ts.Secure1PSID, ts.Secure1PSIDTS, cfg.ProxyURL)
    if err := client.gwc.Init(300, false, 300, true, 540, false); err != nil {
        log.Warnf("Gemini Web init failed for %s: %v. Will retry in background.", client.GetEmail(), err)
        go client.backgroundInitRetry()
    } else {
        client.cookieRotationStarted = true
        client.startCookiePersist()
    }

    return client, nil
}

func (c *GeminiWebClient) Init() error {
    ts := c.tokenStorage.(*gemini.GeminiAppTokenStorage)
    c.gwc = gemweb.NewGeminiClient(ts.Secure1PSID, ts.Secure1PSIDTS, c.cfg.ProxyURL)
    if err := c.gwc.Init(300, false, 300, true, 540, false); err != nil {
        return err
    }
    c.startCookiePersist()
    return nil
}

func (c *GeminiWebClient) Type() string {
	return GEMINI
}

func (c *GeminiWebClient) Provider() string {
    return GEMINI
}

func (c *GeminiWebClient) CanProvideModel(modelName string) bool {
    // Use centralized alias map for consistency
    ensureGeminiWebAliasMap()
    _, ok := geminiWebAliasMap[strings.ToLower(modelName)]
    return ok
}

func (c *GeminiWebClient) GetEmail() string {
    base := filepath.Base(c.tokenFilePath)
    return strings.TrimSuffix(base, ".json")
}

// StableClientID returns an ID that remains stable across restarts,
// used for conversation hashing and lookup.
func (c *GeminiWebClient) StableClientID() string {
    if c.stableClientID != "" {
        return c.stableClientID
    }
    // Fallback: derive from token file name to avoid empty value
    sum := sha256Hex(c.GetEmail())
    if len(sum) > 16 { sum = sum[:16] }
    return "gemini-web-" + sum
}

func (c *GeminiWebClient) SendRawMessage(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
    originalRequestRawJSON := bytes.Clone(rawJSON)

    var handlerType string
    if handler, ok := ctx.Value("handler").(interfaces.APIHandler); ok {
        handlerType = handler.HandlerType()
        rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, false)
    }

    if c.cfg.RequestLog {
        if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
            ginContext.Set("API_REQUEST", rawJSON)
        }
    }

    messages, files, mimes, msgFileIdx, err := c.parseMessagesAndFiles(rawJSON)
    if err != nil {
        return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: fmt.Errorf("bad request: %w", err)}
    }

    cleaned := sanitizeAssistantMessages(messages)

    underlying := mapAliasToUnderlying(modelName)
    model, err := gemweb.ModelFromName(underlying)
    if err != nil {
        return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: err}
    }

    // Find a reusable session (longest reusable prefix; last must be assistant/system)
    reuseMeta, remaining := c.findReusableSession(underlying, cleaned)
    reuse := len(reuseMeta) > 0
    var meta []string
    var useMsgs []roleText
    if reuse {
        meta = reuseMeta
        if len(remaining) == 1 {
            useMsgs = []roleText{remaining[0]}
        } else {
            useMsgs = remaining
        }
    } else {
        // Start a fresh session without reusing account-level metadata
        meta = nil
        useMsgs = cleaned
    }

    tagged := needRoleTags(useMsgs)
    if reuse && len(remaining) == 1 {
        tagged = false
    }
    var filesSubset [][]byte
    var mimesSubset []string
    if reuse && len(remaining) == 1 && len(messages) > 0 {
        // only files from the last message
        lastIdx := len(messages) - 1
        if lastIdx >= 0 && lastIdx < len(msgFileIdx) {
            for _, fi := range msgFileIdx[lastIdx] {
                if fi >= 0 && fi < len(files) {
                    filesSubset = append(filesSubset, files[fi])
                    // mime indices align with files slice
                    if fi < len(mimes) {
                        mimesSubset = append(mimesSubset, mimes[fi])
                    } else {
                        mimesSubset = append(mimesSubset, "")
                    }
                }
            }
        }
    } else {
        filesSubset = files
        mimesSubset = mimes
    }
    uploadedFiles, upErr := c.materializeInlineFiles(filesSubset, mimesSubset)
    if upErr != nil {
        return nil, upErr
    }
    explicitContext := true
    prompt := buildPrompt(useMsgs, tagged, false)
    if strings.TrimSpace(prompt) == "" {
        return nil, &interfaces.ErrorMessage{StatusCode: 400, Error: errors.New("bad request: empty prompt after filtering system/thought content")}
    }

    c.appendUpstreamRequestLog(ctx, modelName, tagged, explicitContext, prompt, len(uploadedFiles), reuse, len(meta))

    chat := c.gwc.StartChat(model, nil, meta)

    log.Debugf("Use Gemini Web account %s for model %s", c.GetEmail(), modelName)
    var output *gemweb.ModelOutput
    if out, genErr := chat.SendMessage(prompt, uploadedFiles); genErr != nil {
        log.Errorf("failed to generate content: %v", genErr)
        status := 500
        switch {
        case errors.Is(genErr, errGeminiUsageLimitExceeded), errors.Is(genErr, errGeminiTemporarilyBlocked):
            status = 429
        case errors.Is(genErr, errGeminiModelInconsistent), errors.Is(genErr, errGeminiModelInvalid):
            status = 400
        }
        if status == 429 {
            now := time.Now()
            c.modelQuotaExceeded[modelName] = &now
            c.SetModelQuotaExceeded(modelName)
        }
        return nil, &interfaces.ErrorMessage{StatusCode: status, Error: genErr}
    } else {
        output = &out
    }

    delete(c.modelQuotaExceeded, modelName)
    c.ClearModelQuotaExceeded(modelName)

    gemBytes, errMsg := c.convertOutputToGemini(output, modelName)
    if errMsg != nil {
        return nil, errMsg
    }

    c.AddAPIResponseData(ctx, gemBytes)

    if output != nil {
        metaAfter := chat.Metadata()
        if len(metaAfter) > 0 {
            // Keep storing for conversation records; not used to seed new sessions
            c.setAccountMetadata(modelName, metaAfter)
        }
    }

    // On success, persist the conversation (strip assistant thought tags) for future reusable-session lookup
    if output != nil {
        c.storeConversationJSON(underlying, cleaned, chat.Metadata(), output)
    }

    if translator.NeedConvert(handlerType, c.Type()) {
        var param any
        out := translator.ResponseNonStream(handlerType, c.Type(), ctx, modelName, originalRequestRawJSON, rawJSON, gemBytes, &param)
        return []byte(out), nil
    }
    return gemBytes, nil
}

func (c *GeminiWebClient) SendRawMessageStream(ctx context.Context, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage)

	go func() {
		defer close(dataChan)
		defer close(errChan)

		// Keep a pristine copy for translator context
		originalRequestRawJSON := bytes.Clone(rawJSON)

		// Normalize request into Gemini-style JSON if coming from another handler
		var handlerType string
		if handler, ok := ctx.Value("handler").(interfaces.APIHandler); ok {
			handlerType = handler.HandlerType()
			rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, true)
		}

		// Log upstream API request body for request logger
		if c.cfg.RequestLog {
			if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
				ginContext.Set("API_REQUEST", rawJSON)
			}
		}

        // Build messages and upload inline files if any
        // - Exclude system prompts and thought/reasoning parts from context
        messages, files, mimes, msgFileIdx, err := c.parseMessagesAndFiles(rawJSON)
        if err != nil {
            errChan <- &interfaces.ErrorMessage{StatusCode: 400, Error: fmt.Errorf("bad request: %w", err)}
            return
        }

        cleaned := sanitizeAssistantMessages(messages)

        underlying := mapAliasToUnderlying(modelName)
        model, err := gemweb.ModelFromName(underlying)
        if err != nil { errChan <- &interfaces.ErrorMessage{StatusCode: 400, Error: err}; return }

        reuseMeta, remaining := c.findReusableSession(underlying, cleaned)
        reuse := len(reuseMeta) > 0
        var meta []string
        var useMsgs []roleText
        if reuse {
            meta = reuseMeta
            if len(remaining) == 1 {
                useMsgs = []roleText{remaining[0]}
            } else {
                useMsgs = remaining
            }
        } else {
            meta = nil
            useMsgs = cleaned
        }

        tagged := needRoleTags(useMsgs)
        if reuse && len(remaining) == 1 {
            tagged = false
        }
        var filesSubset [][]byte
        var mimesSubset []string
        if reuse && len(remaining) == 1 && len(messages) > 0 {
            lastIdx := len(messages) - 1
            if lastIdx >= 0 && lastIdx < len(msgFileIdx) {
                for _, fi := range msgFileIdx[lastIdx] {
                    if fi >= 0 && fi < len(files) {
                        filesSubset = append(filesSubset, files[fi])
                        if fi < len(mimes) {
                            mimesSubset = append(mimesSubset, mimes[fi])
                        } else {
                            mimesSubset = append(mimesSubset, "")
                        }
                    }
                }
            }
        } else {
            filesSubset = files
            mimesSubset = mimes
        }
        uploadedFiles, upErr := c.materializeInlineFiles(filesSubset, mimesSubset)
        if upErr != nil {
            errChan <- upErr
            return
        }
        explicitContext := true
        prompt := buildPrompt(useMsgs, tagged, false)
        if strings.TrimSpace(prompt) == "" {
            errChan <- &interfaces.ErrorMessage{StatusCode: 400, Error: errors.New("bad request: empty prompt after filtering system/thought content")}
            return
        }

        c.appendUpstreamRequestLog(ctx, modelName, tagged, explicitContext, prompt, len(uploadedFiles), reuse, len(meta))

        log.Debugf("Use Gemini Web account %s for model %s", c.GetEmail(), modelName)
        chat := c.gwc.StartChat(model, nil, meta)
        out, genErr := chat.SendMessage(prompt, uploadedFiles)
        if genErr != nil {
            status := 500
            switch {
            case errors.Is(genErr, errGeminiUsageLimitExceeded), errors.Is(genErr, errGeminiTemporarilyBlocked):
                status = 429
            case errors.Is(genErr, errGeminiModelInconsistent), errors.Is(genErr, errGeminiModelInvalid):
                status = 400
            }
            if status == 429 {
                now := time.Now()
                c.modelQuotaExceeded[modelName] = &now
                c.SetModelQuotaExceeded(modelName)
            }
            errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: genErr}
            return
        }

        // Clear quota status on success
        delete(c.modelQuotaExceeded, modelName)
        c.ClearModelQuotaExceeded(modelName)

        gemBytes, errMsg := c.convertOutputToGemini(&out, modelName)
        if errMsg != nil { errChan <- errMsg; return }
        c.AddAPIResponseData(ctx, gemBytes)
        metaAfter := chat.Metadata()
        if len(metaAfter) > 0 { c.setAccountMetadata(modelName, metaAfter) }

        // On success, persist the conversation (strip assistant thought tags) for future reusable-session lookup
        c.storeConversationJSON(underlying, cleaned, chat.Metadata(), &out)

        if translator.NeedConvert(handlerType, c.Type()) && handlerType != GEMINI {
            var param any
            lines := translator.Response(handlerType, c.Type(), ctx, modelName, originalRequestRawJSON, rawJSON, gemBytes, &param)
            for _, l := range lines { if l != "" { dataChan <- []byte(l) } }
            return
        }
        dataChan <- gemBytes
    }()

	return dataChan, errChan
}

// chunkByRunes splits a string into rune-safe chunks of up to size runes.
func chunkByRunes(s string, size int) []string {
	if size <= 0 {
		return []string{s}
	}
	chunks := make([]string, 0, (len(s)/size)+1)
	var buf strings.Builder
	count := 0
	for _, r := range s {
		buf.WriteRune(r)
		count++
		if count >= size {
			chunks = append(chunks, buf.String())
			buf.Reset()
			count = 0
		}
	}
	if buf.Len() > 0 {
		chunks = append(chunks, buf.String())
	}
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

// appendUpstreamRequestLog appends a compact, rune-safe preview of the request context
// into the Gin context's API_REQUEST key so the upstream logger can capture it.
func (c *GeminiWebClient) appendUpstreamRequestLog(ctx context.Context, modelName string, useTags, explicitContext bool, prompt string, filesCount int, reuse bool, metaLen int) {
    if !c.cfg.RequestLog { return }
    ginContext, ok := ctx.Value("gin").(*gin.Context)
    if !ok || ginContext == nil { return }

    var sb strings.Builder
    sb.WriteString("\n\n=== GEMINI WEB UPSTREAM ===\n")
    sb.WriteString(fmt.Sprintf("account: %s\n", c.GetEmail()))
    if reuse { sb.WriteString("reuseIdx: 1\n") } else { sb.WriteString("reuseIdx: 0\n") }
    sb.WriteString(fmt.Sprintf("useTags: %t\n", useTags))
    sb.WriteString(fmt.Sprintf("metadata_len: %d\n", metaLen))
    if explicitContext { sb.WriteString("explicit_context: true\n") } else { sb.WriteString("explicit_context: false\n") }
    if filesCount > 0 { sb.WriteString(fmt.Sprintf("files: %d\n", filesCount)) }

    chunks := chunkByRunes(prompt, 4096)
    preview := prompt
    truncated := false
    if len(chunks) > 1 {
        preview = chunks[0]
        truncated = true
    }
    sb.WriteString("prompt_preview:\n")
    sb.WriteString(preview)
    if truncated { sb.WriteString("\n... [truncated]\n") }

    if existing, exists := ginContext.Get("API_REQUEST"); exists {
        if base, ok2 := existing.([]byte); ok2 {
            merged := append(append([]byte{}, base...), []byte(sb.String())...)
            ginContext.Set("API_REQUEST", merged)
        }
    }
}

func fetchGeneratedImageData(gi gemweb.GeneratedImage) (string, string, error) {
    path, err := gi.Save("", "", true, false, true, false)
    if err != nil { return "", "", err }
    defer func() { _ = os.Remove(path) }()
    b, err := os.ReadFile(path)
    if err != nil { return "", "", err }
    mime := http.DetectContentType(b)
    if !strings.HasPrefix(mime, "image/") {
        mime = extToMimeOrDefault(strings.ToLower(filepath.Ext(path)))
    }
    return mime, base64.StdEncoding.EncodeToString(b), nil
}

func mimeToExt(mimes []string, i int) string {
    if i < len(mimes) {
        return mimeToPreferredExt(strings.ToLower(mimes[i]))
    }
    return ".png"
}

// helpers to centralize mapping to avoid drift
func mimeToPreferredExt(mime string) string {
    switch mime {
    case "image/png":
        return ".png"
    case "image/jpeg", "image/jpg":
        return ".jpg"
    case "image/webp":
        return ".webp"
    case "image/gif":
        return ".gif"
    case "image/bmp":
        return ".bmp"
    case "image/heic":
        return ".heic"
    case "application/pdf":
        return ".pdf"
    default:
        return ".png"
    }
}

func extToMimeOrDefault(ext string) string {
    switch ext {
    case ".png":
        return "image/png"
    case ".jpg", ".jpeg":
        return "image/jpeg"
    case ".webp":
        return "image/webp"
    case ".gif":
        return "image/gif"
    case ".bmp":
        return "image/bmp"
    case ".heic":
        return "image/heic"
    case ".pdf":
        return "application/pdf"
    default:
        return "image/png"
    }
}

func (c *GeminiWebClient) SendRawTokenCount(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
    // No web endpoint for token counting; return a minimal Gemini-like usage structure
    return []byte(`{"totalTokens":0}`), nil
}

func (c *GeminiWebClient) SaveTokenToFile() error {
    ts := c.tokenStorage.(*gemini.GeminiAppTokenStorage)
    // Update storage from current web client cookies if available
    if c.gwc != nil && c.gwc.Cookies != nil {
        if v, ok := c.gwc.Cookies["__Secure-1PSID"]; ok {
            ts.Secure1PSID = v
        }
        if v, ok := c.gwc.Cookies["__Secure-1PSIDTS"]; ok {
            ts.Secure1PSIDTS = v
        }
    }
    log.Infof("Saving Gemini Web credentials to %s", c.tokenFilePath)
    return ts.SaveTokenToFile(c.tokenFilePath)
}

func (c *GeminiWebClient) IsModelQuotaExceeded(model string) bool {
	if t, ok := c.modelQuotaExceeded[model]; ok {
		return time.Since(*t) <= 30*time.Minute
	}
	return false
}

func (c *GeminiWebClient) GetUserAgent() string { return geminiAppUserAgent }

func (c *GeminiWebClient) GetRequestMutex() *sync.Mutex {
	return nil
}

func (c *GeminiWebClient) RefreshTokens(ctx context.Context) error {
    // Re-init underlying web client to refresh cookies/token
    return c.Init()
}


// mapAliasToUnderlying converts our public alias model names to geminiwebapi model names.
// Centralized alias helpers to keep registration and lookup in sync
var (
    geminiWebAliasOnce sync.Once
    geminiWebAliasMap  map[string]string // alias (lower) -> underlying (lower)
)

func ensureGeminiWebAliasMap() {
    geminiWebAliasOnce.Do(func() {
        geminiWebAliasMap = make(map[string]string)
        for _, m := range registry.GetGeminiModels() {
            // Skip models that the web client should not expose
            if m.ID == "gemini-2.5-flash-lite" {
                continue
            }
            alias := aliasFromModelID(m.ID)
            geminiWebAliasMap[strings.ToLower(alias)] = strings.ToLower(m.ID)
        }
    })
}

func getGeminiWebAliasedModels() []*registry.ModelInfo {
    ensureGeminiWebAliasMap()
    aliased := make([]*registry.ModelInfo, 0)
    for _, m := range registry.GetGeminiModels() {
        if m.ID == "gemini-2.5-flash-lite" {
            continue
        }
        cpy := *m
        cpy.ID = aliasFromModelID(m.ID)
        cpy.Name = cpy.ID
        aliased = append(aliased, &cpy)
    }
    return aliased
}

// mapAliasToUnderlying converts our public alias model names to geminiwebapi model names.
func mapAliasToUnderlying(name string) string {
    ensureGeminiWebAliasMap()
    n := strings.ToLower(name)
    if u, ok := geminiWebAliasMap[n]; ok {
        return u
    }
    // Fallback: trim prefix if present, else passthrough
    const prefix = "gemini-web-"
    if strings.HasPrefix(n, prefix) {
        // Rebuild the underlying name with the standard provider prefix
        return "gemini-" + strings.TrimPrefix(n, prefix)
    }
    return name
}

// aliasFromModelID builds the public alias name we expose for a Gemini model ID.
// Example: "gemini-2.5-pro" -> "gemini-web-2.5-pro"
func aliasFromModelID(modelID string) string {
    return "gemini-web-" + strings.TrimPrefix(modelID, "gemini-")
}

// ---------- Persistence of conversation metadata ----------
func (c *GeminiWebClient) convStorePath() string {
    wd, err := os.Getwd()
    if err != nil || wd == "" {
        wd = "."
    }
    convDir := filepath.Join(wd, "conv")
    base := strings.TrimSuffix(filepath.Base(c.tokenFilePath), filepath.Ext(c.tokenFilePath))
    return filepath.Join(convDir, base+".conv.json")
}

// JSON conversation data file (separate from account metadata)
func (c *GeminiWebClient) convDataPath() string {
    wd, err := os.Getwd()
    if err != nil || wd == "" {
        wd = "."
    }
    convDir := filepath.Join(wd, "conv")
    base := strings.TrimSuffix(filepath.Base(c.tokenFilePath), filepath.Ext(c.tokenFilePath))
    return filepath.Join(convDir, base+".data.json")
}

func (c *GeminiWebClient) loadConvStore() error {
    path := c.convStorePath()
    b, err := os.ReadFile(path)
    if err != nil {
        return nil // ignore missing file
    }
    var tmp map[string][]string
    if err := json.Unmarshal(b, &tmp); err != nil {
        return err
    }
    c.convMutex.Lock()
    c.convStore = tmp
    c.convMutex.Unlock()
    return nil
}

func (c *GeminiWebClient) saveConvStore() error {
    path := c.convStorePath()
    c.convMutex.RLock()
    data, err := json.MarshalIndent(c.convStore, "", "  ")
    c.convMutex.RUnlock()
    if err != nil { return err }
    tmp := path + ".tmp"
    // Ensure target directory exists
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { return err }
    if err := os.WriteFile(tmp, data, 0o644); err != nil { return err }
    return os.Rename(tmp, path)
}

// Account-level reusable metadata helpers
func (c *GeminiWebClient) accountMetaKey(modelName string) string {
    return fmt.Sprintf("account-meta|%s|%s", c.GetEmail(), modelName)
}

func (c *GeminiWebClient) getAccountMetadata(modelName string) []string {
    key := c.accountMetaKey(modelName)
    c.convMutex.RLock()
    meta := c.convStore[key]
    c.convMutex.RUnlock()
    return meta
}

func (c *GeminiWebClient) setAccountMetadata(modelName string, metadata []string) {
    key := c.accountMetaKey(modelName)
    c.convMutex.Lock()
    c.convStore[key] = metadata
    c.convMutex.Unlock()
    _ = c.saveConvStore()
}


func (c *GeminiWebClient) loadConvData() error {
    path := c.convDataPath()
    b, err := os.ReadFile(path)
    if err != nil {
        return nil
    }
    var wrapper struct {
        Items map[string]conversationRecord `json:"items"`
        Index map[string]string             `json:"index"`
    }
    if err := json.Unmarshal(b, &wrapper); err != nil {
        return err
    }
    c.convMutex.Lock()
    if wrapper.Items != nil {
        c.convData = wrapper.Items
    }
    if wrapper.Index != nil {
        c.convIndex = wrapper.Index
    }
    c.convMutex.Unlock()
    return nil
}

func (c *GeminiWebClient) saveConvData() error {
    path := c.convDataPath()
    wrapper := struct {
        Items map[string]conversationRecord `json:"items"`
        Index map[string]string             `json:"index"`
    }{
        Items: c.convData,
        Index: c.convIndex,
    }
    c.convMutex.RLock()
    data, err := json.MarshalIndent(wrapper, "", "  ")
    c.convMutex.RUnlock()
    if err != nil { return err }
    tmp := path + ".tmp"
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { return err }
    if err := os.WriteFile(tmp, data, 0o644); err != nil { return err }
    return os.Rename(tmp, path)
}

func (c *GeminiWebClient) backgroundInitRetry() {
    backoffs := []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second, 1 * time.Minute, 2 * time.Minute, 5 * time.Minute}
    i := 0
    for {
        if err := c.Init(); err == nil {
            log.Infof("Gemini Web token recovered for %s", c.GetEmail())
            if !c.cookieRotationStarted {
                c.cookieRotationStarted = true
                // Auto refresh is handled inside geminiwebapi client
            }
            // ensure persistence loop is running
            c.startCookiePersist()
            return
        }
        d := backoffs[i]
        if i < len(backoffs)-1 {
            i++
        }
        time.Sleep(d)
    }
}

// startCookiePersist starts a lightweight loop that detects cookie rotation
// from the underlying web client and persists refreshes to the token file.
func (c *GeminiWebClient) startCookiePersist() {
    if c.gwc == nil {
        return
    }
    // cancel previous loop if running
    if c.cookiePersistCancel != nil {
        c.cookiePersistCancel()
        c.cookiePersistCancel = nil
    }
    ctx, cancel := context.WithCancel(context.Background())
    c.cookiePersistCancel = cancel

    go func() {
        ticker := time.NewTicker(60 * time.Second)
        defer ticker.Stop()
        last := ""
        if v, ok := c.gwc.Cookies["__Secure-1PSIDTS"]; ok {
            last = v
        }
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                cur := ""
                if c.gwc != nil && c.gwc.Cookies != nil {
                    if v, ok := c.gwc.Cookies["__Secure-1PSIDTS"]; ok {
                        cur = v
                    }
                }
                if cur != "" && cur != last {
                    if err := c.SaveTokenToFile(); err != nil {
                        log.Errorf("Failed to persist rotated cookies for %s: %v", c.GetEmail(), err)
                    } else {
                        log.Debugf("Persisted rotated cookies for %s", c.GetEmail())
                        last = cur
                    }
                }
            }
        }
    }()
}

type roleText struct {
    Role string
    Text string
}

func (c *GeminiWebClient) parseMessagesAndFiles(rawJSON []byte) ([]roleText, [][]byte, []string, [][]int, error) {
    var messages []roleText
    var files [][]byte
    var mimes []string
    var perMsgFileIdx [][]int

    contents := gjson.GetBytes(rawJSON, "contents")
    if contents.Exists() {
        contents.ForEach(func(_, content gjson.Result) bool {
            role := normalizeRole(content.Get("role").String())
            // Skip system messages entirely per requirements
            if role == "system" {
                return true
            }
            var b strings.Builder
            startFile := len(files)
            content.Get("parts").ForEach(func(_, part gjson.Result) bool {
                if text := part.Get("text"); text.Exists() {
                    // Skip thought/reasoning parts from context
                    if part.Get("thought").Bool() {
                        return true
                    }
                    if b.Len() > 0 {
                        b.WriteString("\n")
                    }
                    b.WriteString(text.String())
                }
                if inlineData := part.Get("inlineData"); inlineData.Exists() {
                    data := inlineData.Get("data").String()
                    if data != "" {
                        if dec, err := base64.StdEncoding.DecodeString(data); err == nil {
                            files = append(files, dec)
                            // Accept both "mimeType" (Gemini API) and legacy "mime_type"
                            m := inlineData.Get("mimeType").String()
                            if m == "" {
                                m = inlineData.Get("mime_type").String()
                            }
                            mimes = append(mimes, m)
                        }
                    }
                }
                return true
            })
            messages = append(messages, roleText{Role: role, Text: b.String()})
            // record indices of files added by this message
            endFile := len(files)
            if endFile > startFile {
                idxs := make([]int, 0, endFile-startFile)
                for i := startFile; i < endFile; i++ { idxs = append(idxs, i) }
                perMsgFileIdx = append(perMsgFileIdx, idxs)
            } else {
                perMsgFileIdx = append(perMsgFileIdx, nil)
            }
            return true
        })
    }
    return messages, files, mimes, perMsgFileIdx, nil
}

func needRoleTags(msgs []roleText) bool {
    for _, m := range msgs {
        if strings.ToLower(m.Role) != "user" {
            return true
        }
    }
    return false
}

func addRoleTag(role, content string, unclose bool) string {
    if role == "" {
        role = "user"
    }
    if unclose {
        return "<|im_start|>" + role + "\n" + content
    }
    return "<|im_start|>" + role + "\n" + content + "\n<|im_end|>"
}

func buildPrompt(msgs []roleText, tagged bool, appendAssistant bool) string {
    if len(msgs) == 0 {
        if tagged && appendAssistant {
            return addRoleTag("assistant", "", true)
        }
        return ""
    }
    if !tagged {
        var sb strings.Builder
        for i, m := range msgs {
            if i > 0 {
                sb.WriteString("\n")
            }
            sb.WriteString(m.Text)
        }
        return sb.String()
    }
    var sb strings.Builder
    for _, m := range msgs {
        sb.WriteString(addRoleTag(m.Role, m.Text, false))
        sb.WriteString("\n")
    }
    if appendAssistant {
        sb.WriteString(addRoleTag("assistant", "", true))
    }
    return strings.TrimSpace(sb.String())
}

var reThink = regexp.MustCompile(`(?s)^\s*<think>.*?</think>\s*`)

func removeThinkTags(s string) string {
    return strings.TrimSpace(reThink.ReplaceAllString(s, ""))
}

func sanitizeAssistantMessages(msgs []roleText) []roleText {
    out := make([]roleText, 0, len(msgs))
    for _, m := range msgs {
        if strings.ToLower(m.Role) == "assistant" {
            out = append(out, roleText{Role: m.Role, Text: removeThinkTags(m.Text)})
        } else {
            out = append(out, m)
        }
    }
    return out
}

// ========== JSON conversation storage: structs/hashing/lookup/persistence (aligned with prior LMDB-style logic) ==========

type storedMessage struct {
    Role    string `json:"role"`
    Content string `json:"content"`
    Name    string `json:"name,omitempty"`
}

type conversationRecord struct {
    Model     string          `json:"model"`
    ClientID  string          `json:"client_id"`
    Metadata  []string        `json:"metadata,omitempty"`
    Messages  []storedMessage `json:"messages"`
    CreatedAt time.Time       `json:"created_at"`
    UpdatedAt time.Time       `json:"updated_at"`
}

func (c *GeminiWebClient) toStoredMessages(msgs []roleText) []storedMessage {
    out := make([]storedMessage, 0, len(msgs))
    for _, m := range msgs {
        out = append(out, storedMessage{
            Role:    m.Role,
            Content: m.Text,
        })
    }
    return out
}

func (c *GeminiWebClient) hashMessage(m storedMessage) string {
    s := fmt.Sprintf(`{"content":%q,"role":%q}`, m.Content, strings.ToLower(m.Role))
    return sha256Hex(s)
}

func (c *GeminiWebClient) hashConversation(clientID, model string, msgs []storedMessage) string {
    var b strings.Builder
    b.WriteString(clientID)
    b.WriteString("|")
    b.WriteString(model)
    for _, m := range msgs {
        b.WriteString("|")
        b.WriteString(c.hashMessage(m))
    }
    return sha256Hex(b.String())
}

func (c *GeminiWebClient) findByMessageList(model string, msgs []roleText) (conversationRecord, bool) {
    stored := c.toStoredMessages(msgs)
    stableID := c.StableClientID()
    stableHash := c.hashConversation(stableID, model, stored)
    fallbackID := c.GetEmail()
    fallbackHash := c.hashConversation(fallbackID, model, stored)

    c.convMutex.RLock()
    defer c.convMutex.RUnlock()

    // Try stable hash first
    if key, ok := c.convIndex["hash:"+stableHash]; ok {
        if rec, ok2 := c.convData[key]; ok2 {
            return rec, true
        }
    }
    if rec, ok := c.convData[stableHash]; ok {
        return rec, true
    }

    // Fallback to old scheme (file-name based client id)
    if key, ok := c.convIndex["hash:"+fallbackHash]; ok {
        if rec, ok2 := c.convData[key]; ok2 {
            return rec, true
        }
    }
    if rec, ok := c.convData[fallbackHash]; ok {
        return rec, true
    }
    return conversationRecord{}, false
}

func (c *GeminiWebClient) findConversation(model string, msgs []roleText) (conversationRecord, bool) {
    if len(msgs) == 0 {
        return conversationRecord{}, false
    }
    if rec, ok := c.findByMessageList(model, msgs); ok {
        return rec, true
    }
    if rec, ok := c.findByMessageList(model, sanitizeAssistantMessages(msgs)); ok {
        return rec, true
    }
    return conversationRecord{}, false
}

// Returns: reusable metadata (if found) and remaining messages (suffix to send)
func (c *GeminiWebClient) findReusableSession(model string, msgs []roleText) ([]string, []roleText) {
    if len(msgs) < 2 {
        return nil, nil
    }
    searchEnd := len(msgs)
    for searchEnd >= 2 {
        sub := msgs[:searchEnd]
        tail := sub[len(sub)-1]
        if strings.EqualFold(tail.Role, "assistant") || strings.EqualFold(tail.Role, "system") {
            if rec, ok := c.findConversation(model, sub); ok {
                remain := msgs[searchEnd:]
                return rec.Metadata, remain
            }
        }
        searchEnd--
    }
    return nil, nil
}

func (c *GeminiWebClient) storeConversationJSON(model string, history []roleText, metadata []string, output *gemweb.ModelOutput) {
    if output == nil || len(output.Candidates) == 0 {
        return
    }
    text := ""
    if t := output.Candidates[0].Text; t != "" {
        text = removeThinkTags(t)
    }
    final := append([]roleText{}, history...)
    final = append(final, roleText{Role: "assistant", Text: text})

    rec := conversationRecord{
        Model:     model,
        ClientID:  c.StableClientID(),
        Metadata:  metadata,
        Messages:  c.toStoredMessages(final),
        CreatedAt: time.Now(),
        UpdatedAt: time.Now(),
    }

    clientID := rec.ClientID
    hash := c.hashConversation(clientID, model, rec.Messages)

    c.convMutex.Lock()
    c.convData[hash] = rec
    c.convIndex["hash:"+hash] = hash
    // Backward-compat: also index by legacy file-name based client id
    legacyID := c.GetEmail()
    if legacyID != clientID {
        fhash := c.hashConversation(legacyID, model, rec.Messages)
        c.convIndex["hash:"+fhash] = hash
    }
    c.convMutex.Unlock()
    _ = c.saveConvData()
}

func sha256Hex(s string) string {
    sum := sha256.Sum256([]byte(s))
    return hex.EncodeToString(sum[:])
}

func (c *GeminiWebClient) materializeInlineFiles(files [][]byte, mimes []string) ([]string, *interfaces.ErrorMessage) {
    if len(files) == 0 { return nil, nil }
    paths := make([]string, 0, len(files))
    for i, data := range files {
        ext := mimeToExt(mimes, i)
        f, err := os.CreateTemp("", "gemini-upload-*"+ext)
        if err != nil {
            return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to create temp file: %w", err)}
        }
        if _, err = f.Write(data); err != nil {
            _ = f.Close(); _ = os.Remove(f.Name())
            return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to write temp file: %w", err)}
        }
        if err = f.Close(); err != nil {
            _ = os.Remove(f.Name())
            return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to close temp file: %w", err)}
        }
        paths = append(paths, f.Name())
    }
    return paths, nil
}

func (c *GeminiWebClient) generateContent(ctx context.Context, model gemweb.Model, prompt string, chat *gemweb.ChatSession, files ...string) (*gemweb.ModelOutput, error) {
    if c.gwc == nil {
        if err := c.Init(); err != nil { return nil, err }
    }
    out, err := c.gwc.GenerateContent(prompt, files, model, nil, chat)
    if err != nil {
        // Map known errors to our typed errors for status mapping
        switch err.(type) {
        case *gemweb.UsageLimitExceeded:
            return nil, errGeminiUsageLimitExceeded
        case *gemweb.ModelInvalid:
            return nil, errGeminiModelInvalid
        case *gemweb.TemporarilyBlocked:
            return nil, errGeminiTemporarilyBlocked
        case *gemweb.TimeoutError:
            return nil, fmt.Errorf("timeout: %w", err)
        }
        return nil, err
    }
    return &out, nil
}

// convertOutputToGemini converts simplified ModelOutput to a Gemini API-like JSON.
func (c *GeminiWebClient) convertOutputToGemini(output *gemweb.ModelOutput, modelName string) ([]byte, *interfaces.ErrorMessage) {
    if output == nil || len(output.Candidates) == 0 {
        return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("empty output")}
    }

    parts := make([]map[string]any, 0, 2)
    if output.Candidates[0].Thoughts != nil {
        if t := strings.TrimSpace(*output.Candidates[0].Thoughts); t != "" {
            parts = append(parts, map[string]any{"text": t, "thought": true})
        }
    }
    parts = append(parts, map[string]any{"text": output.Candidates[0].Text})

    if imgs := output.Candidates[0].GeneratedImages; len(imgs) > 0 {
        for _, gi := range imgs {
            if mime, data, err := fetchGeneratedImageData(gi); err == nil && data != "" {
                parts = append(parts, map[string]any{
                    "inlineData": map[string]any{
                        "mimeType":  mime,
                        "data":      data,
                    },
                })
            }
        }
    }

	resp := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": parts,
					"role":  "model",
				},
				"finishReason": "STOP",
				"index":        0,
			},
		},
		"createTime":   time.Now().Format(time.RFC3339Nano),
		"responseId":   fmt.Sprintf("gemini-web-%d", time.Now().UnixNano()),
		"modelVersion": modelName,
		"usageMetadata": map[string]any{
			"promptTokenCount":     0,
			"candidatesTokenCount": 0,
			"totalTokenCount":      0,
		},
	}
    b, err := json.Marshal(resp)
    if err != nil {
        return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to marshal gemini response: %w", err)}
    }
    return b, nil
}
