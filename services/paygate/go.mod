module github.com/cadencz/rentpc/services/paygate

go 1.26

// Pinned to mpp-go main (WithRequiresAuth, Currencies, WriteChallenges are newer than v0.2.0).
require github.com/tempoxyz/mpp-go v0.2.1-0.20261007173210-ca2ea5e2a0b4
