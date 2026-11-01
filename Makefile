HL_OP ?= ../homelab/scripts/hl-op
KUBECONFIG ?= ../homelab/.kube/config

.PHONY: generate check dev-api dev-web image hooks hooks-uninstall

generate:
	go tool tygo generate
	go run ./cmd/dash config env --markdown > docs/configuration.md.tmp
	mv docs/configuration.md.tmp docs/configuration.md

check: generate
	git diff --exit-code web/src/api/types.ts docs/configuration.md
	go vet ./...
	go test ./...
	cd web && npm ci && npm run check && npm run build

# dev/site.env (see dev/site.env.example) holds this lab's URLs; it is optional. dev/secrets.env
# references the Proxmox token as one <user>!<token>=<secret> value, split here into the two
# variables dash reads.
dev-api:
	set -a; [ ! -f dev/site.env ] || . ./dev/site.env; set +a; \
	$(HL_OP) run --env-file dev/secrets.env -- sh -c 'DASH_PLUGINS_PROXMOX_TOKEN_ID="$${DEV_PROXMOX_TOKEN%%=*}"; DASH_PLUGINS_PROXMOX_TOKEN_SECRET="$${DEV_PROXMOX_TOKEN#*=}"; export DASH_PLUGINS_PROXMOX_TOKEN_ID DASH_PLUGINS_PROXMOX_TOKEN_SECRET; unset DEV_PROXMOX_TOKEN; exec env DASH_DEV=true KUBECONFIG=$(KUBECONFIG) go run ./cmd/dash'

dev-web:
	cd web && npm run dev

image:
	docker buildx build --load -t dash:dev .

hooks:
	git config core.hooksPath .githooks

hooks-uninstall:
	git config --unset core.hooksPath || true
