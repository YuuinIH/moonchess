.PHONY: test build up down logs smoke experiment-kill experiment-stale clean

test:
	go test -race ./...
	go vet ./...

build:
	go build ./cmd/moonchess

up:
	docker compose up --build -d

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

smoke:
	./scripts/smoke.sh

experiment-kill:
	./scripts/experiment-kill.sh

experiment-stale:
	./scripts/experiment-stale.sh

clean:
	docker compose down -v --remove-orphans
