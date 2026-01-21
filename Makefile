ifndef CIRCLE_ARTIFACTS
CIRCLE_ARTIFACTS=tmp
endif

dependencies:
	@go get -v -t ./...

vet:
	@go vet ./...

test: vet
	@mkdir -p ${CIRCLE_ARTIFACTS}
	@go test -v -race -count=1 -timeout=5m -coverprofile=${CIRCLE_ARTIFACTS}/cover.out ./...
	@go tool cover -func ${CIRCLE_ARTIFACTS}/cover.out -o ${CIRCLE_ARTIFACTS}/cover.txt
	@go tool cover -html ${CIRCLE_ARTIFACTS}/cover.out -o ${CIRCLE_ARTIFACTS}/cover.html

# Run benchmarks (no race - it skews performance measurements)
bench: vet
	@go test -v -bench=. -benchmem -run=^$$ -timeout=30m .

# Quick benchmark run (single iteration, no memory stats)
bench-quick: vet
	@go test -bench=. -run=^$$ .

build: test
	@go build ./...

ci: dependencies test

.PHONY: dependencies vet test bench bench-quick ci
