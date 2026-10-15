package delivery

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/skraus2/email-platform/internal/awsconfig"
	"github.com/skraus2/email-platform/internal/testaws"
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

// TestSESIgnoresAmbientEndpointOverrides: the SDK reads endpoint overrides
// from the environment and the shared AWS config. None of them may redirect
// signed SES requests; only the validated SES_ENDPOINT can.
func TestSESIgnoresAmbientEndpointOverrides(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "config")
	if err := os.WriteFile(profile, []byte("[default]\nendpoint_url = http://127.0.0.1:1\nservices = redirect\n\n[services redirect]\nsesv2 =\n  endpoint_url = http://127.0.0.1:2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"AWS_PROFILE", "AWS_SESSION_TOKEN", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ENDPOINT_URL",
		"AWS_ENDPOINT_URL_SESV2", "AWS_USE_FIPS_ENDPOINT", "AWS_USE_DUALSTACK_ENDPOINT", "AWS_CA_BUNDLE",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Setenv("AWS_CONFIG_FILE", profile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", awsconfig.StubAccessKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local-stub-secret")
	t.Setenv(awsconfig.IgnoreEndpointsEnv, "true")

	for name, env := range map[string]map[string]string{
		"profile":          {},
		"global env":       {"AWS_ENDPOINT_URL": "http://127.0.0.1:3"},
		"service env":      {"AWS_ENDPOINT_URL_SESV2": "http://127.0.0.1:4"},
		"global + service": {"AWS_ENDPOINT_URL": "http://127.0.0.1:3", "AWS_ENDPOINT_URL_SESV2": "http://127.0.0.1:4"},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			rec := &recordingClient{}
			s, err := newSES(context.Background(), SESConfig{Region: "eu-central-1", ConfigurationSet: "x"},
				func(o *sesv2.Options) { o.HTTPClient = rec })
			if err != nil {
				t.Fatal(err)
			}
			_, _ = s.Send(context.Background(), sesMessage())
			if len(rec.hosts) != 1 || rec.hosts[0] != "email.eu-central-1.amazonaws.com" {
				t.Fatalf("request hosts = %v, want only the regional SES endpoint", rec.hosts)
			}
		})
	}

	t.Run("stub endpoint wins over the environment", func(t *testing.T) {
		t.Setenv("AWS_ENDPOINT_URL_SESV2", "http://127.0.0.1:4")
		stub := testaws.NewSESv2(t)
		if _, err := newTestSES(t, stub.URL).Send(context.Background(), sesMessage()); err != nil {
			t.Fatal(err)
		}
		if n := len(stub.Sends()); n != 1 {
			t.Fatalf("stub got %d requests, want 1", n)
		}
	})
}
