package quota

import (
	"context"
	"crypto/sha512"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cliDownloadTransport func(*http.Request) (*http.Response, error)

func (transport cliDownloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestDownloadCLIVerifiesOfficialBinaryBeforeCaching(t *testing.T) {
	const contents = "fixture command"
	const manifestURL = cliManifestBase + "/manifests/windows_amd64.json"
	const officialURL = "https://storage.googleapis.com/antigravity-public/antigravity-cli/version/windows-x64/agy.exe"
	for _, test := range []struct {
		name     string
		binary   string
		body     string
		wantErr  bool
		requests int
	}{
		{"verified", officialURL, contents, false, 2},
		{"bad checksum", officialURL, "unexpected payload", true, 2},
		{"untrusted download", "https://example.com/agy.exe", "", true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "reader", "agy.exe")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("existing binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			requests := 0
			client := &http.Client{Transport: cliDownloadTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				var body string
				switch request.URL.String() {
				case manifestURL:
					body = fmt.Sprintf(`{"url":%q,"version":"1.5.16","sha512":"%x"}`,
						test.binary, sha512.Sum512([]byte(contents)))
				case officialURL:
					body = test.body
				default:
					t.Fatalf("unexpected request: %s", request.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			err := downloadCLI(context.Background(), client, manifestURL, target)
			if (err != nil) != test.wantErr || requests != test.requests {
				t.Fatalf("download error = %v, requests = %d", err, requests)
			}
			want := contents
			if test.wantErr {
				want = "existing binary"
			}
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != want {
				t.Fatalf("cached binary = %q, err = %v", raw, err)
			}
		})
	}
}
