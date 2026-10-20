// Command privatus is the command-line client for Privatus Analytics
// (https://privatusanalytics.com).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// version is set with -ldflags "-X main.version=…".
var version = "dev"

const usage = `privatus — command-line client for Privatus Analytics

Usage:
  privatus [--endpoint URL] [--token pat_…] <command> [flags]

Commands:
  login                      Save an API token (Settings → API tokens)
  logout                     Remove the saved token
  workspaces list            List workspaces the token can access
  sites list                 List sites (all workspaces, or --workspace)
  stats overview <site>      Headline metrics (--period 7d, 30d, mtd, …)
  goals list <site>          List conversion goals
  goals create <site>        Create a goal (--name, --kind, --pattern/--event-name)
  api <METHOD> <path>        Call any API path (".json" is added), -d for a body
  mcp config                 Print MCP client config (Claude Desktop/Code, Cursor)
  version                    Print the version

Environment:
  PRIVATUS_API_TOKEN   token (overrides the saved one)
  PRIVATUS_ENDPOINT    API base URL (default https://privatusanalytics.com)
  PRIVATUS_CONFIG_DIR  config directory (default: <user config dir>/privatus)
`

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	env            func(string) string
	cfgPath        string
	cfg            *Config
	flagEndpoint   string
	flagToken      string
	ctx            context.Context
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

// errUsage marks errors that should print usage hints (exit code 2).
var errUsage = errors.New("usage")

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, env: env, ctx: context.Background()}

	global := flag.NewFlagSet("privatus", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&a.flagEndpoint, "endpoint", "", "API base URL")
	global.StringVar(&a.flagToken, "token", "", "API token")
	if err := global.Parse(args); err != nil {
		fmt.Fprintf(stderr, "error: %v\n\n%s", err, usage)
		return 2
	}
	args = global.Args()
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	var err error
	if a.cfgPath, err = configPath(env); err == nil {
		a.cfg, err = loadConfig(a.cfgPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	err = a.dispatch(args)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(stderr, "error: %v\nRun `privatus help` for usage.\n", err)
		return 2
	default:
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
}

func (a *app) dispatch(args []string) error {
	cmd, rest := args[0], args[1:]
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch {
	case cmd == "version" || cmd == "--version":
		fmt.Fprintln(a.stdout, "privatus", version)
		return nil
	case cmd == "login":
		return a.login(rest)
	case cmd == "logout":
		return a.logout()
	case cmd == "workspaces" && (sub == "list" || sub == "ls"):
		return a.workspacesList(rest[1:])
	case cmd == "sites" && (sub == "list" || sub == "ls"):
		return a.sitesList(rest[1:])
	case cmd == "stats" && sub == "overview":
		return a.statsOverview(rest[1:])
	case cmd == "goals" && (sub == "list" || sub == "ls"):
		return a.goalsList(rest[1:])
	case cmd == "goals" && sub == "create":
		return a.goalsCreate(rest[1:])
	case cmd == "api":
		return a.api(rest)
	case cmd == "mcp" && sub == "config":
		return a.mcpConfig(rest[1:])
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, strings.TrimSpace(cmd+" "+sub))
}

// parse parses flags that may appear before or after positional args.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%w: %v", errUsage, err)
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (a *app) endpoint() string {
	for _, v := range []string{a.flagEndpoint, a.env("PRIVATUS_ENDPOINT"), a.cfg.Endpoint} {
		if v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return DefaultEndpoint
}

func (a *app) token() string {
	for _, v := range []string{a.flagToken, a.env("PRIVATUS_API_TOKEN"), a.cfg.Token} {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *app) client() (*Client, error) {
	t := a.token()
	if t == "" {
		return nil, errors.New("not logged in: run `privatus login` or set PRIVATUS_API_TOKEN")
	}
	return newClient(a.endpoint(), t), nil
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) table(header []string, rows [][]string) {
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// ---- login / logout ------------------------------------------------------

type workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Plan string `json:"plan"`
	Role string `json:"role"`
}

func (a *app) listWorkspaces(c *Client) ([]workspace, error) {
	var all []workspace
	for page := 1; page <= 100; page++ {
		var batch []workspace
		var meta map[string]any
		q := url.Values{"page": {fmt.Sprint(page)}, "per_page": {"100"}}
		if err := c.Do(a.ctx, http.MethodGet, "/workspaces", q, nil, &batch, &meta); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		total, _ := meta["total_pages"].(float64)
		if len(batch) == 0 || (total > 0 && float64(page) >= total) || (total == 0 && len(batch) < 100) {
			break
		}
	}
	return all, nil
}

func (a *app) login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	token := fs.String("token", "", "API token (pat_…); read from stdin when omitted")
	endpoint := fs.String("endpoint", "", "API base URL")
	ws := fs.String("workspace", "", "default workspace id")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	t := *token
	if t == "" {
		t = a.flagToken
	}
	if t == "" {
		fmt.Fprint(a.stderr, "Create a token under Settings → API tokens, then paste it here: ")
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("no token given")
		}
		t = strings.TrimSpace(line)
	}
	if t == "" {
		return errors.New("no token given")
	}
	if !strings.HasPrefix(t, "pat_") {
		return fmt.Errorf("%w: API tokens start with pat_ (crawler log keys pik_… are only for uploading crawler logs)", errUsage)
	}
	if *endpoint != "" {
		a.flagEndpoint = *endpoint
	}
	c := newClient(a.endpoint(), t)
	list, err := a.listWorkspaces(c)
	if err != nil {
		return fmt.Errorf("checking the token: %w", err)
	}
	a.cfg.Token = t
	if a.endpoint() != DefaultEndpoint {
		a.cfg.Endpoint = a.endpoint()
	}
	switch {
	case *ws != "":
		a.cfg.Workspace = *ws
	case len(list) > 0 && a.cfg.Workspace == "":
		a.cfg.Workspace = list[0].ID
	}
	if err := a.cfg.save(a.cfgPath); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Fprintf(a.stdout, "Logged in to %s (%d workspace(s)). Token saved to %s\n", a.endpoint(), len(list), a.cfgPath)
	return nil
}

func (a *app) logout() error {
	a.cfg.Token = ""
	if err := a.cfg.save(a.cfgPath); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, "Logged out. The token itself stays valid until you revoke it in Settings → API tokens.")
	return nil
}

// ---- workspaces / sites --------------------------------------------------

func (a *app) workspacesList(args []string) error {
	fs := flag.NewFlagSet("workspaces list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := a.listWorkspaces(c)
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(list)
	}
	rows := [][]string{}
	for _, w := range list {
		rows = append(rows, []string{w.ID, w.Name, w.Plan, w.Role})
	}
	a.table([]string{"ID", "NAME", "PLAN", "ROLE"}, rows)
	return nil
}

type site struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Domain      string  `json:"domain"`
	Name        string  `json:"name"`
	LastEventAt *string `json:"last_event_at"`
}

func (a *app) sitesList(args []string) error {
	fs := flag.NewFlagSet("sites list", flag.ContinueOnError)
	ws := fs.String("workspace", "", "workspace id (default: all workspaces)")
	query := fs.String("query", "", "filter by domain or name")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ids := []string{*ws}
	if *ws == "" {
		list, err := a.listWorkspaces(c)
		if err != nil {
			return err
		}
		ids = ids[:0]
		for _, w := range list {
			ids = append(ids, w.ID)
		}
	}
	var sites []site
	for _, id := range ids {
		for page := 1; page <= 100; page++ {
			var batch []site
			var meta map[string]any
			q := url.Values{"page": {fmt.Sprint(page)}, "per_page": {"100"}}
			if *query != "" {
				q.Set("query", *query)
			}
			if err := c.Do(a.ctx, http.MethodGet, "/workspaces/"+url.PathEscape(id)+"/sites", q, nil, &batch, &meta); err != nil {
				return err
			}
			sites = append(sites, batch...)
			total, _ := meta["total_pages"].(float64)
			if len(batch) == 0 || (total > 0 && float64(page) >= total) || (total == 0 && len(batch) < 100) {
				break
			}
		}
	}
	if *asJSON {
		return a.printJSON(sites)
	}
	rows := [][]string{}
	for _, s := range sites {
		last := "never"
		if s.LastEventAt != nil {
			last = *s.LastEventAt
		}
		rows = append(rows, []string{s.ID, s.Domain, s.Name, s.WorkspaceID, last})
	}
	a.table([]string{"ID", "DOMAIN", "NAME", "WORKSPACE", "LAST EVENT"}, rows)
	return nil
}

// ---- stats ---------------------------------------------------------------

var metricOrder = []string{"visitors", "visits", "pageviews", "views_per_visit", "bounce_rate", "visit_duration", "events", "conversions", "conversion_rate", "revenue"}

func (a *app) statsOverview(args []string) error {
	fs := flag.NewFlagSet("stats overview", flag.ContinueOnError)
	period := fs.String("period", "30d", "today, yesterday, 24h, 7d, 14d, 28d, 30d, 90d, mtd, last_month, ytd, 12mo, all, custom")
	from := fs.String("from", "", "start date (YYYY-MM-DD, with --period custom)")
	to := fs.String("to", "", "end date (YYYY-MM-DD, with --period custom)")
	compare := fs.String("compare", "", "previous_period or year_over_year")
	asJSON := fs.Bool("json", false, "print the full JSON response")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("%w: stats overview needs exactly one site id, e.g. `privatus stats overview pa_ABC --period 7d`", errUsage)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	q := url.Values{"period": {*period}}
	for k, v := range map[string]string{"from": *from, "to": *to, "compare": *compare} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var data map[string]any
	if err := c.Do(a.ctx, http.MethodGet, "/sites/"+url.PathEscape(pos[0])+"/overview", q, nil, &data, nil); err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(data)
	}
	a.printOverview(pos[0], *period, data)
	return nil
}

func formatValue(v any) string {
	switch n := v.(type) {
	case nil:
		return "–"
	case float64:
		if n == float64(int64(n)) {
			return fmt.Sprintf("%d", int64(n))
		}
		return fmt.Sprintf("%.2f", n)
	case map[string]any:
		if inner, ok := n["value"]; ok {
			return formatValue(inner)
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *app) printOverview(siteID, period string, data map[string]any) {
	fmt.Fprintf(a.stdout, "%s · %s\n\n", siteID, period)
	metrics, _ := data["metrics"].(map[string]any)
	change, _ := data["change"].(map[string]any)
	keys := []string{}
	seen := map[string]bool{}
	for _, k := range metricOrder {
		if _, ok := metrics[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	extra := []string{}
	for k := range metrics {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	rows := [][]string{}
	for _, k := range append(keys, extra...) {
		ch := ""
		if v, ok := change[k]; ok && v != nil {
			ch = formatValue(v) + "%"
		}
		rows = append(rows, []string{k, formatValue(metrics[k]), ch})
	}
	if lv, ok := data["live_visitors"]; ok {
		rows = append(rows, []string{"live_visitors", formatValue(lv), ""})
	}
	a.table([]string{"METRIC", "VALUE", "CHANGE"}, rows)

	panels, _ := data["panels"].(map[string]any)
	for _, dim := range []string{"page", "referrer", "country"} {
		list := panelRows(panels[dim])
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(a.stdout, "\nTop %s\n", dim)
		prow := [][]string{}
		for i, r := range list {
			if i == 5 {
				break
			}
			label := firstString(r, "value", "name", "label", dim)
			prow = append(prow, []string{label, formatValue(firstPresent(r, "visitors", "count", "pageviews"))})
		}
		a.table([]string{strings.ToUpper(dim), "VISITORS"}, prow)
	}
}

func panelRows(v any) []map[string]any {
	var list []any
	switch p := v.(type) {
	case []any:
		list = p
	case map[string]any:
		list, _ = p["rows"].([]any)
	}
	out := []map[string]any{}
	for _, r := range list {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return "(none)"
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

// ---- goals (assumed endpoints: /sites/{site_id}/goals(.json)) ------------

type goal struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	MatchType string  `json:"match_type"`
	Pattern   *string `json:"pattern"`
	EventName *string `json:"event_name"`
	ValueMode string  `json:"value_mode"`
	Archived  bool    `json:"archived"`
}

func (a *app) goalsList(args []string) error {
	fs := flag.NewFlagSet("goals list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("%w: goals list needs a site id", errUsage)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var goals []goal
	q := url.Values{"per_page": {"100"}}
	if err := c.Do(a.ctx, http.MethodGet, "/sites/"+url.PathEscape(pos[0])+"/goals", q, nil, &goals, nil); err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(goals)
	}
	rows := [][]string{}
	for _, g := range goals {
		match := ""
		switch {
		case g.EventName != nil:
			match = *g.EventName
		case g.Pattern != nil:
			match = g.MatchType + " " + *g.Pattern
		}
		state := "active"
		if g.Archived {
			state = "archived"
		}
		rows = append(rows, []string{g.ID, g.Name, g.Kind, match, g.ValueMode, state})
	}
	a.table([]string{"ID", "NAME", "KIND", "MATCH", "VALUE", "STATE"}, rows)
	return nil
}

func (a *app) goalsCreate(args []string) error {
	fs := flag.NewFlagSet("goals create", flag.ContinueOnError)
	name := fs.String("name", "", "goal name (required)")
	kind := fs.String("kind", "page", "page, event, engagement, outbound or download")
	pattern := fs.String("pattern", "", "path or URL pattern (page/outbound/download/engagement goals)")
	matchType := fs.String("match-type", "", "exact, prefix, glob or regex")
	eventName := fs.String("event-name", "", "custom event name (event goals)")
	valueMode := fs.String("value-mode", "", "none, fixed or revenue")
	fixedValue := fs.Float64("fixed-value", 0, "fixed conversion value (with --value-mode fixed)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *name == "" {
		return fmt.Errorf("%w: goals create <site> --name NAME [--kind page --pattern /thanks | --kind event --event-name Signup]", errUsage)
	}
	if *kind == "event" && *eventName == "" {
		return fmt.Errorf("%w: event goals need --event-name", errUsage)
	}
	if *kind != "event" && *pattern == "" && *kind != "engagement" {
		return fmt.Errorf("%w: %s goals need --pattern", errUsage, *kind)
	}
	body := map[string]any{"name": *name, "kind": *kind}
	for k, v := range map[string]string{"pattern": *pattern, "match_type": *matchType, "event_name": *eventName, "value_mode": *valueMode} {
		if v != "" {
			body[k] = v
		}
	}
	if *valueMode == "fixed" {
		body["fixed_value"] = *fixedValue
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var g goal
	if err := c.Do(a.ctx, http.MethodPost, "/sites/"+url.PathEscape(pos[0])+"/goals", nil, body, &g, nil); err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(g)
	}
	fmt.Fprintf(a.stdout, "Created goal %s (%s)\n", g.ID, g.Name)
	return nil
}

// ---- api passthrough -------------------------------------------------------

func (a *app) api(args []string) error {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	data := fs.String("d", "", "JSON body, @file, or @- for stdin")
	fs.StringVar(data, "data", "", "JSON body, @file, or @- for stdin")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		pos = []string{"GET", pos[0]}
	}
	if len(pos) != 2 {
		return fmt.Errorf("%w: api <METHOD> <path>, e.g. `privatus api GET /workspaces`", errUsage)
	}
	method := strings.ToUpper(pos[0])
	var body []byte
	switch {
	case *data == "@-":
		if body, err = io.ReadAll(a.stdin); err != nil {
			return err
		}
	case strings.HasPrefix(*data, "@"):
		if body, err = os.ReadFile((*data)[1:]); err != nil {
			return err
		}
	case *data != "":
		body = []byte(*data)
	}
	if body != nil && !json.Valid(body) {
		return fmt.Errorf("%w: the request body is not valid JSON", errUsage)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	status, out, err := c.Raw(a.ctx, method, pos[1], body)
	if err != nil {
		return err
	}
	var pretty any
	if json.Unmarshal(out, &pretty) == nil {
		_ = a.printJSON(pretty)
	} else if len(out) > 0 {
		a.stdout.Write(out)
		if out[len(out)-1] != '\n' {
			fmt.Fprintln(a.stdout)
		}
	}
	if status >= 400 {
		return fmt.Errorf("HTTP %d", status)
	}
	return nil
}
