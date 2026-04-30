BINARY    := mydhcp
GO        := go
COMPOSE   := docker compose -f docker/lab/docker-compose.yml
CLIENT_SH := bash docker/lab/client.sh

N ?= 1

.PHONY: build test test-cover lint clean \
        lab-up lab-down lab-logs lab-leases \
        lab-client lab-clients lab-kill \
        lab-scenario

## ── Build ──────────────────────────────────────────────────────────────────

build:
	$(GO) build -o $(BINARY) ./cmd/mydhcp

## ── Tests ──────────────────────────────────────────────────────────────────

test:
	$(GO) test ./internal/... -v -race -count=1

test-cover:
	$(GO) test ./internal/... -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "coverage report: coverage.html"

lint:
	$(GO) vet ./...

## ── Interactive Lab ─────────────────────────────────────────────────────────

# Start the DHCP server in the background
lab-up:
	$(COMPOSE) up -d --build
	@echo ""
	@echo "  Server running.  Management API: http://localhost:8067"
	@echo "  make lab-logs     – tail server logs"
	@echo "  make lab-leases   – show current leases"
	@echo "  make lab-client   – spawn a DHCP client"
	@echo "  make lab-down     – stop everything"

# Stop the server and clean up containers/network
lab-down:
	$(COMPOSE) down --remove-orphans
	-$(CLIENT_SH) kill all 2>/dev/null || true

# Follow live server logs (Ctrl-C to stop)
lab-logs:
	$(COMPOSE) logs -f dhcp-server

# Pretty-print current leases from the management API
lab-leases:
	@curl -sf http://localhost:8067/leases | python3 -m json.tool || \
	    echo "(server not running — did you run 'make lab-up'?)"

# Spawn a single real udhcpc Alpine client
lab-client:
	$(CLIENT_SH) spawn

# Spawn N clients: make lab-clients N=5
lab-clients:
	@for i in $$(seq 1 $(N)); do $(CLIENT_SH) spawn; done

# Kill all client containers (or one by name: make lab-kill NAME=foo)
lab-kill:
ifdef NAME
	$(CLIENT_SH) kill $(NAME)
else
	$(CLIENT_SH) kill all
endif

# Run the Go scenario client inside the lab network.
#   make lab-scenario SCENARIO=dora
#   make lab-scenario SCENARIO=flood N=30
#   make lab-scenario SCENARIO=decline MAC=de:ad:be:ef:00:42
lab-scenario:
	$(eval SCENARIO ?= dora)
	$(eval MAC      ?= )
	$(eval VENDOR   ?= )
	$(eval REQIP    ?= )
	docker run --rm --privileged \
	    --network dhcp-lab-net \
	    mydhcp:lab \
	    dhcpclient \
	    -scenario "$(SCENARIO)" \
	    $(if $(MAC),-mac "$(MAC)") \
	    $(if $(VENDOR),-vendor "$(VENDOR)") \
	    $(if $(REQIP),-reqip "$(REQIP)") \
	    $(if $(filter flood,$(SCENARIO)),-flood-count $(N))

## ── Misc ────────────────────────────────────────────────────────────────────

clean:
	rm -f $(BINARY) coverage.out coverage.html
