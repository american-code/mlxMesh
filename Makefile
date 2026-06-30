BIN := bin/oim
COORDINATOR_BIN := bin/oim-coordinator
DIRECTORY_BIN := bin/oim-directory

.PHONY: build build-all test lint clean install

build:
	go build -o $(BIN) ./cmd/oim

build-all:
	go build -o $(BIN) ./cmd/oim
	go build -o $(COORDINATOR_BIN) ./cmd/coordinator
	go build -o $(DIRECTORY_BIN) ./cmd/directory

install:
	go install ./cmd/oim

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf bin/
