package workspace

import (
	"context"

	"github.com/rowantrollope/afs/mount/client"
)

// scopedClient only supplies context fencing; filesystem behavior stays in the
// original engine. The embedded non-I/O helpers have no generation dependency.
type scopedClient struct {
	client.Client
	generation string
}

func (c *scopedClient) bind(ctx context.Context) context.Context {
	return client.WithWorkspaceGeneration(ctx, c.generation)
}
func (c *scopedClient) Stat(ctx context.Context, p string) (*client.StatResult, error) {
	return c.Client.Stat(c.bind(ctx), p)
}
func (c *scopedClient) Cat(ctx context.Context, p string) ([]byte, error) {
	return c.Client.Cat(c.bind(ctx), p)
}
func (c *scopedClient) Echo(ctx context.Context, p string, data []byte) error {
	return c.Client.Echo(c.bind(ctx), p, data)
}
func (c *scopedClient) EchoCreate(ctx context.Context, p string, data []byte, mode uint32) error {
	return c.Client.EchoCreate(c.bind(ctx), p, data, mode)
}
func (c *scopedClient) CreateFile(ctx context.Context, p string, mode uint32, exclusive bool) (*client.StatResult, bool, error) {
	return c.Client.CreateFile(c.bind(ctx), p, mode, exclusive)
}
func (c *scopedClient) EchoAppend(ctx context.Context, p string, data []byte) error {
	return c.Client.EchoAppend(c.bind(ctx), p, data)
}
func (c *scopedClient) Touch(ctx context.Context, p string) error {
	return c.Client.Touch(c.bind(ctx), p)
}
func (c *scopedClient) Mkdir(ctx context.Context, p string) error {
	return c.Client.Mkdir(c.bind(ctx), p)
}
func (c *scopedClient) MkdirMode(ctx context.Context, p string, mode uint32) error {
	return c.Client.MkdirMode(c.bind(ctx), p, mode)
}
func (c *scopedClient) Rm(ctx context.Context, p string) error {
	return c.Client.Rm(c.bind(ctx), p)
}
func (c *scopedClient) Ls(ctx context.Context, p string) ([]string, error) {
	return c.Client.Ls(c.bind(ctx), p)
}
func (c *scopedClient) LsLong(ctx context.Context, p string) ([]client.LsEntry, error) {
	return c.Client.LsLong(c.bind(ctx), p)
}
func (c *scopedClient) Rename(ctx context.Context, src, dst string, flags uint32) error {
	return c.Client.Rename(c.bind(ctx), src, dst, flags)
}
func (c *scopedClient) Mv(ctx context.Context, src, dst string) error {
	return c.Client.Mv(c.bind(ctx), src, dst)
}
func (c *scopedClient) Ln(ctx context.Context, target, linkpath string) error {
	return c.Client.Ln(c.bind(ctx), target, linkpath)
}
func (c *scopedClient) Readlink(ctx context.Context, p string) (string, error) {
	return c.Client.Readlink(c.bind(ctx), p)
}
func (c *scopedClient) Chmod(ctx context.Context, p string, mode uint32) error {
	return c.Client.Chmod(c.bind(ctx), p, mode)
}
func (c *scopedClient) Chown(ctx context.Context, p string, uid, gid uint32) error {
	return c.Client.Chown(c.bind(ctx), p, uid, gid)
}
func (c *scopedClient) Truncate(ctx context.Context, p string, size int64) error {
	return c.Client.Truncate(c.bind(ctx), p, size)
}
func (c *scopedClient) Utimens(ctx context.Context, p string, atimeMs, mtimeMs int64) error {
	return c.Client.Utimens(c.bind(ctx), p, atimeMs, mtimeMs)
}
func (c *scopedClient) SetAttrs(ctx context.Context, p string, update client.AttrUpdate) error {
	return c.Client.SetAttrs(c.bind(ctx), p, update)
}
func (c *scopedClient) Info(ctx context.Context) (*client.InfoResult, error) {
	if root, err := c.Stat(ctx, "/"); err != nil {
		return nil, err
	} else if root == nil || root.Type != "dir" {
		return nil, ErrWorkspaceChanged
	}
	return c.Client.Info(c.bind(ctx))
}
func (c *scopedClient) WriteChunks(ctx context.Context, p string, chunks map[int][]byte, chunkSize int, newSize int64, hashes []string) error {
	return c.Client.WriteChunks(c.bind(ctx), p, chunks, chunkSize, newSize, hashes)
}
func (c *scopedClient) ReadChunks(ctx context.Context, p string, indices []int, chunkSize int) (map[int][]byte, error) {
	return c.Client.ReadChunks(c.bind(ctx), p, indices, chunkSize)
}
func (c *scopedClient) ChunkMeta(ctx context.Context, p string) (int, []string, error) {
	return c.Client.ChunkMeta(c.bind(ctx), p)
}
func (c *scopedClient) ReadChangeStream(ctx context.Context, lastID string, count int64) ([]client.ChangeStreamEntry, error) {
	if root, err := c.Stat(ctx, "/"); err != nil {
		return nil, err
	} else if root == nil || root.Type != "dir" {
		return nil, ErrWorkspaceChanged
	}
	return c.Client.ReadChangeStream(c.bind(ctx), lastID, count)
}
func (c *scopedClient) SubscribeInvalidations(ctx context.Context, handler func(client.InvalidateEvent)) error {
	return c.Client.SubscribeInvalidations(c.bind(ctx), handler)
}
func (c *scopedClient) SubscribeInvalidationsWithReconnect(ctx context.Context, handler func(client.InvalidateEvent), reconnect func()) error {
	return c.Client.SubscribeInvalidationsWithReconnect(c.bind(ctx), handler, reconnect)
}

var _ client.Client = (*scopedClient)(nil)
