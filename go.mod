module github.com/gokrazy/tools

go 1.26.0

// Contains a fix for CVE-2026-111943, see
// https://groups.google.com/g/golang-announce/c/66pojueaQek
toolchain go1.26.10

require (
	github.com/anatol/vmtest v0.0.0-20260509163454-0d3e5bd9399c
	github.com/donovanhide/eventsource v0.0.0-20210830082556-c59027999da0
	github.com/gokrazy/gokapi v0.0.0-20251205165548-0927bab199d4
	github.com/gokrazy/internal v0.0.0-20260625065634-6994f9152c44
	github.com/gokrazy/updater v0.0.0-20260620140544-0a84d8ab3878
	github.com/google/renameio/v2 v2.0.2
	github.com/klauspost/compress v1.19.0
	github.com/mattn/go-isatty v0.0.22
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	golang.org/x/crypto/x509roots/fallback v0.0.0-20261020153306-9f2b28d4331a
	golang.org/x/mod v0.37.0
	golang.org/x/sync v0.21.0
	golang.org/x/sys v0.46.0
)

require (
	github.com/antihax/optional v1.0.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
)
