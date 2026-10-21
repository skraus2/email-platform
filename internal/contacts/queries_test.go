package contacts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestQueriesNeverReadTheToken keeps the unsubscribe token out of every
// contact query except the ones that exist to write or resolve it, so no
// API path can return it by accident.
func TestQueriesNeverReadTheToken(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		"InsertContact": true, "UnsubscribeByToken": true, "ContactIDByToken": true,
		// Fan-out and dispatch of broadcast emails put the token into the
		// recipient's unsubscribe link.
		"FanoutBatch": true, "GetEmailForDispatch": true,
	}
	name := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	for _, file := range []string{"../../db/queries/contacts.sql", "../../db/queries/audiences.sql", "../../db/queries/broadcasts.sql", "../../db/queries/emails.sql"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		blocks := name.Split(string(b), -1)[1:]
		names := name.FindAllStringSubmatch(string(b), -1)
		for i, block := range blocks {
			q := names[i][1]
			touchesContacts := strings.Contains(block, " contacts")
			if touchesContacts && (strings.Contains(block, "SELECT *") || strings.Contains(block, ".*") || strings.Contains(block, "RETURNING *")) {
				t.Errorf("%s: %s selects all columns", file, q)
			}
			if !allowed[q] && strings.Contains(block, "unsubscribe_token") {
				t.Errorf("%s: %s reads the unsubscribe token", file, q)
			}
		}
	}
}
