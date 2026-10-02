# Load variables from .env if it exists and export them to commands.
-include .env
export

.PHONY: run test build

run:
	go run ./cmd/api

test:
	go test ./... -race -cover

build:
	go build -o bin/api ./cmd/api
