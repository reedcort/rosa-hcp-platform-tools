.PHONY: build clean install test help

BINARY_NAME=hcp-node-autoscaling
INSTALL_PATH=/usr/local/bin

# Default target
all: build

## build: Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	go build -o $(BINARY_NAME) .
	@echo "Build complete: ./$(BINARY_NAME)"

## clean: Remove build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -f $(BINARY_NAME)
	@echo "Clean complete"

## install: Install the binary to $(INSTALL_PATH)
install: build
	@echo "Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	install -m 755 $(BINARY_NAME) $(INSTALL_PATH)/$(BINARY_NAME)
	@echo "Installation complete: $(INSTALL_PATH)/$(BINARY_NAME)"

## test: Run tests
test:
	@echo "Running tests..."
	go test -v ./...

