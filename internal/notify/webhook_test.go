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

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"alih/internal/event"
	"alih/internal/state"
)

var notifyTestTime = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func notifyTestEvent() event.Event {
	return event.Event{
		SchemaVersion: event.SchemaVersion,
		Type:          event.TypeOperationFailed,
		OperationID:   "20260901T080000Z-0badc0de",
		Sequence:      2,
		RecordedAt:    notifyTestTime,
		Source: event.Source{
			Connector: "clickup", WorkspaceID: "100", Destination: testAbsolutePath("tmp", "Alih"),
		},
		Operation:   state.OperationBackup,
		Stage:       state.StageScan,
		Outcome:     event.OutcomeFailed,
		Message:     "The run failed while inventorying the source.",
		Metadata:    map[string]string{"reason": "NETWORK_FAILURE"},
		AlihVersion: "dev",
	}
}

func webhookDestination(url string) Destination {
	return Destination{
		ID: "ops", Enabled: true, Type: TypeWebhook, URL: url,
		Events: []string{string(event.TypeOperationFailed)}, MaxAttempts: 1,
	}
}

func TestWebhookDeliversTheStableEnvelopeAndBearerSecret(t *testing.T) {
	t.Parallel()
	secret := "notification-secret-value"
	var received Payload
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %s", request.Method, request.Header.Get("Content-Type"))
		}
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("authorization header was not supplied")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		return response(http.StatusNoContent), nil
	})}

	destination := webhookDestination("https://hooks.example.invalid/delivery")
	destination.SecretEnv = "ALIH_NOTIFY_TEST_TOKEN"
	notifier := NewWebhookNotifier(client, func(name string) (string, bool) {
		return secret, name == destination.SecretEnv
	}, func() time.Time { return notifyTestTime }, "0.3.0")
	result := notifier.Deliver(context.Background(), destination, notifyTestEvent())

	if !result.Delivered || result.Reason != ReasonDelivered || result.Attempts != 1 || result.StatusCode != 204 {
		t.Fatalf("result = %#v", result)
	}
	if received.Kind != payloadKind || received.SchemaVersion != SchemaVersion ||
		received.IdempotencyKey != result.IdempotencyKey || received.AlihVersion != "0.3.0" {
		t.Fatalf("payload = %#v", received)
	}
	encoded, _ := json.Marshal(received)
	if strings.Contains(string(encoded), secret) || strings.Contains(result.Describe(), secret) {
		t.Fatal("a rendered delivery surface exposed the bearer secret")
	}
}

func TestWebhookRetriesOnlyRetryableFailuresWithOneIdempotencyKey(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var keys []string
	responses := []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusAccepted}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		keys = append(keys, request.Header.Get("Idempotency-Key"))
		return response(responses[len(keys)-1]), nil
	})}

	destination := webhookDestination("https://hooks.example.invalid/delivery")
	destination.MaxAttempts = 3
	notifier := NewWebhookNotifier(client, nil, func() time.Time { return notifyTestTime }, "dev")
	var delays []time.Duration
	notifier.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	result := notifier.Deliver(context.Background(), destination, notifyTestEvent())
	if !result.Delivered || result.Attempts != 3 || len(delays) != 2 {
		t.Fatalf("result = %#v, delays = %v", result, delays)
	}
	for _, key := range keys {
		if key != result.IdempotencyKey {
			t.Fatalf("retry changed idempotency key: %q != %q", key, result.IdempotencyKey)
		}
	}
}

func TestWebhookDoesNotRetryPermanentRejectionOrMissingSecret(t *testing.T) {
	t.Parallel()
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return response(http.StatusBadRequest), nil
	})}

	destination := webhookDestination("https://hooks.example.invalid/delivery")
	destination.MaxAttempts = 5
	notifier := NewWebhookNotifier(client, nil, func() time.Time { return notifyTestTime }, "dev")
	notifier.sleep = func(context.Context, time.Duration) error {
		t.Fatal("permanent rejection was retried")
		return nil
	}
	result := notifier.Deliver(context.Background(), destination, notifyTestEvent())
	if result.Reason != ReasonRejected || result.Retryable || requests != 1 {
		t.Fatalf("result = %#v, requests = %d", result, requests)
	}

	destination.SecretEnv = "ALIH_NOTIFY_MISSING"
	missing := NewWebhookNotifier(client, func(string) (string, bool) { return "", false },
		func() time.Time { return notifyTestTime }, "dev").Deliver(context.Background(), destination, notifyTestEvent())
	if missing.Reason != ReasonSecretMissing || missing.Attempts != 0 || requests != 1 {
		t.Fatalf("missing-secret result = %#v, requests = %d", missing, requests)
	}
}

func TestInjectedClientStillCannotFollowRedirects(t *testing.T) {
	t.Parallel()
	targetCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "target.example.invalid" {
			targetCalls++
			return response(http.StatusNoContent), nil
		}
		redirect := response(http.StatusTemporaryRedirect)
		redirect.Header.Set("Location", "https://target.example.invalid/delivery")
		return redirect, nil
	})}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	result := NewWebhookNotifier(client, nil, func() time.Time { return notifyTestTime }, "dev").
		Deliver(context.Background(), webhookDestination("https://redirect.example.invalid/delivery"), notifyTestEvent())
	if result.Reason != ReasonRedirectRefused || result.Retryable || targetCalls != 0 {
		t.Fatalf("result = %#v, target calls = %d", result, targetCalls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func response(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d status", status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    &http.Request{URL: &url.URL{}},
	}
}

func TestWebhookTimeoutAndCancellationAreBounded(t *testing.T) {
	t.Parallel()
	destination := webhookDestination("https://hooks.example.invalid/test")
	destination.TimeoutSeconds = 1
	destination.MaxAttempts = 2
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	notifier := NewWebhookNotifier(client, nil, func() time.Time { return notifyTestTime }, "dev")
	notifier.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	result := notifier.Deliver(context.Background(), destination, notifyTestEvent())
	if result.Reason != ReasonCancelled || result.Attempts != 1 || result.Retryable {
		t.Fatalf("result = %#v", result)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result = notifier.Deliver(cancelled, destination, notifyTestEvent())
	if result.Reason != ReasonCancelled || result.Attempts != 0 {
		t.Fatalf("pre-cancelled result = %#v", result)
	}
}

type countingBody struct{ reads int }

func (body *countingBody) Read(buffer []byte) (int, error) {
	body.reads += len(buffer)
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}
func (*countingBody) Close() error { return nil }

func TestWebhookReadsOnlyABoundedResponsePrefix(t *testing.T) {
	t.Parallel()
	body := &countingBody{}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusAccepted, Body: body, Header: make(http.Header)}, nil
	})}
	result := NewWebhookNotifier(client, nil, func() time.Time { return notifyTestTime }, "dev").
		Deliver(context.Background(), webhookDestination("https://hooks.example.invalid/test"), notifyTestEvent())
	if !result.Delivered {
		t.Fatalf("result = %#v", result)
	}
	if body.reads > maxResponseBytes {
		t.Fatalf("response reader consumed %d bytes, bound is %d", body.reads, maxResponseBytes)
	}
}

func TestWebhookRejectsInvalidEventAndHeaderSecretWithoutEgress(t *testing.T) {
	t.Parallel()
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected egress")
	})}
	destination := webhookDestination("https://hooks.example.invalid/test")
	notifier := NewWebhookNotifier(client, func(string) (string, bool) { return "secret\r\nInjected: yes", true },
		func() time.Time { return notifyTestTime }, "dev")
	destination.SecretEnv = "ALIH_NOTIFY_TOKEN"
	result := notifier.Deliver(context.Background(), destination, notifyTestEvent())
	if result.Reason != ReasonSecretMissing || calls != 0 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}

	invalid := notifyTestEvent()
	invalid.Message = ""
	destination.SecretEnv = ""
	result = notifier.Deliver(context.Background(), destination, invalid)
	if result.Reason != ReasonPayloadNotDeliverable || calls != 0 {
		t.Fatalf("invalid-event result = %#v, calls = %d", result, calls)
	}
}

var _ io.ReadCloser = (*countingBody)(nil)

// testAbsolutePath builds a deterministic absolute path that satisfies
// filepath.IsAbs on every supported platform. Scope identity is compared as a
// string, so the value must not vary by machine, but Windows does not consider
// a rooted path absolute unless it names a volume. Hard-coded POSIX literals
// therefore failed validation on Windows while passing everywhere else.
func testAbsolutePath(elements ...string) string {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, elements...)...)
}
