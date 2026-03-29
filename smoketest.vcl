bus "main" {}

# ── Plain TCP (mqtt://) ────────────────────────────────────────────────────────

client "mqtt" "plain" {
    brokers   = ["mqtt://mqtt.sarna.org:1883"]
    client_id = "vinculum-smoke-plain"

    sender "out" {
        # QoS 0: sensor readings
        topic "smoke/sensor/+deviceId/reading" {
            mqtt_topic = "smoke/plain/sensor/${ctx.fields.deviceId}/reading"
        }
        # QoS 1: alerts
        topic "smoke/alerts/#" {
            mqtt_topic = "smoke/plain/alerts"
            qos        = 1
        }
    }

    receiver "in" {
        subscriber = bus.main

        subscription "smoke/plain/sensor/+/reading" {
            vinculum_topic = "recv/plain/sensor/${ctx.topic}"
        }
        subscription "smoke/plain/alerts" {
            vinculum_topic = "recv/plain/alerts"
            qos            = 1
        }
    }
}

# ── TLS (mqtts://, Traefik self-signed) ──────────────────────────────────────

client "mqtt" "tls" {
    brokers   = ["mqtts://mqtt.sarna.org:8883"]
    client_id = "vinculum-smoke-tls"

    tls {
        enabled              = true
        insecure_skip_verify = true   # Traefik default cert has no trusted CA
    }

    auth {
        username = "testuser"
        password = env.MQTT_PASSWORD
    }

    sender "out" {
        topic "smoke/tls/+" {
            mqtt_topic = "smoke/tls/out"
            qos        = 1
        }
    }

    receiver "in" {
        subscriber = bus.main
        subscription "smoke/tls/out" {
            vinculum_topic = "recv/tls/out"
            qos            = 1
        }
    }
}

# ── WebSocket (ws://) ─────────────────────────────────────────────────────────

client "mqtt" "ws" {
    brokers   = ["ws://mqtt.sarna.org:9001/mqtt"]
    client_id = "vinculum-smoke-ws"

    sender "out" {
        topic "smoke/ws/+" {
            mqtt_topic = "smoke/ws/out"
        }
    }

    receiver "in" {
        subscriber = bus.main
        subscription "smoke/ws/out" {
            vinculum_topic = "recv/ws/out"
        }
    }
}

# ── Periodic senders ──────────────────────────────────────────────────────────

trigger "interval" "send_plain_sensor" {
    initial_delay = "2s"
    delay         = "10s"
    action        = send(ctx, client.plain.sender.out,
                        "smoke/sensor/device42/reading",
                        {temp = 21.5, unit = "C"})
}

trigger "interval" "send_plain_alert" {
    initial_delay = "3s"
    delay         = "10s"
    action        = send(ctx, client.plain.sender.out,
                        "smoke/alerts/critical",
                        {code = 99, msg = "test alert"})
}

trigger "interval" "send_tls" {
    initial_delay = "4s"
    delay         = "10s"
    action        = send(ctx, client.tls.sender.out,
                        "smoke/tls/hello",
                        "hello over TLS")
}

trigger "interval" "send_ws" {
    initial_delay = "5s"
    delay         = "10s"
    action        = send(ctx, client.ws.sender.out,
                        "smoke/ws/hello",
                        "hello over WebSocket")
}

# ── Log received messages ─────────────────────────────────────────────────────

subscription "log_plain_sensor" {
    target     = bus.main
    topics     = ["recv/plain/sensor/#"]
    action     = loginfo("plain sensor", {topic = ctx.topic, msg = ctx.msg})
}

subscription "log_plain_alerts" {
    target     = bus.main
    topics     = ["recv/plain/alerts"]
    action     = loginfo("plain alert", {topic = ctx.topic, msg = ctx.msg})
}

subscription "log_tls" {
    target     = bus.main
    topics     = ["recv/tls/#"]
    action     = loginfo("tls recv", {topic = ctx.topic, msg = ctx.msg})
}

subscription "log_ws" {
    target     = bus.main
    topics     = ["recv/ws/#"]
    action     = loginfo("ws recv", {topic = ctx.topic, msg = ctx.msg})
}
