package geminiwebapi

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Image struct {
	URL   string
	Title string
	Alt   string
	Proxy string
}

func (i Image) String() string {
	short := i.URL
	if len(short) > 20 {
		short = short[:8] + "..." + short[len(short)-12:]
	}
	return fmt.Sprintf("Image(title='%s', alt='%s', url='%s')", i.Title, i.Alt, short)
}

func (i Image) Save(path string, filename string, cookies map[string]string, verbose bool, skipInvalidFilename bool, insecure bool) (string, error) {
	if filename == "" {
		// try to parse filename from URL
		u := i.URL
		if p := strings.Split(u, "/"); len(p) > 0 {
			filename = p[len(p)-1]
		}
		if q := strings.Split(filename, "?"); len(q) > 0 {
			filename = q[0]
		}
	}
	// Regex validation (align with Python: ^(.*\.\w+)) to extract name with extension
	if filename != "" {
		re := regexp.MustCompile(`^(.*\.\w+)`)
		if m := re.FindStringSubmatch(filename); len(m) >= 2 {
			filename = m[1]
		} else {
			if verbose {
				Warning("Invalid filename: %s", filename)
			}
			if skipInvalidFilename {
				return "", nil
			}
		}
	}
	// build client with cookie jar so cookies persist across redirects
	tr := &http.Transport{}
	if i.Proxy != "" {
		if pu, err := url.Parse(i.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: tr, Timeout: 120 * time.Second, Jar: jar}

	// optionally allow a few more redirects than default
	// helper to set raw Cookie header using provided cookies (to mirror Python client behavior)
	buildCookieHeader := func(m map[string]string) string {
		if len(m) == 0 {
			return ""
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
		}
		return strings.Join(parts, "; ")
	}
	rawCookie := buildCookieHeader(cookies)

	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// Ensure provided cookies are always sent across redirects (domain-agnostic)
		if rawCookie != "" {
			req.Header.Set("Cookie", rawCookie)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}

	req, _ := http.NewRequest(http.MethodGet, i.URL, nil)
	// align only minimal headers; Python version does not set UA/Referer for image fetch
	if rawCookie != "" {
		req.Header.Set("Cookie", rawCookie)
	}
	// add some browser-like headers to improve compatibility
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Connection", "keep-alive")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Error downloading image: %d %s", resp.StatusCode, resp.Status)
	}
	// Warn if content-type is not image (align with Python behavior)
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "image") {
		Warning("Content type of %s is not image, but %s.", filename, ct)
	}
	// ensure dir
	if path == "" {
		path = "temp"
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(path, filename)
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, resp.Body)
	_ = f.Close()
	if err != nil {
		return "", err
	}
	if verbose {
		Info("Image saved as %s", dest)
	}
	abspath, _ := filepath.Abs(dest)
	return abspath, nil
}

type WebImage struct{ Image }

type GeneratedImage struct {
	Image
	Cookies map[string]string
}

func (g GeneratedImage) Save(path string, filename string, fullSize bool, verbose bool, skipInvalidFilename bool, insecure bool) (string, error) {
	if len(g.Cookies) == 0 {
		return "", &ValueError{Msg: "GeneratedImage requires cookies."}
	}
	url := g.URL
	if fullSize {
		url = url + "=s2048"
	}
	// default filename if empty: timestamp + end of the hash .png
	if filename == "" {
		name := time.Now().Format("20060102150405")
		if len(url) >= 10 {
			name = fmt.Sprintf("%s_%s.png", name, url[len(url)-10:])
		} else {
			name += ".png"
		}
		filename = name
	}
	tmp := g.Image
	tmp.URL = url
	return tmp.Save(path, filename, g.Cookies, verbose, skipInvalidFilename, insecure)
}
