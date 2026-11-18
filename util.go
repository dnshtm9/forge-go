package main

import (
	"bytes"
	"strconv"
	"time"
)

// parseRFC3339 parses the timestamp form JS Date.parse accepts for the trial's
// fixed clock: RFC3339. It answers epoch milliseconds.
func parseRFC3339(s string) (int64, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixMilli(), nil
	}
	return 0, errBadTimestamp
}

var errBadTimestamp = &timestampError{}

type timestampError struct{}

func (e *timestampError) Error() string { return "bad timestamp" }

// rfc3339FromMs renders epoch milliseconds as ISO, matching JS toISOString
// (always "YYYY-MM-DDTHH:MM:SS.sssZ" with three fractional digits).
func rfc3339FromMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// bytesEqual is bytes.Equal as the store uses it.
func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }

// String helper: the decimal form of an int (mirrors JS String(n)).
func itoa(n int) string { return strconv.Itoa(n) }

// stringPtr makes a *string for nullable JSON fields.
func stringPtr(s string) *string { return &s }
