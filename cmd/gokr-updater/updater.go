// gokr-updater updates a running gokrazy installation over the network.
package main

import (
	"flag"
	"log"
	"net/url"
	"os"

	"github.com/gokrazy/internal/updater"
)

var (
	boot = flag.String("boot",
		"",
		"path to the boot file system (e.g. /tmp/boot.fat)")

	root = flag.String("root",
		"",
		"path to the root file system (e.g. /tmp/root.fat)")

	update = flag.String("update",
		"",
		"URL of a gokrazy installation (e.g. http://gokrazy:mypassword@myhostname/) to update")
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

	if *root != "" {
		log.Printf("Updating %q with root file system %q", *update, *root)
		// Start with the root file system because writing to the non-active
		// partition cannot break the currently running system.
		f, err := os.Open(*root)
		if err != nil {
			log.Fatal(err)
		}
		if err := updater.UpdateRoot(baseUrl.String(), f); err != nil {
			log.Fatalf("updating root file system: %v", err)
		}
	}

	if *boot != "" {
		log.Printf("Updating %q with boot file system %q", *update, *boot)
		f, err := os.Open(*boot)
		if err != nil {
			log.Fatal(err)
		}
		if err := updater.UpdateBoot(baseUrl.String(), f); err != nil {
			log.Fatalf("updating boot file system: %v", err)
		}
	}

	if err := updater.Switch(baseUrl.String()); err != nil {
		log.Fatalf("switching to non-active partition: %v", err)
	}

	if err := updater.Reboot(baseUrl.String()); err != nil {
		log.Fatalf("reboot: %v", err)
	}

	log.Printf("updated, should be back soon")
}
