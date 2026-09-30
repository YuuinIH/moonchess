.PHONY: test integration verify build up down logs smoke experiment-kill experiment-stale clean

test:
	go test -race ./...
	go vet ./...

integration:
	MOONCHESS_TEST_POSTGRES_URL='postgres://moonchess:moonchess@localhost:15432/moonchess?sslmode=disable' go test ./internal/control -run TestPostgresPlaneFencesExpiredOwner -count=1

verify: test up integration
	./scripts/smoke.sh
	./scripts/experiment-kill.sh
	./scripts/experiment-stale.sh

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
