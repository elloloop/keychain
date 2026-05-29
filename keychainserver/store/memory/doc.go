// Package memory is the in-process Store driver. It is the differential
// reference used by the conformance suite and the default backend for
// tests and local development.
//
// Not durable across restarts; intended for tests and local development.
// Production deployments use the postgres backend or another driver
// implementing the Store interface. Even single-instance deployments
// should pick a durable backend — restarting the process invalidates
// every issued key.
package memory
