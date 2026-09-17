.PHONY: build install native test race native-deps-test integration cli-test compat multiwriter multiwriter-native multiwriter-smoke check clean

build:
	go build -o bin/afs ./cmd/afs

# Override with make install INSTALL_DIR=/your/bin
INSTALL_DIR ?= $(HOME)/.local/bin

install: build
	@set -eu; \
	mkdir -p "$(INSTALL_DIR)"; \
	install_dir=$$(cd "$(INSTALL_DIR)" && pwd -P); \
	source_path="$(CURDIR)/bin/afs"; \
	destination="$$install_dir/afs"; \
	if [ "$$destination" = "$$source_path" ]; then \
		printf 'Already installed: %s\n' "$$destination"; \
	elif [ -L "$$destination" ] && [ "$$(readlink "$$destination")" = "$$source_path" ]; then \
		printf 'Already installed: %s -> %s\n' "$$destination" "$$source_path"; \
	elif [ -f "$$destination" ] && [ ! -L "$$destination" ] && \
		go version -m "$$destination" 2>/dev/null | awk '$$1 == "path" && $$2 == "github.com/rowantrollope/afs/cmd/afs" { found = 1 } END { exit !found }'; then \
		staging_dir=$$(mktemp -d "$$install_dir/.afs-install.XXXXXX"); \
		trap 'rm -rf "$$staging_dir"' 0; \
		ln -s "$$source_path" "$$staging_dir/afs"; \
		mv -f "$$staging_dir/afs" "$$destination"; \
		printf 'Updated: %s -> %s\n' "$$destination" "$$source_path"; \
	elif [ -e "$$destination" ] || [ -L "$$destination" ]; then \
		printf 'Refusing to replace existing path: %s\n' "$$destination" >&2; \
		exit 1; \
	else \
		ln -s "$$source_path" "$$destination"; \
		printf 'Installed: %s -> %s\n' "$$destination" "$$source_path"; \
	fi; \
	case ":$$PATH:" in \
		*":$$install_dir:"*) ;; \
		*) printf 'Add this directory to PATH in your shell startup file: %s\n' "$$install_dir" ;; \
	esac
	@set -eu; \
	config_dir="$(HOME)/.config/afs-lite"; \
	if [ ! -e "$$config_dir/config.json" ] && [ ! -L "$$config_dir/config.json" ]; then \
		umask 077; \
		mkdir -p "$$config_dir"; \
		(set -C; cat config.example.json > "$$config_dir/config.json"); \
		printf 'Created config: %s/config.json\n' "$$config_dir"; \
	fi

native: build
	go build -o bin/afsmount ./cmd/afsmount

test:
	go test ./...

race:
	go test -race ./...

native-deps-test:
	go -C third_party/go-fuse test -race ./fuse -run '^TestMountContext'
	go -C third_party/go-fuse test -race ./fs/flush_owner_test.go -run '^TestFlushOwnerBridge$$'
	go -C third_party/go-nfs test -race ./...

integration:
	go test -tags=integration -timeout=15m -count=1 -v ./tests/e2e ./internal/filehistory

cli-test: integration

multiwriter:
	python3 scripts/multiwriter_lab.py $(LAB_ARGS)

multiwriter-native: native
	python3 scripts/native_lab_supervisor.py --binary "$(CURDIR)/bin/afs" --helper "$(CURDIR)/bin/afsmount" $(LAB_ARGS)

multiwriter-smoke:
	python3 -m unittest discover -s tests/multiwriter -p 'test_*.py'
	python3 scripts/multiwriter_lab.py --clients 4 --files 8 --rounds 2

compat:
	go test -tags=compatibility -timeout=15m -count=1 -v ./tests/compat

check:
	go build ./...
	go vet ./...
	go test ./...
	go test -race ./...
	$(MAKE) native-deps-test
	$(MAKE) integration
	$(MAKE) multiwriter-smoke

clean:
	go clean ./...
	rm -f bin/afs bin/afsmount
