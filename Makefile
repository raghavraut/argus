# Project Argus — developer workflow.
#   make build      compile ./cmd/argus to ./argus(.exe)
#   make test       full unit suite
#   make vet        static analysis
#   make snapshot   local GoReleaser snapshot (dist/, no publish)
#   make clean      remove build artifacts

BINARY := argus
PKG := ./cmd/argus

build:
	go build -o $(BINARY) $(PKG)

test:
	go test -count=1 -timeout 300s ./internal/...

vet:
	go vet ./...

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -f $(BINARY) $(BINARY).exe
	rm -f *.db *.db-wal *.db-shm *.db-journal
	rm -f corpus.json *.dot *.mmd
	rm -rf dist/

.PHONY: build test vet snapshot clean
