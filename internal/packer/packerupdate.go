package packer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gokrazy/internal/httpclient"
	"github.com/gokrazy/internal/humanize"
	"github.com/gokrazy/internal/progress"
	"github.com/gokrazy/internal/updateflag"
	"github.com/gokrazy/updater"
)

func (pack *Pack) logicUpdate(ctx context.Context, isDev bool, bootSize int64, rootSize int64, tmpMBR, tmpBoot, tmpRoot *os.File, updateBaseUrl *url.URL, target *updater.Target, updateHttpClient *http.Client) error {
	log := pack.Env.Logger()
	cfg := pack.Cfg       // for convenience
	update := pack.update // for convenience

	var rootReader, bootReader, mbrReader io.Reader
	// Determine where to read the boot, root and MBR images from.
	switch {
	case cfg.InternalCompatibilityFlags.Overwrite != "":
		if isDev {
			bootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite + "1")
			if err != nil {
				return err
			}
			bootReader = bootFile
			rootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite + "2")
			if err != nil {
				return err
			}
			rootReader = rootFile
		} else {
			bootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite)
			if err != nil {
				return err
			}
			if _, err := bootFile.Seek(pack.firstPartitionOffsetSectors*512, io.SeekStart); err != nil {
				return err
			}
			bootReader = &io.LimitedReader{
				R: bootFile,
				N: bootSize,
			}

			rootFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite)
			if err != nil {
				return err
			}
			if _, err := rootFile.Seek(pack.firstPartitionOffsetSectors*512+100*MB, io.SeekStart); err != nil {
				return err
			}
			rootReader = &io.LimitedReader{
				R: rootFile,
				N: rootSize,
			}
		}
		mbrFile, err := os.Open(cfg.InternalCompatibilityFlags.Overwrite)
		if err != nil {
			return err
		}
		mbrReader = &io.LimitedReader{
			R: mbrFile,
			N: 446,
		}

	default:
		if cfg.InternalCompatibilityFlags.OverwriteBoot != "" {
			bootFile, err := os.Open(cfg.InternalCompatibilityFlags.OverwriteBoot)
			if err != nil {
				return err
			}
			bootReader = bootFile
			if cfg.InternalCompatibilityFlags.OverwriteMBR != "" {
				mbrFile, err := os.Open(cfg.InternalCompatibilityFlags.OverwriteMBR)
				if err != nil {
					return err
				}
				mbrReader = mbrFile
			} else {
				if _, err := tmpMBR.Seek(0, io.SeekStart); err != nil {
					return err
				}
				mbrReader = tmpMBR
			}
		}

		if cfg.InternalCompatibilityFlags.OverwriteRoot != "" {
			rootFile, err := os.Open(cfg.InternalCompatibilityFlags.OverwriteRoot)
			if err != nil {
				return err
			}
			rootReader = rootFile
		}

		if cfg.InternalCompatibilityFlags.OverwriteBoot == "" && cfg.InternalCompatibilityFlags.OverwriteRoot == "" {
			if _, err := tmpBoot.Seek(0, io.SeekStart); err != nil {
				return err
			}
			bootReader = tmpBoot

			if _, err := tmpMBR.Seek(0, io.SeekStart); err != nil {
				return err
			}
			mbrReader = tmpMBR

			if _, err := tmpRoot.Seek(0, io.SeekStart); err != nil {
				return err
			}
			rootReader = tmpRoot
		}
	}

	updateBaseUrl.Path = "/"
	log.Printf("Updating %s", updateBaseUrl.String())

	progctx, canc := context.WithCancel(context.Background())
	defer canc()
	prog := &progress.Reporter{}
	go prog.Report(progctx)

	// Start with the root file system because writing to the non-active
	// partition cannot break the currently running system.
	if err := pack.updateWithProgress(prog, rootReader, target, "root file system", "root"); err != nil {
		return err
	}

	for _, rootDeviceFile := range pack.rootDeviceFiles {
		f, err := os.Open(filepath.Join(pack.kernelDir, rootDeviceFile.Name))
		if err != nil {
			return err
		}

		if err := pack.updateWithProgress(
			prog, f, target, fmt.Sprintf("root device file %s", rootDeviceFile.Name),
			filepath.Join("device-specific", rootDeviceFile.Name),
		); err != nil {
			if errors.Is(err, updater.ErrUpdateHandlerNotImplemented) ||
				strings.Contains(err.Error(), "404 Not Found") {
				log.Printf("target does not support updating device file %s yet, ignoring", rootDeviceFile.Name)
				continue
			}
			return err
		}
	}

	if err := pack.updateWithProgress(prog, bootReader, target, "boot file system", "boot"); err != nil {
		return err
	}

	if err := target.StreamTo(ctx, "mbr", mbrReader); err != nil {
		if err == updater.ErrUpdateHandlerNotImplemented {
			log.Printf("target does not support updating MBR yet, ignoring")
		} else {
			return fmt.Errorf("updating MBR: %v", err)
		}
	}

	if cfg.InternalCompatibilityFlags.Testboot {
		if err := target.Testboot(ctx); err != nil {
			return fmt.Errorf("enable testboot of non-active partition: %v", err)
		}
	} else {
		if err := target.Switch(ctx); err != nil {
			return fmt.Errorf("switching to non-active partition: %v", err)
		}
	}

	// Stop progress reporting to not mess up the following logs output.
	canc()

	log.Printf("Triggering reboot")
	if err := target.Reboot(ctx); err != nil {
		if errors.Is(err, syscall.ECONNRESET) {
			log.Printf("ignoring reboot error: %v", err)
		} else {
			return fmt.Errorf("reboot: %v", err)
		}
	}

	const polltimeout = 5 * time.Minute
	log.Printf("Updated, waiting %v for the device to become reachable (cancel with Ctrl-C any time)", polltimeout)

	if update.CertPEM != "" && update.KeyPEM != "" {
		// Use an HTTPS client (post-update),
		// even when the --insecure flag was specified.
		pack.schema = "https"
		var err error
		updateBaseUrl, err = updateflag.Value{
			Update: "yes",
		}.BaseURL(update.HTTPPort, update.HTTPSPort, pack.schema, update.Hostname, update.HTTPPassword)
		if err != nil {
			return err
		}
		updateHttpClient, _, err = httpclient.GetTLSHttpClientByTLSFlag(update.UseTLS, false /* insecure */, updateBaseUrl)
		if err != nil {
			return fmt.Errorf("getting http client by tls flag: %v", err)
		}
	}

	pollctx, canc := context.WithTimeout(context.Background(), polltimeout)
	defer canc()
	for {
		if err := pollctx.Err(); err != nil {
			return fmt.Errorf("device did not become healthy after update (%v)", err)
		}
		if err := pollUpdated1(pollctx, updateHttpClient, updateBaseUrl.String(), pack.buildTimestamp); err != nil {
			log.Printf("device not yet reachable: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		log.Printf("Device ready to use!")
		break
	}

	return nil
}

func (pack *Pack) updateWithProgress(prog *progress.Reporter, reader io.Reader, target *updater.Target, logStr string, stream string) error {
	ctx := context.Background()
	log := pack.Env.Logger()

	start := time.Now()
	prog.SetStatus(fmt.Sprintf("update %s", logStr))
	prog.SetTotal(0)

	if stater, ok := reader.(interface{ Stat() (os.FileInfo, error) }); ok {
		if st, err := stater.Stat(); err == nil {
			prog.SetTotal(uint64(st.Size()))
		}
	}
	if err := target.StreamTo(ctx, stream, io.TeeReader(reader, &progress.Writer{})); err != nil {
		return fmt.Errorf("updating %s: %w", logStr, err)
	}
	duration := time.Since(start)
	transferred := progress.Reset()
	log.Printf("\rTransferred %s (%s) at %.2f MiB/s (total: %v)",
		logStr,
		humanize.Bytes(transferred),
		float64(transferred)/duration.Seconds()/1024/1024,
		duration.Round(time.Second))

	return nil
}
