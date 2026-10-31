package quality

// MovieDiscoveryAllowed decides only whether to observe candidates. It confers no
// replacement authority; candidates still require a fresh explicit upgrade preview.
func MovieDiscoveryAllowed(rules PolicyRules, current Profile, mode UpgradeMode) bool {
	if !mode.Enabled() || ValidateRules(rules) != nil || !KnownProfile(current) || current.ResolutionParserVersion != ResolutionTokenParserVersion {
		return false
	}
	if _, ok := rules.ResolutionOrder[current.Resolution]; !ok {
		return false
	}
	if _, ok := rules.SourceOrder[current.Source]; !ok {
		return false
	}
	policy := Policy{ResolutionOrder: rules.ResolutionOrder, SourceOrder: rules.SourceOrder, PreferHDR: rules.PreferHDR}
	return mode == UpgradeReplaceIfBetter || rules.CutoffScore <= 0 || policy.Score(current) < rules.CutoffScore
}
