.DEFAULT_GOAL := help

.PHONY: help test test-git test-process test-all lint lint-go lint-actions

help:
	@echo Targets: test test-git test-process test-all lint lint-go lint-actions

test:
	go test ./...

test-git:
	go test -parallel=4 -tags=git_integration ./...

test-process:
	go test -tags=process_integration ./...

test-all:
	go test -parallel=4 -tags=git_integration,process_integration ./...

lint: lint-go lint-actions

lint-go:
	go vet ./...

lint-actions:
	actionlint
