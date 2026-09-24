# Lenovo Staff Manager.
#
# Six working targets (SPEC §1): build, run, test, migrate, image, clean,
# plus `ios` (SPEC §10.1: the app's one build target) and `help`, which only
# lists them. Formatting, vet and other checks run as
# steps inside the working targets and are deliberately not callable on their
# own. Do not add further targets.
#
# A target's `## ` comment is its `make help` line.

GO      ?= go
BIN     := bin/lsm
IMAGE   ?= lsm
TAG     ?= latest
GOFILES  = $(shell find cmd internal migrations web -name '*.go')

# Local runtime paths; override on the command line or in the environment.
export LSM_DB_PATH    ?= data/lsm.db
export LSM_BACKUP_DIR ?= backup
export LSM_SEED_DIR   ?= seed

# Checks shared by build, test and image.
define check
	@unformatted="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...
endef

.PHONY: build run test migrate image clean ios help

# Bare `make` lists the targets rather than building.
.DEFAULT_GOAL := help

build: ## Check formatting and vet, then compile bin/lsm
	$(check)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/lsm

run: build ## Build, then serve on :8080 (applies migrations first)
	./$(BIN) serve

test: ## Check formatting and vet, then run all tests with -race
	$(check)
	$(GO) test -race -count=1 ./...

migrate: build ## Build, then apply pending migrations and load missing seed data
	./$(BIN) migrate -seed

image: ## Check formatting and vet, then build the Docker image (lsm:latest)
	$(check)
	docker build -f deploy/Dockerfile -t $(IMAGE):$(TAG) .

ios: ## Build the iOS app and install it on the connected iPhone (DEVICE=sim for the Simulator)
	ios/install.sh

# Removes build output only. The database and backups are data, not build
# artefacts, and are never touched here.
clean: ## Remove build output (never the database or backups)
	rm -rf bin ios/build
	$(GO) clean -testcache

help: ## List the targets
	@awk -F':.*## ' '/^[a-z]+:.*## / { printf "  %-8s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
