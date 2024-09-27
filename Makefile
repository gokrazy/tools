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

third_party/edk2-2022.11-6/QEMU_EFI.fd: Dockerfile
	docker build --rm -t gokrazy-edk2 .
	docker run --rm -v $$(pwd)/third_party/edk2-2022.11-6:/tmp/bins gokrazy-edk2 cp /usr/share/qemu-efi-aarch64/QEMU_EFI.fd /usr/share/OVMF/OVMF_CODE.fd /tmp/bins
