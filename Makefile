HL_OP ?= ../homelab/scripts/hl-op
KUBECONFIG ?= ../homelab/.kube/config

.PHONY: generate check dev-api dev-web image hooks hooks-uninstall

generate:
	go tool tygo generate
	go run ./cmd/dash config env --markdown > docs/configuration.md.tmp
	mv docs/configuration.md.tmp docs/configuration.md
	go run ./cmd/dash api openapi > docs/api/openapi.json.tmp
	mv docs/api/openapi.json.tmp docs/api/openapi.json
	go run ./cmd/dash config schema > charts/dash/files/config.schema.json.tmp
	mv charts/dash/files/config.schema.json.tmp charts/dash/files/config.schema.json
	go run ./cmd/dash config env | awk '$$2 ~ /^DASH_/ && $$(NF-1) == "yes" { print $$2 }' > charts/dash/files/secret-env.txt.tmp
	mv charts/dash/files/secret-env.txt.tmp charts/dash/files/secret-env.txt

# vacuum lives in its own module (tools/go.mod), so its dependency tree stays out of the app's
# go.mod and image; it comes from the module cache like tygo. Run from the repository root.
check: generate
	git diff --exit-code -- web/src/api/types.ts ':(glob)web/src/plugins/**/model.gen.ts' docs/configuration.md docs/api/openapi.json charts/dash/files/config.schema.json charts/dash/files/secret-env.txt
	test -z "$$(git ls-files --others --exclude-standard -- web/src docs charts)" || { echo "untracked files: a generated file nobody committed fails, so add or remove them"; git ls-files --others --exclude-standard -- web/src docs charts; exit 1; }
	go tool -modfile=tools/go.mod vacuum lint -r docs/api/ruleset.yaml -n warn -q -b --no-update-check docs/api/openapi.json
	go vet ./...
	go test ./...
	cd web && npm ci && npm run check && npm test && npm run build && node scripts/check-chunks.mjs

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
