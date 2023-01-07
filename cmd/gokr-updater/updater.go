// gokr-updater updates a running gokrazy installation over the network.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"

	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/updater"
)

var (
	boot = flag.String("boot",
		"",
		"path to the boot file system (e.g. /tmp/boot.fat)")

	root = flag.String("root",
		"",
		"path to the root file system (e.g. /tmp/root.fat)")

	update = flag.String("update",
		os.Getenv("GOKRAZY_UPDATE"),
		"URL of a gokrazy installation (e.g. http://gokrazy:mypassword@myhostname/) to update")

	useTLS = flag.String("tls",
		"",
		`TLS certificate for the web interface (-tls=<certificate or full chain path>,<private key path>).
Use -tls=self-signed to generate a self-signed RSA4096 certificate using the hostname specified with -hostname. In this case, the certificate and key will be placed in your local config folder (on Linux: ~/.config/gokrazy/<hostname>/).
WARNING: When reusing a hostname, no new certificate will be generated and the stored one will be used.
When updating a running instance, the specified certificate will be used to verify the connection. Otherwise the updater will load the hostname-specific certificate from your local config folder in addition to the system trust store.
You can also create your own certificate-key-pair (e.g. by using https://github.com/FiloSottile/mkcert) and place them into your local config folder.`,
	)

	tlsInsecure = flag.Bool("insecure",
		false,
		"Ignore TLS stripping detection.")
)

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if *update == "" {
		log.Fatal("-update is required")
	}

	if *boot == "" || *root == "" {
		log.Fatal("one of -boot or -root is required")
	}

	baseUrl, err := url.Parse(*update)
	if err != nil {
		log.Fatal(err)
	}
	baseUrl.Path = "/"
	httpClient, foundMatchingCertificate, err := httpclient.GetTLSHttpClientByTLSFlag(*useTLS, *tlsInsecure, baseUrl)
	remoteScheme, err := httpclient.GetRemoteScheme(baseUrl)
	if remoteScheme == "https" {
		baseUrl.Scheme = "https"
		*update = baseUrl.String()
	}

	if baseUrl.Scheme != "https" && foundMatchingCertificate {
		fmt.Printf("\n")
		fmt.Printf("!!!WARNING!!! Possible SSL-Stripping detected!\n")
		fmt.Printf("Found certificate for hostname in your client configuration but the host does not offer https!\n")
		fmt.Printf("\n")
		if !*tlsInsecure {
			log.Fatalf("update canceled: TLS certificate found, but negotiating a TLS connection with the target failed")
		}
		fmt.Printf("Proceeding anyway as requested (--insecure).\n")
	}

	target, err := updater.NewTarget(baseUrl.String(), httpClient)
	if err != nil {
		log.Fatal(err)
	}

	if *root != "" {
		log.Printf("Updating %q with root file system %q", *update, *root)
		// Start with the root file system because writing to the non-active
		// partition cannot break the currently running system.
		f, err := os.Open(*root)
		if err != nil {
			log.Fatal(err)
		}
		if err := target.StreamTo("root", f); err != nil {
			log.Fatalf("updating root file system: %v", err)
		}
	}

	if *boot != "" {
		log.Printf("Updating %q with boot file system %q", *update, *boot)
		f, err := os.Open(*boot)
		if err != nil {
			log.Fatal(err)
		}
		if err := target.StreamTo("boot", f); err != nil {
			log.Fatalf("updating boot file system: %v", err)
		}
	}

	if err := target.Switch(); err != nil {
		log.Fatalf("switching to non-active partition: %v", err)
	}

	if err := target.Reboot(); err != nil {
		log.Fatalf("reboot: %v", err)
	}

	log.Printf("updated, should be back soon")
}
