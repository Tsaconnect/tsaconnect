.PHONY: run build test clean

run:
	go run ./cmd/server

build:
	go build -o ./tmp/server ./cmd/server

test:
	go test ./... -v -count=1

clean:
	rm -rf ./tmp
