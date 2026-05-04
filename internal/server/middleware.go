package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SessionCookieName is the HTTP cookie webconfig sets after a successful
// login or setup. HttpOnly + SameSite=Strict + (no Secure for LAN-HTTP).
const SessionCookieName = "airplanes-webconfig-session"

// JSONBodyLimit caps incoming JSON request bodies. None of webconfig's
// endpoints need more than a few hundred bytes; 1 KiB is well above
// realistic password lengths and gives no headroom to memory-exhaustion
// flooders.
const JSONBodyLimit = 1024

// writeJSON writes v as the response body with proper headers and a
// Cache-Control: no-store directive (auth/state responses must never be
// cached).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError sends {"error": message}.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// readJSON enforces JSONBodyLimit, requires a JSON Content-Type, rejects
// unknown fields, and rejects multi-document bodies.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return errBadContentType
	}
	r.Body = http.MaxBytesReader(w, r.Body, JSONBodyLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errExtraJSON
	}
	return nil
}

var (
	errBadContentType = errors.New("Content-Type must be application/json")
	errExtraJSON      = errors.New("body contains more than one JSON value")
)

func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	mediaType, _, _ := strings.Cut(ct, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "application/json")
}

// requireOriginMatchesHost is middleware enforced on every mutating method
// (POST/PUT/PATCH/DELETE). Defense-in-depth atop SameSite=Strict cookies.
func requireOriginMatchesHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !originMatchesHost(r.Header.Get("Origin"), r.Host) {
			writeJSONError(w, http.StatusForbidden, "origin check failed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originMatchesHost(origin, host string) bool {
	if origin == "" || origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return normalizeHostPort(u.Host, u.Scheme) == normalizeHostPort(host, u.Scheme)
}

func normalizeHostPort(hostport, scheme string) string {
	hostport = strings.ToLower(hostport)
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	defaultPort := ""
	switch scheme {
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	}
	if port == defaultPort {
		return host
	}
	return net.JoinHostPort(host, port)
}

// securityHeaders sets cross-cutting headers on every response. CSP is tight
// because the SPA only references self-hosted assets — no inline scripts,
// no external resources.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; "+
				"img-src 'self' data:; connect-src 'self'; "+
				"frame-ancestors 'none'; object-src 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// readSessionToken returns the session token from the cookie or "" if absent.
func readSessionToken(r *http.Request) string {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return c.Value
}

// setSessionCookie issues a session cookie expiring at `expires`. Browser-
// side TTL is kept in sync with server-side sliding TTL by re-emitting the
// cookie on every successful Validate (see requireSession).
func setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// requireSession enforces a valid session cookie on protected routes. On
// success the cookie's Max-Age is refreshed so the browser-side expiry stays
// aligned with the server-side sliding TTL.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := readSessionToken(r)
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "auth required")
			return
		}
		expires, err := s.sessions.Validate(token)
		if err != nil {
			clearSessionCookie(w)
			writeJSONError(w, http.StatusUnauthorized, "auth required")
			return
		}
		setSessionCookie(w, token, expires)
		next.ServeHTTP(w, r)
	}
}
