.PHONY: test integration lint build generate demo

test:
	go test -race ./...
integration:
	go test -race -tags=integration ./internal/store -count=1
lint:
	go vet ./...
build:
	go build -o bin/relay ./cmd/relay
	go build -o bin/receiver ./cmd/receiver
generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.29.0 generate
demo:
	sh scripts/demo.sh
