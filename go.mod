module github.com/tsarna/vinculum

go 1.24.5

require go.uber.org/zap v1.27.0

require (
	github.com/amir-yaghoubi/mqttpattern v0.0.0-20250829083210-f7d8d46a786e
	github.com/coder/websocket v1.8.13
	github.com/hashicorp/go-cty-funcs v0.0.0-20250818135842-6aab67130928
	github.com/hashicorp/hcl/v2 v2.24.0
	github.com/itchyny/gojq v0.12.17
	github.com/robfig/cron/v3 v3.0.1
	github.com/sosodev/duration v1.3.1
	github.com/spf13/cobra v1.10.1
	github.com/stretchr/testify v1.11.1
	github.com/tsarna/go-structdiff v0.2.0
	github.com/tsarna/go2cty2go v0.1.0
	github.com/tsarna/hcl-jqfunc v0.1.0
	github.com/tsarna/vinculum-bus v0.8.0
	golang.org/x/sys v0.36.0
)

require (
	github.com/apparentlymart/go-cidr v1.0.1 // indirect
	github.com/bmatcuk/doublestar v1.1.5 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/itchyny/timefmt-go v0.1.6 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/rogpeppe/go-internal v1.9.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/crypto v0.38.0 // indirect
)

require (
	github.com/agext/levenshtein v1.2.1 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/heimdalr/dag v1.5.0
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/zclconf/go-cty v1.17.0
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/mod v0.26.0 // indirect
	golang.org/x/sync v0.16.0 // indirect
	golang.org/x/text v0.28.0 // indirect
	golang.org/x/tools v0.35.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/tsarna/hcl-jqfunc => ../hcl-jqfunc
