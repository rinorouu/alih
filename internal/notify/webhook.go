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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"alih/internal/event"
)

// Reason is the stable identity of a delivery outcome. It is deliberately its
// own vocabulary: a webhook that is down says nothing about the health of the
// source Alih backs up, and the two must never be confused in a report.
type Reason string

const (
	ReasonDelivered             Reason = "DELIVERED"
	ReasonRejected              Reason = "REJECTED"
	ReasonRateLimited           Reason = "RATE_LIMITED"
	ReasonDestinationDown       Reason = "DESTINATION_UNAVAILABLE"
	ReasonNetworkFailure        Reason = "NETWORK_FAILURE"
	ReasonTimeout               Reason = "TIMEOUT"
	ReasonRedirectRefused       Reason = "REDIRECT_REFUSED"
	ReasonInvalidResponse       Reason = "INVALID_RESPONSE"
	ReasonSecretMissing         Reason = "SECRET_MISSING"
	ReasonPayloadNotDeliverable Reason = "PAYLOAD_NOT_DELIVERABLE"
	ReasonCancelled             Reason = "CANCELLED"
)

const (
	maxPayloadBytes  = 64 * 1024
	maxResponseBytes = 4 * 1024
	retryBaseDelay   = 500 * time.Millisecond
	maxRetryDelay    = 5 * time.Second
	payloadKind      = "alih.notification"
)

// Result is what one delivery attempt sequence established. Message is Alih's
// own bounded wording; a destination's response body is never read into it.
type Result struct {
	DestinationID  string    `json:"destination_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Delivered      bool      `json:"delivered"`
	Attempts       int       `json:"attempts"`
	StatusCode     int       `json:"status_code,omitempty"`
	Reason         Reason    `json:"reason"`
	Retryable      bool      `json:"retryable"`
	Message        string    `json:"message"`
	ObservedAt     time.Time `json:"observed_at"`
}

// Notifier delivers one event to one destination.
type Notifier interface {
	Deliver(ctx context.Context, destination Destination, e event.Event) Result
}

// Payload is exactly what a destination receives: the recorded event, plus the
// key that lets the destination recognise a repeat of it.
type Payload struct {
	SchemaVersion  int         `json:"schema_version"`
	Kind           string      `json:"kind"`
	IdempotencyKey string      `json:"idempotency_key"`
	AlihVersion    string      `json:"alih_version,omitempty"`
	Event          event.Event `json:"event"`
}

// IdempotencyKey identifies one transition, so a destination can discard a
// repeat. Alih cannot enforce this: a destination that ignores the key will
// process a retried notification twice, and that is the destination's contract
// to honour, not something Alih can promise on its behalf.
func IdempotencyKey(e event.Event) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		strconv.Itoa(SchemaVersion), e.OperationID, strconv.Itoa(e.Sequence), string(e.Type),
	}, "\x00")))
	return hex.EncodeToString(digest[:16])
}

// WebhookNotifier posts events to an HTTPS endpoint.
//
// It never follows a redirect: a redirect is the classic way to move a request,
// and the credential it carries, to a host the user never configured. It reads
// only a bounded prefix of any response and never records the body.
type WebhookNotifier struct {
	client      *http.Client
	lookupEnv   func(string) (string, bool)
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	alihVersion string
}

// NewWebhookNotifier builds the notifier. Every external dependency is injected
// so that delivery can be tested without a network.
func NewWebhookNotifier(client *http.Client, lookupEnv func(string) (string, bool), now func() time.Time, alihVersion string) *WebhookNotifier {
	if lookupEnv == nil {
		lookupEnv = osLookupEnv
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &WebhookNotifier{
		client: client, lookupEnv: lookupEnv, now: now,
		sleep: sleepContext, alihVersion: alihVersion,
	}
}

// Deliver sends one event, retrying only failures that a retry could fix, and
// only within the destination's bounded attempt count.
func (n *WebhookNotifier) Deliver(ctx context.Context, destination Destination, e event.Event) Result {
	key := IdempotencyKey(e)
	result := Result{
		DestinationID: destination.ID, IdempotencyKey: key,
		Reason: ReasonPayloadNotDeliverable, ObservedAt: n.now().UTC(),
	}
	if err := validateDestination(destination); err != nil {
		result.Message = "The notification destination is not valid."
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Reason = ReasonCancelled
		result.Message = "Delivery was interrupted before it started."
		return result
	}
	if err := event.Validate(e); err != nil {
		result.Message = "The event could not be sent because it is not a valid record."
		return result
	}

	secret := ""
	if destination.SecretEnv != "" {
		value, present := n.lookupEnv(destination.SecretEnv)
		if !present || strings.TrimSpace(value) == "" {
			result.Reason = ReasonSecretMissing
			result.Message = "The environment variable named by secret_env is not set."
			return result
		}
		if strings.ContainsAny(value, "\r\n") {
			result.Reason = ReasonSecretMissing
			result.Message = "The configured secret is not usable as an HTTP header value."
			return result
		}
		secret = value
	}

	body, err := json.Marshal(Payload{
		SchemaVersion: SchemaVersion, Kind: payloadKind, IdempotencyKey: key,
		AlihVersion: n.alihVersion, Event: e,
	})
	if err != nil || len(body) > maxPayloadBytes {
		result.Message = "The notification payload could not be prepared within its size bound."
		return result
	}

	attempts := destination.Attempts()
	for attempt := 1; attempt <= attempts; attempt++ {
		result.Attempts = attempt
		result.ObservedAt = n.now().UTC()
		delivered, statusCode, reason, retryable, message := n.attempt(ctx, destination, secret, key, body)
		result.StatusCode, result.Reason, result.Retryable, result.Message = statusCode, reason, retryable, message
		if delivered {
			result.Delivered = true
			return result
		}
		if !retryable || attempt == attempts {
			return result
		}
		if err := n.sleep(ctx, backoff(attempt)); err != nil {
			result.Reason, result.Retryable = ReasonCancelled, false
			result.Message = "Delivery was interrupted before it could be retried."
			return result
		}
	}
	return result
}

func (n *WebhookNotifier) attempt(ctx context.Context, destination Destination, secret, key string, body []byte) (bool, int, Reason, bool, string) {
	requestCtx, cancel := context.WithTimeout(ctx, destination.Timeout())
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, destination.URL, bytes.NewReader(body))
	if err != nil {
		return false, 0, ReasonPayloadNotDeliverable, false, "The destination URL could not be used for a request."
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "alih-v0")
	request.Header.Set("Idempotency-Key", key)
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}

	response, err := n.httpClient(destination).Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return false, 0, ReasonTimeout, true, "The destination did not respond within the configured timeout."
		}
		if errors.Is(err, context.Canceled) {
			return false, 0, ReasonCancelled, false, "Delivery was interrupted."
		}
		if errors.Is(err, errRedirectRefused) {
			return false, 0, ReasonRedirectRefused, false,
				"The destination redirected the request; Alih does not follow redirects for notifications."
		}
		return false, 0, ReasonNetworkFailure, true, "The destination could not be reached."
	}
	defer response.Body.Close()
	// The response body is read only to drain a bounded prefix so the
	// connection can be reused. Its content is never inspected or recorded.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return true, response.StatusCode, ReasonDelivered, false, "The destination accepted the notification."
	case response.StatusCode == http.StatusTooManyRequests:
		return false, response.StatusCode, ReasonRateLimited, true, "The destination is rate limiting notifications."
	case response.StatusCode >= 500:
		return false, response.StatusCode, ReasonDestinationDown, true, "The destination reported a server error."
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return false, response.StatusCode, ReasonRedirectRefused, false,
			"The destination redirected the request; Alih does not follow redirects for notifications."
	case response.StatusCode >= 400:
		return false, response.StatusCode, ReasonRejected, false, "The destination rejected the notification."
	default:
		return false, response.StatusCode, ReasonInvalidResponse, false, "The destination returned an unusable response."
	}
}

func (n *WebhookNotifier) httpClient(destination Destination) *http.Client {
	if n.client != nil {
		// Copy the injected client so tests and embedders may provide a custom
		// transport without being able to weaken the redirect boundary.
		client := *n.client
		client.CheckRedirect = refuseRedirect
		if client.Timeout <= 0 || client.Timeout > destination.Timeout() {
			client.Timeout = destination.Timeout()
		}
		return &client
	}
	return &http.Client{
		Timeout:       destination.Timeout(),
		CheckRedirect: refuseRedirect,
	}
}

var errRedirectRefused = errors.New("redirect refused")

func refuseRedirect(*http.Request, []*http.Request) error { return errRedirectRefused }

func backoff(attempt int) time.Duration {
	delay := retryBaseDelay << (attempt - 1)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
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

// osLookupEnv is the default secret source: the environment of the process the
// user started, never a file this package invents.
func osLookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

// Describe renders a delivery result for people, with nothing in it that a
// destination controls.
func (r Result) Describe() string {
	if r.Delivered {
		return fmt.Sprintf("%s: delivered after %d attempt(s)", r.DestinationID, r.Attempts)
	}
	return fmt.Sprintf("%s: not delivered after %d attempt(s) (%s)", r.DestinationID, r.Attempts, r.Reason)
}
