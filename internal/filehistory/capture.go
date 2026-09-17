package filehistory

import _ "embed"

// CaptureLua is shared by conditional inode publications and fenced bulk
// lifecycle operations. Call history_capture only after validating the live
// mutation. It preflights all history metadata before copying immutable bodies,
// then records those bodies in the same Redis command as the live mutation.
// Each change supplies id, path, after (an inode field table or false), body
// (the staged after-content key), and operation. Bulk callers may also supply
// before, before_body and file_id to preserve lineage across inode replacement.
// The after table must include the revision that the caller will publish.
//
var CaptureLua = globLua + captureLua

//go:embed capture.lua
var captureLua string

//go:embed glob.lua
var globLua string
