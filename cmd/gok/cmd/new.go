package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/instanceflag"
	"github.com/gokrazy/tools/internal/pwgen"
	"github.com/spf13/cobra"
)

// newCmd is gok new.
var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new gokrazy instance",
	Long: `Create a new gokrazy instance.

If you are unfamiliar with gokrazy, please follow:
https://gokrazy.org/quickstart/
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().NArg() > 0 {
			fmt.Fprint(os.Stderr, `positional arguments are not supported

`)
			return cmd.Usage()
		}

		return newImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
	},
}

type newImplConfig struct {
	empty bool
}

var newImpl newImplConfig

func init() {
	instanceflag.RegisterPflags(newCmd.Flags())
	newCmd.Flags().BoolVarP(&newImpl.empty, "empty", "", false, "create an empty gokrazy instance, without the default packages")
}

func (r *newImplConfig) createBreakglassAuthorizedKeys(authorizedPath string, matches []string) error {
	f, err := os.OpenFile(authorizedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			log.Printf("%s already exists, not replacing it", authorizedPath)
			return nil
		}
		return err
	}
	defer f.Close()

	hostname, err := os.Hostname()
	if err != nil {
		log.Print(err)
	}
	authorized := "# This authorized_keys(5) file allows access from keys on " + hostname + "\n\n"
	for _, match := range matches {
		b, err := os.ReadFile(match)
		if err != nil {
			authorized += "# " + match + ": " + err.Error() + "\n\n"
			continue
		}

		authorized += "# " + match + "\n" + string(b) + "\n"
	}

	if _, err := f.WriteString(authorized); err != nil {
		return err
	}

	return f.Close()
}

func (r *newImplConfig) addBreakglassAuthorizedKeys(authorizedPath string, matches []string, packageConfig map[string]config.PackageConfig) error {
	if err := r.createBreakglassAuthorizedKeys(authorizedPath, matches); err != nil {
		return err
	}

	packageConfig["github.com/gokrazy/breakglass"] = config.PackageConfig{
		CommandLineFlags: []string{
			"-authorized_keys=/etc/breakglass.authorized_keys",
		},
		ExtraFilePaths: map[string]string{
			"/etc/breakglass.authorized_keys": authorizedPath,
		},
	}
	return nil
}

func (r *newImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	parentDir := instanceflag.ParentDir()
	instance := instanceflag.Instance()

	if err := os.MkdirAll(filepath.Join(parentDir, instance), 0755); err != nil {
		return err
	}

	configJSON := filepath.Join(parentDir, instance, "config.json")
	f, err := os.OpenFile(configJSON, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("gokrazy instance already exists! If you want to re-create it, rm '%s' and retry", configJSON)
		}
	}
	defer f.Close()

	packageConfig := make(map[string]config.PackageConfig)
	var packages []string
	if !r.empty {
		packages = append(packages,
			"github.com/gokrazy/fbstatus",
			"github.com/gokrazy/hello",
			"github.com/gokrazy/serial-busybox")

		idPattern := os.Getenv("HOME") + "/.ssh/id_*.pub"
		matches, err := filepath.Glob(idPattern)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			log.Printf("No SSH keys found in %s, not adding breakglass", idPattern)
		}
		if len(matches) > 0 {
			packages = append(packages, "github.com/gokrazy/breakglass")
			authorizedPath := filepath.Join(parentDir, instance, "breakglass.authorized_keys")
			if err := r.addBreakglassAuthorizedKeys(authorizedPath, matches, packageConfig); err != nil {
				return err
			}
		}
	}
	pw, err := pwgen.RandomPassword(20)
	if err != nil {
		return err
	}
	cfg := &config.Struct{
		Hostname: instance,
		Packages: packages,
		Update: &config.UpdateStruct{
			HttpPassword: pw,
		},
		PackageConfig: packageConfig,
		SerialConsole: "disabled",
	}
	b, err := cfg.FormatForFile()
	if err != nil {
		return err
	}
	f.Write(b)

	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("gokrazy instance configuration created in %s\n", configJSON)
	fmt.Printf("(Use 'gok -i %s edit' to edit the configuration interactively.)\n", instance)
	fmt.Println()
	fmt.Printf("Use 'gok -i %s add' to add packages to this instance\n", instance)
	fmt.Println()
	fmt.Printf("To deploy this gokrazy instance, see 'gok help overwrite'\n")

	return nil
}
