package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSyncNewLargeFileUploadsAfterMount(t *testing.T) {
	env := newSyncTestEnv(t)
	env.startDaemon(t)
	defer env.stopDaemon()

	// Create the file after startup so it uses the background uploader,
	// including a partial final chunk, rather than startup reconciliation.
	content := make([]byte, 2*defaultChunkThreshold+17)
	for i := range content {
		content[i] = byte(i*31 + 7)
	}
	const name = "new-large-file.bin"
	if err := os.WriteFile(filepath.Join(env.localRoot, name), content, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	assertEventually(t, 5*time.Second, "complete large file to upload", func() bool {
		got, err := env.fsClient.Cat(ctx, "/"+name)
		return err == nil && bytes.Equal(got, content)
	})
	chunkSize, hashes, err := env.fsClient.ChunkMeta(ctx, "/"+name)
	if err != nil {
		t.Fatal(err)
	}
	if chunkSize != defaultChunkSize {
		t.Fatalf("chunk size = %d, want %d", chunkSize, defaultChunkSize)
	}
	wantChunks := (len(content) + defaultChunkSize - 1) / defaultChunkSize
	if len(hashes) != wantChunks {
		t.Fatalf("chunk count = %d, want %d", len(hashes), wantChunks)
	}
}

func TestSyncRecoveryUploadsLargeFilesWithChunks(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const name, chunkSize = "recovered.bin", 8
	d.reconciler.chunkSize = chunkSize
	d.reconciler.chunkThreshold = 16
	content := []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN")
	abs := filepath.Join(env.localRoot, name)
	write := func(t *testing.T, stamp int64) {
		t.Helper()
		if err := os.WriteFile(abs, content, 0o640); err != nil {
			t.Fatal(err)
		}
		mtime := time.Unix(stamp, 0)
		if err := os.Chtimes(abs, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T) {
		t.Helper()
		got, err := env.fsClient.Cat(ctx, "/"+name)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("recovered content = %q, %v; want %q", got, err, content)
		}
		gotSize, hashes, err := env.fsClient.ChunkMeta(ctx, "/"+name)
		wantHashes := uploadChunkHashes(content, chunkSize)
		if err != nil || gotSize != chunkSize || !reflect.DeepEqual(hashes, wantHashes) {
			t.Fatalf("recovered chunks = %d %v, %v; want %d %v", gotSize, hashes, err, chunkSize, wantHashes)
		}
		entry := d.Snapshot().Entries[name]
		if entry.ChunkSize != chunkSize || !reflect.DeepEqual(entry.ChunkHashes, wantHashes) ||
			entry.LocalHash != compositeHash(wantHashes) || entry.RemoteHash != entry.LocalHash {
			t.Fatalf("recovered chunk baseline = %+v", entry)
		}
	}
	for i, operation := range []string{"create", "edit", "truncate"} {
		t.Run(operation, func(t *testing.T) {
			switch operation {
			case "edit":
				copy(content[9:13], "EDIT")
			case "truncate":
				content = content[:25]
			}
			write(t, int64(100+i))
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			check(t)
		})
	}
	// A later watcher upload must retain the recovery baseline and send only
	// the changed chunk, including the partial final chunk in its manifest.
	content[2] = 'X'
	write(t, 200)
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: name, KindHint: "write"})
	op := nextDiagnosticUpload(t, d)
	if !op.Chunked || !reflect.DeepEqual(op.DirtyChunks, []int{0}) {
		t.Fatalf("delta after recovery = %+v", op)
	}
	d.uploader.processFile(ctx, op)
	res := <-d.reconciler.uploadResCh
	if res.Err != nil || res.Conflict || res.Skipped {
		t.Fatalf("delta after recovery result = %+v", res)
	}
	d.reconciler.handleUploadResult(ctx, res)
	check(t)
}
