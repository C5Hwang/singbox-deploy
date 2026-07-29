// Package release resolves GitHub release assets and fetches stable release
// tags for sing-box core management and singbox-deploy self-update.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const githubReleasePageSize = 30

// SingBoxArchiveName returns the upstream sing-box archive name for a release
// tag and OS/arch, e.g. sing-box-1.12.0-linux-amd64.tar.gz.
func SingBoxArchiveName(tag, goos, goarch string) string {
	version := strings.TrimPrefix(tag, "v")
	return "sing-box-" + version + "-" + goos + "-" + goarch + ".tar.gz"
}

// SafeTag sanitizes a release tag for use in a filename.
func SafeTag(tag string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "..", "-")
	return replacer.Replace(tag)
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

// listReleasesPage fetches one releases page (newest first, as GitHub returns
// it). GitHub's default page is frequently dominated by prereleases, so stable
// release discovery must be able to continue beyond it.
func (c *Client) listReleasesPage(ctx context.Context, owner, repo string, page int) ([]ghRelease, error) {
	endpoint, err := url.Parse(fmt.Sprintf("%s/repos/%s/%s/releases", c.baseURL, owner, repo))
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("per_page", strconv.Itoa(githubReleasePageSize))
	query.Set("page", strconv.Itoa(page))
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
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

// stableTags returns up to limit non-draft, non-prerelease tags in GitHub
// order. A non-positive limit returns every stable tag.
func (c *Client) stableTags(ctx context.Context, owner, repo string, limit int) ([]string, error) {
	var tags []string
	for page := 1; ; page++ {
		releases, err := c.listReleasesPage(ctx, owner, repo, page)
		if err != nil {
			return nil, err
		}
		for _, r := range releases {
			if r.Draft || r.Prerelease || r.TagName == "" {
				continue
			}
			tags = append(tags, r.TagName)
			if limit > 0 && len(tags) == limit {
				return tags, nil
			}
		}
		if len(releases) < githubReleasePageSize {
			return tags, nil
		}
	}
}

// LatestStable returns the newest non-draft, non-prerelease release tag.
func (c *Client) LatestStable(ctx context.Context, owner, repo string) (string, error) {
	tags, err := c.stableTags(ctx, owner, repo, 1)
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
	return c.stableTags(ctx, owner, repo, n)
}
