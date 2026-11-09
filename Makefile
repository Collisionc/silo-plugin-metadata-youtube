.PHONY: build build-all test lint clean

BINARY=plugin
PLATFORMS=linux/amd64 linux/arm64 darwin/arm64
VERSION ?= $(shell git describe --tags --always 2>/dev/null | sed 's/^v//')
LDFLAGS=-s -w -X main.version=$(VERSION)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

# One static binary per platform Silo runs plugins on, each uploadable in
# Admin > Plugins.
build-all:
	@for platform in $(PLATFORMS); do \
		GOOS=$${platform%%/*} GOARCH=$${platform##*/} CGO_ENABLED=0 \
		go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-$${platform%%/*}-$${platform##*/} . || exit 1; \
	done

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

clean:
	rm -f $(BINARY) dist/$(BINARY)-*
