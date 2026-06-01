# Root Makefile — delegates to api-server/
# Run any command from the repo root: make fmt, make test, make dev, etc.

%:
	$(MAKE) -C api-server $@
