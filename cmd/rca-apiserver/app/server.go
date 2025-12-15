package app

import (
	"context"
	"fmt"

	"github.com/onexstack/onexstack/pkg/cli/cli"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"zk8s.com/rca-api/cmd/rca-apiserver/app/options"
)

const (
	// defaultHomeDir defines the default directory to store configuration files
	// for the rca-apiserver service, typically within the user's home directory.
	defaultHomeDir = ".rca-api"

	// defaultConfigName specifies the default configuration file name
	// for the rca-apiserver service.
	defaultConfigName = "rca-apiserver.yaml"
)

// configFile stores the path to the configuration file, set via command-line flag.
var configFile string

// NewAPIServerCommand creates a new *cobra.Command object that represents
// the root command for the rca-apiserver application. It sets up command-line
// flags, configuration loading, and the main execution logic.
func NewAPIServerCommand() *cobra.Command {
	opts := options.NewServerOptions() // Create default application command-line options

	cmd := &cobra.Command{
		// Specify the name of the command, which will appear in the help information
		Use: "rca-apiserver",
		// A short description of the command.
		Short: "RCA platform API for K8s microservices",
		// A detailed description of the command.
		Long: "rca-api: Ingest alerts, collect evidence (K8s/Prom/ES), run diagnosis (rules -> multi-agent), and support approve-based remediation actions with audit.",
		// SilenceUsage ensures that the help message is not printed when an error occurs.
		SilenceUsage: true,
		// RunE defines the function to execute when cmd.Execute() is called.
		RunE: func(cmd *cobra.Command, args []string) error {
			// Setup a context that listens for OS signals (e.g., Ctrl+C) for graceful shutdown.
			ctx := genericapiserver.SetupSignalContext()

			// Check if the --version flag was requested. If so, print version info and exit.
			version.PrintAndExitIfRequested()

			// Unmarshal the configuration from Viper into the ServerOptions struct.
			if err := viper.Unmarshal(opts); err != nil {
				return fmt.Errorf("failed to unmarshal configuration: %w", err)
			}

			// Complete the options by setting default values and deriving configurations.
			if err := opts.Complete(); err != nil {
				return fmt.Errorf("failed to complete options: %w", err)
			}

			// Validate the command-line options to ensure they are valid.
			if err := opts.Validate(); err != nil {
				return fmt.Errorf("invalid options: %w", err)
			}

			// Initialize and configure OpenTelemetry providers based on enabled signals.
			if err := opts.OTelOptions.Apply(); err != nil {
				return err
			}
			// Ensure OpenTelemetry resources are properly cleaned up on application shutdown.
			defer func() { _ = opts.OTelOptions.Shutdown(ctx) }()

			return run(ctx, opts)
		},

		// Args ensures no command-line arguments are allowed, e.g., './rca-apiserver param1'.
		Args: cobra.NoArgs,
	}

	// Register the configuration initialization function, which runs before command execution.
	// It sets up Viper to search for configuration files in specified directories.
	cobra.OnInitialize(core.OnInitialize(&configFile, "RCA_API_APISERVER", cli.SearchDirs(defaultHomeDir), defaultConfigName))

	// Define persistent flags that apply to this command and its subcommands.
	cmd.PersistentFlags().StringVarP(
		&configFile,
		"config",
		"c",
		cli.FilePath(defaultHomeDir, defaultConfigName),
		"Path to the rca-apiserver configuration file.",
	)

	// Add server-specific options as command-line flags.
	opts.AddFlags(cmd.PersistentFlags())

	// Add the standard --version flag to the command.
	version.AddFlags(cmd.PersistentFlags())

	return cmd
}

// run contains the main logic for initializing and running the server.
func run(ctx context.Context, opts *options.ServerOptions) error {
	// Retrieve the application configuration from the parsed options.
	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create a new server instance based on the configuration.
	server, err := cfg.New(ctx)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Run the server until the context is canceled or an error occurs.
	return server.Run(ctx)
}
