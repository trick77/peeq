.PHONY: build test coverage backend-coverage fe-build fe-test fe-coverage run dev tidy

tidy:
	cd backend && go mod tidy

test:
	cd backend && go test ./...

coverage: backend-coverage fe-coverage

backend-coverage:
	mkdir -p coverage
	cd backend && go test -race -covermode=atomic -coverprofile=../coverage/backend.out ./...
	cd backend && go tool cover -func=../coverage/backend.out | tail -1
	./hack/coverage-gate.sh backend

fe-test:
	cd ui && npm run test -- --run

fe-coverage:
	cd ui && npm run test -- --run --coverage

fe-build:
	cd ui && npm ci && npm run build

build: fe-build
	cd backend && CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/trick77/peeq/internal/version.Version=$$(git rev-parse --short HEAD 2>/dev/null || echo dev)" -o ../bin/peeq ./cmd/peeq

run:
	cd backend && go run ./cmd/peeq

dev:
	./hack/dev.sh
