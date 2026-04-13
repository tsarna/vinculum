package cmd

// Blank imports trigger init() registrations for all client, server, trigger,
// ambient provider, and function plugin implementations that live outside the config package.
import (
	_ "github.com/tsarna/vinculum/ambient"
	_ "github.com/tsarna/vinculum/conditions"
	_ "github.com/tsarna/vinculum/editors/line"
	_ "github.com/tsarna/vinculum/clients/http"
	_ "github.com/tsarna/vinculum/clients/kafka"
	_ "github.com/tsarna/vinculum/clients/mqtt"
	_ "github.com/tsarna/vinculum/clients/openai"
	_ "github.com/tsarna/vinculum/clients/otlp"
	_ "github.com/tsarna/vinculum/clients/vws"
	_ "github.com/tsarna/vinculum/servers/http"
	_ "github.com/tsarna/vinculum/servers/metrics"
	_ "github.com/tsarna/vinculum/servers/mcp"
	_ "github.com/tsarna/vinculum/servers/vws"
	_ "github.com/tsarna/vinculum/servers/websocket"
	_ "github.com/tsarna/vinculum/triggers/after"
	_ "github.com/tsarna/vinculum/triggers/at"
	_ "github.com/tsarna/vinculum/triggers/cron"
	_ "github.com/tsarna/vinculum/triggers/file"
	_ "github.com/tsarna/vinculum/triggers/interval"
	_ "github.com/tsarna/vinculum/triggers/once"
	_ "github.com/tsarna/vinculum/triggers/shutdown"
	_ "github.com/tsarna/vinculum/triggers/signals"
	_ "github.com/tsarna/vinculum/triggers/start"
	_ "github.com/tsarna/vinculum/triggers/watch"
	_ "github.com/tsarna/vinculum/triggers/watchdog"
	_ "github.com/tsarna/vinculum/functions"
)
