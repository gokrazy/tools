// Package systemd provides a bundled copy of the systemd-boot UEFI app.
package systemd

import "embed"

//go:embed systemd-bootx64.efi
var SystemdBootX64 embed.FS

//go:embed systemd-bootaa64.efi
var SystemdBootAA64 embed.FS
