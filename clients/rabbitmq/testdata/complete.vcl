bus "main" {}

client "rabbitmq" "events" {
  brokers = ["amqp://rabbitmq.internal:5672/production"]

  auth {
    username = "vinculum"
    password = "secret"
  }

  reconnect {
    initial_delay  = "1s"
    max_delay      = "60s"
    backoff_factor = 2.0
  }

  sender "out" {
    exchange                = "alerts"
    confirm_mode            = true
    persistent              = true
    default_topic_transform = "slash_to_dot"

    topic "alerts/#" {
      routing_key = "alerts"
    }
  }

  receiver "in" {
    queue      = "vinculum-sensors"
    subscriber = bus.main
    prefetch   = 20

    declare {
      durable = true
    }

    binding "sensor.#" {
      exchange = "sensor-events"
    }

    subscription "sensor.*deviceId.reading" {
      vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"
    }
    subscription "sensor.*deviceId.status" {
      vinculum_topic = "sensor/${ctx.fields.deviceId}/status"
    }
  }
}

subscription "alerts_to_rabbitmq" {
  target     = bus.main
  topics     = ["alerts/#"]
  subscriber = client.events.sender.out
}
