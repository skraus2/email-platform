// Package awsconfig builds the AWS SDK configuration for the SES and SQS
// clients. Clients are only ever built from explicit settings: a region, and
// either the default credential chain (real AWS) or a loopback endpoint with
// fixed fake credentials (local stubs). Nothing here runs unless the SES
// opt-in configuration is complete.
package awsconfig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// StubAccessKeyID is the access key of the fake credentials used with a
// loopback endpoint.
const StubAccessKeyID = "LOCALSTUBACCESSKEY"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]?)?-[a-z]+-[0-9]+$`)

// ValidRegion reports whether region looks like an AWS region name.
func ValidRegion(region string) bool { return regionPattern.MatchString(region) }

// ValidateEndpoint accepts only a bare http(s) URL on a loopback host with a
// port: endpoint overrides exist for local stubs, never to redirect traffic
// to another server.
func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
		return errors.New("endpoint must be an http(s) URL")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return errors.New("endpoint must not carry credentials, a path, a query or a fragment")
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		return errors.New("endpoint must name a port")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || (!ip.Equal(net.IPv4(127, 0, 0, 1)) && !ip.Equal(net.IPv6loopback)) {
			return fmt.Errorf("endpoint host %q is not loopback (allowed: localhost, 127.0.0.1, ::1)", host)
		}
	}
	return nil
}

// Load returns the configuration for region. With an endpoint (a local
// stub) it uses fixed fake credentials and never the default chain, so no
// real credential can reach a stub; without one it uses the default chain
// (IAM role, environment, shared files). SDK retries are off in both cases:
// the platform retries itself, and a transparently retried send could go
// out twice.
//
// BaseEndpoint is the validated endpoint, or nil for AWS. The SDK also reads
// endpoint overrides from AWS_ENDPOINT_URL* and the shared config while
// building a service client, so every client must set its BaseEndpoint from
// this value as its last option.
func Load(ctx context.Context, region, endpoint string) (aws.Config, error) {
	if !ValidRegion(region) {
		return aws.Config{}, fmt.Errorf("invalid AWS region %q", region)
	}
	noRetry := func() aws.Retryer { return aws.NopRetryer{} }
	if endpoint == "" {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithRetryer(noRetry))
		if err != nil {
			return aws.Config{}, fmt.Errorf("load AWS configuration: %w", err)
		}
		cfg.BaseEndpoint = nil // never an unchecked override from the environment or a profile
		return cfg, nil
	}
	if err := ValidateEndpoint(endpoint); err != nil {
		return aws.Config{}, err
	}
	return aws.Config{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(StubAccessKeyID, "local-stub-secret", ""),
		BaseEndpoint: aws.String(endpoint),
		Retryer:      noRetry,
	}, nil
}
