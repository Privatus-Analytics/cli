# privatus CLI

A single-binary command-line client for [Privatus Analytics](https://privatusanalytics.com),
the privacy-first, cookieless web analytics. Pure Go standard library, no
dependencies.

## Install

```sh
go build -o privatus .
# or without a local Go toolchain:
docker run --rm -v "$PWD":/src -w /src golang:1.26 go build -o privatus .
# cross-compile, e.g.: GOOS=windows GOARCH=amd64 go build -o privatus.exe .
```

Build with a supported Go release (`go.mod` asks for 1.26.8 or newer). The
binary carries Go's standard library, and end-of-life Go lines no longer
get security fixes for TLS and HTTP.

## Commands

```sh
privatus login                              # paste a pat_… token; verified, then saved
privatus login --token pat_… --workspace ws_…
privatus logout

privatus workspaces list
privatus sites list [--workspace ws_…] [--query shop] [--json]
privatus stats overview pa_ABC --period 7d  # today, 24h, 7d, 30d, mtd, ytd, 12mo, custom (--from/--to)
privatus stats overview pa_ABC --period 30d --compare previous_period --json

privatus goals list pa_ABC
privatus goals create pa_ABC --name Signup --pattern /welcome --match-type prefix
privatus goals create pa_ABC --name Purchase --kind event --event-name Purchase --value-mode revenue

privatus api GET /workspaces                # ".json" is appended; output pretty-printed
privatus api GET "/sites/pa_ABC/breakdown/page?period=7d&limit=5"
privatus api PATCH /sites/pa_ABC -d '{"honor_gpc":true}'
privatus api POST /sites/pa_ABC/goals -d @goal.json   # or -d @- for stdin

privatus mcp config                         # all clients
privatus mcp config --client claude-code    # or claude-desktop, cursor
privatus mcp config --client cursor --env   # reference $PRIVATUS_API_TOKEN instead of embedding it
```

Global flags `--endpoint URL` and `--token pat_…` go before the command.
Exit codes: `0` ok, `1` API/network error (the API's `{error:{code,message}}`
is printed), `2` usage error.

## Configuration

| Setting  | Flag         | Environment          | Saved by `login` |
|----------|--------------|----------------------|------------------|
| Token    | `--token`    | `PRIVATUS_API_TOKEN` | yes              |
| Endpoint | `--endpoint` | `PRIVATUS_ENDPOINT`  | yes (if not the default) |
| Config dir | –          | `PRIVATUS_CONFIG_DIR`| –                |

The config file is `<user config dir>/privatus/config.json`
(`~/.config/privatus` on Linux, `~/Library/Application Support/privatus` on
macOS, `%AppData%\privatus` on Windows). The directory is created `0700`
and the file is written atomically with mode `0600`. `logout` removes the
token from the file; revoke the token itself under **Settings → API tokens**.

`login` rejects crawler log keys (`pik_…`): those are only for
`POST /api/logs`, not the management API.

## MCP config

`privatus mcp config` prints ready-to-paste JSON pointing at
`{endpoint}/mcp` (default `https://privatusanalytics.com/mcp`) with an
`Authorization: Bearer pat_…` header:

- **Claude Code**: `.mcp.json` with `"type": "http"`, plus the equivalent
  `claude mcp add --transport http privatus … --header …` command.
- **Cursor**: `~/.cursor/mcp.json` with `url` and `headers`.
- **Claude Desktop**: its config file starts local servers, so the remote
  server is bridged with [`mcp-remote`](https://www.npmjs.com/package/mcp-remote)
  (`npx -y mcp-remote@0.14.3 <url> --header Authorization:…`). Needs Node.js.
  The bridge is a third-party package that runs with your token on every
  launch, so the config names one exact release (`mcpRemote` in `mcp.go`).
  Keep the version when you paste it, and raise it on purpose.

The output embeds your token unless you pass `--env`. Don't commit it.

## API notes

Uses the management API: `.json` paths, `Authorization: Bearer pat_…`,
`{data, meta}` on success and `{error:{code,message,details}}` on failure.
`goals list/create` call `/sites/{site_id}/goals.json`, which is routed in
the app but **not yet in the published openapi.json** (assumed shape:
`id, name, kind, match_type, pattern, event_name, value_mode, archived`).

## Development

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.26 sh -c 'go vet ./... && go test ./...'
```

Tests run every command against an `httptest` server with a temporary
config directory.

## License

MIT, see [LICENSE](LICENSE).
