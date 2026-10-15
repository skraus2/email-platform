package emails

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUUIDV7FloorLayout(t *testing.T) {
	at := time.UnixMilli(0x0192_f0aa_1234).Add(987 * time.Microsecond)
	got := uuidV7Floor(at)
	if want := uuid.MustParse("0192f0aa-1234-7000-8000-000000000000"); got != want {
		t.Fatalf("floor = %s, want %s", got, want)
	}
	if got.Version() != 7 || got.Variant() != uuid.RFC4122 {
		t.Fatalf("version %d variant %v", got.Version(), got.Variant())
	}
	// Any v7 id of the same millisecond, whatever its other bits, sorts at
	// or after the floor; the previous millisecond sorts before it.
	same := uuid.MustParse("0192f0aa-1234-7000-8000-000000000000")
	for _, s := range []string{"0192f0aa-1234-7000-8000-000000000000", "0192f0aa-1234-7fff-bfff-ffffffffffff", "0192f0aa-1234-7001-8000-000000000001"} {
		if u := uuid.MustParse(s); string(u[:]) < string(same[:]) {
			t.Errorf("%s sorts before the floor", s)
		}
	}
	if prev := uuid.MustParse("0192f0aa-1233-7fff-bfff-ffffffffffff"); string(prev[:]) >= string(got[:]) {
		t.Error("previous millisecond does not sort before the floor")
	}
	if uuidV7Floor(time.Unix(-1, 0)) != uuid.Nil {
		t.Error("pre-1970 floor is not the nil UUID")
	}
}
