GO_IMAGE ?= golang:1.26-alpine
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DOCKER_GO = docker run --rm \
	-v noroshi-gomod:/go/pkg/mod -v noroshi-gocache:/root/.cache/go-build \
	-v "$(CURDIR):/src" -w /src $(GO_IMAGE)

.PHONY: test vet fmt tidy image image-amd64 validate

test:
	$(DOCKER_GO) go test ./...

vet:
	$(DOCKER_GO) go vet ./...

fmt:
	$(DOCKER_GO) gofmt -w .

tidy:
	$(DOCKER_GO) go mod tidy

validate:
	$(DOCKER_GO) go run ./cmd/noroshi validate deploy/config.example.yaml

image:
	docker build --build-arg VERSION=$(VERSION) -t noroshi:$(VERSION) .

image-amd64:
	docker buildx build --platform linux/amd64 --build-arg VERSION=$(VERSION) -t noroshi:$(VERSION)-amd64 --load .

.PHONY: dev dev-down e2e e2e-down rules-check

dev:
	docker compose -f deploy/compose/compose.dev.yaml up -d --build

dev-down:
	docker compose -f deploy/compose/compose.dev.yaml down -v

e2e:
	docker compose -f deploy/compose/compose.e2e.yaml up -d --build

e2e-down:
	docker compose -f deploy/compose/compose.e2e.yaml down -v

rules-check:
	docker run --rm --entrypoint promtool -v "$(CURDIR)/deploy/prometheus:/rules:ro" prom/prometheus:v3.5.0 check rules /rules/rules.yaml
