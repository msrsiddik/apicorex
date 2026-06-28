.PHONY: proto build test run

proto:
	protoc \
		--go_out=gen --go_opt=paths=source_relative \
		--go-grpc_out=gen --go-grpc_opt=paths=source_relative,require_unimplemented_servers=false \
		-I . \
		proto/apicorex/v1/registry.proto proto/apicorex/v1/plugin.proto

build:
	go build ./...

test:
	go test ./...

run:
	go run cmd/apicorex/main.go
