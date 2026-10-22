package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// memSessionStore is an in-memory sessionStore; expiry is not simulated.
type memSessionStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemSessionStore() *memSessionStore { return &memSessionStore{data: map[string]string{}} }

func (m *memSessionStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *memSessionStore) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key], nil
}

func (m *memSessionStore) Del(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memSessionStore) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.ParseInt(m.data[key], 10, 64)
	n++
	m.data[key] = strconv.FormatInt(n, 10)
	return n, nil
}

const testAdminKey = "test-admin-key-0123456789-abcdefghijklmnop"

func testAuth(t *testing.T, store sessionStore, key string) *Auth {
	t.Helper()
	a, err := newAuth(store, key, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func postLogin(a *Auth, key, next string) *httptest.ResponseRecorder {
	form := url.Values{"key": {key}, "next": {next}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.Login(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieSecure {
			return c
		}
	}
	t.Fatalf("no session cookie set (status %d)", rec.Code)
	return nil
}

func TestAdminKeyMustBePresentAndLong(t *testing.T) {
	store := newMemSessionStore()
	if _, err := newAuth(store, "", time.Hour, true); err == nil {
		t.Fatal("an empty key was accepted")
	}
	if _, err := newAuth(store, strings.Repeat("x", MinAdminKeyLength-1), time.Hour, true); err == nil {
		t.Fatal("a short key was accepted")
	}
	if _, err := newAuth(store, strings.Repeat("x", MinAdminKeyLength), time.Hour, true); err != nil {
		t.Fatalf("a key of the minimum length was refused: %v", err)
	}
}

func TestAuthFromEnvRejectsAmbiguousSettings(t *testing.T) {
	t.Setenv("ADMIN_ACCESS_KEY", testAdminKey)
	t.Setenv("COOKIE_SECURE", "yes")
	if _, err := AuthFromEnv(nil); err == nil {
		t.Fatal("COOKIE_SECURE=yes was accepted")
	}
	t.Setenv("COOKIE_SECURE", "")
	t.Setenv("SESSION_TTL_HOURS", "0")
	if _, err := AuthFromEnv(nil); err == nil {
		t.Fatal("SESSION_TTL_HOURS=0 was accepted")
	}
	t.Setenv("SESSION_TTL_HOURS", "")
	t.Setenv("ADMIN_ACCESS_KEY", "")
	if _, err := AuthFromEnv(nil); err == nil {
		t.Fatal("startup would run without an admin key")
	}
}

func TestKeyComparison(t *testing.T) {
	a := testAuth(t, newMemSessionStore(), testAdminKey)
	if !a.keyMatches(testAdminKey) || !a.keyMatches("  "+testAdminKey+"\n") {
		t.Fatal("the correct key was refused")
	}
	for _, wrong := range []string{"", testAdminKey[:len(testAdminKey)-1], testAdminKey + "x", strings.ToUpper(testAdminKey)} {
		if a.keyMatches(wrong) {
			t.Fatalf("%q was accepted", wrong)
		}
	}
}

func TestLoginIssuesHardenedSessionCookie(t *testing.T) {
	defer chdirRepoRoot(t)()
	store := newMemSessionStore()
	a := testAuth(t, store, testAdminKey)

	rec := postLogin(a, testAdminKey, "/companies")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/companies" {
		t.Fatalf("got %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookie(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie is not hardened: %+v", c)
	}
	if strings.Contains(c.Value, testAdminKey) {
		t.Fatal("the cookie carries the admin key")
	}
	for key, value := range store.data {
		if strings.Contains(key, c.Value) || strings.Contains(value, testAdminKey) {
			t.Fatal("the session store holds the raw token or the key")
		}
	}
}

func TestWrongKeyIsRefusedAndLocksOut(t *testing.T) {
	defer chdirRepoRoot(t)()
	a := testAuth(t, newMemSessionStore(), testAdminKey)
	for i := 0; i < loginFailureLimit; i++ {
		rec := postLogin(a, "not-the-key", "/")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d", i+1, rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatal("a failed login set a cookie")
		}
	}
	// Once locked, even the right key is refused until the window passes.
	if rec := postLogin(a, testAdminKey, "/"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked out login got %d", rec.Code)
	}
}

func TestRequireGuardsEveryKindOfRequest(t *testing.T) {
	defer chdirRepoRoot(t)()
	a := testAuth(t, newMemSessionStore(), testAdminKey)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	guarded := a.Require(ok)

	nav := httptest.NewRequest(http.MethodGet, "/opportunities?page=2", nil)
	nav.Header.Set("Sec-Fetch-Mode", "navigate")
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, nav)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fopportunities%3Fpage%3D2" {
		t.Fatalf("navigation got %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	fetch := httptest.NewRequest(http.MethodGet, "/dashboard/snapshot", nil)
	fetch.Header.Set("Sec-Fetch-Mode", "cors")
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, fetch)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("background fetch got %d; it must not receive the login page", rec.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "/compositions/1/send", nil)
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, post)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated send got %d", rec.Code)
	}

	forged := httptest.NewRequest(http.MethodGet, "/", nil)
	forged.AddCookie(&http.Cookie{Name: sessionCookieSecure, Value: "made-up"})
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, forged)
	if rec.Code == http.StatusOK {
		t.Fatal("an invented session token was accepted")
	}

	login := postLogin(a, testAdminKey, "/")
	authed := httptest.NewRequest(http.MethodPost, "/compositions/1/send", nil)
	authed.AddCookie(sessionCookie(t, login))
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, authed)
	if rec.Code != http.StatusOK {
		t.Fatalf("a valid session got %d", rec.Code)
	}
}

func TestLogoutAndKeyRotationEndSessions(t *testing.T) {
	defer chdirRepoRoot(t)()
	store := newMemSessionStore()
	a := testAuth(t, store, testAdminKey)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	request := func(auth *Auth, c *http.Cookie) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		auth.Require(ok).ServeHTTP(rec, req)
		return rec.Code
	}

	cookie := sessionCookie(t, postLogin(a, testAdminKey, "/"))
	rotated := testAuth(t, store, testAdminKey+"-rotated")
	if request(rotated, cookie) == http.StatusOK {
		t.Fatal("a session created under the old key survived rotation")
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.AddCookie(cookie)
	rec := httptest.NewRecorder()
	a.Logout(rec, logoutReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout got %d", rec.Code)
	}
	if request(a, cookie) == http.StatusOK {
		t.Fatal("the session still works after logout")
	}
}

func TestPostLoginRedirectStaysOnSite(t *testing.T) {
	for in, want := range map[string]string{
		"/companies?x=1":      "/companies?x=1",
		"":                    "/",
		"https://example.com": "/",
		"//example.com":       "/",
		"/\\example.com":      "/",
		"/login":              "/",
		"/logout":             "/",
	} {
		if got := safeNext(in); got != want {
			t.Fatalf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// main.go registers the application routes through r.With(auth.Require) and then .Group; this
// pins that chi carries the middleware into those groups, so no group is left open by accident.
func TestProtectedGroupsInheritTheGuard(t *testing.T) {
	a := testAuth(t, newMemSessionStore(), testAdminKey)
	r := chi.NewRouter()
	r.Get("/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	protected := r.With(a.Require)
	protected.Get("/events/activity", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	protected.Group(func(r chi.Router) {
		r.Post("/admin/enrich", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/events/activity"},
		{http.MethodPost, "/admin/enrich"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s got %d without a session", tc.method, tc.path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/login is not reachable without a session: %d", rec.Code)
	}
}
