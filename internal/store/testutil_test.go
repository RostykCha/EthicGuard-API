package store

import (
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// newMockStore returns a Store wired to a pgxmock pool with strict query
// matching. Strict matching means tests pin the exact SQL text — drift
// between production code and tests fails loudly rather than silently.
// Callers must defer mock.ExpectationsWereMet() through t.Cleanup or
// directly at the end of the test.
func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet pgxmock expectations: %v", err)
		}
		mock.Close()
	})
	return &Store{DB: mock}, mock
}
