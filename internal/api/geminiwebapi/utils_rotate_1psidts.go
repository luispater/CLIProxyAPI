package geminiwebapi

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rotate1psidts refreshes __Secure-1PSIDTS and caches it locally.
func rotate1psidts(cookies map[string]string, proxy string, insecure bool) (string, error) {
	psid, ok := cookies["__Secure-1PSID"]
    if !ok { return "", &AuthError{Msg: "__Secure-1PSID missing"} }

	cacheDir := "temp"
	_ = os.MkdirAll(cacheDir, 0o755)
	cacheFile := filepath.Join(cacheDir, ".cached_1psidts_"+psid+".txt")

	// avoid hammering within a minute
	if st, err := os.Stat(cacheFile); err == nil {
		if time.Since(st.ModTime()) <= time.Minute {
			if b, err := os.ReadFile(cacheFile); err == nil {
				v := strings.TrimSpace(string(b))
                if v != "" { return v, nil }
			}
		}
	}

	tr := &http.Transport{}
	if proxy != "" {
        if pu, err := url.Parse(proxy); err == nil { tr.Proxy = http.ProxyURL(pu) }
	}
    if insecure { tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} }
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second}

	req, _ := http.NewRequest(http.MethodPost, EndpointRotateCookies, io.NopCloser(stringsReader("[000,\"-0000000000000000000\"]")))
	applyHeaders(req, HeadersRotateCookies)
	applyCookies(req, cookies)

	resp, err := client.Do(req)
    if err != nil { return "", err }
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return "", &AuthError{Msg: "unauthorized"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New(resp.Status)
	}

	for _, c := range resp.Cookies() {
		if c.Name == "__Secure-1PSIDTS" {
			_ = os.WriteFile(cacheFile, []byte(c.Value), 0o644)
			return c.Value, nil
		}
	}
	return "", nil
}

// Minimal reader helpers to avoid importing strings everywhere
type constReader struct{ s string; i int }
func (r *constReader) Read(p []byte) (int, error) {
    if r.i >= len(r.s) { return 0, io.EOF }
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}
func stringsReader(s string) io.Reader { return &constReader{s: s} }
