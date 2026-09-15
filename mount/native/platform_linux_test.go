//go:build linux

package native

import (
	"strings"
	"testing"
)

func TestParseMountInfoFindsAbandonedAndEscapedMount(t *testing.T) {
	fixture := "36 25 0:33 / /tmp/mount\\040with\\040spaces rw - fuse.afs afs rw\n"
	kind, source, found, err := parseMountInfo(strings.NewReader(fixture), "/tmp/mount with spaces")
	if err != nil || !found || kind != "fuse.afs" || source != "afs" {
		t.Fatalf("mount=%s,%s,%v,%v", kind, source, found, err)
	}
	_, _, found, err = parseMountInfo(strings.NewReader(fixture), "/tmp/mount with spaces/child")
	if err != nil || found {
		t.Fatal("matched non-mount child")
	}
}
