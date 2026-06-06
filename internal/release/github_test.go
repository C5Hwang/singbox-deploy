package release

import (
	"context"
	"net/http"
	"net/http/httptest"
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
