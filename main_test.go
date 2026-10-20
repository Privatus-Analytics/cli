package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type result struct {
	code           int
	stdout, stderr string
}

type harness struct {
	t        *testing.T
	dir      string
	srv      *httptest.Server
	requests []*http.Request
	bodies   []string
}

func newHarness(t *testing.T, handler http.HandlerFunc) *harness {
	h := &harness{t: t, dir: t.TempDir()}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.requests = append(h.requests, r)
		h.bodies = append(h.bodies, string(b))
		if r.Header.Get("Authorization") != "Bearer pat_good" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"Invalid token"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) run(stdin string, env map[string]string, args ...string) result {
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if k == "PRIVATUS_CONFIG_DIR" {
			return h.dir
		}
		if k == "PRIVATUS_ENDPOINT" {
			return h.srv.URL
		}
		return env[k]
	}
	code := run(args, strings.NewReader(stdin), &out, &errb, getenv)
	return result{code, out.String(), errb.String()}
}

func api(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/workspaces.json":
		w.Write([]byte(`{"data":[{"id":"ws_A","name":"Acme","plan":"pro","role":"owner"},{"id":"ws_B","name":"Beta","plan":"free","role":"viewer"}],"meta":{"page":1,"per_page":100,"total":2,"total_pages":1}}`))
	case "/workspaces/ws_A/sites.json":
		w.Write([]byte(`{"data":[{"id":"pa_1","workspace_id":"ws_A","domain":"a.com","name":"A","last_event_at":"2026-09-28T10:00:00Z"}],"meta":{"total_pages":1}}`))
	case "/workspaces/ws_B/sites.json":
		w.Write([]byte(`{"data":[{"id":"pa_2","workspace_id":"ws_B","domain":"b.com","name":"B","last_event_at":null}],"meta":{"total_pages":1}}`))
	case "/sites/pa_1/overview.json":
		w.Write([]byte(`{"data":{"period":{"from":"2026-09-22","to":"2026-09-28"},"metrics":{"visitors":1234,"pageviews":5678,"bounce_rate":41.5,"custom_metric":3},"change":{"visitors":12.5},"live_visitors":3,"panels":{"page":[{"value":"/","visitors":900},{"value":"/pricing","visitors":120}]}},"meta":{}}`))
	case "/sites/pa_1/goals.json":
		if r.Method == http.MethodPost {
			w.WriteHeader(201)
			w.Write([]byte(`{"data":{"id":"goal_9","name":"Signup","kind":"page","match_type":"exact","pattern":"/welcome","value_mode":"none"}}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"goal_1","name":"Purchase","kind":"event","event_name":"Purchase","value_mode":"revenue"},{"id":"goal_2","name":"Pricing","kind":"page","match_type":"prefix","pattern":"/pricing","value_mode":"none","archived":true}],"meta":{}}`))
	case "/sites/pa_missing/overview.json":
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"code":"not_found","message":"Site not found"}}`))
	case "/openapi.json":
		w.Write([]byte(`{"openapi":"3.1.0"}`))
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"code":"not_found","message":"No route"}}`))
	}
}

func TestLoginStoresTokenWith0600(t *testing.T) {
	h := newHarness(t, api)
	res := h.run("pat_good\n", nil, "login")
	if res.code != 0 {
		t.Fatalf("login failed: %+v", res)
	}
	path := filepath.Join(h.dir, "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config perms = %v, want 0600", info.Mode().Perm())
	}
	var cfg Config
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &cfg)
	if cfg.Token != "pat_good" || cfg.Workspace != "ws_A" {
		t.Fatalf("config = %+v", cfg)
	}
	if h.requests[0].URL.Path != "/workspaces.json" {
		t.Fatalf("token was not verified: %v", h.requests[0].URL)
	}

	// Subsequent commands use the stored token.
	if res := h.run("", nil, "workspaces", "list"); res.code != 0 || !strings.Contains(res.stdout, "Acme") {
		t.Fatalf("workspaces list: %+v", res)
	}

	if res := h.run("", nil, "logout"); res.code != 0 {
		t.Fatalf("logout: %+v", res)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "pat_good") {
		t.Fatal("token still in config after logout")
	}
}

func TestLoginRejectsBadToken(t *testing.T) {
	h := newHarness(t, api)
	if res := h.run("", nil, "login", "--token", "pat_bad"); res.code != 1 || !strings.Contains(res.stderr, "Invalid token") {
		t.Fatalf("expected auth failure, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "config.json")); err == nil {
		t.Fatal("config should not be written for a bad token")
	}
	if res := h.run("", nil, "login", "--token", "pik_crawlerlogkey"); res.code != 2 {
		t.Fatalf("crawler log key should be refused as usage error, got %+v", res)
	}
}

func TestSitesListAcrossWorkspaces(t *testing.T) {
	h := newHarness(t, api)
	env := map[string]string{"PRIVATUS_API_TOKEN": "pat_good"}
	res := h.run("", env, "sites", "list")
	if res.code != 0 {
		t.Fatalf("%+v", res)
	}
	for _, want := range []string{"pa_1", "a.com", "pa_2", "b.com", "never", "ws_B"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("output missing %q:\n%s", want, res.stdout)
		}
	}
	res = h.run("", env, "sites", "list", "--workspace", "ws_B", "--json")
	var sites []site
	if err := json.Unmarshal([]byte(res.stdout), &sites); err != nil || len(sites) != 1 || sites[0].ID != "pa_2" {
		t.Fatalf("json output: %v %s", err, res.stdout)
	}
}

func TestStatsOverview(t *testing.T) {
	h := newHarness(t, api)
	env := map[string]string{"PRIVATUS_API_TOKEN": "pat_good"}
	res := h.run("", env, "stats", "overview", "pa_1", "--period", "7d")
	if res.code != 0 {
		t.Fatalf("%+v", res)
	}
	last := h.requests[len(h.requests)-1]
	if last.URL.Path != "/sites/pa_1/overview.json" || last.URL.Query().Get("period") != "7d" {
		t.Fatalf("request = %v", last.URL)
	}
	for _, want := range []string{"visitors", "1234", "12.50%", "bounce_rate", "41.50", "custom_metric", "live_visitors", "/pricing"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("output missing %q:\n%s", want, res.stdout)
		}
	}
	// Flag before the positional argument works too.
	if res := h.run("", env, "stats", "overview", "--period", "30d", "pa_1", "--json"); res.code != 0 || !strings.Contains(res.stdout, `"metrics"`) {
		t.Fatalf("%+v", res)
	}
	if res := h.run("", env, "stats", "overview", "pa_missing"); res.code != 1 || !strings.Contains(res.stderr, "Site not found") {
		t.Fatalf("expected API error, got %+v", res)
	}
	if res := h.run("", env, "stats", "overview"); res.code != 2 {
		t.Fatalf("expected usage error, got %+v", res)
	}
}

func TestGoals(t *testing.T) {
	h := newHarness(t, api)
	env := map[string]string{"PRIVATUS_API_TOKEN": "pat_good"}
	res := h.run("", env, "goals", "list", "pa_1")
	if res.code != 0 || !strings.Contains(res.stdout, "Purchase") || !strings.Contains(res.stdout, "prefix /pricing") || !strings.Contains(res.stdout, "archived") {
		t.Fatalf("%+v", res)
	}
	res = h.run("", env, "goals", "create", "pa_1", "--name", "Signup", "--pattern", "/welcome")
	if res.code != 0 || !strings.Contains(res.stdout, "goal_9") {
		t.Fatalf("%+v", res)
	}
	var body map[string]any
	json.Unmarshal([]byte(h.bodies[len(h.bodies)-1]), &body)
	if body["name"] != "Signup" || body["kind"] != "page" || body["pattern"] != "/welcome" {
		t.Fatalf("body = %v", body)
	}
	if res := h.run("", env, "goals", "create", "pa_1", "--name", "X", "--kind", "event"); res.code != 2 {
		t.Fatalf("event goal without --event-name should be a usage error: %+v", res)
	}
}

func TestAPIPassthrough(t *testing.T) {
	h := newHarness(t, api)
	env := map[string]string{"PRIVATUS_API_TOKEN": "pat_good"}
	res := h.run("", env, "api", "GET", "/workspaces")
	if res.code != 0 || !strings.Contains(res.stdout, `"ws_A"`) {
		t.Fatalf("%+v", res)
	}
	if h.requests[len(h.requests)-1].URL.Path != "/workspaces.json" {
		t.Fatal(".json not appended")
	}
	if res := h.run("", env, "api", "GET", "/openapi.json"); res.code != 0 || !strings.Contains(res.stdout, "3.1.0") {
		t.Fatalf("%+v", res)
	}
	res = h.run(`{"name":"x"}`, env, "api", "POST", "/sites/pa_1/goals", "-d", "@-")
	if res.code != 0 || h.bodies[len(h.bodies)-1] != `{"name":"x"}` || h.requests[len(h.requests)-1].Method != "POST" {
		t.Fatalf("%+v %v", res, h.bodies)
	}
	if res := h.run("", env, "api", "GET", "/nope"); res.code != 1 || !strings.Contains(res.stdout, "No route") {
		t.Fatalf("expected HTTP error exit 1 with body printed: %+v", res)
	}
	if res := h.run("", env, "api", "POST", "/x", "-d", "{bad"); res.code != 2 {
		t.Fatalf("invalid JSON body should be usage error: %+v", res)
	}
}

func TestJSONPath(t *testing.T) {
	cases := map[string]string{
		"/workspaces":          "/workspaces.json",
		"workspaces":           "/workspaces.json",
		"/openapi.json":        "/openapi.json",
		"/sites/pa_1?period=7": "/sites/pa_1.json?period=7",
		"/sites/pa_1/goals":    "/sites/pa_1/goals.json",
	}
	for in, want := range cases {
		if got := jsonPath(in); got != want {
			t.Errorf("jsonPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMCPConfig(t *testing.T) {
	h := newHarness(t, api)
	env := map[string]string{"PRIVATUS_API_TOKEN": "pat_good"}
	url := h.srv.URL + "/mcp"

	res := h.run("", env, "mcp", "config", "--client", "claude-code")
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &cfg); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, res.stdout)
	}
	s := cfg.MCPServers["privatus"]
	if s.Type != "http" || s.URL != url || s.Headers["Authorization"] != "Bearer pat_good" {
		t.Fatalf("claude-code config = %+v", s)
	}

	res = h.run("", env, "mcp", "config", "--client", "cursor", "--env")
	cfg.MCPServers = nil
	json.Unmarshal([]byte(res.stdout), &cfg)
	if s := cfg.MCPServers["privatus"]; s.URL != url || s.Headers["Authorization"] != "Bearer ${env:PRIVATUS_API_TOKEN}" || strings.Contains(res.stdout, "pat_good") {
		t.Fatalf("cursor config = %s", res.stdout)
	}

	res = h.run("", env, "mcp", "config", "--client", "claude-desktop")
	cfg.MCPServers = nil
	json.Unmarshal([]byte(res.stdout), &cfg)
	if s := cfg.MCPServers["privatus"]; s.Command != "npx" || s.Args[2] != url || s.Env["PRIVATUS_AUTH_HEADER"] != "Bearer pat_good" {
		t.Fatalf("claude-desktop config = %s", res.stdout)
	}

	res = h.run("", env, "mcp", "config")
	if res.code != 0 || strings.Count(res.stdout, `"mcpServers"`) != 3 || !strings.Contains(res.stdout, "claude mcp add --transport http") {
		t.Fatalf("all: %+v", res)
	}
	if res := h.run("", env, "mcp", "config", "--client", "vim"); res.code != 2 {
		t.Fatalf("unknown client should be usage error: %+v", res)
	}
	if res := h.run("", nil, "mcp", "config"); res.code != 1 {
		t.Fatalf("no token should fail: %+v", res)
	}
}

func TestNotLoggedIn(t *testing.T) {
	h := newHarness(t, api)
	if res := h.run("", nil, "sites", "list"); res.code != 1 || !strings.Contains(res.stderr, "privatus login") {
		t.Fatalf("%+v", res)
	}
	if res := h.run("", nil, "frobnicate"); res.code != 2 {
		t.Fatalf("%+v", res)
	}
	if res := h.run("", nil, "help"); res.code != 0 || !strings.Contains(res.stdout, "mcp config") {
		t.Fatalf("%+v", res)
	}
}
