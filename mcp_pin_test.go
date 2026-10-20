package main

import (
	"encoding/json"
	"regexp"
	"testing"
)

// The Claude Desktop config starts the bridge with npx every time the app
// launches and hands it the API token. An unpinned package name would run
// whatever release is newest at that moment, so the config must name one
// exact release.
func TestMCPConfigPinsBridgeRelease(t *testing.T) {
	h := newHarness(t, api)
	env := map[string]string{"PRIVATUS_API_TOKEN": "pat_good"}
	res := h.run("", env, "mcp", "config", "--client", "claude-desktop")
	if res.code != 0 {
		t.Fatalf("mcp config failed: %+v", res)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &cfg); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, res.stdout)
	}
	s := cfg.MCPServers["privatus"]
	if s.Command != "npx" || len(s.Args) < 3 {
		t.Fatalf("claude-desktop config = %+v", s)
	}
	exact := regexp.MustCompile(`^mcp-remote@\d+\.\d+\.\d+$`)
	if !exact.MatchString(s.Args[1]) {
		t.Fatalf("bridge package %q is not pinned to an exact release", s.Args[1])
	}
	// The URL stays the argument right after the package.
	if s.Args[2] != h.srv.URL+"/mcp" {
		t.Fatalf("url argument = %q", s.Args[2])
	}
}
