package monitor

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

var networkTimeURLs = []string{
	"https://www.cloudflare.com/",
	"https://www.google.com/generate_204",
	"https://www.microsoft.com/",
}

// NetworkGMTNow fetches a trusted GMT timestamp from the HTTP Date header of a
// well-known HTTPS endpoint. It intentionally avoids timezone APIs and uses the
// RFC-defined server date that every compliant HTTP response should include.
func NetworkGMTNow(ctx context.Context) (time.Time, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for _, url := range networkTimeURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		date := resp.Header.Get("Date")
		_ = resp.Body.Close()
		if date == "" {
			lastErr = fmt.Errorf("%s missing Date header", url)
			continue
		}
		parsed, err := http.ParseTime(date)
		if err != nil {
			lastErr = fmt.Errorf("parse Date header from %s: %w", url, err)
			continue
		}
		return parsed.UTC(), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no network time endpoints configured")
	}
	return time.Time{}, lastErr
}

// NetworkClock keeps a monotonic local offset from the fetched GMT time. The
// monitor can then use GMT consistently without querying the network per sample.
type NetworkClock struct {
	offset time.Duration
}

func NewNetworkClock(ctx context.Context) (*NetworkClock, error) {
	remote, err := NetworkGMTNow(ctx)
	if err != nil {
		return nil, err
	}
	return &NetworkClock{offset: remote.Sub(time.Now())}, nil
}

func (c *NetworkClock) Now() time.Time {
	if c == nil {
		return time.Now().UTC()
	}
	return time.Now().Add(c.offset).UTC()
}
