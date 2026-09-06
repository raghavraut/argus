# Project Rarefy — developer workflow.
#   make build      compile ./cmd/rarefy to ./rarefy(.exe)
#   make test       full unit suite
#   make vet        static analysis
#   make install    build + copy binary to /usr/local/bin (use sudo)
#   make snapshot   local GoReleaser snapshot (dist/, no publish)
#   make clean      remove build artifacts

BINARY := rarefy
PKG := ./cmd/rarefy
PREFIX := /usr/local/bin

build:
	go build -o $(BINARY) $(PKG)

test:
	go test -count=1 -timeout 300s ./internal/...

vet:
	go vet ./...

install: build
	install -m755 $(BINARY) $(PREFIX)/$(BINARY)
	@echo "installed to $(PREFIX)/$(BINARY) — run 'rarefy --help'"

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -f $(BINARY) $(BINARY).exe
	rm -f *.db *.db-wal *.db-shm *.db-journal
	rm -f corpus.json *.dot *.mmd
	rm -rf dist/

.PHONY: build test vet install snapshot clean
