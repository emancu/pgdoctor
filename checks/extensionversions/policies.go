package extensionversions

// ExtensionPolicy declares version floors for an extension; below FailBelow is unsupported (fail), below WarnBelow is deprecated (warn), an empty floor means that level does not apply.
type ExtensionPolicy struct {
	Name      string // exact pg_extension.extname
	WarnBelow string // installed < this → SeverityWarn
	FailBelow string // installed < this → SeverityFail (optional)
	Reason    string // short justification
}

// ExtensionPolicies lists extensions with version requirements; extensions not listed here are reported for inventory only (SeverityPass).
var ExtensionPolicies = []ExtensionPolicy{
	{
		Name:      "pg_partman",
		WarnBelow: "5.1.0",
		FailBelow: "3.0",
		Reason: "4.x is EOL upstream (deprecated) and < 3.0 is unsupported. < 5.1.0 doesn't inherit " +
			"REPLICA IDENTITY onto new child partitions, breaking CDC (pg_partman #502).",
	},
	{
		Name:      "postgis",
		WarnBelow: "3.3",
		FailBelow: "3.0",
		Reason:    "PostGIS < 3.3 is EOL upstream (deprecated); < 3.0 (2.x) is unsupported.",
	},
}

// requiredVersion is the version to upgrade to: WarnBelow if set, else FailBelow.
func (p ExtensionPolicy) requiredVersion() string {
	if p.WarnBelow != "" {
		return p.WarnBelow
	}
	return p.FailBelow
}
