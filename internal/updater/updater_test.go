package appupdater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestGitHubAuthSourceManifestAndAssetOpen(t *testing.T) {
	manifestName := fmt.Sprintf("update-%s-%s.json", runtime.GOOS, runtime.GOARCH)
	archiveName := fmt.Sprintf("antigravity-monitor-0.2.0-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/oysterhyd/antigravity-monitor/releases/latest":
			_ = json.NewEncoder(w).Encode(ghRelease{
				TagName: "v0.2.0",
				Assets: []ghAsset{
					{Name: manifestName, URL: srv.URL + "/assets/101"},
					{Name: archiveName, URL: srv.URL + "/assets/102"},
				},
			})
		case "/assets/101":
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "bad accept", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"version":"0.2.0"}`))
		case "/assets/102":
			_, _ = w.Write([]byte("SIGNED-ARCHIVE-BYTES"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := newGitHubAuthSource("oysterhyd/antigravity-monitor", "secret-tok")
	src.apiURL = srv.URL

	rc, err := src.Manifest(context.Background(), false)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	gotManifest, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(gotManifest) != `{"version":"0.2.0"}` {
		t.Fatalf("unexpected manifest: %s", gotManifest)
	}

	arc, err := src.Open(context.Background(), "https://github.com/oysterhyd/antigravity-monitor/releases/download/v0.2.0/"+archiveName)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gotArchive, _ := io.ReadAll(arc)
	_ = arc.Close()
	if string(gotArchive) != "SIGNED-ARCHIVE-BYTES" {
		t.Fatalf("unexpected archive bytes: %s", gotArchive)
	}
}
