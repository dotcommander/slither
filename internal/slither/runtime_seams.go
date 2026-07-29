package slither

import (
	"context"
	"time"
)

// currentTime keeps report timestamps and stale-marker age calculations under
// one clock. Production uses the wall clock; seam tests replace it serially.
var currentTime = time.Now

// sourcePrefixReader is the bounded, UTF-8-aware source reader used by both
// inspection and score-context construction. It deliberately retains the
// truncation status needed by callers to preserve existing limits.
var sourcePrefixReader = readTextPrefixWithStatus

// outcomeWriter is deliberately path-bound: an implementation is configured
// for one outcome destination before it is passed into later agent work. Phase
// A defines the boundary only; no ledger implementation is provided yet.
type outcomeWriter interface {
	WriteOutcome(context.Context, []byte) error
}

type outcomeWriterFunc func(context.Context, []byte) error

func (f outcomeWriterFunc) WriteOutcome(ctx context.Context, record []byte) error {
	return f(ctx, record)
}
