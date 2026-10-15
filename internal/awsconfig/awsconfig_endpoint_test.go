package awsconfig

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
)

// recordingClient records request hosts and never touches the network.
type recordingClient struct {
	mu    sync.Mutex
	hosts []string
}

func (c *recordingClient) Do(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hosts = append(c.hosts, r.URL.Host)
	return nil, errors.New("recording client: not sent")
}

// isolate points the SDK at an empty profile, disables instance metadata and
// clears ambient settings that would change what the tests prove.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, k := range []string{
		"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_ROLE_ARN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_SESV2",
		"AWS_USE_FIPS_ENDPOINT", "AWS_USE_DUALSTACK_ENDPOINT", "AWS_CA_BUNDLE", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	return dir
}

func TestLoadAWSRequiresIgnoringConfiguredEndpoints(t *testing.T) {
	isolate(t)
	t.Setenv("AWS_ACCESS_KEY_ID", StubAccessKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local-stub-secret")
	if _, err := Load(context.Background(), "eu-central-1", ""); err == nil || !strings.Contains(err.Error(), IgnoreEndpointsEnv) {
		t.Fatalf("err = %v, want refusal naming %s", err, IgnoreEndpointsEnv)
	}
	t.Setenv(IgnoreEndpointsEnv, "false")
	if _, err := Load(context.Background(), "eu-central-1", ""); err == nil {
		t.Fatal("explicit false accepted")
	}
	t.Setenv(IgnoreEndpointsEnv, "true")
	cfg, err := Load(context.Background(), "eu-central-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseEndpoint != nil {
		t.Fatalf("BaseEndpoint = %q, want none", *cfg.BaseEndpoint)
	}
}

// TestCredentialClientsIgnoreEndpointOverrides: with role credentials the SDK
// calls STS itself; an endpoint override must not redirect that call (it
// carries the web identity token).
func TestCredentialClientsIgnoreEndpointOverrides(t *testing.T) {
	dir := isolate(t)
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("web-identity-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := "[default]\nendpoint_url = http://127.0.0.1:1\nservices = redirect\n\n[services redirect]\nsts =\n  endpoint_url = http://127.0.0.1:2\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::123456789012:role/sender")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", token)
	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:3")
	t.Setenv("AWS_ENDPOINT_URL_STS", "http://127.0.0.1:4")
	t.Setenv(IgnoreEndpointsEnv, "true")

	rec := &recordingClient{}
	cfg, err := load(context.Background(), "eu-central-1", "", config.WithHTTPClient(rec))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cfg.Credentials.Retrieve(context.Background())
	if len(rec.hosts) == 0 {
		t.Fatal("no STS request recorded")
	}
	for _, h := range rec.hosts {
		if !strings.HasSuffix(h, ".amazonaws.com") {
			t.Fatalf("credential request went to %q, want an AWS host", h)
		}
	}
}
