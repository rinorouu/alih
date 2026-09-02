// Copyright 2025 rinorouu
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package notion implements the Notion connector against Notion's official
// public API. Everything Notion-specific lives in this package: the API host,
// the pinned API version, payload shapes, provider vocabulary, error meanings,
// and how a signed file URL behaves. Alih Core knows none of it.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"alih/internal/connector"
)

const (
	// officialAPIBaseURL is Notion's public API. It is compiled in here and
	// nowhere else; Core never learns a provider's host.
	officialAPIBaseURL = "https://api.notion.com/v1"

	// apiVersion pins the Notion API version this adapter was written and
	// tested against.
	//
	// Notion ships breaking changes between dated versions, so the version is
	// pinned rather than floating: 2025-09-03 split a database into one or more
	// data sources and moved row queries from /databases/{id}/query to
	// /data_sources/{id}/query, and an unpinned client would have changed
	// meaning underneath a sealed archive. Raising this is a deliberate edit
	// with its own re-test, never a side effect.
	apiVersion = "2026-03-11"

	// maxPageSize is the largest page Notion accepts for a paginated list.
	maxPageSize = 100

	maxResponseBytes  = 32 << 20
	maxRequestRetries = 3
	maxRetryDelay     = 8 * time.Second

	// maxTraversalRequests bounds one extraction against a pathological or
	// malformed source. It is an operational guard, not a semantic depth limit:
	// reaching it makes the run incomplete rather than quietly truncated.
	maxTraversalRequests = 200000
)

// ErrorKind classifies a Notion failure without exposing provider prose to
// Core. Core reads the mapped operational assessment, never these strings.
type ErrorKind string

const (
	ErrorAuthentication ErrorKind = "authentication"
	ErrorForbidden      ErrorKind = "forbidden"
	ErrorNotFound       ErrorKind = "not_found"
	ErrorRateLimit      ErrorKind = "rate_limit"
	ErrorAPI            ErrorKind = "api"
	ErrorNetwork        ErrorKind = "network"
	ErrorResponse       ErrorKind = "response"
)

// Error is a sanitized Notion failure. It never contains the credential and
// never carries an unfiltered provider body.
type Error struct {
	Kind       ErrorKind
	Operation  string
	StatusCode int
	// Code is Notion's own machine-readable error code, kept because it is
	// stable and provider-owned. Core never reads it.
	Code  string
	Cause error
}

func (e *Error) Error() string {
	switch e.Kind {
	case ErrorAuthentication:
		if e.StatusCode == 0 {
			return fmt.Sprintf("invalid Notion integration token during %s", e.Operation)
		}
		return fmt.Sprintf("Notion rejected the integration token during %s (HTTP %d)", e.Operation, e.StatusCode)
	case ErrorForbidden:
		return fmt.Sprintf("Notion refused access during %s (HTTP %d); the integration may not be connected to this content",
			e.Operation, e.StatusCode)
	case ErrorNotFound:
		return fmt.Sprintf("Notion could not find the requested object during %s (HTTP %d); it may not be shared with this integration",
			e.Operation, e.StatusCode)
	case ErrorRateLimit:
		return fmt.Sprintf("Notion rate limit reached during %s (HTTP %d); try again later", e.Operation, e.StatusCode)
	case ErrorNetwork:
		detail := "no response"
		if e.Cause != nil {
			detail = e.Cause.Error()
		}
		return fmt.Sprintf("Notion request failed during %s: %s", e.Operation, detail)
	case ErrorResponse:
		return fmt.Sprintf("Notion returned an invalid response during %s: %v", e.Operation, e.Cause)
	default:
		detail := ""
		if e.Code != "" {
			detail = ", " + e.Code
		}
		return fmt.Sprintf("Notion API failed during %s (HTTP %d%s)", e.Operation, e.StatusCode, detail)
	}
}

func (e *Error) Unwrap() error { return e.Cause }

// Client is a read-only Notion API client.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	evidence   connector.RawEvidenceSink
	sleep      func(context.Context, time.Duration) error
	requests   int
}

// NewClient creates a client fixed to Notion's official public API. A nil HTTP
// client is replaced by a bounded default; tests pass their own transport.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	baseURL, err := url.Parse(officialAPIBaseURL)
	if err != nil {
		panic("invalid Notion API base URL: " + err.Error())
	}
	return &Client{httpClient: httpClient, baseURL: baseURL, sleep: sleepContext}
}

// Name is the stable machine identity of this connector.
func (c *Client) Name() string { return "notion" }

// DisplayName is how this connector names itself to a person.
func (c *Client) DisplayName() string { return "Notion" }

// APIVersion reports the pinned Notion API version, so an archive's raw
// evidence can be read back knowing which contract produced it.
func (c *Client) APIVersion() string { return apiVersion }

// get performs a read-only GET and returns the exact response bytes.
func (c *Client) get(ctx context.Context, credential, path string, query url.Values, operation string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, credential, path, query, nil, operation)
}

// post performs a read-only query. Notion uses POST for data source queries
// and search because they carry a filter body; neither modifies the source.
func (c *Client) post(ctx context.Context, credential, path string, body any, operation string) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{Kind: ErrorResponse, Operation: operation, Cause: err}
	}
	return c.do(ctx, http.MethodPost, credential, path, nil, encoded, operation)
}

func (c *Client) do(ctx context.Context, method, credential, path string, query url.Values, body []byte, operation string) ([]byte, error) {
	if strings.TrimSpace(credential) == "" {
		return nil, &Error{Kind: ErrorAuthentication, Operation: operation,
			Cause: errors.New("integration token is empty or malformed")}
	}
	c.requests++
	if c.requests > maxTraversalRequests {
		return nil, &Error{Kind: ErrorAPI, Operation: operation,
			Cause: fmt.Errorf("extraction exceeded %d requests; refusing to continue", maxTraversalRequests)}
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}

	var lastErr error
	for attempt := 1; attempt <= maxRequestRetries; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
		if err != nil {
			return nil, &Error{Kind: ErrorNetwork, Operation: operation, Cause: err}
		}
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("Notion-Version", apiVersion)
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}

		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, &Error{Kind: ErrorNetwork, Operation: operation, Cause: ctx.Err()}
			}
			lastErr = &Error{Kind: ErrorNetwork, Operation: operation, Cause: err}
			retrying := attempt < maxRequestRetries
			if recordErr := c.recordFailure(path, query, operation, method, attempt, 0, retrying, lastErr.Error()); recordErr != nil {
				return nil, recordErr
			}
			if !retrying {
				return nil, lastErr
			}
			if sleepErr := c.sleep(ctx, retryDelay(attempt, nil)); sleepErr != nil {
				return nil, &Error{Kind: ErrorNetwork, Operation: operation, Cause: sleepErr}
			}
			continue
		}

		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		response.Body.Close()
		if readErr != nil {
			return nil, &Error{Kind: ErrorNetwork, Operation: operation, Cause: readErr}
		}
		if len(payload) > maxResponseBytes {
			return nil, &Error{Kind: ErrorResponse, Operation: operation,
				Cause: fmt.Errorf("response exceeded %d bytes", maxResponseBytes)}
		}

		if response.StatusCode == http.StatusOK {
			if err := c.recordResponse(path, query, operation, method, attempt, response.StatusCode, payload); err != nil {
				return nil, err
			}
			return payload, nil
		}

		apiErr := classify(operation, response.StatusCode, payload)
		retrying := attempt < maxRequestRetries && retryableStatus(response.StatusCode)
		if recordErr := c.recordFailure(path, query, operation, method, attempt, response.StatusCode, retrying, apiErr.Error()); recordErr != nil {
			return nil, recordErr
		}
		if !retrying {
			return nil, apiErr
		}
		lastErr = apiErr
		if sleepErr := c.sleep(ctx, retryDelay(attempt, response.Header)); sleepErr != nil {
			return nil, &Error{Kind: ErrorNetwork, Operation: operation, Cause: sleepErr}
		}
	}
	return nil, lastErr
}

// classify maps a Notion HTTP failure onto this adapter's error kinds. Notion's
// own error code is preserved because it is stable and provider-owned; the
// human message is never propagated, so a provider cannot inject text into
// Alih's output.
func classify(operation string, status int, payload []byte) *Error {
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(payload, &envelope)

	kind := ErrorAPI
	switch status {
	case http.StatusUnauthorized:
		kind = ErrorAuthentication
	case http.StatusForbidden:
		kind = ErrorForbidden
	case http.StatusNotFound:
		kind = ErrorNotFound
	case http.StatusTooManyRequests:
		kind = ErrorRateLimit
	}
	return &Error{Kind: kind, Operation: operation, StatusCode: status, Code: safeCode(envelope.Code)}
}

// safeCode keeps Notion's machine-readable code only if it looks like one.
func safeCode(code string) string {
	if len(code) > 64 {
		return ""
	}
	for _, character := range code {
		switch {
		case character >= 'a' && character <= 'z', character == '_':
		default:
			return ""
		}
	}
	return code
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// retryDelay honours Retry-After when Notion sends it, which its documentation
// asks clients to respect, and otherwise backs off exponentially within bounds.
func retryDelay(attempt int, header http.Header) time.Duration {
	if header != nil {
		if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
			if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds > 0 {
				if seconds > maxRetryDelay {
					return maxRetryDelay
				}
				return seconds
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

func (c *Client) recordResponse(path string, query url.Values, operation, method string, attempt, statusCode int, body []byte) error {
	if c.evidence == nil {
		return nil
	}
	return c.evidence.RecordResponse(connector.RawResponse{
		Operation: operation, Method: method, Path: path, Query: cloneQuery(query),
		Attempt: attempt, StatusCode: statusCode, Body: append([]byte(nil), body...),
	})
}

func (c *Client) recordFailure(path string, query url.Values, operation, method string, attempt, statusCode int, retrying bool, message string) error {
	if c.evidence == nil {
		return nil
	}
	return c.evidence.RecordFailure(connector.RequestFailure{
		Operation: operation, Method: method, Path: path, Query: cloneQuery(query),
		Attempt: attempt, StatusCode: statusCode, Retrying: retrying, Error: message,
	})
}

func cloneQuery(query url.Values) url.Values {
	if query == nil {
		return nil
	}
	clone := make(url.Values, len(query))
	for key, values := range query {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}
