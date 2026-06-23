.PHONY: fmt test vet build migrate seed reconcile retain

fmt:
	gofmt -w cmd internal web

test:
	go test ./...

vet:
	go vet ./...

build:
	go build -o bin/demo ./cmd/demo

migrate:
	go run ./cmd/demo migrate

seed:
	go run ./cmd/demo seed

reconcile:
	go run ./cmd/demo reconcile

retain:
	go run ./cmd/demo retain
