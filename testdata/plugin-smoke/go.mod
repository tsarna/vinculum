// The release smoke gate rewrites the vinculum require to the exact version
// being released (`go mod edit -require=...@<ref>` + `go mod tidy`) before
// building. The pinned version here is just a sensible default for running
// the gate locally against the current release.
module vinculumpluginsmoke

go 1.26.0

require (
	github.com/hashicorp/hcl/v2 v2.24.0
	github.com/tsarna/vinculum v0.39.0
	github.com/zclconf/go-cty v1.18.1
)
