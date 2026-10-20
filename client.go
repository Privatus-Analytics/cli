package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a minimal management API client (`.json` paths, Bearer pat_…).
type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

// APIError is a non-2xx response, decoded from {error:{code,message,details}}.
type APIError struct {
	Status  int
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	s := fmt.Sprintf("API error %d", e.Status)
	if e.Code != "" {
		s += " (" + e.Code + ")"
	}
	s += ": " + msg
	if len(e.Details) > 0 {
		if b, err := json.Marshal(e.Details); err == nil {
			s += " " + string(b)
		}
	}
	return s
}

func newClient(endpoint, token string) *Client {
	return &Client{Endpoint: strings.TrimRight(endpoint, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// jsonPath appends ".json" to an app path unless it already has an
// extension, keeping any query string.
func jsonPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	path, query, hasQuery := strings.Cut(p, "?")
	last := path[strings.LastIndex(path, "/")+1:]
	if path != "/" && !strings.Contains(last, ".") {
		path += ".json"
	}
	if hasQuery {
		return path + "?" + query
	}
	return path
}

// Raw performs a request and returns status and body without decoding.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+jsonPath(path), reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "privatus-cli/"+version)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, b, err
}

// Do sends body (JSON-encoded if non-nil) and decodes `data` into out and
// `meta` into meta (both optional).
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any, meta *map[string]any) error {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	status, b, err := c.Raw(ctx, method, path, raw)
	if err != nil {
		return err
	}
	var env struct {
		Data  json.RawMessage `json:"data"`
		Meta  map[string]any  `json:"meta"`
		Error *APIError       `json:"error"`
	}
	decodeErr := json.Unmarshal(b, &env)
	if status >= 300 {
		apiErr := env.Error
		if apiErr == nil {
			apiErr = &APIError{Message: strings.TrimSpace(truncate(string(b), 200))}
		}
		apiErr.Status = status
		return apiErr
	}
	if decodeErr != nil && len(bytes.TrimSpace(b)) > 0 {
		return fmt.Errorf("decoding response: %w", decodeErr)
	}
	if meta != nil {
		*meta = env.Meta
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
