// Package release resolves GitHub release assets and fetches stable release
// tags for sing-box core management and singbox-deploy self-update.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// SingBoxArchiveName returns the upstream sing-box archive name for a release
// tag and OS/arch, e.g. sing-box-1.12.0-linux-amd64.tar.gz.
func SingBoxArchiveName(tag, goos, goarch string) string {
	version := strings.TrimPrefix(tag, "v")
	return "sing-box-" + version + "-" + goos + "-" + goarch + ".tar.gz"
}

// Client talks to the GitHub REST API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client. baseURL defaults to the public GitHub API when
// empty; httpClient defaults to http.DefaultClient when nil.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// listReleases fetches the releases list (newest first, as GitHub returns it).
func (c *Client) listReleases(ctx context.Context, owner, repo string) ([]ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases", c.baseURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases: status %d", resp.StatusCode)
	}
	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// stableTags returns the non-draft, non-prerelease tags in GitHub order.
func (c *Client) stableTags(ctx context.Context, owner, repo string) ([]string, error) {
	releases, err := c.listReleases(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, r := range releases {
		if r.Draft || r.Prerelease || r.TagName == "" {
			continue
		}
		tags = append(tags, r.TagName)
	}
	return tags, nil
}

// LatestStable returns the newest non-draft, non-prerelease release tag.
func (c *Client) LatestStable(ctx context.Context, owner, repo string) (string, error) {
	tags, err := c.stableTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("no stable releases found for %s/%s", owner, repo)
	}
	return tags[0], nil
}

// StableReleases returns up to n newest stable tags.
func (c *Client) StableReleases(ctx context.Context, owner, repo string, n int) ([]string, error) {
	tags, err := c.stableTags(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if n > 0 && len(tags) > n {
		tags = tags[:n]
	}
	return tags, nil
}
