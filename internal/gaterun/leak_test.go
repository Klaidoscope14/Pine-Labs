package gaterun

import (
	"testing"

	"go.uber.org/goleak"
)

// Every test in this package must leave no goroutines behind (sandbox slots,
// provider limits and parallel gates all start goroutines).
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
