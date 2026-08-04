.PHONY: build install test race vet demo

build:
	go build ./cmd/platoon

install:
	go install ./cmd/platoon

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

demo:
	go test ./internal/commander -run '^TestFakeEndToEnd$$' -count=1 -v
