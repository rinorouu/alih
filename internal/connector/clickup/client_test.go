package clickup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func fixtureResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func fixtureClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	client, err := newTestClient("https://api.test/api/v2", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestAuthenticateUsesOfficialReadOnlyEndpointsAndParsesResult(t *testing.T) {
	t.Parallel()

	const token = "pk_test_secret"
	var requested []string
	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		requested = append(requested, request.Method+" "+request.URL.Path)
		if got := request.Header.Get("Authorization"); got != token {
			t.Errorf("Authorization header = %q", got)
		}
		if request.URL.RawQuery != "" {
			t.Errorf("unexpected query string %q", request.URL.RawQuery)
		}

		switch request.URL.Path {
		case "/api/v2/user":
			return fixtureResponse(http.StatusOK, `{"user":{"id":42,"username":"Local Developer","email":"ignored@example.test"}}`), nil
		case "/api/v2/team":
			return fixtureResponse(http.StatusOK, `{"teams":[{"id":"100","name":"Primary","members":[]},{"id":200,"name":"Secondary"}]}`), nil
		default:
			return fixtureResponse(http.StatusNotFound, `{}`), nil
		}
	})

	result, err := client.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	if got, want := strings.Join(requested, ","), "GET /api/v2/user,GET /api/v2/team"; got != want {
		t.Fatalf("requests = %q, want %q", got, want)
	}
	if result.Identity.ID != "42" || result.Identity.Name != "Local Developer" {
		t.Fatalf("Identity = %#v", result.Identity)
	}
	if len(result.Workspaces) != 2 || result.Workspaces[0].ID != "100" || result.Workspaces[1].ID != "200" {
		t.Fatalf("Workspaces = %#v", result.Workspaces)
	}
}

func TestAuthenticateClassifiesRejectedTokenAndDoesNotContinue(t *testing.T) {
	t.Parallel()

	const token = "pk_rejected_secret"
	requests := 0
	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		requests++
		body := fmt.Sprintf(`{"err":"token %s rejected","ECODE":"OAUTH_025"}`, token)
		return fixtureResponse(http.StatusUnauthorized, body), nil
	})

	_, err := client.Authenticate(context.Background(), token)
	if err == nil {
		t.Fatal("Authenticate() error = nil")
	}
	var clickUpError *Error
	if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorAuthentication {
		t.Fatalf("error = %#v, want authentication Error", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the failed user request", requests)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("error exposed the rejected token")
	}
}

func TestAuthenticateClassifiesWorkspaceAPIFailure(t *testing.T) {
	t.Parallel()

	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/api/v2/user" {
			return fixtureResponse(http.StatusOK, `{"user":{"id":1,"username":"User"}}`), nil
		}
		return fixtureResponse(http.StatusServiceUnavailable, `{"err":"maintenance","ECODE":"SERVICE_UNAVAILABLE"}`), nil
	})

	_, err := client.Authenticate(context.Background(), "token")
	var clickUpError *Error
	if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorAPI || clickUpError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %#v, want API Error with HTTP 503", err)
	}
	if !strings.Contains(err.Error(), "get authorized workspaces") {
		t.Fatalf("failure operation is not explicit: %v", err)
	}
}

func TestAuthenticateClassifiesRateLimit(t *testing.T) {
	t.Parallel()

	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		return fixtureResponse(http.StatusTooManyRequests, `{"err":"rate limited"}`), nil
	})

	_, err := client.Authenticate(context.Background(), "token")
	var clickUpError *Error
	if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorRateLimit {
		t.Fatalf("error = %#v, want rate-limit Error", err)
	}
}

func TestAuthenticateClassifiesNetworkFailure(t *testing.T) {
	t.Parallel()

	networkFailure := errors.New("offline")
	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		return nil, networkFailure
	})

	_, err := client.Authenticate(context.Background(), "token")
	var clickUpError *Error
	if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorNetwork || !errors.Is(err, networkFailure) {
		t.Fatalf("error = %#v, want network Error", err)
	}
}

func TestAuthenticateRejectsMalformedAndDuplicateWorkspaceResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
	}{
		{name: "malformed", response: `{"teams":`},
		{name: "missing teams", response: `{}`},
		{name: "null teams", response: `{"teams":null}`},
		{name: "duplicate ids", response: `{"teams":[{"id":"10","name":"A"},{"id":10,"name":"B"}]}`},
		{name: "missing id", response: `{"teams":[{"name":"A"}]}`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/api/v2/user" {
					return fixtureResponse(http.StatusOK, `{"user":{"id":1,"username":"User"}}`), nil
				}
				return fixtureResponse(http.StatusOK, test.response), nil
			})

			_, err := client.Authenticate(context.Background(), "token")
			var clickUpError *Error
			if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorResponse {
				t.Fatalf("error = %#v, want response Error", err)
			}
		})
	}
}

func TestAuthenticateRejectsMalformedCredentialBeforeRequest(t *testing.T) {
	t.Parallel()

	client := NewClient(nil)
	for _, token := range []string{"", "   ", "token\nheader"} {
		_, err := client.Authenticate(context.Background(), token)
		var clickUpError *Error
		if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorAuthentication {
			t.Fatalf("Authenticate(%q) error = %#v", token, err)
		}
	}
}

func TestGetRetriesRateLimitWithRecordedFailureThenRecordsRawSuccess(t *testing.T) {
	t.Parallel()

	requests := 0
	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			response := fixtureResponse(http.StatusTooManyRequests, `{"err":"slow down"}`)
			response.Header.Set("X-RateLimit-Reset", "110")
			return response, nil
		}
		return fixtureResponse(http.StatusOK, `{"value":"ok"}`), nil
	})
	evidence := &memoryEvidence{}
	client.evidence = evidence
	client.now = func() time.Time { return time.Unix(100, 0) }
	var delays []time.Duration
	client.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	var response struct {
		Value string `json:"value"`
	}
	if err := client.get(context.Background(), "secret", "/test", "test retry", &response); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || response.Value != "ok" {
		t.Fatalf("requests=%d response=%#v", requests, response)
	}
	if len(delays) != 1 || delays[0] != 10*time.Second {
		t.Fatalf("retry delays = %#v, want 10s rate-limit reset", delays)
	}
	if len(evidence.failures) != 1 || !evidence.failures[0].Retrying || evidence.failures[0].StatusCode != 429 {
		t.Fatalf("failure evidence = %#v", evidence.failures)
	}
	if len(evidence.responses) != 1 || evidence.responses[0].Attempt != 2 || string(evidence.responses[0].Body) != `{"value":"ok"}` {
		t.Fatalf("raw success evidence = %#v", evidence.responses)
	}
}

func TestGetRecordsSuccessfulRawBodyBeforeParseFailure(t *testing.T) {
	t.Parallel()

	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		return fixtureResponse(http.StatusOK, `{"broken":`), nil
	})
	evidence := &memoryEvidence{}
	client.evidence = evidence
	var response any
	err := client.get(context.Background(), "secret", "/test", "test malformed response", &response)
	if err == nil {
		t.Fatal("get() accepted malformed JSON")
	}
	if len(evidence.responses) != 1 || string(evidence.responses[0].Body) != `{"broken":` {
		t.Fatalf("malformed raw response was not preserved: %#v", evidence.responses)
	}
}
