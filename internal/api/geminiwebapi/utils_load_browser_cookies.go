package geminiwebapi

// loadBrowserCookies attempts to load cookies from local browsers.
// In this Go port, we do not depend on external browser cookie libraries; return empty.
func loadBrowserCookies(domainName string, verbose bool) map[string]string {
    // Intentionally unimplemented. Python version uses optional browser-cookie3.
    return map[string]string{}
}

