.PHONY: lint-patch
lint-patch: $(CUSTOM_GOLANGCI_LINT_BIN)
	$(CUSTOM_GOLANGCI_LINT_RUN) --timeout 10m --max-issues-per-linter 0 --max-same-issues 0 ./pkg/kgateway/proxy_syncer
.PHONY: fmt-patch
fmt-patch:
	$(CUSTOM_GOLANGCI_LINT_FMT) pkg/kgateway/proxy_syncer/queued_publication_test.go pkg/kgateway/proxy_syncer/carry_recovery_test.go
