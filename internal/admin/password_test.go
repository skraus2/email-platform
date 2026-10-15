package admin

import (
	"strings"
	"testing"
)

var fastParams = Params{Memory: 64, Time: 1, Threads: 1}

func TestHashPasswordPHC(t *testing.T) {
	h, err := HashPassword("correct horse battery", DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=2,p=1" {
		t.Fatalf("not a PHC argon2id string with the default parameters: %s", h)
	}
	if h2, _ := HashPassword("correct horse battery", DefaultParams); h2 == h {
		t.Fatal("two hashes of the same password are identical (salt missing)")
	}
	ok, outdated, err := VerifyPassword(h, "correct horse battery", DefaultParams)
	if err != nil || !ok || outdated {
		t.Fatalf("verify: %v %v %v", ok, outdated, err)
	}
	if ok, _, err := VerifyPassword(h, "correct horse batterY", DefaultParams); err != nil || ok {
		t.Fatalf("wrong password accepted: %v %v", ok, err)
	}
	// Hashes with other parameters still verify and are reported outdated.
	old, _ := HashPassword("pw-with-old-params", fastParams)
	ok, outdated, err = VerifyPassword(old, "pw-with-old-params", DefaultParams)
	if err != nil || !ok || !outdated {
		t.Fatalf("old parameters: %v %v %v", ok, outdated, err)
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	good, _ := HashPassword("x", fastParams)
	parts := strings.Split(good, "$")
	for _, bad := range []string{
		"", "plain", "$argon2i$v=19$m=64,t=1,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=18$m=64,t=1,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=64,t=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=999999999,t=1,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=64,t=99,p=1$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=64,t=1,p=1$!!$" + parts[5],
		"$argon2id$v=19$m=64,t=1,p=1$" + parts[4] + "$",
	} {
		if ok, _, err := VerifyPassword(bad, "x", DefaultParams); err == nil || ok {
			t.Errorf("%q: ok=%v err=%v, want an error", bad, ok, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for _, ok := range []string{strings.Repeat("a", 12), strings.Repeat("ü", 128), "correct horse battery staple"} {
		if err := ValidatePassword(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 11), strings.Repeat("a", 129), "valid length\x00nul", "\xff\xfe invalid utf8"} {
		if err := ValidatePassword(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestThrottleLocksUnknownNames(t *testing.T) {
	th := newThrottle(3)
	now := testNow
	for i := 1; i <= 4; i++ {
		if d := th.locked("ghost", now); d != 0 {
			t.Fatalf("locked before 5 failures (after %d)", i-1)
		}
		th.fail("ghost", now)
	}
	th.fail("ghost", now) // 5th failure locks for 1 minute
	if d := th.locked("ghost", now); d != time1m {
		t.Fatalf("lock after 5 failures = %v, want 1m", d)
	}
	th.fail("ghost", now) // 6th: 2 minutes
	if d := th.locked("ghost", now); d != 2*time1m {
		t.Fatalf("lock after 6 failures = %v, want 2m", d)
	}
	for range 20 {
		th.fail("ghost", now)
	}
	if d := th.locked("ghost", now); d != 15*time1m {
		t.Fatalf("lock is capped at 15m, got %v", d)
	}
	// Bounded: the oldest names are evicted.
	th.fail("a", now)
	th.fail("b", now)
	th.fail("c", now)
	if len(th.items) > 3 {
		t.Fatalf("throttle holds %d names, want ≤ 3", len(th.items))
	}
}
