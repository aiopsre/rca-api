package queue

// AdaptiveWaiterOptionSet marks which AdaptiveWaiterOptions fields are explicitly configured.
type AdaptiveWaiterOptionSet struct {
	PollInterval         bool
	WatermarkCacheTTL    bool
	MaxPollingWaiters    bool
	DBErrorWindow        bool
	DBErrorRateThreshold bool
	DBErrorMinSamples    bool
}

// ResolveAdaptiveWaiterOptions applies precedence: CLI > YAML > env > default.
func ResolveAdaptiveWaiterOptions(
	yamlOpts AdaptiveWaiterOptions,
	yamlSet AdaptiveWaiterOptionSet,
	cliOpts AdaptiveWaiterOptions,
	cliSet AdaptiveWaiterOptionSet,
) AdaptiveWaiterOptions {

	opts := DefaultAdaptiveWaiterOptions()
	opts = ApplyAdaptiveWaiterEnvOverrides(opts)
	opts = applyAdaptiveWaiterOptionOverrides(opts, yamlOpts, yamlSet)
	opts = applyAdaptiveWaiterOptionOverrides(opts, cliOpts, cliSet)
	opts.applyDefaults()
	return opts
}

func applyAdaptiveWaiterOptionOverrides(
	base AdaptiveWaiterOptions,
	override AdaptiveWaiterOptions,
	set AdaptiveWaiterOptionSet,
) AdaptiveWaiterOptions {

	if set.PollInterval {
		base.PollInterval = override.PollInterval
	}
	if set.WatermarkCacheTTL {
		base.WatermarkCacheTTL = override.WatermarkCacheTTL
	}
	if set.MaxPollingWaiters {
		base.MaxPollingWaiters = override.MaxPollingWaiters
	}
	if set.DBErrorWindow {
		base.DBErrorWindow = override.DBErrorWindow
	}
	if set.DBErrorRateThreshold {
		base.DBErrorRateThreshold = override.DBErrorRateThreshold
	}
	if set.DBErrorMinSamples {
		base.DBErrorMinSamples = override.DBErrorMinSamples
	}
	return base
}
