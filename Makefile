BIN            := bin/oim
COORDINATOR_BIN := bin/oim-coordinator
DIRECTORY_BIN  := bin/oim-directory
STUB_EXO_BIN   := bin/stub-exo

.PHONY: build build-all test lint clean install sim sim-down

build:
	go build -o $(BIN) ./cmd/oim

build-all:
	go build -o $(BIN) ./cmd/oim
	go build -o $(COORDINATOR_BIN) ./cmd/coordinator
	go build -o $(DIRECTORY_BIN) ./cmd/directory
	go build -o $(STUB_EXO_BIN) ./cmd/stub-exo

install:
	go install ./cmd/oim

test:
	go test ./...

lint:
	go vet ./...

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
