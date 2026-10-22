package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Single-operator authentication. One secret, ADMIN_ACCESS_KEY, is typed once on the login page;
// the server answers with an opaque random session token whose record lives in Redis. The key
// itself never reaches the browser beyond that one form post, is never stored client-side and is
// never sent with ordinary requests.
//
// Server-side sessions rather than a signed cookie: Redis is already a hard dependency, and a
// stored session can actually be revoked — logout deletes it, and rotating the key invalidates
// every session at once because each record carries a fingerprint of the key that created it.

const (
	// MinAdminKeyLength refuses a key short enough to be guessed or typed from memory.
	MinAdminKeyLength = 32

	sessionCookieSecure = "__Host-prospector_session"
	sessionCookiePlain  = "prospector_session"
	sessionKeyPrefix    = "auth:session:"

	// Failed logins are counted globally, not per address: there is exactly one legitimate
	// operator, and behind a tunnel or reverse proxy every request can share one source address.
	loginFailureKey    = "auth:login_failures"
	loginFailureLimit  = 5
	loginFailureWindow = 15 * time.Minute

	defaultSessionTTL = 7 * 24 * time.Hour
)

// sessionStore is the slice of Redis that authentication uses, kept narrow so tests can supply
// an in-memory store.
type sessionStore interface {
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// Get returns "" with a nil error when the key does not exist.
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, key string) error
	// Incr increments a counter, starting its expiry window on the first increment.
	Incr(ctx context.Context, key string, window time.Duration) (int64, error)
}

type redisSessionStore struct{ client *redis.Client }

func (s redisSessionStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

func (s redisSessionStore) Get(ctx context.Context, key string) (string, error) {
	value, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

func (s redisSessionStore) Del(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

func (s redisSessionStore) Incr(ctx context.Context, key string, window time.Duration) (int64, error) {
	count, err := s.client.Incr(ctx, key).Result()
	if err == nil && count == 1 {
		err = s.client.Expire(ctx, key, window).Err()
	}
	return count, err
}

// Auth guards every application route behind the admin key.
type Auth struct {
	keyHash     [32]byte
	fingerprint string
	store       sessionStore
	ttl         time.Duration
	secure      bool
}

// AuthFromEnv reads ADMIN_ACCESS_KEY, SESSION_TTL_HOURS and COOKIE_SECURE. A missing or weak key,
// or a setting that does not parse, is an error: startup refuses rather than running an
// application that is open, or protected differently from what the configuration says.
func AuthFromEnv(client *redis.Client) (*Auth, error) {
	ttl := defaultSessionTTL
	if v := strings.TrimSpace(os.Getenv("SESSION_TTL_HOURS")); v != "" {
		hours, err := strconv.Atoi(v)
		if err != nil || hours <= 0 {
			return nil, fmt.Errorf("SESSION_TTL_HOURS must be a positive whole number of hours")
		}
		ttl = time.Duration(hours) * time.Hour
	}
	secure := true
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COOKIE_SECURE"))) {
	case "", "true":
	case "false":
		secure = false
	default:
		return nil, fmt.Errorf("COOKIE_SECURE must be true or false")
	}
	return newAuth(redisSessionStore{client}, os.Getenv("ADMIN_ACCESS_KEY"), ttl, secure)
}

func newAuth(store sessionStore, key string, ttl time.Duration, secure bool) (*Auth, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("ADMIN_ACCESS_KEY is not set")
	}
	if len(key) < MinAdminKeyLength {
		return nil, fmt.Errorf("ADMIN_ACCESS_KEY must be at least %d characters (generate one with: openssl rand -base64 48)", MinAdminKeyLength)
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("prospector-session-v1"))
	return &Auth{
		keyHash:     sha256.Sum256([]byte(key)),
		fingerprint: hex.EncodeToString(mac.Sum(nil)),
		store:       store,
		ttl:         ttl,
		secure:      secure,
	}, nil
}

func (a *Auth) cookieName() string {
	// The __Host- prefix makes the browser refuse the cookie unless it is Secure, host-only and
	// scoped to "/", so it cannot be planted by another subdomain or over plain HTTP.
	if a.secure {
		return sessionCookieSecure
	}
	return sessionCookiePlain
}

// keyMatches compares fixed-length digests in constant time, so neither the key's contents nor
// its length can be learned from response timing.
func (a *Auth) keyMatches(candidate string) bool {
	sum := sha256.Sum256([]byte(strings.TrimSpace(candidate)))
	return subtle.ConstantTimeCompare(sum[:], a.keyHash[:]) == 1
}

func sessionRecordKey(token string) string {
	// Only a digest of the token is stored, so a Redis dump does not contain usable sessions.
	sum := sha256.Sum256([]byte(token))
	return sessionKeyPrefix + hex.EncodeToString(sum[:])
}

// authenticated reports whether the request carries a live session created under the current
// key. Any storage error fails closed.
func (a *Auth) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(a.cookieName())
	if err != nil || cookie.Value == "" {
		return false
	}
	stored, err := a.store.Get(r.Context(), sessionRecordKey(cookie.Value))
	if err != nil {
		log.Printf("auth: session lookup failed: %v", err)
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(a.fingerprint)) == 1
}

// Require is the middleware in front of every application route. A browser navigating to a page
// is sent to the login form; anything else (fetches, form posts, the event stream) gets a 401,
// so a background fetch never silently receives the login page in place of its content.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && isNavigation(r) {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

func isNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// safeNext keeps the post-login redirect on this site: only a local absolute path is accepted.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.HasPrefix(next, "/login") || strings.HasPrefix(next, "/logout") {
		return "/"
	}
	return next
}

type loginView struct {
	Error string
	Next  string
}

func renderLogin(w http.ResponseWriter, status int, view loginView) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	renderTemplate(w, "login.html", view)
}

// LoginPage shows the key form, or skips it when the session is already valid.
func (a *Auth) LoginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if a.authenticated(r) {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	renderLogin(w, http.StatusOK, loginView{Next: next})
}

// Login checks the submitted key and, on success, issues a new session.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	next := safeNext(r.PostForm.Get("next"))
	ctx := r.Context()

	failures, err := a.store.Get(ctx, loginFailureKey)
	if err != nil {
		log.Printf("auth: login unavailable: %v", err)
		renderLogin(w, http.StatusServiceUnavailable, loginView{Error: "Sign-in is temporarily unavailable.", Next: next})
		return
	}
	if n, _ := strconv.Atoi(failures); n >= loginFailureLimit {
		log.Printf("auth: login refused while locked out (from %s)", r.RemoteAddr)
		renderLogin(w, http.StatusTooManyRequests, loginView{Error: "Too many failed attempts. Try again in 15 minutes.", Next: next})
		return
	}

	if !a.keyMatches(r.PostForm.Get("key")) {
		if _, err := a.store.Incr(ctx, loginFailureKey, loginFailureWindow); err != nil {
			log.Printf("auth: could not record failed login: %v", err)
		}
		log.Printf("auth: failed login (from %s)", r.RemoteAddr)
		renderLogin(w, http.StatusUnauthorized, loginView{Error: "That key is not correct.", Next: next})
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := a.store.Set(ctx, sessionRecordKey(token), a.fingerprint, a.ttl); err != nil {
		log.Printf("auth: could not store session: %v", err)
		renderLogin(w, http.StatusServiceUnavailable, loginView{Error: "Sign-in is temporarily unavailable.", Next: next})
		return
	}
	_ = a.store.Del(ctx, loginFailureKey)
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Value:    token,
		Path:     "/",
		MaxAge:   int(a.ttl / time.Second),
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteStrictMode,
	})
	log.Printf("auth: signed in (from %s)", r.RemoteAddr)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// Logout deletes the stored session and expires the cookie.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(a.cookieName()); err == nil && cookie.Value != "" {
		if err := a.store.Del(r.Context(), sessionRecordKey(cookie.Value)); err != nil {
			log.Printf("auth: could not delete session: %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Healthz is the unauthenticated container health check. It reports only whether the database
// and Redis answer, never why not, so it reveals nothing about the data behind the login.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var one int
	dbErr := h.db.QueryRow(ctx, `SELECT 1`).Scan(&one)
	redisErr := h.redis.Ping(ctx).Err()
	w.Header().Set("Cache-Control", "no-store")
	if dbErr != nil || redisErr != nil {
		log.Printf("healthz: database error=%v, redis error=%v", dbErr, redisErr)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}
