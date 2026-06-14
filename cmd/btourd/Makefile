.PHONY: release dev version

IMAGE       ?= ghcr.io/knusbaum/btourd
VERSION     := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
DEV_VERSION := $(or $(VERSION),$(shell git rev-parse --short HEAD))-dev.$(shell date +%s)

version:
	@echo $(VERSION)

release:
	@git describe --tags --exact-match HEAD 2>/dev/null || (echo "error: HEAD is not on a tagged commit" >&2 && false)
	@git diff --quiet && git diff --cached --quiet || (echo "error: uncommitted changes" >&2 && false)
	docker build -f Dockerfile -t $(IMAGE):$(VERSION) ../..
	docker push $(IMAGE):$(VERSION)

dev:
	docker build -f Dockerfile -t $(IMAGE):$(DEV_VERSION) ../..
	docker push $(IMAGE):$(DEV_VERSION)
	echo $(DEV_VERSION) > /tmp/btourd.devversion
