BINARY := pulse

build:
	go build -o $(BINARY) .

test:
	go test ./...

vet: ; go vet ./...

.PHONY: build test vet
