package client

// StatResult holds a parsed stat response.
type StatResult struct {
	Revision string // opaque successful file publication identity
	Inode    uint64
	Type     string // "file", "dir", "symlink"
	Mode     uint32 // POSIX permission bits
	UID      uint32
	GID      uint32
	Size     int64
	Ctime    int64 // milliseconds since epoch
	Mtime    int64
	Atime    int64
}

// LsEntry holds one entry from a long directory listing.
type LsEntry struct {
	Inode uint64
	Name  string
	Type  string
	Mode  uint32
	UID   uint32
	GID   uint32
	Size  int64
	Mtime int64
}

// InfoResult holds a parsed filesystem info response.
type InfoResult struct {
	Files          int64
	Directories    int64
	Symlinks       int64
	TotalDataBytes int64
	TotalInodes    int64
}
