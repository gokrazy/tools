.PHONY: test install cover

all: install

test:
	go test -v github.com/gokrazy/tools/...

cover:
	go test \
		-coverpkg="$(shell go list ./... | paste -sd ,)" \
		-coverprofile=/tmp/cover.profile \
		-v \
		github.com/gokrazy/tools/...

install:
	go install github.com/gokrazy/tools/cmd/...
