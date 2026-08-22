.PHONY: proto build test run dev

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

# dev runs Core under air, rebuilding and restarting on every save.
#
# .env.dev puts it on :19999 with its own plugin key, so it is a second stack
# beside the deployed one on :9999 rather than a replacement for it. Both can run
# at once; a plugin belongs to exactly one of them.
dev:
	@test -f .env.dev || (echo "no .env.dev — copy .env.dev.example and fill it in"; exit 1)
	@set -a; . ./.env.dev; set +a; air
