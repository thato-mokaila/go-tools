# Makefile for Go SMB Log Tool

# Define the name of your Go program's output binary
BINARY_NAME = smb_tool

# Define the source file
GO_SOURCE = smb_tool.go

# Default target: builds and then runs the application
.PHONY: all
all: build run

# Build the Go application
.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	go build -o $(BINARY_NAME) $(GO_SOURCE)
	@echo "Build complete. Executable: $(BINARY_NAME)"

# Run the Go application
.PHONY: run
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BINARY_NAME)

# Install Go module dependencies
.PHONY: deps
deps:
	@echo "Installing Go dependencies..."
	go mod tidy
	go get github.com/stacktitan/smb
	go get golang.org/x/text
	@echo "Dependencies installed."

# Clean up compiled binaries and other generated files
.PHONY: clean
clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@rm -rf $(HOME)/go/bin/$(BINARY_NAME) # If you ever installed it globally
	@echo "Clean complete."

# Help target to display available commands
.PHONY: help
help:
	@echo "Available commands:"
	@echo "  make all    - Builds and then runs the application (default)."
	@echo "  make build  - Compiles the Go application."
	@echo "  make run    - Runs the compiled application."
	@echo "  make deps   - Installs Go module dependencies."
	@echo "  make clean  - Removes compiled binaries."
	@echo "  make help   - Displays this help message."

