package schemadoc

import "github.com/tsarna/vinculum/config"

// testDoc builds a small but structurally complete SchemaDocument: a typed
// block with two variants, a plain block, a nested sub-block, constraints, an
// attribute naming a closed `ctx` shape and another naming an open one with
// site-specific additions.
//
// Fixtures rather than the live registry, deliberately. config.GenerateSchema
// only sees a populated registry when every subsystem has been blank-imported
// (which is why cmd/schema_test.go exists where it does), and a renderer test
// that depended on the real schema would fail whenever the schema changed —
// reporting a rendering regression for what was a documentation edit. Tests
// against the real document belong in cmd/, where they exercise the command.
func testDoc() *config.SchemaDocument {
	tls := &config.SchemaNestedBlock{
		Labels:     []string{},
		Repeatable: false,
		SchemaBody: config.SchemaBody{
			Summary: "TLS settings for the connection.",
			Attributes: []*config.SchemaAttr{
				{Name: "cert_file", Type: "string", Summary: "Client certificate."},
				{Name: "key_file", Type: "string", Summary: "Client private key."},
			},
			Blocks: map[string]*config.SchemaNestedBlock{},
			Constraints: []config.Constraint{
				config.RequiredTogether("cert_file", "key_file"),
			},
		},
	}

	mqtt := &config.SchemaBody{
		Summary: "An MQTT 5.0 publisher and subscriber.",
		Doc:     "Connects to an MQTT broker.\n\nAvailable in expressions as `client.<name>`.",
		DocPage: "client-mqtt.md",
		Attributes: []*config.SchemaAttr{
			{Name: "broker", Type: "string", Required: true, Summary: "Broker URL.", Hint: config.HintURL},
			{Name: "disabled", Type: "bool", Summary: "Skip this block entirely."},
			{
				Name: "on_connect", Type: "expression", Summary: "Evaluated after the connection is ready.",
				Doc: "Runs synchronously; no messages flow until it returns.", Hint: config.HintActionExpression,
				Context: "connection",
			},
			{
				Name: "on_decode_error", Type: "expression", Summary: "Evaluated when a payload fails to decode.",
				Hint: config.HintActionExpression, Context: "decode-error",
				ContextFields: []*config.SchemaContextField{
					{Name: "mqtt_topic", Type: "string", Summary: "The MQTT topic the message arrived on."},
				},
			},
			{
				Name: "qos", Type: "number", Summary: "Quality of service level.",
				Enum: []string{"0", "1", "2"},
			},
			{Name: "client_id", Type: "string", Summary: "Old name for `id`.", Deprecated: "Use `id` instead."},
		},
		Blocks:      map[string]*config.SchemaNestedBlock{"tls": tls},
		Constraints: []config.Constraint{config.MutuallyExclusive("broker", "brokers")},
	}

	http := &config.SchemaBody{
		Summary:    "An HTTP(S) request/response client.",
		Attributes: []*config.SchemaAttr{{Name: "base_url", Type: "string", Summary: "Prepended to relative paths.", Hint: config.HintURL}},
		Blocks:     map[string]*config.SchemaNestedBlock{},
	}

	subscription := &config.SchemaBody{
		Summary: "Subscribes to messages from a bus or client.",
		Attributes: []*config.SchemaAttr{
			{Name: "target", Type: "expression", Required: true, Summary: "Bus to subscribe to.", Hint: config.HintBusRef},
			{Name: "action", Type: "expression", Summary: "Evaluated per message.", Hint: config.HintActionExpression, Context: "message"},
		},
		Blocks: map[string]*config.SchemaNestedBlock{},
		Constraints: []config.Constraint{
			config.MutuallyExclusive("action", "subscriber"),
		},
	}

	// A server "http" as well as a client "http", because that collision is
	// real — http and vws are each both a client type and a server type — and
	// it is what makes a short path ambiguous.
	serverHTTP := &config.SchemaBody{
		Summary: "An HTTP server.",
		Attributes: []*config.SchemaAttr{
			{Name: "listen", Type: "string", Required: true, Summary: "Listen address.", Hint: config.HintListenAddr},
		},
		Blocks: map[string]*config.SchemaNestedBlock{
			"handle": {
				Labels:     []string{"route"},
				Repeatable: true,
				SchemaBody: config.SchemaBody{
					Summary: "An HTTP route handler.",
					Attributes: []*config.SchemaAttr{
						{Name: "action", Type: "expression", Summary: "Evaluated per request.", Context: "message"},
					},
					Blocks: map[string]*config.SchemaNestedBlock{},
				},
			},
		},
	}

	// A .vinit block, because the file kind changes how a block is presented:
	// its page carries a note the .vcl blocks do not, and the index lists it
	// apart from them.
	git := &config.SchemaBody{
		Summary: "Clones a repository at startup.",
		Attributes: []*config.SchemaAttr{
			{Name: "repo", Type: "string", Required: true, Summary: "Repository URL to clone."},
		},
		Blocks: map[string]*config.SchemaNestedBlock{
			"auth": {
				Labels: []string{},
				SchemaBody: config.SchemaBody{
					Summary: "Credentials for the clone.",
					Attributes: []*config.SchemaAttr{
						{Name: "token", Type: "string", Summary: "Personal-access-token shorthand."},
					},
					Blocks: map[string]*config.SchemaNestedBlock{},
				},
			},
		},
	}

	return &config.SchemaDocument{
		SchemaVersion:   "1",
		VinculumVersion: "0.0.0-test",
		Blocks: map[string]*config.SchemaBlock{
			"client": {
				File:         config.FileVCL,
				Labels:       []string{"type", "name"},
				VariantLabel: "type",
				Summary:      "A connection to an external service.",
				Variants:     map[string]*config.SchemaBody{"mqtt": mqtt, "http": http},
			},
			"server": {
				File:         config.FileVCL,
				Labels:       []string{"type", "name"},
				VariantLabel: "type",
				Summary:      "A network server.",
				Variants:     map[string]*config.SchemaBody{"http": serverHTTP},
			},
			"subscription": {
				File:   config.FileVCL,
				Labels: []string{"name"},
				Body:   subscription,
			},
			"git": {
				File:   config.FileVinit,
				Labels: []string{"label"},
				Body:   git,
			},
		},
		Contexts: map[string]*config.SchemaContext{
			"message": {
				Summary: "Evaluated once per message received.",
				Fields: []*config.SchemaContextField{
					{Name: "topic", Type: "string", Summary: "Topic of the message."},
					{Name: "msg", Type: "dynamic", Summary: "Decoded payload."},
					{Name: "trace_id", Type: "string", Summary: "Current trace ID.", Universal: true},
				},
			},
			"connection": {
				Summary: "Evaluated on a connection lifecycle change.",
				Fields:  []*config.SchemaContextField{{Name: "trace_id", Type: "string", Summary: "Current trace ID.", Universal: true}},
			},
			"decode-error": {
				Summary:    "Evaluated when a received payload cannot be decoded.",
				OpenFields: true,
				Fields: []*config.SchemaContextField{
					{Name: "error", Type: "string", Summary: "The decode failure."},
					{Name: "raw", Type: "bytes", Summary: "The undecodable payload.", Optional: true},
				},
			},
		},
		// One namespace of each kind that renders differently: a fixed one with
		// a nested object and a partly-free member, a wholly free one, a
		// constant one, and a block namespace — which is in the document but is
		// not a topic, and is here so a test can prove it.
		Namespaces: map[string]*config.SchemaNamespace{
			"sys": {
				Kind:    config.NamespaceProvider,
				Summary: "Process and host identity.",
				Doc:     "Captured once at startup.",
				DocPage: "config.md#variables",
				Members: []*config.SchemaMember{
					{Name: "hostname", Type: "string", Summary: "Hostname of the machine."},
					{
						Name: "functy", Type: "object", Summary: "The bundled functy language.",
						Members: []*config.SchemaMember{
							{Name: "version", Type: "string", Summary: "Its version.", Doc: "Read from build info."},
						},
					},
					{
						Name: "signals", Type: "object", Summary: "Signal numbers for the host OS.",
						FreeMembers: true,
						Members: []*config.SchemaMember{
							{Name: "bynumber", Type: "map", Summary: "Signal name by number.", Doc: "Keyed as a string."},
						},
					},
				},
			},
			"env": {
				Kind:        config.NamespaceProvider,
				Summary:     "Environment variables of the running process.",
				FreeMembers: true,
			},
			"http_status": {
				Kind:     config.NamespaceProvider,
				Summary:  "Constants for the HTTP status codes.",
				Constant: true,
				Members: []*config.SchemaMember{
					{Name: "NotFound", Type: "number", Value: "404", Summary: "The numeric HTTP status code."},
					{Name: "OK", Type: "number", Value: "200", Summary: "The numeric HTTP status code."},
				},
			},
			"subscription": {
				Kind:        config.NamespaceBlock,
				Block:       "subscription",
				Summary:     "Each subscription, by name.",
				FreeMembers: true,
			},
		},
	}
}

// renderNode walks a node and renders it as Markdown, the form the golden
// assertions read.
func renderNode(n Node, opts WalkOptions) string {
	return RenderMarkdown(Walk(n, opts), MarkdownOptions{})
}
