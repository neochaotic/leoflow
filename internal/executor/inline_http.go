package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

var defaultSuccessCodes = map[int]bool{200: true, 201: true, 202: true, 204: true}

// InlineHTTPExecutor runs http_api tasks in-process (no pod, no agent), with
// retries and a per-request timeout.
type InlineHTTPExecutor struct {
	client     *http.Client
	maxRetries int
}

// NewInlineHTTPExecutor builds an executor with the given underlying client
// (nil uses a default) and retry count.
func NewInlineHTTPExecutor(client *http.Client, maxRetries int) *InlineHTTPExecutor {
	if client == nil {
		client = &http.Client{}
	}
	return &InlineHTTPExecutor{client: client, maxRetries: maxRetries}
}

// Execute performs the request and returns nil on a success status, retrying
// transient failures with exponential backoff.
func (e *InlineHTTPExecutor) Execute(ctx context.Context, req Request) error {
	_, err := e.Run(ctx, req)
	return err
}

// Run performs the request like Execute but also returns the response body of
// the successful attempt, so callers can capture it as an XCom value.
func (e *InlineHTTPExecutor) Run(ctx context.Context, req Request) ([]byte, error) {
	hr := req.HTTPRequest
	if hr == nil {
		return nil, errors.New("http_api task has no http_request")
	}
	success := successCodes(hr.SuccessStatusCodes)

	var lastErr error
	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}
		status, body, err := e.do(ctx, hr)
		if err != nil {
			lastErr = err
			continue
		}
		if success[status] {
			return body, nil
		}
		lastErr = fmt.Errorf("http %s %s returned status %d", hr.Method, hr.URL, status)
	}
	return nil, fmt.Errorf("http_api task failed after %d attempts: %w", e.maxRetries+1, lastErr)
}

func (e *InlineHTTPExecutor) do(ctx context.Context, hr *domain.HTTPRequest) (status int, body []byte, err error) {
	timeout := time.Duration(hr.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if hr.Body != nil {
		encoded, merr := json.Marshal(hr.Body)
		if merr != nil {
			return 0, nil, fmt.Errorf("encoding request body: %w", merr)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	httpReq, err := http.NewRequestWithContext(rctx, hr.Method, hr.URL, bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	for k, v := range hr.Headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return 0, nil, fmt.Errorf("performing request: %w", err)
	}
	// Cap the read so a huge response cannot exhaust memory; oversize XComs are
	// rejected downstream by the 256KB limit.
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, responseReadLimit))
	cerr := resp.Body.Close()
	if rerr != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading response body: %w", rerr)
	}
	if cerr != nil {
		return resp.StatusCode, nil, fmt.Errorf("closing response body: %w", cerr)
	}
	return resp.StatusCode, data, nil
}

// responseReadLimit caps how much of an http_api response body is read; values
// beyond the XCom size limit are rejected when pushed.
const responseReadLimit = 1 << 20 // 1 MB

func successCodes(codes []int) map[int]bool {
	if len(codes) == 0 {
		return defaultSuccessCodes
	}
	out := make(map[int]bool, len(codes))
	for _, c := range codes {
		out[c] = true
	}
	return out
}

func backoff(attempt int) time.Duration {
	return (1 << (attempt - 1)) * 100 * time.Millisecond
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
