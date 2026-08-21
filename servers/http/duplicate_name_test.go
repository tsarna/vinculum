package httpserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "github.com/tsarna/vinculum/ambient"
	_ "github.com/tsarna/vinculum/clients/http"
	_ "github.com/tsarna/vinculum/servers/mcp"
)

// `server` and `client` are stored keyed by type, but the namespace expressions
// see is flat: server.x is one server whatever its type. So a name collision
// need not be within a single type, and the check for it has to look wider than
// the block that triggered it — which is what these cover.
//
// This used to panic rather than report: the diagnostic looked the colliding
// server up in its own type's bucket, found nothing there because the collision
// was with another type, and called a method on the nil it got back.

func TestServerNamesCollideAcrossTypes(t *testing.T) {
	msg := buildFails(t, `
server "http" "x" {
  listen = "127.0.0.1:0"

  handle "/a" {
    action = "ok"
  }
}

server "mcp" "x" {
  server_name = "dup"
}
`)
	assert.Contains(t, msg, "Server \"x\" is already defined")
	// Names the other block rather than saying "already defined" and stopping,
	// which is the part that needs the cross-type lookup.
	assert.Contains(t, msg, "names are global")
}

func TestClientNamesCollideAcrossTypes(t *testing.T) {
	msg := buildFails(t, `
client "http" "x" {
  base_url = "http://127.0.0.1:9"
}

client "mqtt" "x" {
  broker = "tcp://127.0.0.1:9"
}
`)
	assert.Contains(t, msg, "Client \"x\" is already defined")
	assert.Contains(t, msg, "names are global")
}

func TestServerNamesCollideWithinAType(t *testing.T) {
	msg := buildFails(t, `
server "http" "x" {
  listen = "127.0.0.1:0"

  handle "/a" {
    action = "ok"
  }
}

server "http" "x" {
  listen = "127.0.0.1:0"

  handle "/b" {
    action = "ok"
  }
}
`)
	assert.Contains(t, msg, "Server \"x\" is already defined")
}

// TestDisabledSelectsBetweenSameNamedServers is the idiom the uniqueness rule
// must not break: two declarations of a name, an environment variable choosing
// between them. A disabled block returns before registering anything, so it is
// not a duplicate of the one that stays.
func TestDisabledSelectsBetweenSameNamedServers(t *testing.T) {
	handler := buildFrom(t, `
server "mcp" "x" {
  disabled    = true
  server_name = "not this one"
}

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/a" {
    action = "ok"
  }
}

server "http" "x" {
  listen = "127.0.0.1:0"

  handle "/b" {
    action = "ok"
  }
}
`)
	require.NotNil(t, handler)
}
