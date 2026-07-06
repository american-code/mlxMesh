BIN            := bin/oim
COORDINATOR_BIN := bin/oim-coordinator
DIRECTORY_BIN  := bin/oim-directory
STUB_EXO_BIN   := bin/stub-exo

VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo dev-$(shell git rev-parse --short HEAD 2>/dev/null || echo none))
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
VPKG    := github.com/open-inference-mesh/oim/internal/version
LDFLAGS := -s -w -buildid= -X $(VPKG).Version=$(VERSION) -X $(VPKG).Commit=$(COMMIT)

.PHONY: build build-all test test-integration lint clean install sim sim-down release image version

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/oim

build-all:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/oim
	go build -ldflags "$(LDFLAGS)" -o $(COORDINATOR_BIN) ./cmd/coordinator
	go build -ldflags "$(LDFLAGS)" -o $(DIRECTORY_BIN) ./cmd/directory
	go build -ldflags "$(LDFLAGS)" -o $(STUB_EXO_BIN) ./cmd/stub-exo

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/oim

version:
	@echo "$(VERSION) ($(COMMIT))"

# Cross-platform, reproducible, checksummed release artifacts into dist/.
release:
	VERSION=$(VERSION) scripts/build-release.sh

# Version-stamped container image.
image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$$(TZ=UTC git log -1 --pretty=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ) \
		-t mlxmesh:$(VERSION) -t mlxmesh:latest .

test:
	go test ./...

lint:
	golangci-lint run ./...

test-integration:
	go test -tags integration ./tests/ -run Integration -v

clean:
	rm -rf bin/

# Docker simulation cluster
gen-compose:
	go run ./tools/gen-compose --us=13 --eu=12 > docker-compose.yml

sim: gen-compose
	docker compose build
	docker compose up

sim-down:
	docker compose down
