package gok

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/gokrazy/internal/instanceflag"
	"github.com/spf13/cobra"
)

// editCmd is gok edit.
func editCmd() *cobra.Command {
	cmd := &cobra.Command{
		GroupID: "edit",
		Use:     "edit",
		Short:   "Edit a gokrazy instance configuration interactively",
		Long: `Edit a gokrazy instance configuration interactively.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().NArg() > 0 {
				fmt.Fprint(os.Stderr, `positional arguments are not supported

`)
				return cmd.Usage()
			}

			return editImpl.run(cmd.Context(), args, cmd.OutOrStdout(), cmd.OutOrStderr())
		},
	}
	instanceflag.RegisterPflags(cmd.Flags())
	return cmd
}

type editImplConfig struct{}

var editImpl editImplConfig

func (r *editImplConfig) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	parentDir := instanceflag.ParentDir()
	instance := instanceflag.Instance()

	configJSON := filepath.Join(parentDir, instance, "config.json")
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi" // most likely available
	}
	shellCmd := fmt.Sprintf("%s %q", editor, configJSON)
	return syscall.Exec("/bin/sh", []string{"/bin/sh", "-c", shellCmd}, os.Environ())
}
