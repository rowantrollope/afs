.PHONY: build test race integration cli-test compat check clean

build:
	go build -o bin/afs ./cmd/afs

test:
	go test ./...

race:
	go test -race ./...

integration:
	go test -tags=integration -timeout=15m -count=1 -v ./tests/e2e

cli-test: integration

compat:
	go test -tags=compatibility -timeout=15m -count=1 -v ./tests/compat

check:
	go build ./...
	go vet ./...
	go test ./...
	go test -race ./...
	$(MAKE) integration

clean:
	go clean ./...
	rm -f bin/afs
