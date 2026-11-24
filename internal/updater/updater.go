package appupdater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updaternative "github.com/egoist/mygo/plugins/updater/native"
)

var registered atomic.Bool

// Register installs mygo's native GPU-rendered Sparkle-style auto-updater plugin
// (zero WebView) and automatically configures an authenticated GitHub Releases
// UpdateSource when GITHUB_TOKEN or GH_TOKEN is set in the environment (enabling
// seamless updates from private GitHub repositories while preserving Ed25519
// signature verification).
func Register(githubRepo string) {
	if tok := firstEnv("GITHUB_TOKEN", "GH_TOKEN"); tok != "" && strings.TrimSpace(githubRepo) != "" {
		mygo.Updater.SetSource(newGitHubAuthSource(githubRepo, tok))
	}
	mygo.Use(updaternative.New(updater.Options{
		Interval: 24 * time.Hour,
	}))
	registered.Store(true)
}

// MenuItem returns the native "Check for Updates..." / "检查更新…" menu item.
func MenuItem() *mygo.MenuItem {
	if !registered.Load() {
		return &mygo.MenuItem{ID: "check-updates", Label: "Check for Updates…", Disabled: true}
	}
	return updater.MenuItem()
}

// CheckNow opens the native update window and checks for a newer release immediately.
func CheckNow() {
	if !registered.Load() {
		return
	}
	updater.CheckForUpdates()
}

// AvailableVersion returns the newly discovered version string if one is pending,
// or "" when up to date or not yet checked.
func AvailableVersion() string {
	if !registered.Load() {
		return ""
	}
	return updater.AvailableVersion()
}

// VersionLabel returns a clean display version string such as "v0.1.0" or "dev".
func VersionLabel() string {
	v := strings.TrimSpace(mygo.App.Version())
	if v == "" {
		return "dev"
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// githubAuthSource implements mygo.UpdateSource over the GitHub Releases API
// using a Bearer token so private repositories can serve mygo's signed manifests,
// archives, and delta patches.
type githubAuthSource struct {
	repo   string
	token  string
	apiURL string
	client *http.Client

	mu     sync.RWMutex
	assets map[string]string // filename -> GitHub API asset URL
}

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func newGitHubAuthSource(repo, token string) *githubAuthSource {
	return &githubAuthSource{
		repo:   strings.Trim(strings.TrimSpace(repo), "/"),
		token:  token,
		apiURL: "https://api.github.com",
		client: &http.Client{Timeout: 60 * time.Second},
		assets: make(map[string]string),
	}
}

func (s *githubAuthSource) Manifest(ctx context.Context, prereleases bool) (io.ReadCloser, error) {
	rel, err := s.fetchRelease(ctx, prereleases)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	for _, a := range rel.Assets {
		s.assets[a.Name] = a.URL
	}
	s.mu.Unlock()

	manifestName := fmt.Sprintf("update-%s-%s.json", runtime.GOOS, runtime.GOARCH)
	assetURL, ok := s.lookupAsset(manifestName)
	if !ok {
		return nil, fmt.Errorf("release %s does not contain %s", rel.TagName, manifestName)
	}
	return s.downloadAsset(ctx, assetURL)
}

func (s *githubAuthSource) Open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	name := path.Base(u.Path)
	if assetURL, ok := s.lookupAsset(name); ok {
		return s.downloadAsset(ctx, assetURL)
	}
	// Fallback: direct authenticated GET (e.g. non-GitHub URLs).
	return s.downloadAsset(ctx, rawURL)
}

func (s *githubAuthSource) lookupAsset(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.assets[name]
	return u, ok
}

func (s *githubAuthSource) fetchRelease(ctx context.Context, prereleases bool) (ghRelease, error) {
	if !prereleases {
		endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", s.apiURL, s.repo)
		req, err := s.newRequest(ctx, endpoint, "application/vnd.github+json")
		if err != nil {
			return ghRelease{}, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return ghRelease{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ghRelease{}, fmt.Errorf("github latest release %s: %s", s.repo, resp.Status)
		}
		var rel ghRelease
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			return ghRelease{}, err
		}
		return rel, nil
	}

	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=10", s.apiURL, s.repo)
	req, err := s.newRequest(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return ghRelease{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ghRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ghRelease{}, fmt.Errorf("github releases %s: %s", s.repo, resp.Status)
	}
	var list []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return ghRelease{}, err
	}
	for _, r := range list {
		if !r.Draft {
			return r, nil
		}
	}
	return ghRelease{}, fmt.Errorf("no published release found in %s", s.repo)
}

func (s *githubAuthSource) downloadAsset(ctx context.Context, endpoint string) (io.ReadCloser, error) {
	req, err := s.newRequest(ctx, endpoint, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s", endpoint, resp.Status)
	}
	return resp.Body, nil
}

func (s *githubAuthSource) newRequest(ctx context.Context, endpoint, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return req, nil
}
