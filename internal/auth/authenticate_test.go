package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/store"
)

type fakeKeys map[string]store.GetActiveKeyByPrefixRow

func (f fakeKeys) lookup(_ context.Context, prefix string) (store.GetActiveKeyByPrefixRow, error) {
	row, ok := f[prefix]
	if !ok {
		return store.GetActiveKeyByPrefixRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func newFakeKey(t *testing.T, scopes ...string) (string, store.GetActiveKeyByPrefixRow) {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k.Token, store.GetActiveKeyByPrefixRow{
		ID: uuid.New(), ProjectID: uuid.New(), SecretHash: k.SecretHash, Scopes: scopes,
	}
}

func TestAuthenticateValidKey(t *testing.T) {
	token, row := newFakeKey(t, ScopeEmailsSend)
	prefix, _, _ := ParseToken(token)
	p, err := Authenticate(context.Background(), fakeKeys{prefix: row}.lookup, token)
	if err != nil {
		t.Fatal(err)
	}
	if p.ProjectID != row.ProjectID || p.KeyID != row.ID || !p.Has(ScopeEmailsSend) || p.Has(ScopeEmailsRead) {
		t.Fatalf("principal = %+v", p)
	}
}

func TestAuthenticateRejects(t *testing.T) {
	token, row := newFakeKey(t, ScopeEmailsSend)
	prefix, _, _ := ParseToken(token)
	other, _ := newFakeKey(t, ScopeEmailsSend)
	otherPrefix, otherSecret, _ := ParseToken(other)
	keys := fakeKeys{prefix: row}

	cases := map[string]string{
		"empty":                               "",
		"malformed":                           "not-a-key",
		"unknown prefix":                      other,
		"wrong secret":                        "ep_" + prefix + "_" + otherSecret,
		"prefix of other key with own secret": "ep_" + otherPrefix + "_" + otherSecret,
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Authenticate(context.Background(), keys.lookup, tok); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("err = %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestAuthenticateLookupFailureIsNotUnauthorized(t *testing.T) {
	token, _ := newFakeKey(t)
	failing := func(context.Context, string) (store.GetActiveKeyByPrefixRow, error) {
		return store.GetActiveKeyByPrefixRow{}, errors.New("connection refused")
	}
	_, err := Authenticate(context.Background(), failing, token)
	if err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want a non-auth error so outages are not reported as 401", err)
	}
}
