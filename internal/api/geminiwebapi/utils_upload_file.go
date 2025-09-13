package geminiwebapi

import (
    "bytes"
    "crypto/tls"
    "io"
    "mime/multipart"
    "net/http"
    "net/url"
    "os"
    "path/filepath"
    "time"
)

// uploadFile uploads a file and returns the server identifier string.
func uploadFile(path string, proxy string, insecure bool) (string, error) {
    f, err := os.Open(path)
    if err != nil { return "", err }
    defer f.Close()

    var buf bytes.Buffer
    mw := multipart.NewWriter(&buf)
    fw, err := mw.CreateFormFile("file", filepath.Base(path))
    if err != nil { return "", err }
    if _, err := io.Copy(fw, f); err != nil { return "", err }
    _ = mw.Close()

    tr := &http.Transport{}
    if proxy != "" { if pu, err := url.Parse(proxy); err == nil { tr.Proxy = http.ProxyURL(pu) } }
    if insecure { tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} }
    client := &http.Client{Transport: tr, Timeout: 300 * time.Second}

    req, _ := http.NewRequest(http.MethodPost, EndpointUpload, &buf)
    for k, v := range HeadersUpload { for _, vv := range v { req.Header.Add(k, vv) } }
    req.Header.Set("Content-Type", mw.FormDataContentType())
    req.Header.Set("Accept", "*/*")
    req.Header.Set("Connection", "keep-alive")

    resp, err := client.Do(req)
    if err != nil { return "", err }
    defer resp.Body.Close()
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return "", &APIError{Msg: resp.Status}
    }
    b, err := io.ReadAll(resp.Body)
    if err != nil { return "", err }
    return string(b), nil
}

// parseFileName returns the base filename for a given path and ensures file exists.
func parseFileName(path string) (string, error) {
    if st, err := os.Stat(path); err != nil || st.IsDir() {
        return "", &ValueError{Msg: path + " is not a valid file."}
    }
    return filepath.Base(path), nil
}

