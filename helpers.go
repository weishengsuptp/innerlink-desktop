// Build-tag-free helpers for main.go.
package main

import (
	"strconv"
	"time"
)

// itoa is strconv.Itoa under a friendlier name; we use it
// only in this file for the lockfile's "pid=<n>" line so
// the lockfile contents are easy to grep by hand when
// chasing an orphan.
func itoa(n int) string { return strconv.Itoa(n) }

// timeSinceHours reports how many whole hours have
// passed since t. We use it to decide whether an
// existing lockfile is a stale artifact (process
// crashed > 1h ago) or a live sibling instance.
func timeSinceHours(t time.Time) float64 {
	return time.Since(t).Hours()
}