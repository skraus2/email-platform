package awsconfig

import (
	"context"
	"testing"
)

func TestValidRegion(t *testing.T) {
	t.Parallel()
	for _, r := range []string{"eu-central-1", "us-east-1", "us-gov-west-1", "ap-southeast-2"} {
		if !ValidRegion(r) {
			t.Errorf("ValidRegion(%q) = false", r)
		}
	}
	for _, r := range []string{"", "eu-central", "EU-CENTRAL-1", "eu-central-1 ", "eu-central-1.evil", "localhost"} {
		if ValidRegion(r) {
			t.Errorf("ValidRegion(%q) = true", r)
		}
	}
}

func TestValidateEndpointLoopbackOnly(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"http://127.0.0.1:4566", "https://localhost:8443", "http://[::1]:9000", "http://127.0.0.1:4566/"} {
		if err := ValidateEndpoint(ok); err != nil {
			t.Errorf("ValidateEndpoint(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "127.0.0.1:4566", "ftp://127.0.0.1:21", "http://email.eu-central-1.amazonaws.com",
		"https://10.0.0.1:443", "http://127.0.0.2:4566", "http://localhost.evil.test:80",
		"http://user:pw@127.0.0.1:4566", "http://127.0.0.1:4566/path", "http://127.0.0.1:4566?x=1",
		"http://127.0.0.1:4566#f", "http://127.0.0.1", "http://0.0.0.0:4566",
	} {
		if err := ValidateEndpoint(bad); err == nil {
			t.Errorf("ValidateEndpoint(%q) = nil, want error", bad)
		}
	}
}

func TestLoadWithEndpointUsesFakeCredentials(t *testing.T) {
	// Real credentials in the environment must never reach a stub.
	t.Setenv("AWS_ACCESS_KEY_ID", "REALLOOKINGKEYID0001")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "real-secret")
	cfg, err := Load(context.Background(), "eu-central-1", "http://127.0.0.1:4566")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != StubAccessKeyID || creds.SecretAccessKey == "real-secret" {
		t.Fatalf("credentials = %q, want the fixed stub credentials", creds.AccessKeyID)
	}
	if cfg.BaseEndpoint == nil || *cfg.BaseEndpoint != "http://127.0.0.1:4566" || cfg.Region != "eu-central-1" {
		t.Fatalf("config = region %q endpoint %v", cfg.Region, cfg.BaseEndpoint)
	}
	if cfg.Retryer == nil || cfg.Retryer().MaxAttempts() != 1 {
		t.Fatal("SDK retries must be off: the platform retries itself, and a retried send can duplicate mail")
	}
}

func TestLoadRefusesBadInput(t *testing.T) {
	t.Parallel()
	if _, err := Load(context.Background(), "nowhere", ""); err == nil {
		t.Error("invalid region accepted")
	}
	if _, err := Load(context.Background(), "eu-central-1", "https://email.eu-central-1.amazonaws.com"); err == nil {
		t.Error("non-loopback endpoint override accepted")
	}
}
