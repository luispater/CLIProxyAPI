package geminiwebapi

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type httpOptions struct {
	ProxyURL        string
	Insecure        bool
	FollowRedirects bool
}

func newHTTPClient(opts httpOptions) *http.Client {
	transport := &http.Transport{}
	if opts.ProxyURL != "" {
		if pu, err := url.Parse(opts.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	if opts.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, Jar: jar}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client
}

func applyHeaders(req *http.Request, headers http.Header) {
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
}

func applyCookies(req *http.Request, cookies map[string]string) {
	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
}

func sendInitRequest(cookies map[string]string, proxy string, insecure bool) (*http.Response, map[string]string, error) {
	client := newHTTPClient(httpOptions{ProxyURL: proxy, Insecure: insecure, FollowRedirects: true})
	req, _ := http.NewRequest(http.MethodGet, EndpointInit, nil)
	applyHeaders(req, HeadersGemini)
	applyCookies(req, cookies)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil, &AuthError{Msg: resp.Status}
	}
	// collect cookies back
	outCookies := map[string]string{}
	for _, c := range resp.Cookies() {
		outCookies[c.Name] = c.Value
	}
	// preserve provided cookies too
	for k, v := range cookies {
		outCookies[k] = v
	}
	return resp, outCookies, nil
}

func getAccessToken(baseCookies map[string]string, proxy string, verbose bool, insecure bool) (string, map[string]string, error) {
	// Warm-up google.com to gain extra cookies (NID, etc.) and capture them
	extraCookies := map[string]string{}
	{
		client := newHTTPClient(httpOptions{ProxyURL: proxy, Insecure: insecure, FollowRedirects: true})
		req, _ := http.NewRequest(http.MethodGet, EndpointGoogle, nil)
		resp, _ := client.Do(req)
		if resp != nil {
			// collect cookies from client jar for google.com
			if u, err := url.Parse(EndpointGoogle); err == nil {
				for _, c := range client.Jar.Cookies(u) {
					extraCookies[c.Name] = c.Value
				}
			}
			_ = resp.Body.Close()
		}
	}

	trySets := make([]map[string]string, 0, 8)

	// base cookies
	if v1, ok1 := baseCookies["__Secure-1PSID"]; ok1 {
		if v2, ok2 := baseCookies["__Secure-1PSIDTS"]; ok2 {
			merged := map[string]string{"__Secure-1PSID": v1, "__Secure-1PSIDTS": v2}
			// include NID if present
            if nid, ok := baseCookies["NID"]; ok { merged["NID"] = nid }
			trySets = append(trySets, merged)
		} else if verbose {
			Debug("Skipping base cookies: __Secure-1PSIDTS missing")
		}
	}

	// cached cookies
	cacheDir := "temp"
	_ = os.MkdirAll(cacheDir, 0o755)
	if v1, ok1 := baseCookies["__Secure-1PSID"]; ok1 {
		cacheFile := filepath.Join(cacheDir, ".cached_1psidts_"+v1+".txt")
		if b, err := os.ReadFile(cacheFile); err == nil {
			cv := strings.TrimSpace(string(b))
			if cv != "" {
				m := map[string]string{"__Secure-1PSID": v1, "__Secure-1PSIDTS": cv}
				trySets = append(trySets, m)
			} else if verbose {
				Debug("Cached cookie file empty: %s", cacheFile)
			}
		} else if verbose {
			Debug("No cached cookie file for %s", v1)
		}
	} else {
		// try all caches
		entries, _ := os.ReadDir(cacheDir)
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".cached_1psidts_") && strings.HasSuffix(name, ".txt") {
				v1 := name[len(".cached_1psidts_") : len(name)-len(".txt")]
				if b, err := os.ReadFile(filepath.Join(cacheDir, name)); err == nil {
					cv := strings.TrimSpace(string(b))
					if cv != "" {
						m := map[string]string{"__Secure-1PSID": v1, "__Secure-1PSIDTS": cv}
						trySets = append(trySets, m)
					}
				}
			}
		}
	}

	// browser cookies (not implemented; kept for parity)
	if bc := loadBrowserCookies("google.com", verbose); len(bc) > 0 {
		if v1, ok1 := bc["__Secure-1PSID"]; ok1 {
			m := map[string]string{"__Secure-1PSID": v1}
            if v2, ok2 := bc["__Secure-1PSIDTS"]; ok2 { m["__Secure-1PSIDTS"] = v2 }
            if nid, ok := bc["NID"]; ok { m["NID"] = nid }
			trySets = append(trySets, m)
		} else if verbose {
			Debug("Skipping local browser cookies: __Secure-1PSID missing")
		}
	}

	re := regexp.MustCompile(`"SNlM0e":"(.*?)"`)

	// Attempt sequentially, merging extra google.com cookies into each try set
	for i, cset := range trySets {
		// merge cookies: extraCookies first, then specific try-set overrides (same as Python {**extra, **base})
		merged := map[string]string{}
        for k, v := range extraCookies { merged[k] = v }
        for k, v := range cset { merged[k] = v }

		resp, used, err := sendInitRequest(merged, proxy, insecure)
		if err != nil {
            if verbose { Debug("Init attempt (%d/%d) failed: %v", i+1, len(trySets), err) }
			continue
		}
		// read full body (Python searches entire response.text)
		all, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		body := string(all)
		if m := re.FindStringSubmatch(body); len(m) >= 2 {
            if verbose { Debug("Init attempt (%d/%d) succeeded.", i+1, len(trySets)) }
			return m[1], used, nil
		}
        if verbose { Debug("Init attempt (%d/%d) failed: token not found", i+1, len(trySets)) }
	}

	return "", nil, &AuthError{Msg: "Failed to initialize client. SECURE_1PSIDTS may be expired."}
}
