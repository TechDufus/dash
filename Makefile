HL_OP ?= ../homelab/scripts/hl-op
KUBECONFIG ?= ../homelab/.kube/config

.PHONY: generate check dev-api dev-web image hooks hooks-uninstall

generate:
	go tool tygo generate

check: generate
	git diff --exit-code web/src/api/types.ts
	go vet ./...
	go test ./...
	cd web && npm ci && npm run check && npm run build

dev-api:
	$(HL_OP) run --env-file dev/secrets.env -- env DASH_DEV=true KUBECONFIG=$(KUBECONFIG) go run ./cmd/dash

dev-web:
	cd web && npm run dev

image:
	docker buildx build --load -t dash:dev .

hooks:
	git config core.hooksPath .githooks

hooks-uninstall:
	git config --unset core.hooksPath || true
