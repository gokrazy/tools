// Package edk provides a bundled copy of the systemd-boot UEFI app.
package edk

import _ "embed"

//go:embed QEMU_EFI.fd
var Arm64EFI []byte

//go:embed OVMF_CODE_4M.fd
var Amd64OVMFCODE4M []byte

//go:embed OVMF_VARS_4M.fd
var Amd64OVMFVARS4M []byte
