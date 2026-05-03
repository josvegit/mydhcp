GO     := go
UI_DIR := plugins/dashboard/ui

N ?= 1

COMPOSE_UI    := docker compose -f docker/lab/docker-compose.ui.yml
CLIENT_SH     := bash docker/lab/client.sh
LAB_JSON      := docker/lab/server.json

LAB_SUBNET    := $(shell python3 -c "import json; d=json.load(open('$(LAB_JSON)')); print(d['subnets'][0]['network'])")
LAB_GATEWAY   := $(shell python3 -c "import json; d=json.load(open('$(LAB_JSON)')); print(d['subnets'][0]['router'])")
LAB_SERVER_IP := $(shell python3 -c "import json; d=json.load(open('$(LAB_JSON)')); print(d['server']['server_ip'])")

export LAB_SUBNET LAB_GATEWAY LAB_SERVER_IP

.PHONY: test test-cover ui-install ui-build \
        lab-up lab-down lab-logs lab-clients lab-kill

## ── Tests ───────────────────────────────────────────────────────────────────

test:
	$(GO) test ./internal/... -v -race -count=1

test-cover:
	$(GO) test ./internal/... -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -html=coverage.out -o coverage.html

## ── UI build ────────────────────────────────────────────────────────────────

ui-install:
	cd $(UI_DIR) && npm install

ui-build:
	cd $(UI_DIR) && npm run build

## ── UI Lab ──────────────────────────────────────────────────────────────────

lab-up:
	$(COMPOSE_UI) up -d --build
	@echo ""
	@echo "  Dashboard UI  → http://localhost:8080"
	@echo "  Management API → http://localhost:8067"

lab-down:
	$(COMPOSE_UI) down --remove-orphans
	-$(CLIENT_SH) kill all 2>/dev/null || true

lab-logs:
	$(COMPOSE_UI) logs -f dhcp-server

# Spawn N clients: make lab-clients N=5
lab-clients:
	@for i in $$(seq 1 $(N)); do $(CLIENT_SH) spawn; done

lab-kill:
	$(CLIENT_SH) kill all
