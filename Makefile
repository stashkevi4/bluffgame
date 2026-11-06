.PHONY: run test build

run:
	go run .

test:
	go test -race ./...
	node --test scripts/i18n.test.cjs

build:
	go build -o varianty .
