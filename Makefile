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
	docker run --rm --entrypoint promtool -v "$(CURDIR)/deploy/prometheus:/rules:ro" -w /rules prom/prometheus:v3.5.0 test rules rules_test.yaml

HELM_IMAGE        ?= alpine/helm:4.3.1
KUBECONFORM_IMAGE ?= ghcr.io/yannh/kubeconform:v0.8.0
HELM        = docker run --rm -v "$(CURDIR)/charts:/charts:ro" $(HELM_IMAGE)
KUBECONFORM = docker run --rm -i $(KUBECONFORM_IMAGE) -strict -summary \
	-schema-location default \
	-schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

.PHONY: chart-check

# The chart carries a copy of the rules; they must not drift apart.
chart-check:
	cmp deploy/prometheus/rules.yaml charts/noroshi/files/rules.yaml
	$(HELM) lint --strict /charts/noroshi -f /charts/noroshi/ci/full-values.yaml
	$(HELM) template t /charts/noroshi --set secret.name=noroshi | $(KUBECONFORM) -
	$(HELM) template t /charts/noroshi -f /charts/noroshi/ci/full-values.yaml | $(KUBECONFORM) -
