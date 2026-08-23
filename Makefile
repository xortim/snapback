COMMIT_HASH ?= $(shell git rev-parse --short HEAD 2>/dev/null)
GITVERSION  ?= $(shell git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo "v0.0.0-$(COMMIT_HASH)")

GO          ?= go
GOOS        ?= $(shell $(GO) env GOOS)
GOARCH      ?= $(shell $(GO) env GOARCH)
PACKAGENAME := $(shell go list -m -f '{{.Path}}')
GOLDFLAGS   ?= -s -w -X $(PACKAGENAME)/conf.Executable=$(EXECUTABLE) -X $(PACKAGENAME)/conf.GitVersion=$(GITVERSION)
GOBUILD     ?= CGO_ENABLED=0 $(GO) build -ldflags="$(GOLDFLAGS)"
GO_FILES    := $(shell find . -type f -name '*.go' -not -path './vendor/*')

EXECUTABLE  := snapback
ARTIFACT    := dist/$(GOOS)-$(GOARCH)/$(EXECUTABLE)

.PHONY: all
all: clean verify lint test build

###############
##@ Development

# This is to allow make to detect when other targets should be rerun (source changes)
$(GO_FILES):
	@ls -l "$@"

.PHONY: build
build: $(ARTIFACT) ## Build binary
$(ARTIFACT): $(GO_FILES)
	@$(MAKE) --no-print-directory log-build
	@$(GOBUILD) -o $@
	@ln -fs $(GOOS)-$(GOARCH)/$(EXECUTABLE) dist/$(EXECUTABLE)

.PHONY: verify
verify: ## Verify module dependencies
	@$(MAKE) --no-print-directory log-$@
	$(GO) mod verify

.PHONY: container
container: ## Build container using docker
	@$(MAKE) --no-print-directory log-$@
	@docker build -t $(EXECUTABLE):$(GITVERSION) -t $(EXECUTABLE):local .

.PHONY: fmt
fmt: ## Check the project follows idiomatic formatting
	@$(MAKE) --no-print-directory log-$@
	@golangci-lint fmt --diff

.PHONY: fmt-fix
fmt-fix: ## Apply idiomatic formatting fixes
	@$(MAKE) --no-print-directory log-$@
	@golangci-lint fmt

.PHONY: lint
lint: fmt ## Lint the project
	@$(MAKE) --no-print-directory log-$@
	@golangci-lint run

.PHONY: test
test: ## Execute tests
	@$(MAKE) --no-print-directory log-$@
	$(GO) test -coverprofile=coverage.out -covermode=atomic -v ./...

.PHONY: clean
clean: ## Clean the workspace and dist/
	@$(MAKE) --no-print-directory log-$@
	@$(GO) clean
	@rm -rf dist/* coverage.out

.PHONY: tools
tools: ## Install tools needed for development
	@$(MAKE) --no-print-directory log-$@
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1
	@go install github.com/goreleaser/goreleaser/v2@latest
	@echo "NOTE: git-cliff must be installed separately (brew install git-cliff or cargo install git-cliff)"

###############
##@ Release
# Requires: goreleaser (make tools), git-cliff (see above)

.PHONY: changelog
changelog: ## Generate CHANGELOG.md using git-cliff
	@$(MAKE) --no-print-directory log-$@
	git-cliff --output CHANGELOG.md

.PHONY: snapshot
snapshot: changelog ## Build a snapshot release locally (no publish)
	@$(MAKE) --no-print-directory log-$@
	goreleaser release --snapshot --clean

.PHONY: release
release: changelog ## Create a release with goreleaser
	@$(MAKE) --no-print-directory log-$@
	goreleaser release --clean

###########################################################################
## Self-Documenting Makefile Help and logging                            ##
## https://github.com/terraform-docs/terraform-docs/blob/master/Makefile ##
## https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html    ##
###########################################################################

########
##@ Help

.PHONY: help
help:   ## Display this help
	@awk \
		-v "col=\033[36m" -v "nocol=\033[0m" \
		' \
			BEGIN { \
				FS = ":.*##" ; \
				printf "Usage:\n  make %s<target>%s\n", col, nocol \
			} \
			/^[a-zA-Z_-]+:.*?##/ { \
				printf "  %s%-12s%s %s\n", col, $$1, nocol, $$2 \
			} \
			/^##@/ { \
				printf "\n%s%s%s\n", nocol, substr($$0, 5), nocol \
			} \
		' $(MAKEFILE_LIST)

log-%:
	@grep -h -E '^$*:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk \
			'BEGIN { \
				FS = ":.*?## " \
			}; \
			{ \
				printf "\033[36m==> %s\033[0m\n", $$2 \
			}'
