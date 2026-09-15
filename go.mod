module github.com/rowantrollope/afs

go 1.22.2

require (
	github.com/alicebob/miniredis/v2 v2.37.0
	github.com/fsnotify/fsnotify v1.7.0
	github.com/redis/go-redis/v9 v9.18.0
	github.com/sabhiram/go-gitignore v0.0.0-20210923224102-525f6e181f06
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/rasky/go-xdr v0.0.0-20170124162913-1a41d1a06c93 // indirect
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-git/go-billy/v5 v5.6.2
	github.com/hanwen/go-fuse/v2 v2.7.2
	github.com/willscott/go-nfs v0.0.3
	github.com/willscott/go-nfs-client v0.0.0-20240104095149-b44639837b00
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.29.0
)

replace github.com/willscott/go-nfs => ./third_party/go-nfs

replace github.com/hanwen/go-fuse/v2 => ./third_party/go-fuse
