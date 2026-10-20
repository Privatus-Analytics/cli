package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

// mcpRemote is the stdio-to-HTTP bridge Claude Desktop starts with npx on
// every launch, with the API token in its environment. It is a third-party
// package, so it is pinned to one exact, reviewed release: a bare name
// would run whatever is newest on npm that day. Keep it the same as
// content/docs/mcp/clients.md (a spec in the app compares the two).
const mcpRemote = "mcp-remote@0.14.3"

// mcpClients maps a client name to its config builder. Each builder gets
// the MCP URL and the Authorization header value (a literal "Bearer pat_…"
// or an env-var reference in the client's own syntax).
var mcpClients = []struct {
	name, file string
	build      func(url string, useEnv bool, token string) any
}{
	{
		// Claude Desktop's config file launches local (stdio) servers, so the
		// remote server is bridged with mcp-remote.
		name: "claude-desktop",
		file: "claude_desktop_config.json (Settings → Developer → Edit Config)",
		build: func(url string, useEnv bool, token string) any {
			auth := "Bearer " + token
			if useEnv {
				// Claude Desktop does not expand env vars in its config file.
				auth = "Bearer pat_REPLACE_WITH_YOUR_TOKEN"
			}
			return map[string]any{"mcpServers": map[string]any{"privatus": map[string]any{
				"command": "npx",
				"args":    []string{"-y", mcpRemote, url, "--header", "Authorization:${PRIVATUS_AUTH_HEADER}"},
				"env":     map[string]string{"PRIVATUS_AUTH_HEADER": auth},
			}}}
		},
	},
	{
		name: "claude-code",
		file: ".mcp.json in your project (or run the `claude mcp add` command below)",
		build: func(url string, useEnv bool, token string) any {
			auth := "Bearer " + token
			if useEnv {
				auth = "Bearer ${PRIVATUS_API_TOKEN}"
			}
			return map[string]any{"mcpServers": map[string]any{"privatus": map[string]any{
				"type":    "http",
				"url":     url,
				"headers": map[string]string{"Authorization": auth},
			}}}
		},
	},
	{
		name: "cursor",
		file: "~/.cursor/mcp.json (or .cursor/mcp.json in a project)",
		build: func(url string, useEnv bool, token string) any {
			auth := "Bearer " + token
			if useEnv {
				auth = "Bearer ${env:PRIVATUS_API_TOKEN}"
			}
			return map[string]any{"mcpServers": map[string]any{"privatus": map[string]any{
				"url":     url,
				"headers": map[string]string{"Authorization": auth},
			}}}
		},
	},
}

func (a *app) mcpConfig(args []string) error {
	fs := flag.NewFlagSet("mcp config", flag.ContinueOnError)
	clientName := fs.String("client", "all", "claude-desktop, claude-code, cursor or all")
	useEnv := fs.Bool("env", false, "reference $PRIVATUS_API_TOKEN instead of embedding the token")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	token := a.token()
	if token == "" && !*useEnv {
		return errors.New("no token: run `privatus login`, set PRIVATUS_API_TOKEN, or pass --env to print an env-var reference")
	}
	url := a.endpoint() + "/mcp"

	matched := false
	for _, c := range mcpClients {
		if *clientName != "all" && *clientName != c.name {
			continue
		}
		matched = true
		if *clientName == "all" {
			fmt.Fprintf(a.stdout, "# %s — %s\n", c.name, c.file)
		}
		if err := a.printJSON(c.build(url, *useEnv, token)); err != nil {
			return err
		}
		if c.name == "claude-code" && *clientName == "all" {
			header := "Authorization: Bearer " + token
			if *useEnv {
				header = "Authorization: Bearer $PRIVATUS_API_TOKEN"
			}
			fmt.Fprintf(a.stdout, "# or: claude mcp add --transport http privatus %s --header %q\n", url, header)
		}
		if *clientName == "all" {
			fmt.Fprintln(a.stdout)
		}
	}
	if !matched {
		names := []string{}
		for _, c := range mcpClients {
			names = append(names, c.name)
		}
		return fmt.Errorf("%w: unknown --client %q (use %s or all)", errUsage, *clientName, strings.Join(names, ", "))
	}
	if !*useEnv {
		fmt.Fprintln(a.stderr, "Note: this output contains your API token. Keep it out of version control, or use --env.")
	}
	return nil
}
