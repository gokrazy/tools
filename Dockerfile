FROM debian:bookworm

RUN apt-get update && apt-get install -y qemu-efi-aarch64 ovmf
