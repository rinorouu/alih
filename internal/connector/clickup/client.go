// Package clickup implements the ClickUp connector using ClickUp's official
// public API. M1 is limited to authenticated identity and Workspace discovery.
package clickup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"alih/internal/connector"
)

const (
	officialAPIBaseURL = "https://api.clickup.com/api/v2"
	maxResponseBody    = 4 * 1024 * 1024
	maxRequestAttempts = 3
	maxRetryDelay      = 60 * time.Second
)

// ErrorKind classifies connector failures without relying on provider text.
type ErrorKind string

const (
	ErrorAuthentication ErrorKind = "authentication"
	ErrorRateLimit      ErrorKind = "rate_limit"
	ErrorAPI            ErrorKind = "api"
	ErrorNetwork        ErrorKind = "network"
	ErrorResponse       ErrorKind = "response"
)

// Error is a sanitized ClickUp failure. It never contains the credential or a
// raw response body.
type Error struct {
	Kind       ErrorKind
	Operation  string
	StatusCode int
	Code       string
	Message    string
	Cause      error
}

func (e *Error) Error() string {
	switch e.Kind {
	case ErrorAuthentication:
		if e.StatusCode == 0 {
			return fmt.Sprintf("invalid ClickUp personal token during %s", e.Operation)
		}
		return fmt.Sprintf("ClickUp rejected the personal token during %s (HTTP %d)", e.Operation, e.StatusCode)
	case ErrorRateLimit:
		return fmt.Sprintf("ClickUp rate limit reached during %s (HTTP %d); try again later", e.Operation, e.StatusCode)
	case ErrorNetwork:
		detail := e.Message
		if detail == "" && e.Cause != nil {
			detail = e.Cause.Error()
		}
		return fmt.Sprintf("ClickUp request failed during %s: %s", e.Operation, detail)
	case ErrorResponse:
		return fmt.Sprintf("ClickUp returned an invalid response during %s: %v", e.Operation, e.Cause)
	default:
		detail := ""
		if e.Code != "" {
			detail = " code=" + e.Code
		}
		if e.Message != "" {
			detail += " message=" + e.Message
		}
		return fmt.Sprintf("ClickUp API failed during %s (HTTP %d%s)", e.Operation, e.StatusCode, detail)
	}
}

func (e *Error) Unwrap() error { return e.Cause }

// Client is a read-only ClickUp API client for the M1 authentication surface.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	evidence   connector.RawEvidenceSink
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
}

// NewClient creates a client fixed to ClickUp's official public API.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL, err := url.Parse(officialAPIBaseURL)
	if err != nil {
		panic("invalid ClickUp API base URL: " + err.Error())
	}
	return &Client{baseURL: baseURL, httpClient: httpClient, now: time.Now, sleep: sleepContext}
}

// newTestClient permits isolated HTTP fixtures without making the production
// connector endpoint configurable.
func newTestClient(baseURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: parsed, httpClient: httpClient, now: time.Now, sleep: sleepContext}, nil
}

func (c *Client) Name() string { return "clickup" }

// Authenticate validates credential through the authorized-user endpoint and
// then discovers every Workspace returned by the authorized-workspaces endpoint.
func (c *Client) Authenticate(ctx context.Context, credential string) (connector.Authentication, error) {
	if strings.TrimSpace(credential) == "" || strings.ContainsAny(credential, "\r\n") {
		return connector.Authentication{}, &Error{
			Kind:      ErrorAuthentication,
			Operation: "validate credential",
			Cause:     errors.New("personal token is empty or malformed"),
		}
	}

	identity, err := c.authorizedUser(ctx, credential)
	if err != nil {
		return connector.Authentication{}, err
	}
	workspaces, err := c.authorizedWorkspaces(ctx, credential)
	if err != nil {
		return connector.Authentication{}, err
	}
	return connector.Authentication{Identity: identity, Workspaces: workspaces}, nil
}

type authorizedUserResponse struct {
	User struct {
		ID       json.RawMessage `json:"id"`
		Username string          `json:"username"`
	} `json:"user"`
}

func (c *Client) authorizedUser(ctx context.Context, credential string) (connector.Identity, error) {
	var response authorizedUserResponse
	if err := c.get(ctx, credential, "/user", "get authorized user", &response); err != nil {
		return connector.Identity{}, err
	}
	id, err := parseID(response.User.ID)
	if err != nil {
		return connector.Identity{}, responseError("get authorized user", fmt.Errorf("invalid user id: %w", err))
	}
	if strings.TrimSpace(response.User.Username) == "" {
		return connector.Identity{}, responseError("get authorized user", errors.New("missing username"))
	}
	return connector.Identity{ID: id, Name: response.User.Username}, nil
}

type authorizedWorkspacesResponse struct {
	Teams json.RawMessage `json:"teams"`
}

type workspaceResponse struct {
	ID   json.RawMessage `json:"id"`
	Name string          `json:"name"`
}

func (c *Client) authorizedWorkspaces(ctx context.Context, credential string) ([]connector.Workspace, error) {
	var response authorizedWorkspacesResponse
	if err := c.get(ctx, credential, "/team", "get authorized workspaces", &response); err != nil {
		return nil, err
	}
	if len(response.Teams) == 0 || string(response.Teams) == "null" {
		return nil, responseError("get authorized workspaces", errors.New("missing teams array"))
	}
	var teams []workspaceResponse
	if err := json.Unmarshal(response.Teams, &teams); err != nil {
		return nil, responseError("get authorized workspaces", fmt.Errorf("invalid teams array: %w", err))
	}
	if teams == nil {
		return nil, responseError("get authorized workspaces", errors.New("missing teams array"))
	}

	workspaces := make([]connector.Workspace, 0, len(teams))
	seen := make(map[string]struct{}, len(teams))
	for index, team := range teams {
		id, err := parseID(team.ID)
		if err != nil {
			return nil, responseError("get authorized workspaces", fmt.Errorf("workspace %d has invalid id: %w", index, err))
		}
		if _, exists := seen[id]; exists {
			return nil, responseError("get authorized workspaces", fmt.Errorf("duplicate workspace id %q", id))
		}
		seen[id] = struct{}{}
		workspaces = append(workspaces, connector.Workspace{ID: id, Name: team.Name})
	}
	return workspaces, nil
}

func (c *Client) get(ctx context.Context, credential, path, operation string, destination any) error {
	return c.getWithQuery(ctx, credential, path, nil, operation, destination)
}

func (c *Client) getWithQuery(ctx context.Context, credential, path string, query url.Values, operation string, destination any) error {
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: strings.TrimRight(c.baseURL.Path, "/") + path})
	endpoint.RawQuery = query.Encode()
	var body []byte
	for attempt := 1; attempt <= maxRequestAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return &Error{Kind: ErrorNetwork, Operation: operation, Cause: err, Message: sanitize(err.Error(), credential)}
		}
		request.Header.Set("Authorization", credential)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "alih-v0")

		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			failure := &Error{Kind: ErrorNetwork, Operation: operation, Cause: requestErr, Message: sanitize(requestErr.Error(), credential)}
			retrying := attempt < maxRequestAttempts && ctx.Err() == nil
			if err := c.recordFailure(path, query, operation, attempt, 0, retrying, failure.Error()); err != nil {
				return responseError(operation, fmt.Errorf("record request failure: %w", err))
			}
			if !retrying {
				return failure
			}
			if err := c.sleep(ctx, retryDelay(attempt, nil, c.now())); err != nil {
				return &Error{Kind: ErrorNetwork, Operation: operation, Cause: err}
			}
			continue
		}

		body, requestErr = readBody(response.Body)
		_ = response.Body.Close()
		if requestErr != nil {
			failure := responseError(operation, requestErr)
			if err := c.recordFailure(path, query, operation, attempt, response.StatusCode, false, failure.Error()); err != nil {
				return responseError(operation, fmt.Errorf("record request failure: %w", err))
			}
			return failure
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			failure := apiError(operation, response.StatusCode, bytes.NewReader(body), credential)
			retrying := attempt < maxRequestAttempts && retryableStatus(response.StatusCode)
			if err := c.recordFailure(path, query, operation, attempt, response.StatusCode, retrying, failure.Error()); err != nil {
				return responseError(operation, fmt.Errorf("record request failure: %w", err))
			}
			if !retrying {
				return failure
			}
			if err := c.sleep(ctx, retryDelay(attempt, response.Header, c.now())); err != nil {
				return &Error{Kind: ErrorNetwork, Operation: operation, Cause: err}
			}
			continue
		}
		if err := c.recordResponse(path, query, operation, attempt, response.StatusCode, body); err != nil {
			return responseError(operation, fmt.Errorf("record raw response: %w", err))
		}
		break
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		return responseError(operation, err)
	}
	if err := requireEOF(decoder); err != nil {
		return responseError(operation, err)
	}
	return nil
}

func readBody(body io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(content) > maxResponseBody {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBody)
	}
	return content, nil
}

func retryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable || statusCode == http.StatusGatewayTimeout ||
		statusCode == http.StatusInternalServerError
}

func retryDelay(attempt int, headers http.Header, now time.Time) time.Duration {
	if headers != nil {
		if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
			delay := time.Unix(reset, 0).Sub(now)
			if delay > maxRetryDelay {
				return maxRetryDelay
			}
			if delay > 0 {
				return delay
			}
		}
	}
	delay := time.Duration(1<<(attempt-1)) * 250 * time.Millisecond
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) recordResponse(path string, query url.Values, operation string, attempt, statusCode int, body []byte) error {
	if c.evidence == nil {
		return nil
	}
	return c.evidence.RecordResponse(connector.RawResponse{
		Operation: operation, Method: http.MethodGet, Path: path, Query: cloneQuery(query),
		Attempt: attempt, StatusCode: statusCode, Body: append([]byte(nil), body...),
	})
}

func (c *Client) recordFailure(path string, query url.Values, operation string, attempt, statusCode int, retrying bool, message string) error {
	if c.evidence == nil {
		return nil
	}
	return c.evidence.RecordFailure(connector.RequestFailure{
		Operation: operation, Method: http.MethodGet, Path: path, Query: cloneQuery(query),
		Attempt: attempt, StatusCode: statusCode, Retrying: retrying, Error: message,
	})
}

func cloneQuery(query url.Values) url.Values {
	if query == nil {
		return nil
	}
	cloned := make(url.Values, len(query))
	for key, values := range query {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func apiError(operation string, statusCode int, body io.Reader, credential string) error {
	kind := ErrorAPI
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		kind = ErrorAuthentication
	} else if statusCode == http.StatusTooManyRequests {
		kind = ErrorRateLimit
	}

	var provider struct {
		Code    string `json:"ECODE"`
		Message string `json:"err"`
	}
	_ = json.NewDecoder(body).Decode(&provider)
	provider.Code = sanitize(provider.Code, credential)
	provider.Message = sanitize(provider.Message, credential)
	return &Error{
		Kind:       kind,
		Operation:  operation,
		StatusCode: statusCode,
		Code:       provider.Code,
		Message:    provider.Message,
	}
}

func responseError(operation string, cause error) error {
	return &Error{Kind: ErrorResponse, Operation: operation, Cause: cause}
}

func parseID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("missing value")
	}
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		if strings.TrimSpace(stringID) == "" {
			return "", errors.New("empty value")
		}
		return stringID, nil
	}
	var numberID json.Number
	if err := json.Unmarshal(raw, &numberID); err != nil {
		return "", errors.New("value is not a string or integer")
	}
	value := numberID.String()
	if value == "" || strings.ContainsAny(value, ".eE-") {
		return "", errors.New("value is not a non-negative integer")
	}
	return value, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func sanitize(value, credential string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	if credential != "" {
		value = strings.ReplaceAll(value, credential, "[REDACTED]")
	}
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

var _ connector.Authenticator = (*Client)(nil)
var _ connector.Scanner = (*Client)(nil)
var _ connector.Extractor = (*Client)(nil)
