.PHONY: test integration verify build up down logs smoke experiment-kill experiment-stale experiment-migrate experiment-checkpoint experiment-locality clean

test:
	go test -race ./...
	go vet ./...

integration:
	MOONCHESS_TEST_ETCD_ENDPOINT=http://localhost:12379 go test ./internal/control -run TestEtcdFencesExpiredOwner -count=1

verify: test up integration
	./scripts/smoke.sh
	./scripts/experiment-kill.sh
	./scripts/experiment-stale.sh
	./scripts/experiment-migrate.sh
	./scripts/experiment-checkpoint.sh
	./scripts/experiment-locality.sh

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

experiment-migrate:
	./scripts/experiment-migrate.sh

experiment-checkpoint:
	./scripts/experiment-checkpoint.sh

experiment-locality:
	./scripts/experiment-locality.sh

clean:
	docker compose down -v --remove-orphans
