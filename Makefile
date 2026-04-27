.PHONY: help dev up down logs build test fmt vet tidy ps psql redis-cli seed

help:
	@echo "Полезные команды для разработки EduQuiz backend"
	@echo ""
	@echo "  make dev      — запустить postgres + redis в фоне и api локально (go run)"
	@echo "  make up       — поднять всё (api+pg+redis) через docker compose"
	@echo "  make down     — остановить и удалить контейнеры"
	@echo "  make logs     — tail логов сервисов"
	@echo "  make build    — собрать бинарь в bin/api"
	@echo "  make test     — go test -race ./..."
	@echo "  make fmt      — gofmt -w ."
	@echo "  make vet      — go vet ./..."
	@echo "  make tidy     — go mod tidy"
	@echo "  make psql     — открыть psql внутри контейнера"
	@echo "  make seed     — залить демо-учётки (teacher/student) и банк вопросов по надёжности"

dev:
	docker compose up -d postgres redis
	@echo "→ postgres on :5432, redis on :6379"
	@echo "→ запускаем api локально (Ctrl+C для остановки):"
	go run ./cmd/api

up:
	docker compose up -d --build

down:
	docker compose down -v

logs:
	docker compose logs -f --tail=100

build:
	mkdir -p bin
	go build -o bin/api ./cmd/api

test:
	go test -race -coverprofile=coverage.txt ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

ps:
	docker compose ps

psql:
	docker compose exec postgres psql -U eduquiz -d eduquiz

redis-cli:
	docker compose exec redis redis-cli

seed:
	go run ./cmd/seed
