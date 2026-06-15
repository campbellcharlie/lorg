package app

import "sync/atomic"

// legacyDataWrites gates the lorgdb _data / _req / _resp write in the capture
// paths (ADR-004). Default OFF: every agent-facing reader, the host-rows tool,
// the traffic-list UI, and traffic-detail now read the per-project http_traffic
// store, so the legacy global tables are no longer written. The few remaining
// _data/_req/_resp reads are guarded legacy fallbacks for pre-ADR-004 ids.
// Re-enable (CLI/setting/test) only to keep populating the old tables during a
// migration window.
var legacyDataWrites atomic.Bool

func init() { legacyDataWrites.Store(false) }

// legacyDataEnabled reports whether the legacy _data/_req/_resp write is on.
func legacyDataEnabled() bool { return legacyDataWrites.Load() }

// SetLegacyDataWrites toggles the legacy _data write (ADR-004 E9).
func SetLegacyDataWrites(on bool) { legacyDataWrites.Store(on) }
