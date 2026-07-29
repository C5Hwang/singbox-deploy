package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
)

func TestSingBoxArchiveName(t *testing.T) {
	got := SingBoxArchiveName("v1.12.0", "linux", "amd64")
	want := "sing-box-1.12.0-linux-amd64.tar.gz"
	if got != want {
		t.Fatalf("archive = %q, want %q", got, want)
	}
}

const releasesJSON = `[
  {"tag_name": "v1.13.0-alpha.1", "prerelease": true, "draft": false},
  {"tag_name": "v1.12.4", "prerelease": false, "draft": false},
  {"tag_name": "v1.12.3", "prerelease": false, "draft": false},
  {"tag_name": "v1.12.2", "prerelease": false, "draft": true},
  {"tag_name": "v1.12.1", "prerelease": false, "draft": false},
  {"tag_name": "v1.12.0", "prerelease": false, "draft": false},
  {"tag_name": "v1.11.9", "prerelease": false, "draft": false}
]`

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/SagerNet/sing-box/releases" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasesJSON))
	}))
}

func TestLatestStableSkipsPrereleaseAndDraft(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	tag, err := c.LatestStable(context.Background(), "SagerNet", "sing-box")
	if err != nil {
		t.Fatalf("LatestStable error: %v", err)
	}
	if tag != "v1.12.4" {
		t.Fatalf("LatestStable = %q, want v1.12.4", tag)
	}
}

func TestStableReleasesReturnsTopN(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	tags, err := c.StableReleases(context.Background(), "SagerNet", "sing-box", 5)
	if err != nil {
		t.Fatalf("StableReleases error: %v", err)
	}
	want := []string{"v1.12.4", "v1.12.3", "v1.12.1", "v1.12.0", "v1.11.9"}
	if len(tags) != len(want) {
		t.Fatalf("got %d tags: %v", len(tags), tags)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("tags[%d] = %q, want %q", i, tags[i], want[i])
		}
	}
}

func TestStableReleasesContinuesPastPrereleaseHeavyFirstPage(t *testing.T) {
	var requestedPages []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/SagerNet/sing-box/releases" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("per_page"); got != strconv.Itoa(githubReleasePageSize) {
			t.Errorf("per_page = %q, want %d", got, githubReleasePageSize)
		}
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			t.Errorf("invalid page query: %v", err)
		}
		requestedPages = append(requestedPages, page)

		var releases []ghRelease
		switch page {
		case 1:
			releases = append(releases,
				ghRelease{TagName: "v1.14.0-beta.2", Prerelease: true},
				ghRelease{TagName: "v1.13.14"},
				ghRelease{TagName: "v1.13.13"},
			)
			for i := len(releases); i < githubReleasePageSize; i++ {
				releases = append(releases, ghRelease{
					TagName:    fmt.Sprintf("v1.14.0-alpha.%d", githubReleasePageSize-i),
					Prerelease: true,
				})
			}
		case 2:
			for patch := 12; patch >= 7; patch-- {
				releases = append(releases, ghRelease{TagName: fmt.Sprintf("v1.13.%d", patch)})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(releases); err != nil {
			t.Errorf("encode releases: %v", err)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	tags, err := c.StableReleases(context.Background(), "SagerNet", "sing-box", 8)
	if err != nil {
		t.Fatalf("StableReleases error: %v", err)
	}
	want := []string{
		"v1.13.14", "v1.13.13", "v1.13.12", "v1.13.11",
		"v1.13.10", "v1.13.9", "v1.13.8", "v1.13.7",
	}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	if !reflect.DeepEqual(requestedPages, []int{1, 2}) {
		t.Fatalf("requested pages = %v, want [1 2]", requestedPages)
	}
}
