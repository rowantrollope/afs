package filehistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/rediscontent"
)

var ErrPreparationConflict = errors.New("file changed while preparing history")

type PrepareRequest struct {
	InodeID          string
	ExpectedRevision string
	Path             string
	AfterBody        string
	AfterRef         string
	AfterSize        int64
	AfterType        string
	AfterHash        string
	KnownContent     string
	HasKnownContent  bool
}

// HashContent hashes logical bytes with bounded memory, including sparse Array
// regions. Callers must supply immutable or fenced content and its exact size.
func HashContent(ctx context.Context, rdb *redis.Client, key, ref string, size int64) (string, error) {
	if size < 0 {
		return "", fmt.Errorf("invalid history content size")
	}
	if size > 0 {
		exists, err := rdb.Exists(ctx, key).Result()
		if err != nil {
			return "", err
		}
		if exists == 0 {
			return "", fmt.Errorf("history content is missing")
		}
	}
	h := sha256.New()
	const batch = 1 << 20
	for offset := int64(0); offset < size; offset += batch {
		n := min(int64(batch), size-offset)
		data, err := rediscontent.ReadRange(ctx, rdb, key, ref, size, offset, int(n))
		if err != nil {
			return "", err
		}
		if int64(len(data)) != n {
			return "", fmt.Errorf("history content has inconsistent size")
		}
		_, _ = h.Write(data)
	}
	if size > 0 {
		exists, err := rdb.Exists(ctx, key).Result()
		if err != nil {
			return "", err
		}
		if exists == 0 {
			return "", fmt.Errorf("history content expired while hashing")
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var preparePinScript = redis.NewScript(`
local values=redis.call('HGETALL',KEYS[1])
local fields={}
for i=1,#values,2 do fields[values[i]]=values[i+1] end
if (fields.revision or '')~=ARGV[1] then return redis.error_reply('HISTORY_PREPARATION_CONFLICT') end
if not fields.type then return '{}' end
local id=fields.parent
local parts={fields.name or ''}
local seen={}
while id and id~='1' and id~='' do
 if seen[id] or #parts>4096 then return redis.error_reply('invalid inode ancestry') end
 seen[id]=true
 local parent=redis.call('HMGET',ARGV[2]..'inode:'..id,'parent','name')
 if not parent[1] or not parent[2] then return redis.error_reply('invalid inode ancestry') end
 table.insert(parts,1,parent[2]);id=parent[1]
end
fields.path='/'..table.concat(parts,'/')
if fields.type=='file' and tonumber(fields.size or 0)>0 and (fields.content_ref=='ext' or fields.content_ref=='array')
 and not (fields.history_revision==fields.revision and fields.history_hash) then
 if redis.call('COPY',KEYS[2],KEYS[3],'REPLACE')==0 then return redis.error_reply('history content is missing') end
 redis.call('PEXPIRE',KEYS[3],3600000)
end
return cjson.encode(fields)
`)

// Prepare computes SHA256 before publication, pinning the exact preimage and
// recording its revision. The capture script consumes only matching prepared
// hashes and fails closed if policy changes from off to on after this read.
func Prepare(ctx context.Context, rdb *redis.Client, id, operationID string, requests []PrepareRequest) error {
	policy, err := GetPolicy(ctx, rdb, id)
	if err != nil {
		return err
	}
	if policy.Mode == ModeOff {
		return nil
	}
	prefix := Prefix(id)
	base := "afs:{" + id + "}:"
	values := map[string]any{}
	for _, request := range requests {
		pin := prefix + "prepare-read:" + uuid.NewString()
		raw, err := preparePinScript.Run(ctx, rdb, []string{base + "inode:" + request.InodeID, base + "content:" + request.InodeID, pin}, request.ExpectedRevision, base).Text()
		if err != nil {
			if strings.Contains(err.Error(), "HISTORY_PREPARATION_CONFLICT") {
				return ErrPreparationConflict
			}
			return err
		}
		fields := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return err
		}
		cleanup := func() {
			c, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = rdb.Unlink(c, pin).Err()
		}
		if !policy.Matches(fields["path"]) && !policy.Matches(request.Path) {
			cleanup()
			continue
		}
		beforeHash := ""
		if fields["type"] == "file" {
			if fields["history_revision"] == fields["revision"] {
				beforeHash = fields["history_hash"]
			}
			if beforeHash == "" {
				size, parseErr := strconv.ParseInt(fields["size"], 10, 64)
				if parseErr != nil {
					cleanup()
					return parseErr
				}
				if fields["content_ref"] != "ext" && fields["content_ref"] != "array" {
					sum := sha256.Sum256([]byte(fields["content"]))
					beforeHash = hex.EncodeToString(sum[:])
				} else {
					beforeHash, err = HashContent(ctx, rdb, pin, fields["content_ref"], size)
				}
				if err != nil {
					cleanup()
					return err
				}
			}
		}
		cleanup()
		stem := request.InodeID + ":"
		values[stem+"revision"] = request.ExpectedRevision
		if beforeHash != "" {
			values[stem+"before_hash"] = beforeHash
		}
		if request.AfterType == "file" {
			afterHash := beforeHash
			if request.AfterHash != "" {
				afterHash = request.AfterHash
			} else if request.HasKnownContent {
				if int64(len(request.KnownContent)) != request.AfterSize {
					return fmt.Errorf("invalid known history content size")
				}
				sum := sha256.Sum256([]byte(request.KnownContent))
				afterHash = hex.EncodeToString(sum[:])
			} else if request.AfterBody != "" {
				afterHash, err = HashContent(ctx, rdb, request.AfterBody, request.AfterRef, request.AfterSize)
				if err != nil {
					return err
				}
			}
			values[stem+"after_hash"] = afterHash
			values[stem+"after_body"] = request.AfterBody
		}
	}
	if len(values) == 0 {
		return nil
	}
	_, err = rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, prefix+"prepared:"+operationID, values)
		pipe.Expire(ctx, prefix+"prepared:"+operationID, time.Hour)
		return nil
	})
	return err
}

func DiscardPreparation(rdb *redis.Client, id, operationID string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = rdb.Unlink(ctx, Prefix(id)+"prepared:"+operationID).Err()
}
