.PHONY: build test lint tidy clean release-dry

build:
	go build ./...

test:
	go test ./... -count=1 -timeout 120s

test-verbose:
	go test ./... -count=1 -timeout 120s -v

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

clean:
	go clean ./...

release-dry:
	goreleaser release --snapshot --clean

# Cross-platform build check
build-all:
	GOOS=linux   GOARCH=amd64  go build -o /dev/null ./cmd/lapp
	GOOS=darwin  GOARCH=arm64  go build -o /dev/null ./cmd/lapp
	GOOS=windows GOARCH=amd64  go build -o /dev/null ./cmd/lapp

# Generate hash test vectors (requires bun)
vectors:
	bun run scripts/gen_vectors.mjs
