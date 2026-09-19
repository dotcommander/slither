module github.com/dotcommander/slither

go 1.26.0

require (
	github.com/dlclark/regexp2 v1.12.0
	github.com/dotcommander/verdict v0.0.0
	github.com/garyblankenship/wormhole/v3 v3.0.1
)

replace github.com/garyblankenship/wormhole/v3 => ../wormhole

replace github.com/dotcommander/verdict => ../../dotcommander/verdict
