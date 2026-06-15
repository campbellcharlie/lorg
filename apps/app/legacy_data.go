package app

import "sync/atomic"

// legacyDataWrites gates the lorgdb _data / _req / _resp write in the capture
// paths (ADR-004 E9). Default ON: the projectDB http_traffic store is now
// authoritative for every agent-facing read tool, but the sitemap / host-rows /
// traffic-list-UI subsystem still reads _data, so the dual-write stays on until
// that subsystem is migrated. Flipping it OFF stops the _data write entirely —
// captured traffic then lives ONLY in the per-project http_traffic DBs, which the
// migrated readers serve. Exposed so a CLI flag / setting / test can toggle it.
var legacyDataWrites atomic.Bool

func init() { legacyDataWrites.Store(true) }

// legacyDataEnabled reports whether the legacy _data/_req/_resp write is on.
func legacyDataEnabled() bool { return legacyDataWrites.Load() }

// SetLegacyDataWrites toggles the legacy _data write (ADR-004 E9).
func SetLegacyDataWrites(on bool) { legacyDataWrites.Store(on) }
