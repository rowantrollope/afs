package client

import (
	"context"
	"errors"
	"strconv"
	"time"
)

func (s *nativeSession) InodePath(ctx context.Context, inode uint64) (result string, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.InodePath(ctx, inode)
}

// Requests bind the fixed mount generation and session even when adapters
// supply fresh background contexts. Reads validate again before returning.
func (s *nativeSession) Stat(ctx context.Context, p string) (result *StatResult, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Stat(ctx, p)
}

func (s *nativeSession) Cat(ctx context.Context, p string) (result []byte, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Cat(ctx, p)
}

func (s *nativeSession) Echo(ctx context.Context, p string, data []byte) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Echo(ctx, p, data)
}

func (s *nativeSession) EchoCreate(ctx context.Context, p string, data []byte, mode uint32) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.EchoCreate(ctx, p, data, mode)
}

func (s *nativeSession) CreateFile(ctx context.Context, p string, mode uint32, exclusive bool) (stat *StatResult, created bool, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.CreateFile(ctx, p, mode, exclusive)
}

func (s *nativeSession) EchoAppend(ctx context.Context, p string, data []byte) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.EchoAppend(ctx, p, data)
}

func (s *nativeSession) Touch(ctx context.Context, p string) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Touch(ctx, p)
}

func (s *nativeSession) Mkdir(ctx context.Context, p string) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Mkdir(ctx, p)
}

func (s *nativeSession) MkdirMode(ctx context.Context, p string, mode uint32) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.MkdirMode(ctx, p, mode)
}

func (s *nativeSession) Rm(ctx context.Context, p string) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Rm(ctx, p)
}

func (s *nativeSession) Ls(ctx context.Context, p string) (result []string, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Ls(ctx, p)
}

func (s *nativeSession) LsLong(ctx context.Context, p string) (result []LsEntry, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.LsLong(ctx, p)
}

func (s *nativeSession) Rename(ctx context.Context, src, dst string, flags uint32) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Rename(ctx, src, dst, flags)
}

func (s *nativeSession) Mv(ctx context.Context, src, dst string) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Mv(ctx, src, dst)
}

func (s *nativeSession) Ln(ctx context.Context, target, linkpath string) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Ln(ctx, target, linkpath)
}

func (s *nativeSession) Readlink(ctx context.Context, p string) (result string, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Readlink(ctx, p)
}

func (s *nativeSession) Chmod(ctx context.Context, p string, mode uint32) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Chmod(ctx, p, mode)
}

func (s *nativeSession) Chown(ctx context.Context, p string, uid, gid uint32) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Chown(ctx, p, uid, gid)
}

func (s *nativeSession) Truncate(ctx context.Context, p string, size int64) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Truncate(ctx, p, size)
}

func (s *nativeSession) Utimens(ctx context.Context, p string, atimeMs, mtimeMs int64) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Utimens(ctx, p, atimeMs, mtimeMs)
}

func (s *nativeSession) SetAttrs(ctx context.Context, p string, upd AttrUpdate) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.SetAttrs(ctx, p, upd)
}

func (s *nativeSession) Info(ctx context.Context) (result *InfoResult, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Info(ctx)
}

func (s *nativeSession) WriteChunks(ctx context.Context, p string, chunks map[int][]byte, chunkSize int, newSize int64, hashes []string) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.WriteChunks(ctx, p, chunks, chunkSize, newSize, hashes)
}

func (s *nativeSession) ReadChunks(ctx context.Context, p string, indices []int, chunkSize int) (result map[int][]byte, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.ReadChunks(ctx, p, indices, chunkSize)
}

func (s *nativeSession) ChunkMeta(ctx context.Context, p string) (size int, hashes []string, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.ChunkMeta(ctx, p)
}

func (s *nativeSession) ReadChangeStream(ctx context.Context, lastID string, count int64) (result []ChangeStreamEntry, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.ReadChangeStream(ctx, lastID, count)
}

func (s *nativeSession) SubscribeInvalidationsWithReconnect(ctx context.Context, handler func(InvalidateEvent), onReconnect func()) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.SubscribeInvalidationsWithReconnect(ctx, handler, onReconnect)
}

func (s *nativeSession) SubscribeInvalidations(ctx context.Context, handler func(InvalidateEvent)) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.SubscribeInvalidations(ctx, handler)
}

func (s *nativeSession) WarmPathCache(ctx context.Context) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.WarmPathCache(ctx)
}

func (s *nativeSession) StatInode(ctx context.Context, inode uint64) (result *StatResult, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.StatInode(ctx, inode)
}

func (s *nativeSession) ReadInodeAt(ctx context.Context, inode uint64, off int64, size int) (result []byte, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.ReadInodeAt(ctx, inode, off, size)
}

func (s *nativeSession) WriteInodeAt(ctx context.Context, inode uint64, payload []byte, off int64) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.WriteInodeAt(ctx, inode, payload, off)
}

func (s *nativeSession) WriteInodeAtPath(ctx context.Context, inode uint64, path string, payload []byte, off int64) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.WriteInodeAtPath(ctx, inode, path, payload, off)
}

func (s *nativeSession) TruncateInode(ctx context.Context, inode uint64, size int64) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.TruncateInode(ctx, inode, size)
}

func (s *nativeSession) TruncateInodeAtPath(ctx context.Context, inode uint64, path string, size int64) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.TruncateInodeAtPath(ctx, inode, path, size)
}

func (s *nativeSession) Getlk(ctx context.Context, inode uint64, owner string, lock *FileLock) (result *FileLock, err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.Getlk(ctx, inode, s.lockOwner(owner), lock)
}

func (s *nativeSession) Setlk(ctx context.Context, inode uint64, owner string, lock *FileLock, wait bool) (err error) {
	if wait {
		// Sleeping for a lock is not an in-flight mutation. Admit each actual
		// attempt separately so barriers can drain, and each resumed attempt
		// still checks the pause gate, generation and session lease.
		for {
			err := s.Setlk(ctx, inode, owner, lock, false)
			if !errors.Is(err, ErrLockWouldBlock) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-s.ctx.Done():
				return ErrNativeSessionLost
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	err = s.nativeClient.Setlk(ctx, inode, s.lockOwner(owner), lock, wait)
	if err == nil {
		s.lockInodes.Store(s.keys.locks(strconv.FormatUint(inode, 10)), struct{}{})
	}
	return err
}

func (s *nativeSession) UnlockAll(ctx context.Context, inode uint64, owner string) (err error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return
	}
	defer func() { err = finish(err) }()
	return s.nativeClient.UnlockAll(ctx, inode, s.lockOwner(owner))
}
