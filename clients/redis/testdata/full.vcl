client "redis" "myredis" {
    address        = "localhost:6379"
    database       = 3
    username       = "default"
    password       = "s3cret"
    pool_size      = 5
    min_idle_conns = 1
    dial_timeout   = "3s"
}
