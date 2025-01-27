# pinned for reproducibility purposes
# to get the most recent testing goodies
# update the tag to the most recent tag
# https://hub.docker.com/_/debian/tags?name=testing&ordering=-name
FROM debian:testing-20250113

RUN apt-get update && apt-get install -y qemu-efi-aarch64 ovmf
