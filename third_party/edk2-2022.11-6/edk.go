// Package edk provides a bundled copy of the systemd-boot UEFI app.
package edk

import _ "embed"

//go:embed QEMU_EFI.fd
var Arm64EFI []byte

//go:embed OVMF_CODE.fd
var Amd64EFI []byte
