package quota

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	cliManifestHost = "antigravity-cli-auto-updater-974169037036.us-central1.run.app"
	cliManifestBase = "https://" + cliManifestHost
	cliDownloadPath = "/antigravity-public/antigravity-cli/"
	maxManifestSize = 64 << 10
	maxCLISize      = 512 << 20
)

func trustedCLIURL(target *url.URL) bool {
	if target == nil || target.Scheme != "https" || target.User != nil {
		return false
	}
	return (target.Host == cliManifestHost && strings.HasPrefix(target.Path, "/manifests/")) ||
		(target.Host == "storage.googleapis.com" && strings.HasPrefix(target.Path, cliDownloadPath))
}

func downloadCLI(ctx context.Context, client *http.Client, manifestURL, executable string) error {
	response, err := fetchCLIFile(ctx, client, manifestURL)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxManifestSize+1))
	response.Body.Close()
	if err != nil {
		return err
	}
	if len(raw) > maxManifestSize {
		return fmt.Errorf("quota reader manifest exceeds the size limit")
	}
	var manifest struct {
		URL     string `json:"url"`
		Version string `json:"version"`
		SHA512  string `json:"sha512"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	checksum, err := hex.DecodeString(manifest.SHA512)
	binaryURL, urlErr := url.Parse(manifest.URL)
	if err != nil || len(checksum) != sha512.Size || urlErr != nil || !trustedCLIURL(binaryURL) ||
		binaryURL.Host != "storage.googleapis.com" || !supportsUsage(manifest.Version) {
		return fmt.Errorf("invalid quota reader manifest")
	}

	binary, err := fetchCLIFile(ctx, client, manifest.URL)
	if err != nil {
		return err
	}
	defer binary.Body.Close()
	if binary.ContentLength > maxCLISize {
		return fmt.Errorf("quota reader exceeds the size limit")
	}
	dir := filepath.Dir(executable)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".agy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	hash := sha512.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(binary.Body, maxCLISize+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if size == 0 || size > maxCLISize || !bytes.Equal(hash.Sum(nil), checksum) {
		return fmt.Errorf("quota reader checksum or size mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), executable)
}

func fetchCLIFile(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	if !trustedCLIURL(request.URL) {
		return nil, fmt.Errorf("unrecognized quota reader download address")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("quota reader download returned HTTP %d", response.StatusCode)
	}
	return response, nil
}
