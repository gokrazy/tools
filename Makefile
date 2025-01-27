.PHONY: test install cover efi

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


efi: Dockerfile
	rm -rf thirdparty/edk2-2024.11-4 && \
	docker build --rm -t gokrazy-edk2 . && \
	docker run --rm -v $$(pwd)/third_party/edk2-2024.11-4:/tmp/bins gokrazy-edk2 cp /usr/share/qemu-efi-aarch64/QEMU_EFI.fd /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_VARS_4M.fd /tmp/bins
