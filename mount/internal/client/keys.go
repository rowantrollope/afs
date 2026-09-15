package client

import (
	"path"
	"strings"
)

type keyBuilder struct {
	fsKey string
}

func newKeyBuilder(fsKey string) keyBuilder {
	return keyBuilder{fsKey: fsKey}
}

func (k keyBuilder) inode(id string) string {
	return "afs-lite:{" + k.fsKey + "}:inode:" + id
}

func (k keyBuilder) dirents(id string) string {
	return "afs-lite:{" + k.fsKey + "}:dirents:" + id
}

func (k keyBuilder) info() string {
	return "afs-lite:{" + k.fsKey + "}:info"
}

func (k keyBuilder) nextInode() string {
	return "afs-lite:{" + k.fsKey + "}:next_inode"
}

func (k keyBuilder) rootDirty() string {
	return "afs-lite:{" + k.fsKey + "}:root_dirty"
}

func (k keyBuilder) content(id string) string {
	return "afs-lite:{" + k.fsKey + "}:content:" + id
}

// invalidateChannel is the Redis pub/sub channel name used to broadcast
// cache invalidation events between clients that share this FS key. The
// hash tag keeps it colocated on the same cluster slot as the data keys.
func (k keyBuilder) invalidateChannel() string {
	return "afs-lite:{" + k.fsKey + "}:invalidate"
}

// changesStream is the Redis Stream key for the durable change journal.
// Sync clients read from this stream on reconnect to catch up on events
// missed while their pub/sub subscription was down.
func (k keyBuilder) changesStream() string {
	return "afs-lite:{" + k.fsKey + "}:changes"
}

func (k keyBuilder) inodePrefix() string {
	return "afs-lite:{" + k.fsKey + "}:inode:"
}

func (k keyBuilder) direntsPrefix() string {
	return "afs-lite:{" + k.fsKey + "}:dirents:"
}

func (k keyBuilder) scanPattern() string {
	return "afs-lite:{" + k.fsKey + "}:*"
}

func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	clean := path.Clean(p)
	if clean == "." {
		return "/"
	}
	return clean
}

func (k keyBuilder) generation() string { return "afs-lite:{" + k.fsKey + "}:generation" }
