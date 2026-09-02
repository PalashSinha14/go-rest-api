.PHONY: help run build test test-race cover lint fmt tidy up down logs clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

run: ## Run the API locally (needs MongoDB on MONGO_URI)
	go run ./cmd/api

build: ## Compile the binary to ./bin/api
	go build -trimpath -ldflags="-s -w" -o bin/api ./cmd/api

test: ## Run the test suite
	go test ./... -count=1

test-race: ## Run the test suite under the race detector
	go test ./... -race -count=1

cover: ## Run tests and open an HTML coverage report
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -html=coverage.out -o coverage.html
	@echo "report written to coverage.html"

lint: ## Vet the code
	go vet ./...

fmt: ## Format all Go source
	gofmt -w .

tidy: ## Prune and verify the module graph
	go mod tidy

up: ## Start API + MongoDB in Docker
	docker compose up --build -d

down: ## Stop the stack and remove volumes
	docker compose down -v

logs: ## Follow the API container logs
	docker compose logs -f api

clean: ## Remove build and coverage artefacts
	rm -rf bin coverage.out coverage.html
