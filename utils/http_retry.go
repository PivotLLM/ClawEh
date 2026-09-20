package utils

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const maxRetries = 3

var retryDelayUnit = time.Second

func shouldRetry(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode >= 500
}

func DoRequestWithRetry(client *http.Client, req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for i := range maxRetries {
		if i > 0 && resp != nil {
			CloseQuietly(resp.Body)
		}

		resp, err = client.Do(req) //nolint:gosec // URL is built from the ClawHub registry / GitHub raw path by skills.installer
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				break
			}
			if !shouldRetry(resp.StatusCode) {
				break
			}
		}

		if i < maxRetries-1 {
			if err = sleepWithCtx(req.Context(), retryDelayUnit*time.Duration(i+1)); err != nil {
				if resp != nil {
					CloseQuietly(resp.Body)
				}
				return nil, fmt.Errorf("failed to sleep: %w", err)
			}
		}
	}
	return resp, err
}

func sleepWithCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
