package geminiwebapi

import (
    "encoding/json"
    "io"
    "strings"
)

// FetchGems queries predefined and custom gems and caches them on the client
func (c *GeminiClient) FetchGems(includeHidden bool) (GemJar, error) {
    if err := c.ensureRunning(); err != nil { return nil, err }
    sysPayload := "[3]"
    if includeHidden { sysPayload = "[4]" }
    resp, err := c.batchExecute([]RPCData{
        {RPCID: GRPCListGems, Payload: sysPayload, Identifier: "system"},
        {RPCID: GRPCListGems, Payload: "[2]", Identifier: "custom"},
    })
    if err != nil { return nil, err }
    defer resp.Body.Close()

    b, _ := io.ReadAll(resp.Body)
    lines := strings.Split(string(b), "\n")
    if len(lines) < 3 { c.Close(0); return nil, &APIError{Msg: "Failed to fetch gems. Invalid response"} }
    var top []any
    if err := json.Unmarshal([]byte(lines[2]), &top); err != nil {
        c.Close(0); return nil, &APIError{Msg: "Failed to fetch gems. Invalid response"}
    }
    var predefined, custom []any
    for _, part := range top {
        arr, ok := part.([]any); if !ok || len(arr) < 3 { continue }
        tag := arr[len(arr)-1]
        s, ok := arr[2].(string); if !ok { continue }
        if tag == "system" {
            var sys []any
            if err := json.Unmarshal([]byte(s), &sys); err == nil && len(sys) > 2 {
                if a, ok := sys[2].([]any); ok { predefined = a }
            }
        } else if tag == "custom" {
            var cu []any
            if err := json.Unmarshal([]byte(s), &cu); err == nil && len(cu) > 2 {
                if a, ok := cu[2].([]any); ok { custom = a }
            }
        }
    }
    if len(predefined) == 0 && len(custom) == 0 {
        c.Close(0); return nil, &APIError{Msg: "Failed to fetch gems. Invalid response data received."}
    }

    jar := GemJar{}
    // helper to parse entries
    parseList := func(list []any, predefinedFlag bool) {
        for _, gi := range list {
            gArr, ok := gi.([]any); if !ok || len(gArr) < 3 { continue }
            id, _ := gArr[0].(string)
            var name, desc *string
            if a, ok := gArr[1].([]any); ok {
                if len(a) > 0 { if s, ok := a[0].(string); ok { name = &s } }
                if len(a) > 1 { if s, ok := a[1].(string); ok { desc = &s } }
            }
            var prompt *string
            if p, ok := gArr[2].([]any); ok && len(p) > 0 {
                if s, ok := p[0].(string); ok { prompt = &s }
            }
            nm := ""; if name != nil { nm = *name }
            jar[id] = Gem{ID: id, Name: nm, Description: desc, Prompt: prompt, Predefined: predefinedFlag}
        }
    }
    parseList(predefined, true)
    parseList(custom, false)

    c.gemsCached = &jar
    return jar, nil
}

func (c *GeminiClient) Gems() (GemJar, error) {
    if c.gemsCached == nil { return nil, &APIError{Msg: "Gems not fetched yet."} }
    return *c.gemsCached, nil
}

// CreateGem creates a custom gem
func (c *GeminiClient) CreateGem(name string, prompt string, description string) (Gem, error) {
    payload := []any{
        []any{
            nil,
            nil,
            []any{
                name,
                description,
                prompt,
                nil, nil, nil, nil, nil,
                0,
                nil,
                1,
                nil, nil, nil,
                []any{},
            },
        },
    }
    b, _ := json.Marshal(payload)
    resp, err := c.batchExecute([]RPCData{{RPCID: GRPCCreateGem, Payload: string(b)}})
    if err != nil { return Gem{}, err }
    defer resp.Body.Close()
    raw, _ := io.ReadAll(resp.Body)
    lines := strings.Split(string(raw), "\n")
    if len(lines) < 3 { c.Close(0); return Gem{}, &APIError{Msg: "Failed to create gem. Invalid response"} }
    var top []any
    if err := json.Unmarshal([]byte(lines[2]), &top); err != nil { c.Close(0); return Gem{}, &APIError{Msg: "Failed to create gem. Invalid response data"} }
    if len(top) == 0 { c.Close(0); return Gem{}, &APIError{Msg: "Failed to create gem. Empty response"} }
    arr, _ := top[0].([]any)
    s, _ := arr[2].(string)
    var inner []any
    if err := json.Unmarshal([]byte(s), &inner); err != nil || len(inner) == 0 { c.Close(0); return Gem{}, &APIError{Msg: "Failed to create gem. Invalid response data"} }
    id, _ := inner[0].(string)
    d := description; p := prompt
    return Gem{ID: id, Name: name, Description: &d, Prompt: &p, Predefined: false}, nil
}

// UpdateGem updates an existing custom gem
func (c *GeminiClient) UpdateGem(gemID string, name string, prompt string, description string) (Gem, error) {
    payload := []any{
        gemID,
        []any{
            name,
            description,
            prompt,
            nil, nil, nil, nil, nil,
            0,
            nil,
            1,
            nil, nil, nil,
            []any{},
            0,
        },
    }
    b, _ := json.Marshal(payload)
    if _, err := c.batchExecute([]RPCData{{RPCID: GRPCUpdateGem, Payload: string(b)}}); err != nil { return Gem{}, err }
    d := description; p := prompt
    return Gem{ID: gemID, Name: name, Description: &d, Prompt: &p, Predefined: false}, nil
}

// DeleteGem deletes a custom gem
func (c *GeminiClient) DeleteGem(gemID string) error {
    b, _ := json.Marshal([]any{gemID})
    _, err := c.batchExecute([]RPCData{{RPCID: GRPCDeleteGem, Payload: string(b)}})
    return err
}
