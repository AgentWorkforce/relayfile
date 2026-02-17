package relayfile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type NotionAccessTokenProvider func(ctx context.Context) (string, error)

type NotionHTTPClientOptions struct {
	BaseURL       string
	TokenProvider NotionAccessTokenProvider
	HTTPClient    *http.Client
	APIVersion    string
	UserAgent     string
	MaxRetries    int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
}

type HTTPNotionWriteClient struct {
	baseURL       string
	tokenProvider NotionAccessTokenProvider
	httpClient    *http.Client
	apiVersion    string
	userAgent     string
	maxRetries    int
	baseDelay     time.Duration
	maxDelay      time.Duration
}

func NewHTTPNotionWriteClient(opts NotionHTTPClientOptions) *HTTPNotionWriteClient {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.notion.com"
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	apiVersion := strings.TrimSpace(opts.APIVersion)
	if apiVersion == "" {
		apiVersion = "2022-06-28"
	}
	maxRetries := opts.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	baseDelay := opts.BaseDelay
	if baseDelay <= 0 {
		baseDelay = 100 * time.Millisecond
	}
	maxDelay := opts.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 2 * time.Second
	}
	return &HTTPNotionWriteClient{
		baseURL:       baseURL,
		tokenProvider: opts.TokenProvider,
		httpClient:    httpClient,
		apiVersion:    apiVersion,
		userAgent:     strings.TrimSpace(opts.UserAgent),
		maxRetries:    maxRetries,
		baseDelay:     baseDelay,
		maxDelay:      maxDelay,
	}
}

func (c *HTTPNotionWriteClient) UpsertPage(ctx context.Context, req NotionUpsertRequest) error {
	return c.doWrite(ctx, "/v1/notion/pages/upsert", req.CorrelationID, req)
}

func (c *HTTPNotionWriteClient) DeletePage(ctx context.Context, req NotionDeleteRequest) error {
	return c.doWrite(ctx, "/v1/notion/pages/delete", req.CorrelationID, req)
}

func (c *HTTPNotionWriteClient) doWrite(ctx context.Context, path, correlationID string, payload any) error {
	if c == nil {
		return fmt.Errorf("notion http client is nil")
	}
	tokenProvider := c.tokenProvider
	if tokenProvider == nil {
		return fmt.Errorf("notion token provider is required")
	}
	token, err := tokenProvider(ctx)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("notion token is empty")
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := c.baseURL + path
	if correlationID == "" {
		correlationID = fmt.Sprintf("notion_%d", time.Now().UnixNano())
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Notion-Version", c.apiVersion)
		req.Header.Set("X-Correlation-Id", correlationID)
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < c.maxRetries {
				if waitErr := sleepContext(ctx, c.retryDelay(attempt+1, "")); waitErr != nil {
					return waitErr
				}
				continue
			}
			return err
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return nil
		}

		if (resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode <= 599)) && attempt < c.maxRetries {
			if waitErr := sleepContext(ctx, c.retryDelay(attempt+1, resp.Header.Get("Retry-After"))); waitErr != nil {
				return waitErr
			}
			continue
		}

		errCode := ""
		errMessage := strings.TrimSpace(string(respBody))
		var parsed map[string]any
		if json.Unmarshal(respBody, &parsed) == nil {
			if code, ok := parsed["code"].(string); ok {
				errCode = code
			}
			if message, ok := parsed["message"].(string); ok && strings.TrimSpace(message) != "" {
				errMessage = message
			}
		}
		if errCode != "" {
			return fmt.Errorf("notion write failed: status=%d code=%s message=%s", resp.StatusCode, errCode, errMessage)
		}
		return fmt.Errorf("notion write failed: status=%d message=%s", resp.StatusCode, errMessage)
	}
}

func (c *HTTPNotionWriteClient) retryDelay(attempt int, retryAfterHeader string) time.Duration {
	if retryAfter := parseRetryAfterSeconds(retryAfterHeader); retryAfter > 0 {
		if retryAfter > c.maxDelay {
			return c.maxDelay
		}
		return retryAfter
	}
	delay := c.baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= c.maxDelay {
			return c.maxDelay
		}
	}
	if delay > c.maxDelay {
		return c.maxDelay
	}
	return delay
}

func parseRetryAfterSeconds(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
