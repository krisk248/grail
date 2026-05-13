.PHONY: help build frontend image up down restart logs clean

IMAGE ?= ghcr.io/krisk248/grail:latest

help:
	@echo "Targets:"
	@echo "  make build      Build frontend + embed + docker image (tagged $(IMAGE))"
	@echo "  make frontend   Build only the SvelteKit static bundle"
	@echo "  make image      Build only the docker image (assumes frontend is already built)"
	@echo "  make up         docker compose up -d"
	@echo "  make down       docker compose down"
	@echo "  make restart    down + up"
	@echo "  make logs       Tail grail logs"
	@echo "  make clean      Remove build artefacts (web/build, internal/web/build)"

frontend:
	cd web && npm install --no-fund --no-audit
	cd web && npm run build
	rm -rf internal/web/build
	cp -r web/build internal/web/build

image:
	docker build -t $(IMAGE) .

build: frontend image

up:
	docker compose up -d

down:
	docker compose down

restart: down up

logs:
	docker compose logs -f grail

clean:
	rm -rf web/build internal/web/build/*
	@touch internal/web/build/.gitkeep
