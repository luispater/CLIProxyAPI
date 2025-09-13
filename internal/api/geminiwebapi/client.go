package geminiwebapi

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "regexp"
    "strings"
    "sync"
    "time"
)

// GeminiClient is the async http client interface (Go port)
type GeminiClient struct {
    Cookies          map[string]string
    Proxy            string
    Running          bool
    httpClient       *http.Client
    AccessToken      string
    Timeout          time.Duration
    AutoClose        bool
    CloseDelay       time.Duration
    closeMu          sync.Mutex
    closeTimer       *time.Timer
    AutoRefresh      bool
    RefreshInterval  time.Duration
    rotateCancel     context.CancelFunc
    insecure         bool

    gemsCached       *GemJar
}

// NewGeminiClient creates a client. Pass empty strings to auto-detect via browser cookies (not implemented in Go port).
func NewGeminiClient(secure1psid string, secure1psidts string, proxy string, opts ...func(*GeminiClient)) *GeminiClient {
    c := &GeminiClient{
        Cookies:         map[string]string{},
        Proxy:           proxy,
        Running:         false,
        Timeout:         300 * time.Second,
        AutoClose:       false,
        CloseDelay:      300 * time.Second,
        AutoRefresh:     true,
        RefreshInterval: 540 * time.Second,
        insecure:        false,
    }
    if secure1psid != "" {
        c.Cookies["__Secure-1PSID"] = secure1psid
        if secure1psidts != "" {
            c.Cookies["__Secure-1PSIDTS"] = secure1psidts
        }
    }
    for _, f := range opts { f(c) }
    return c
}

// WithInsecureTLS sets skipping TLS verification (to mirror httpx verify=False)
func WithInsecureTLS(insecure bool) func(*GeminiClient) { return func(c *GeminiClient) { c.insecure = insecure } }

// Init initializes the access token and http client.
func (c *GeminiClient) Init(timeoutSec float64, autoClose bool, closeDelaySec float64, autoRefresh bool, refreshIntervalSec float64, verbose bool) error {
    // get access token
    token, validCookies, err := getAccessToken(c.Cookies, c.Proxy, verbose, c.insecure)
    if err != nil { c.Close(0); return err }
    c.AccessToken = token
    c.Cookies = validCookies

    tr := &http.Transport{}
    if c.Proxy != "" {
        if pu, err := url.Parse(c.Proxy); err == nil { tr.Proxy = http.ProxyURL(pu) }
    }
    if c.insecure {
        // set via roundtripper in utils_get_access_token for token; here we reuse via default Transport
        // intentionally not adding here, as requests rely on endpoints with normal TLS
    }
    c.httpClient = &http.Client{Transport: tr, Timeout: time.Duration(timeoutSec*float64(time.Second))}
    c.Running = true

    c.Timeout = time.Duration(timeoutSec * float64(time.Second))
    c.AutoClose = autoClose
    c.CloseDelay = time.Duration(closeDelaySec * float64(time.Second))
    if c.AutoClose { c.resetCloseTimer() }

    c.AutoRefresh = autoRefresh
    c.RefreshInterval = time.Duration(refreshIntervalSec * float64(time.Second))
    if c.AutoRefresh {
        c.startAutoRefresh()
    }
    if verbose { Success("Gemini client initialized successfully.") }
    return nil
}

func (c *GeminiClient) Close(delaySec float64) {
    if delaySec > 0 { time.Sleep(time.Duration(delaySec * float64(time.Second))) }
    c.Running = false
    c.closeMu.Lock()
    if c.closeTimer != nil { c.closeTimer.Stop(); c.closeTimer = nil }
    c.closeMu.Unlock()
    // Transport/client closed by GC; nothing explicit
    if c.rotateCancel != nil { c.rotateCancel(); c.rotateCancel = nil }
}

func (c *GeminiClient) resetCloseTimer() {
    c.closeMu.Lock()
    defer c.closeMu.Unlock()
    if c.closeTimer != nil { c.closeTimer.Stop(); c.closeTimer = nil }
    c.closeTimer = time.AfterFunc(c.CloseDelay, func(){ c.Close(0) })
}

func (c *GeminiClient) startAutoRefresh() {
    if c.rotateCancel != nil { c.rotateCancel() }
    ctx, cancel := context.WithCancel(context.Background())
    c.rotateCancel = cancel
    go func() {
        ticker := time.NewTicker(c.RefreshInterval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                newVal, err := rotate1psidts(c.Cookies, c.Proxy, c.insecure)
                if err != nil {
                    Warning("Failed to refresh cookies. Background auto refresh canceled: %v", err)
                    cancel(); return
                }
                if newVal != "" { c.Cookies["__Secure-1PSIDTS"] = newVal }
                Debug("Cookies refreshed. New __Secure-1PSIDTS: %s", newVal)
            }
        }
    }()
}

// ensureRunning mirrors the Python decorator behavior and retries on APIError.
func (c *GeminiClient) ensureRunning() error {
    if c.Running { return nil }
    return c.Init(float64(c.Timeout/time.Second), c.AutoClose, float64(c.CloseDelay/time.Second), c.AutoRefresh, float64(c.RefreshInterval/time.Second), false)
}

// GenerateContent sends a prompt (with optional files) and parses the response into ModelOutput.
func (c *GeminiClient) GenerateContent(prompt string, files []string, model Model, gem *Gem, chat *ChatSession) (ModelOutput, error) {
    var empty ModelOutput
    if prompt == "" { return empty, &ValueError{Msg: "Prompt cannot be empty."} }
    if err := c.ensureRunning(); err != nil { return empty, err }
    if c.AutoClose { c.resetCloseTimer() }

    // Retry wrapper similar to decorator (retry=2)
    retries := 2
    for {
        out, err := c.generateOnce(prompt, files, model, gem, chat)
        if err == nil { return out, nil }
        var apiErr *APIError
        var imgErr *ImageGenerationError
        shouldRetry := false
        if errors.As(err, &imgErr) {
            if retries > 1 { retries = 1 } // only once for image generation
            shouldRetry = true
        } else if errors.As(err, &apiErr) {
            shouldRetry = true
        }
        if shouldRetry && retries > 0 {
            time.Sleep(time.Second)
            retries--
            continue
        }
        return empty, err
    }
}

func (c *GeminiClient) generateOnce(prompt string, files []string, model Model, gem *Gem, chat *ChatSession) (ModelOutput, error) {
    var empty ModelOutput
    // Build f.req
    var uploaded [][]any
    for _, fp := range files {
        id, err := uploadFile(fp, c.Proxy, c.insecure)
        if err != nil { return empty, err }
        name, err := parseFileName(fp)
        if err != nil { return empty, err }
        uploaded = append(uploaded, []any{[]any{id}, name})
    }
    var item0 any
    if len(uploaded) > 0 {
        item0 = []any{prompt, 0, nil, uploaded}
    } else {
        item0 = []any{prompt}
    }
    var item2 any = nil
    if chat != nil { item2 = chat.Metadata() }

    inner := []any{item0, nil, item2}
    if gem != nil {
        // pad with 16 nils then gem ID
        for i := 0; i < 16; i++ { inner = append(inner, nil) }
        inner = append(inner, gem.ID)
    }
    innerJSON, _ := json.Marshal(inner)
    outer := []any{nil, string(innerJSON)}
    outerJSON, _ := json.Marshal(outer)

    // form
    form := url.Values{}
    form.Set("at", c.AccessToken)
    form.Set("f.req", string(outerJSON))

    req, _ := http.NewRequest(http.MethodPost, EndpointGenerate, strings.NewReader(form.Encode()))
    // headers
    for k, v := range HeadersGemini { for _, vv := range v { req.Header.Add(k, vv) } }
    for k, v := range model.ModelHeader { for _, vv := range v { req.Header.Add(k, vv) } }
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
    for k, v := range c.Cookies { req.AddCookie(&http.Cookie{Name: k, Value: v}) }

    resp, err := c.httpClient.Do(req)
    if err != nil { return empty, &TimeoutError{GeminiError{Msg: "Generate content request timed out."}} }
    defer resp.Body.Close()

    if resp.StatusCode == 429 {
        // Surface 429 as TemporarilyBlocked to match Python behavior
        c.Close(0)
        return empty, &TemporarilyBlocked{GeminiError{Msg: "Too many requests. IP temporarily blocked."}}
    }
    if resp.StatusCode != 200 {
        c.Close(0)
        return empty, &APIError{Msg: fmt.Sprintf("Failed to generate contents. Status %d", resp.StatusCode)}
    }

    // Read body and split lines; take the 3rd line (index 2)
    b, _ := io.ReadAll(resp.Body)
    parts := strings.Split(string(b), "\n")
    if len(parts) < 3 {
        c.Close(0)
        return empty, &APIError{Msg: "Invalid response data received."}
    }
    var responseJSON []any
    if err := json.Unmarshal([]byte(parts[2]), &responseJSON); err != nil {
        c.Close(0)
        return empty, &APIError{Msg: "Invalid response data received."}
    }

    // find body where main_part[4] exists
    var (
        body any
        bodyIndex int
    )
    for i, p := range responseJSON {
        arr, ok := p.([]any)
        if !ok || len(arr) < 3 { continue }
        s, ok := arr[2].(string); if !ok { continue }
        var mainPart []any
        if err := json.Unmarshal([]byte(s), &mainPart); err != nil { continue }
        if len(mainPart) > 4 && mainPart[4] != nil {
            body = mainPart
            bodyIndex = i
            break
        }
    }
    if body == nil {
        // Parse nested error code to align with Python mapping
        var top []any
        if err := json.Unmarshal([]byte(parts[2]), &top); err == nil && len(top) > 0 {
            if code, ok := extractErrorCode(top); ok {
                switch code {
                case ErrorUsageLimitExceeded:
                    return empty, &UsageLimitExceeded{GeminiError{Msg: fmt.Sprintf("Failed to generate contents. Usage limit of %s has exceeded. Please try switching to another model.", model.Name)}}
                case ErrorModelInconsistent:
                    return empty, &ModelInvalid{GeminiError{Msg: "Selected model is inconsistent or unavailable."}}
                case ErrorModelHeaderInvalid:
                    return empty, &APIError{Msg: "Invalid model header string. Please update the selected model header."}
                case ErrorIPTemporarilyBlocked:
                    return empty, &TemporarilyBlocked{GeminiError{Msg: "Too many requests. IP temporarily blocked."}}
                }
            }
        }
        Debug("Invalid response: %s", truncateForLog(string(b), 512))
        return empty, &APIError{Msg: "Failed to generate contents. Invalid response data received."}
    }

    bodyArr := body.([]any)
    // metadata
    var metadata []string
    if len(bodyArr) > 1 {
        if metaArr, ok := bodyArr[1].([]any); ok {
            for _, v := range metaArr { if s, ok := v.(string); ok { metadata = append(metadata, s) } }
        }
    }

    // candidates parsing
    candContainer, ok := bodyArr[4].([]any)
    if !ok { return empty, &APIError{Msg: "Failed to parse response body."} }
    candidates := make([]Candidate, 0, len(candContainer))
    reCard := regexp.MustCompile(`^http://googleusercontent\.com/card_content/\d+`)
    reGen := regexp.MustCompile(`http://googleusercontent\.com/image_generation_content/\d+`)

    for ci, candAny := range candContainer {
        cArr, ok := candAny.([]any); if !ok { continue }
        // text: cArr[1][0]
        var text string
        if len(cArr) > 1 {
            if sArr, ok := cArr[1].([]any); ok && len(sArr) > 0 {
                text, _ = sArr[0].(string)
            }
        }
        if reCard.MatchString(text) {
            // candidate[22] and candidate[22][0] or text
            if len(cArr) > 22 { if arr, ok := cArr[22].([]any); ok && len(arr) > 0 { if s, ok := arr[0].(string); ok { text = s } } }
        }

        // thoughts: candidate[37][0][0]
        var thoughts *string
        if len(cArr) > 37 {
            if a, ok := cArr[37].([]any); ok && len(a) > 0 {
                if b, ok := a[0].([]any); ok && len(b) > 0 {
                    if s, ok := b[0].(string); ok { ss := decodeHTML(s); thoughts = &ss }
                }
            }
        }

        // web images: candidate[12][1]
        webImages := []WebImage{}
        var imgSection any
        if len(cArr) > 12 { imgSection = cArr[12] }
        if arr, ok := imgSection.([]any); ok && len(arr) > 1 {
            if imagesArr, ok := arr[1].([]any); ok {
                for _, wiAny := range imagesArr {
                    wiArr, ok := wiAny.([]any); if !ok { continue }
                    // url: wiArr[0][0][0], title: wiArr[7][0], alt: wiArr[0][4]
                    var urlStr, title, alt string
                    if len(wiArr) > 0 {
                        if a, ok := wiArr[0].([]any); ok && len(a) > 0 {
                            if b, ok := a[0].([]any); ok && len(b) > 0 {
                                urlStr, _ = b[0].(string)
                            }
                            if len(a) > 4 { if s, ok := a[4].(string); ok { alt = s } }
                        }
                    }
                    if len(wiArr) > 7 {
                        if a, ok := wiArr[7].([]any); ok && len(a) > 0 {
                            title, _ = a[0].(string)
                        }
                    }
                    webImages = append(webImages, WebImage{Image: Image{URL: urlStr, Title: title, Alt: alt, Proxy: c.Proxy}})
                }
            }
        }

        // generated images
        genImages := []GeneratedImage{}
        hasGen := false
        if arr, ok := imgSection.([]any); ok && len(arr) > 7 {
            if a, ok := arr[7].([]any); ok && len(a) > 0 && a[0] != nil {
                hasGen = true
            }
        }
        if hasGen {
            // find img part
            var imgBody []any
            for pi := bodyIndex; pi < len(responseJSON); pi++ {
                part := responseJSON[pi]
                arr, ok := part.([]any); if !ok || len(arr) < 3 { continue }
                s, ok := arr[2].(string); if !ok { continue }
                var mp []any
                if err := json.Unmarshal([]byte(s), &mp); err != nil { continue }
                if len(mp) > 4 {
                    if tt, ok := mp[4].([]any); ok && len(tt) > ci {
                        if sec, ok := tt[ci].([]any); ok && len(sec) > 12 {
                            if ss, ok := sec[12].([]any); ok && len(ss) > 7 {
                                if first, ok := ss[7].([]any); ok && len(first) > 0 && first[0] != nil {
                                    imgBody = mp
                                    break
                                }
                            }
                        }
                    }
                }
            }
            if imgBody == nil {
                return empty, &ImageGenerationError{APIError{Msg: "Failed to parse generated images."}}
            }
            imgCand := imgBody[4].([]any)[ci].([]any)
            if len(imgCand) > 1 {
                if a, ok := imgCand[1].([]any); ok && len(a) > 0 {
                    if s, ok := a[0].(string); ok {
                        text = strings.TrimSpace(reGen.ReplaceAllString(s, ""))
                    }
                }
            }
            // images list at imgCand[12][7][0]
            if len(imgCand) > 12 {
                if s1, ok := imgCand[12].([]any); ok && len(s1) > 7 {
                    if s2, ok := s1[7].([]any); ok && len(s2) > 0 {
                        if s3, ok := s2[0].([]any); ok {
                            for ii, giAny := range s3 {
                                ga, ok := giAny.([]any); if !ok || len(ga) < 4 { continue }
                                // url: ga[0][3][3]
                                var urlStr, title, alt string
                                if a, ok := ga[0].([]any); ok && len(a) > 3 {
                                    if b, ok := a[3].([]any); ok && len(b) > 3 {
                                        urlStr, _ = b[3].(string)
                                    }
                                }
                                // title from ga[3][6]
                                if len(ga) > 3 {
                                    if a, ok := ga[3].([]any); ok {
                                        if len(a) > 6 {
                                            if v, ok := a[6].(float64); ok && v != 0 {
                                                title = fmt.Sprintf("[Generated Image %.0f]", v)
                                            } else { title = "[Generated Image]" }
                                        } else { title = "[Generated Image]" }
                                        // alt from ga[3][5][ii] fallback
                                        if len(a) > 5 {
                                            if tt, ok := a[5].([]any); ok {
                                                if ii < len(tt) {
                                                    if s, ok := tt[ii].(string); ok { alt = s }
                                                } else if len(tt) > 0 {
                                                    if s, ok := tt[0].(string); ok { alt = s }
                                                }
                                            }
                                        }
                                    }
                                }
                                genImages = append(genImages, GeneratedImage{Image: Image{URL: urlStr, Title: title, Alt: alt, Proxy: c.Proxy}, Cookies: c.Cookies})
                            }
                        }
                    }
                }
            }
        }

        cand := Candidate{
            RCID:            fmt.Sprintf("%v", cArr[0]),
            Text:            decodeHTML(text),
            Thoughts:        thoughts,
            WebImages:       webImages,
            GeneratedImages: genImages,
        }
        candidates = append(candidates, cand)
    }

    if len(candidates) == 0 {
        return empty, &GeminiError{Msg: "Failed to generate contents. No output data found in response."}
    }
    output := ModelOutput{Metadata: metadata, Candidates: candidates, Chosen: 0}
    if chat != nil { chat.lastOutput = &output }
    return output, nil
}

// extractErrorCode attempts to navigate the known nested error structure and fetch the integer code.
// Mirrors Python path: response_json[0][5][2][0][1][0]
func extractErrorCode(top []any) (int, bool) {
    if len(top) == 0 { return 0, false }
    a, ok := top[0].([]any); if !ok || len(a) <= 5 { return 0, false }
    b, ok := a[5].([]any); if !ok || len(b) <= 2 { return 0, false }
    c, ok := b[2].([]any); if !ok || len(c) == 0 { return 0, false }
    d, ok := c[0].([]any); if !ok || len(d) <= 1 { return 0, false }
    e, ok := d[1].([]any); if !ok || len(e) == 0 { return 0, false }
    f, ok := e[0].(float64); if !ok { return 0, false }
    return int(f), true
}

// truncateForLog returns a shortened string for logging
func truncateForLog(s string, n int) string {
    if n <= 0 || len(s) <= n { return s }
    return s[:n]
}

// StartChat returns a ChatSession attached to the client
func (c *GeminiClient) StartChat(model Model, gem *Gem, metadata []string) *ChatSession {
    return &ChatSession{client: c, metadata: normalizeMeta(metadata), model: model, gem: gem}
}

// batchExecute executes a batch of RPCs
func (c *GeminiClient) batchExecute(payloads []RPCData) (*http.Response, error) {
    if err := c.ensureRunning(); err != nil { return nil, err }
    arr := make([][]any, 0, len(payloads))
    for _, p := range payloads { arr = append(arr, p.Serialize()) }
    outer := []any{arr}
    b, _ := json.Marshal(outer)
    form := url.Values{}
    form.Set("at", c.AccessToken)
    form.Set("f.req", string(b))

    req, _ := http.NewRequest(http.MethodPost, EndpointBatchExec, strings.NewReader(form.Encode()))
    for k, v := range HeadersGemini { for _, vv := range v { req.Header.Add(k, vv) } }
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
    for k, v := range c.Cookies { req.AddCookie(&http.Cookie{Name: k, Value: v}) }

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, &TimeoutError{GeminiError{Msg: "Batch execute request timed out."}}
    }
    if resp.StatusCode != 200 {
        c.Close(0)
        return nil, &APIError{Msg: fmt.Sprintf("Batch execution failed with status code %d", resp.StatusCode)}
    }
    return resp, nil
}

// ChatSession holds conversation metadata
type ChatSession struct {
    client     *GeminiClient
    metadata   []string // cid, rid, rcid
    lastOutput *ModelOutput
    model      Model
    gem        *Gem
}

func (cs *ChatSession) String() string {
    var cid, rid, rcid string
    if len(cs.metadata) > 0 { cid = cs.metadata[0] }
    if len(cs.metadata) > 1 { rid = cs.metadata[1] }
    if len(cs.metadata) > 2 { rcid = cs.metadata[2] }
    return fmt.Sprintf("ChatSession(cid='%s', rid='%s', rcid='%s')", cid, rid, rcid)
}

func normalizeMeta(v []string) []string {
    out := []string{"", "", ""}
    for i := 0; i < len(v) && i < 3; i++ { out[i] = v[i] }
    return out
}

func (cs *ChatSession) Metadata() []string { return cs.metadata }
func (cs *ChatSession) SetMetadata(v []string) { cs.metadata = normalizeMeta(v) }
func (cs *ChatSession) CID() string { if len(cs.metadata)>0 { return cs.metadata[0] }; return "" }
func (cs *ChatSession) RID() string { if len(cs.metadata)>1 { return cs.metadata[1] }; return "" }
func (cs *ChatSession) RCID() string { if len(cs.metadata)>2 { return cs.metadata[2] }; return "" }
func (cs *ChatSession) setCID(v string)  { if len(cs.metadata)<1 { cs.metadata = normalizeMeta(cs.metadata) }; cs.metadata[0] = v }
func (cs *ChatSession) setRID(v string)  { if len(cs.metadata)<2 { cs.metadata = normalizeMeta(cs.metadata) }; cs.metadata[1] = v }
func (cs *ChatSession) setRCID(v string) { if len(cs.metadata)<3 { cs.metadata = normalizeMeta(cs.metadata) }; cs.metadata[2] = v }

// SendMessage shortcut to client's GenerateContent
func (cs *ChatSession) SendMessage(prompt string, files []string) (ModelOutput, error) {
    out, err := cs.client.GenerateContent(prompt, files, cs.model, cs.gem, cs)
    if err == nil {
        cs.lastOutput = &out
        cs.SetMetadata(out.Metadata)
        cs.setRCID(out.RCID())
    }
    return out, err
}

// ChooseCandidate selects a candidate from last output and updates rcid
func (cs *ChatSession) ChooseCandidate(index int) (ModelOutput, error) {
    if cs.lastOutput == nil { return ModelOutput{}, &ValueError{Msg: "No previous output data found in this chat session."} }
    if index >= len(cs.lastOutput.Candidates) { return ModelOutput{}, &ValueError{Msg: fmt.Sprintf("Index %d exceeds candidates", index)} }
    cs.lastOutput.Chosen = index
    cs.setRCID(cs.lastOutput.RCID())
    return *cs.lastOutput, nil
}
