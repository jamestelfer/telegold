# Default recipe
default: build test

# Build all packages
build:
    go build ./...

# Run all tests
test:
    go test ./...

# Fix lint issues in place
lint: lint-fix lint-fmt lint-tidy

# Run go fix
lint-fix:
    go fix ./...

# Run go fmt
lint-fmt:
    gofmt -w .

# Run go mod tidy
lint-tidy:
    go mod tidy

# Check for issues without fixing (fails on problems)
check: check-vet check-fmt check-tidy check-vuln

# Run go vet
check-vet:
    go vet ./...

# Check formatting (fail if not formatted)
check-fmt:
    @test -z "$(gofmt -l .)" || (echo "Files not formatted:"; gofmt -l .; exit 1)

# Check go.mod is tidy
check-tidy:
    go mod tidy -diff

# Run govulncheck
check-vuln:
    go tool -modfile=tools.mod govulncheck ./...
