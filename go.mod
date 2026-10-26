module github.com/gomaja/go-smpp

go 1.26.0

// v1.0.1 exists only to carry this directive, so it retracts itself too.

retract [v1.0.0, v1.0.1] // Tagged releases are withdrawn; depend on the main branch.

require (
	github.com/allegro/bigcache/v3 v3.2.0
	github.com/gomaja/go-sms v0.0.0-20261026131213-e73e45916687
	github.com/stretchr/testify v1.12.1
	golang.org/x/text v0.43.0
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect
