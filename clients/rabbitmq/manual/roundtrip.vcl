# Manual smoke test for the rabbitmq client: covers scenarios 1.1 (sender),
# 2.1 (receiver), and 3.1 (round-trip) from the test plan, plus 5.1 (reconnect)
# when you close the broker connection out-of-band.
#
# The bus is exposed over a VWS WebSocket (mounted under an HTTP server) so the
# `vinculum publish` / `vinculum subscribe` CLIs can drive and observe it.
#
# Run (source secrets without printing them; see rabbitmq.env handling):
#
#   set -a; . ./rabbitmq.env; set +a
#   go run . serve clients/rabbitmq/manual/roundtrip.vcl
#
# In two other shells (env need not be sourced for these):
#
#   # observe everything on the bus
#   vinculum subscribe ws://localhost:8080/vws '#'
#
#   # inject onto the bus: bus "out/demo" -> sender -> vinculum.test.topic
#   #   -> receiver (queue bound "out.#") -> bus "in/out.demo"
#   vinculum publish ws://localhost:8080/vws out/demo hello
#
# The subscriber should print both "out/demo" (your publish) and "in/out.demo"
# (the round-tripped message). Observe the broker side with rabbitmqadmin / the
# RabbitMQ management UI.
#
# For 5.1 (reconnect): while running, close the client's connection via the
# management API, e.g.
#   curl -u "$RABBITMQ_MGMT_USER:$RABBITMQ_MGMT_PASS" -X DELETE \
#     "$RABBITMQ_MGMT_URL/api/connections/<name>"
# and watch the server log fire on_disconnect, back off, reconnect, and
# on_connect, after which publishes flow again.

bus "main" {}

server "http" "h" {
  listen = ":8080"

  handle "/vws" {
    handler = server.vws
  }
}

server "vws" "vws" {
  bus        = bus.main
  allow_send = true
}

client "rabbitmq" "events" {
  # RABBITMQ_VHOST carries its own leading slash (e.g. "/vinculum-test"), so it
  # is appended directly after the port with no extra "/".
  brokers = ["amqp://${env.RABBITMQ_HOST}:${env.RABBITMQ_PORT}${env.RABBITMQ_VHOST}"]

  auth {
    username = env.RABBITMQ_USER
    password = env.RABBITMQ_PASS
  }

  on_connect    = log_info("rabbitmq connected")
  on_disconnect = log_info("rabbitmq disconnected")

  sender "out" {
    exchange = "vinculum.test.topic"
  }

  receiver "in" {
    queue      = "vinculum.test.manual"
    subscriber = bus.main

    declare {
      durable     = false
      auto_delete = true
    }

    binding "out.#" {
      exchange = "vinculum.test.topic"
    }

    subscription "out.#" {
      vinculum_topic = "in/${ctx.routing_key}"
    }
  }
}

subscription "bus_to_rmq" {
  target     = bus.main
  topics     = ["out/#"]
  subscriber = client.events.sender.out
}

# Logs the round-tripped message so the loop is visible in the server log even
# without a separate `vinculum subscribe` client.
subscription "log_in" {
  target = bus.main
  topics = ["in/#"]
  action = log_info("round-trip received", { topic = ctx.topic, msg = ctx.msg })
}
