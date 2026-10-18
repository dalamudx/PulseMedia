package ports

// PreparedSubscriptionCreationMetadata contains provider observations fetched
// outside a subscription transaction. Units describe actual episodes, never a
// synthetic 1..count sequence. Seasons is the complete evidence for just the
// requested nonempty seasons; zero-count announced seasons publish no episodes
// and confer no retirement or complete-coverage authority.
//
// This material grants no permission by itself: publication must revalidate the
// current actor, media version, affected subscriptions, roots and command key.
type PreparedSubscriptionCreationMetadata struct {
	TMDBID  int64
	Media   SubscriptionMediaInput
	Units   []SubscriptionMediaUnitSpec
	Seasons []TVSeasonMetadata
}
