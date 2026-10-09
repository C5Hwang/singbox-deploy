package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func releaseMetadataJSON(asset string, content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf(`{"assets":[{"name":%q,"digest":"sha256:%s"}]}`, asset, hex.EncodeToString(sum[:]))
}

func TestFetchSingBoxArchiveVerifiesUpstreamDigest(t *testing.T) {
	const tag = "v1.12.4"
	name := SingBoxArchiveName(tag, "linux", "arm64")
	content := []byte("archive bytes")
	for _, tc := range []struct {
		name     string
		metadata string
		wantErr  string
	}{
		{name: "match", metadata: releaseMetadataJSON(name, content)},
		{name: "mismatch", metadata: releaseMetadataJSON(name, []byte("other")), wantErr: "checksum mismatch"},
		{name: "missing asset", metadata: releaseMetadataJSON("other.tar.gz", content), wantErr: "no digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var urls []string
			download := func(_ context.Context, url, dest string) error {
				urls = append(urls, url)
				switch url {
				case SingBoxArchiveURL(tag, name):
					return os.WriteFile(dest, content, 0o600)
				case SingBoxReleaseMetadataURL(tag):
					return os.WriteFile(dest, []byte(tc.metadata), 0o600)
				}
				return fmt.Errorf("unexpected url %s", url)
			}
			archive, err := FetchSingBoxArchive(context.Background(), download, tag, "linux", "arm64", t.TempDir())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchSingBoxArchive: %v", err)
			}
			sum := sha256.Sum256(content)
			if archive.Name != name || archive.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("archive = %+v", archive)
			}
			if len(urls) != 2 {
				t.Fatalf("downloaded %v", urls)
			}
		})
	}
}

func TestStallGuardedDownloaderAbortsSilentTransfer(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first chunk"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	download := NewStallGuardedDownloader(150 * time.Millisecond)
	start := time.Now()
	err := download(context.Background(), server.URL, t.TempDir()+"/out")
	if err == nil || !strings.Contains(err.Error(), "no data received") {
		t.Fatalf("error = %v, want stall", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stall detection took %s", elapsed)
	}
}

func TestStallGuardedDownloaderKeepsSteadyTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// The whole transfer outlasts the idle window, but no gap does.
		for i := 0; i < 6; i++ {
			_, _ = w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer server.Close()

	dest := t.TempDir() + "/out"
	if err := NewStallGuardedDownloader(150*time.Millisecond)(context.Background(), server.URL, dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != strings.Repeat("chunk", 6) {
		t.Fatalf("body = %q, err = %v", body, err)
	}
}
