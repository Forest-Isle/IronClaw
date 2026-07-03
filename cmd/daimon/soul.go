package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Forest-Isle/daimon/internal/appdir"
	"github.com/Forest-Isle/daimon/internal/soul"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// soulPassphraseEnv lets non-interactive callers (scripts, CI) supply the
// passphrase without a TTY.
const soulPassphraseEnv = "DAIMON_SOUL_PASSPHRASE"

// newSoulCmd builds the `daimon soul` subcommand group: export/import the
// whole agent state directory as one encrypted portable archive.
func newSoulCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "soul",
		Short: "Export/import the agent's encrypted soul archive",
	}
	cmd.AddCommand(newSoulExportCmd(), newSoulImportCmd())
	return cmd
}

func newSoulExportCmd() *cobra.Command {
	var outPath string
	var srcDir string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the state directory to an encrypted .dsoul archive",
		RunE: func(cmd *cobra.Command, args []string) error {
			if outPath == "" {
				outPath = fmt.Sprintf("daimon-soul-%s.dsoul", time.Now().Format("20060102-150405"))
			}
			passphrase, err := soulPassphrase(true)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Note: stop the daimon runtime before exporting so the snapshot is consistent.")
			manifest, err := soul.Export(srcDir, outPath, passphrase)
			if err != nil {
				return err
			}
			fmt.Printf("Exported %d files from %s to %s\n", manifest.FileCount, srcDir, outPath)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "output archive path (default: daimon-soul-<timestamp>.dsoul)")
	cmd.Flags().StringVar(&srcDir, "dir", appdir.BaseDir(), "state directory to export")
	return cmd
}

func newSoulImportCmd() *cobra.Command {
	var targetDir string
	var force bool
	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Restore an encrypted .dsoul archive into the state directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			passphrase, err := soulPassphrase(false)
			if err != nil {
				return err
			}
			manifest, err := soul.Import(args[0], targetDir, passphrase, force)
			if err != nil {
				return err
			}
			fmt.Printf("Imported %d files (exported %s) into %s\n", manifest.FileCount, manifest.CreatedAt, targetDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&targetDir, "target", appdir.BaseDir(), "directory to restore into")
	cmd.Flags().BoolVar(&force, "force", false, "import into a non-empty target (overwrites files, does not clear)")
	return cmd
}

// soulPassphrase resolves the archive passphrase: env var first, then an
// interactive prompt (double-read when confirm is set, i.e. on export). A
// non-TTY session without the env var gets an actionable error instead of a
// hang on a hidden prompt.
func soulPassphrase(confirm bool) (string, error) {
	if fromEnv := os.Getenv(soulPassphraseEnv); fromEnv != "" {
		return fromEnv, nil
	}
	if !isInteractive() {
		return "", fmt.Errorf("no TTY for passphrase prompt: set %s", soulPassphraseEnv)
	}
	passphrase, err := promptPassphrase("Passphrase: ")
	if err != nil {
		return "", err
	}
	if passphrase == "" {
		return "", fmt.Errorf("passphrase must not be empty")
	}
	if confirm {
		again, err := promptPassphrase("Confirm passphrase: ")
		if err != nil {
			return "", err
		}
		if passphrase != again {
			return "", fmt.Errorf("passphrases do not match")
		}
	}
	return passphrase, nil
}

func promptPassphrase(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	return string(raw), nil
}
