client "redis" "myredis" {
    mode      = "cluster"
    addresses = ["redis1:6379", "redis2:6379"]
}
