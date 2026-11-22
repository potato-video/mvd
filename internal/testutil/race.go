//go:build race

package testutil

// Race: built with -race, which allocates as it goes, so tests that count
// garbage skip.
const Race = true
