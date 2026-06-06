package push

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
)

// sendUnifiedPush POSTs payload to a UnifiedPush endpoint URL.
// The token registered by noscall Android IS the endpoint URL, so we forward
// the raw relay callback body directly.
func sendUnifiedPush(ctx context.Context, endpointURL string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post to UnifiedPush endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("UnifiedPush endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}
