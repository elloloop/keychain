// Package postgres is keychain's durable Store driver. The schema lives in
// embedded SQL migrations; the driver itself is driven by pgx/v5 and is
// safe for concurrent use.
//
// Requirements: Postgres 15+. New connects to the DSN, runs the embedded
// migrations synchronously, and pings the pool; it returns an error if any
// of those steps fails. Callers own the returned *Store's lifetime via
// Close.
package postgres
